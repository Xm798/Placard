// Package password hashes and verifies local account passwords with argon2id.
//
// The encoding is the PHC string format ($argon2id$v=19$m=...,t=...,p=...$salt$hash,
// both base64 raw-standard), which carries the parameters alongside the digest.
// Raising the cost below therefore keeps every existing hash verifiable: an old
// hash is checked with the parameters it was written with, not today's.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Cost parameters for new hashes: the RFC 9106 second recommended option
// (64 MiB, 3 passes), which stays comfortably servable on the small VPS a
// self-hosted instance typically runs on while costing an offline cracker the
// memory.
const (
	hashMemory  uint32 = 64 * 1024
	hashTime    uint32 = 3
	hashThreads uint8  = 2
	hashLength  uint32 = 32
	saltLength         = 16
)

// Hash returns the PHC encoding of plain under the current cost parameters.
func Hash(plain string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	sum := argon2.IDKey([]byte(plain), salt, hashTime, hashMemory, hashThreads, hashLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, hashMemory, hashTime, hashThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

// Verify reports whether plain matches the PHC-encoded hash. Anything it cannot
// parse — an empty string included, which is what a user with no local password
// carries — is false, so a missing credential can never be mistaken for a
// matching one.
func Verify(encoded, plain string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(plain), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is a real hash of a fixed throwaway string, written out rather than
// computed at init so importing this package stays free. It is not a secret:
// its only job is to give VerifyDecoy the same work to do as a real Verify.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=2$s+Jpn+3a5Ws2G5dqQ2kQyg$9JUqIN7srVv2rovOEyguD6EyM0RP+vPvozSa7bglhGk"

// VerifyDecoy burns one argon2id verification and always reports false. Login
// calls it on the "no such user" path so a request for an account that does not
// exist costs the same as one for an account that does — without it, response
// time alone enumerates who has an account here.
func VerifyDecoy(plain string) bool {
	return Verify(dummyHash, plain)
}
