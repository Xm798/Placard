// Package sharecode implements the share code that gates a `link` page: a
// 6-digit secret an owner hands out alongside the URL, and the unlock ticket a
// visitor who typed it correctly carries afterwards.
//
// Two different primitives, both keyed by server.secret_key, and deliberately
// not interchangeable:
//
//   - Seal/Open store the code itself. It has to be recoverable (the owner's
//     dialog shows it), so this is encryption — AES-GCM — not a password hash.
//     A 6-digit secret would not survive a hash-and-compare design anyway: the
//     whole keyspace is a million guesses, which is why the online guessing rate
//     is what actually protects it (see the per-file+IP failure budget).
//   - Ticket/ValidTicket mint the cookie a visitor gets after unlocking. It is a
//     MAC over (file, code version, expiry) rather than an opaque id, so no
//     server-side state is needed and a code change invalidates every ticket
//     issued before it by bumping the version.
//
// Rotating server.secret_key therefore invalidates both: sealed codes stop
// decrypting (Open returns ErrUndecryptable, which callers read as "this page
// has no code") and outstanding tickets stop verifying.
package sharecode

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Digits is the length of a generated code. Six is short enough to read out
// over a call and retype on a phone; the failure budget is what makes it safe.
const Digits = 6

// ErrUndecryptable is returned by Open when the stored ciphertext does not
// authenticate under the current key — the expected outcome after
// server.secret_key is rotated or lost.
var ErrUndecryptable = errors.New("sharecode: ciphertext does not decrypt under this key")

// Generate returns a fresh code: Digits uniformly random decimal digits,
// leading zeros kept, so every value in the keyspace is equally likely.
func Generate() (string, error) {
	maximum := new(big.Int).Exp(big.NewInt(10), big.NewInt(Digits), nil)
	n, err := rand.Int(rand.Reader, maximum)
	if err != nil {
		return "", fmt.Errorf("sharecode: generate: %w", err)
	}
	return fmt.Sprintf("%0*d", Digits, n), nil
}

// Valid reports whether code is well-formed: exactly Digits ASCII digits. It is
// what the unlock endpoint rejects a malformed submission with before spending
// any of the caller's failure budget on it.
func Valid(code string) bool {
	if len(code) != Digits {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}

// key derives the AES/HMAC key from the instance secret. secret_key is a
// config string of no fixed length or encoding, so it is hashed to the 32 bytes
// AES-256 needs rather than used raw.
func key(secretKey string) [32]byte {
	return sha256.Sum256([]byte(secretKey))
}

// Seal encrypts code under secretKey and returns nonce||ciphertext, base64
// (raw URL alphabet, so the value is safe in any column or header).
func Seal(secretKey, code string) (string, error) {
	gcm, err := aead(secretKey)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("sharecode: nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(code), nil)), nil
}

// Open decrypts a value produced by Seal. Every failure mode — bad base64,
// short input, failed authentication — collapses to ErrUndecryptable: to a
// caller they all mean the same thing, that the stored code cannot be read
// back.
func Open(secretKey, sealed string) (string, error) {
	gcm, err := aead(secretKey)
	if err != nil {
		return "", err
	}
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < gcm.NonceSize() {
		return "", ErrUndecryptable
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", ErrUndecryptable
	}
	return string(plain), nil
}

func aead(secretKey string) (cipher.AEAD, error) {
	k := key(secretKey)
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, fmt.Errorf("sharecode: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sharecode: gcm: %w", err)
	}
	return gcm, nil
}

// Equal compares a submitted code against the stored one in constant time.
func Equal(submitted, stored string) bool {
	return subtle.ConstantTimeCompare([]byte(submitted), []byte(stored)) == 1
}

// Ticket mints the unlock cookie value for fileID at code version, expiring at
// exp: "<unix exp>.<hmac>". The expiry travels in the clear because it is also
// covered by the MAC — a visitor who edits it invalidates the whole value.
//
// version is what makes a ticket outlive exactly one code: regenerating or
// clearing the code increments it, and every ticket minted under the old
// number stops verifying at once.
func Ticket(secretKey, fileID string, version int, exp time.Time) string {
	unix := exp.UTC().Unix()
	return strconv.FormatInt(unix, 10) + "." + mac(secretKey, fileID, version, unix)
}

// ValidTicket reports whether ticket authenticates for fileID at version and
// has not expired as of now.
func ValidTicket(secretKey, fileID string, version int, now time.Time, ticket string) bool {
	unixText, sum, ok := strings.Cut(ticket, ".")
	if !ok {
		return false
	}
	unix, err := strconv.ParseInt(unixText, 10, 64)
	if err != nil {
		return false
	}
	if !hmac.Equal([]byte(sum), []byte(mac(secretKey, fileID, version, unix))) {
		return false
	}
	return now.UTC().Unix() < unix
}

// mac authenticates the ticket's three bound fields. The separator is a
// character none of them can contain, so no two different triples can produce
// the same signed string.
func mac(secretKey, fileID string, version int, unix int64) string {
	k := key(secretKey)
	h := hmac.New(sha256.New, k[:])
	fmt.Fprintf(h, "%s|%d|%d", fileID, version, unix)
	return hex.EncodeToString(h.Sum(nil))
}

// CookieName is the per-file cookie an unlock ticket lives in. Per file, so
// unlocking one page never unlocks another, and prefixed with __Host- — which
// pins it to this exact host at path / with Secure — everywhere the instance
// is served over HTTPS. A browser silently drops a __Host- cookie that arrives
// without Secure, so a local HTTP instance gets the bare name instead and the
// flow still works while developing.
//
// The cost of one cookie per page is that __Host- mandates path /, so every
// ticket a visitor holds rides on every request to the origin, and a reader of
// very many coded pages eventually meets the browser's per-domain cookie cap
// (~180 in Chrome) — at which point a new unlock is dropped and the prompt
// explains it (see unlockShellTmpl). Folding every ticket into one cookie
// would remove both, at the price of rewriting the whole set on each unlock.
func CookieName(secure bool, fileID string) string {
	name := "placard_share_" + fileID
	if secure {
		return "__Host-" + name
	}
	return name
}
