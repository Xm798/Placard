package handler

import (
	"context"
	"testing"
	"time"
)

// TestIssueTokenTakesNoFiberCtx pins the whole point of the refactor: the
// device-code redemption endpoint is unauthenticated, so anything that reads
// identity or audit context out of *fiber.Ctx cannot be reused there.
func TestIssueTokenTakesNoFiberCtx(t *testing.T) {
	_, deps := newTestApp(t)
	h := New(deps)

	exp := time.Now().Add(30 * 24 * time.Hour)
	plaintext, tok, err := h.issueToken(context.Background(), "u_device_user", "CLI on laptop", exp, AuditMeta{
		Channel:    "device",
		ApproverIP: "192.0.2.7",
		ExchangeIP: "198.51.100.9",
	})
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	if len(plaintext) != len(patPrefix)+patNanoIDLen {
		t.Fatalf("plaintext len = %d", len(plaintext))
	}
	if tok.UserID != "u_device_user" || tok.Name != "CLI on laptop" {
		t.Fatalf("token = %+v", tok)
	}
	if tok.ExpiresAt.Sub(exp).Abs() > time.Second {
		t.Fatalf("expires_at = %v, want %v", tok.ExpiresAt, exp)
	}
	// The stored value must be the HMAC, never the plaintext.
	if tok.TokenHash == plaintext {
		t.Fatal("token_hash equals the plaintext")
	}
}

// The PAT hash is HMAC'd with server.secret_key, so rotating the key must
// invalidate every token already issued — the property that proves the key
// really is the pepper and not a second, unused secret.
func TestTokenInvalidAfterSecretKeyRotation(t *testing.T) {
	app, deps := newTestApp(t)
	_, plaintext := createTestToken(t, app, "30d")

	validate := New(deps).TokenValidator()
	identity, ok := validate(plaintext)
	if !ok {
		t.Fatal("freshly minted token did not validate")
	}
	if identity.AuthzID != testAuthzID {
		t.Fatalf("identity.AuthzID = %q, want %q", identity.AuthzID, testAuthzID)
	}

	deps.Cfg.Server.SecretKey = "rotated-secret-key"
	if _, ok := New(deps).TokenValidator()(plaintext); ok {
		t.Fatal("token still validates after the secret key rotated; the stored hash is not keyed by it")
	}
}
