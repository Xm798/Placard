package password

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	h, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("encoding = %q, want an argon2id PHC string", h)
	}
	if !Verify(h, "correct horse battery staple") {
		t.Error("Verify(correct password) = false, want true")
	}
	if Verify(h, "Correct horse battery staple") {
		t.Error("Verify(wrong password) = true, want false")
	}
}

// Every hash must carry its own salt, so the same password never encodes to
// the same string twice — otherwise a leaked table shows which accounts share
// a password.
func TestHashIsSalted(t *testing.T) {
	a, err := Hash("same")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	b, err := Hash("same")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical, want distinct salts")
	}
}

// A user with no local password has an empty password_hash. Verify must reject
// it rather than treat "no credential" as "any credential".
func TestVerifyRejectsMalformed(t *testing.T) {
	for _, enc := range []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=65536,t=3,p=2$bad$bad",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaGhhc2g",
	} {
		if Verify(enc, "anything") {
			t.Errorf("Verify(%q) = true, want false", enc)
		}
	}
}

// The decoy has to be a hash this package can actually parse — a typo in the
// constant would make it return early and stop costing anything.
func TestVerifyDecoyDoesTheWork(t *testing.T) {
	if VerifyDecoy("anything") {
		t.Error("VerifyDecoy = true, want false")
	}
	if !strings.HasPrefix(dummyHash, "$argon2id$v=19$") || !Verify(dummyHash, "placard-timing-equalizer-not-a-real-password") {
		t.Error("dummyHash is not a verifiable argon2id hash")
	}
}
