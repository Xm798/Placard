package sharecode

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

const testKey = "test-secret-key"

func TestGenerateIsSixDigits(t *testing.T) {
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		code, err := Generate()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if !Valid(code) {
			t.Fatalf("generate produced %q, want %d digits", code, Digits)
		}
		seen[code]++
	}
	if len(seen) < 100 {
		t.Fatalf("only %d distinct codes in 200 draws — not random", len(seen))
	}
}

func TestValidRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "12345", "1234567", "12345a", "12 456", "１２３４５６"} {
		if Valid(bad) {
			t.Fatalf("Valid(%q) = true", bad)
		}
	}
	if !Valid("000000") {
		t.Fatal("leading zeros must be a legal code")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	sealed, err := Seal(testKey, "042195")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := Open(testKey, sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got != "042195" {
		t.Fatalf("open = %q, want 042195", got)
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	a, _ := Seal(testKey, "123456")
	b, _ := Seal(testKey, "123456")
	if a == b {
		t.Fatal("two seals of the same code are identical — nonce is not fresh")
	}
}

// A rotated (or lost) server.secret_key must surface as ErrUndecryptable, which
// is the signal callers turn into "this page has no code".
func TestOpenUnderWrongKeyIsUndecryptable(t *testing.T) {
	sealed, _ := Seal(testKey, "123456")
	if _, err := Open("a-different-secret", sealed); !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("err = %v, want ErrUndecryptable", err)
	}
}

func TestOpenGarbageIsUndecryptable(t *testing.T) {
	for _, bad := range []string{"", "!!!not base64!!!", "AAAA"} {
		if _, err := Open(testKey, bad); !errors.Is(err, ErrUndecryptable) {
			t.Fatalf("Open(%q) err = %v, want ErrUndecryptable", bad, err)
		}
	}
}

// Flipping a bit of the raw ciphertext, not of its base64 spelling: the last
// base64 character of a 34-byte payload carries slack bits the decoder ignores,
// so editing it can round-trip to the very same bytes.
func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	sealed, _ := Seal(testKey, "123456")
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw[len(raw)-1] ^= 0x01
	if _, err := Open(testKey, base64.RawURLEncoding.EncodeToString(raw)); !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("err = %v, want ErrUndecryptable", err)
	}
}

func TestTicketRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	tk := Ticket(testKey, "abc12345", 1, now.Add(time.Hour))
	if !ValidTicket(testKey, "abc12345", 1, now, tk) {
		t.Fatal("freshly minted ticket does not verify")
	}
}

func TestTicketIsBoundToFileVersionKeyAndExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	tk := Ticket(testKey, "abc12345", 1, now.Add(time.Hour))

	cases := []struct {
		name   string
		verify func() bool
	}{
		{"other file", func() bool { return ValidTicket(testKey, "zzz99999", 1, now, tk) }},
		{"other version", func() bool { return ValidTicket(testKey, "abc12345", 2, now, tk) }},
		{"other key", func() bool { return ValidTicket("other", "abc12345", 1, now, tk) }},
		{"expired", func() bool { return ValidTicket(testKey, "abc12345", 1, now.Add(2*time.Hour), tk) }},
		{"malformed", func() bool { return ValidTicket(testKey, "abc12345", 1, now, "nonsense") }},
		{"no signature", func() bool { return ValidTicket(testKey, "abc12345", 1, now, "9999999999.") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.verify() {
				t.Fatal("ticket verified when it must not")
			}
		})
	}
}

// The expiry is carried in the clear so the server can read it back, but it is
// covered by the MAC: pushing it out has to invalidate the whole value.
func TestTicketExpiryCannotBeExtended(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	tk := Ticket(testKey, "abc12345", 1, now.Add(time.Hour))
	_, sum, _ := strings.Cut(tk, ".")
	if ValidTicket(testKey, "abc12345", 1, now, "9999999999."+sum) {
		t.Fatal("rewriting the expiry produced a ticket that still verifies")
	}
}

func TestCookieNameIsPerFileAndHostPrefixedOnHTTPS(t *testing.T) {
	if got := CookieName(true, "abc12345"); got != "__Host-placard_share_abc12345" {
		t.Fatalf("secure name = %q", got)
	}
	if got := CookieName(false, "abc12345"); got != "placard_share_abc12345" {
		t.Fatalf("insecure name = %q", got)
	}
	if CookieName(true, "abc12345") == CookieName(true, "zzz99999") {
		t.Fatal("two files share one cookie name")
	}
}
