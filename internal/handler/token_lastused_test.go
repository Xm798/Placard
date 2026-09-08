package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
)

// touchFunc matches TokenRepo.TouchLastUsed — the injectable async touch write.
type touchFunc = func(ctx context.Context, id uint, at time.Time) error

// setupValidatorTest wires a TokenValidator whose async last_used_at write is
// replaced by touch, creates a PAT, runs prepare (may be nil; e.g. seeding
// last_used_at), then asserts the first validate resolves the owner. Returns
// the token id, its plaintext and the validator.
func setupValidatorTest(t *testing.T, app *fiber.App, deps Deps, touch touchFunc, prepare func(tokenID uint)) (uint, string, middleware.TokenValidator) {
	t.Helper()
	h := New(deps)
	h.lastUsed = newLastUsedToucher(touch)
	validate := h.TokenValidator()

	tokenID, plaintext := createTestToken(t, app, "30d")
	if prepare != nil {
		prepare(tokenID)
	}
	ident, ok := validate(plaintext)
	if !ok || ident.AuthzID != testAuthzID {
		t.Fatalf("validate: ok=%v ident=%+v", ok, ident)
	}
	return tokenID, plaintext, validate
}

// reloadToken re-reads a token row by id.
func reloadToken(t *testing.T, deps Deps, id uint) model.Token {
	t.Helper()
	var tok model.Token
	if err := deps.DB.First(&tok, id).Error; err != nil {
		t.Fatalf("reload token: %v", err)
	}
	return tok
}

// TestTokenValidatorTouchesNeverUsed asserts a successful Bearer auth on a
// never-used token (1970 sentinel) stamps last_used_at asynchronously.
func TestTokenValidatorTouchesNeverUsed(t *testing.T) {
	app, deps := newTestApp(t)
	touched := make(chan error, 4)
	// Delegate to the real repo write but signal completion for the test.
	id, _, _ := setupValidatorTest(t, app, deps, func(ctx context.Context, tid uint, at time.Time) error {
		err := deps.Tokens.TouchLastUsed(ctx, tid, at)
		touched <- err
		return err
	}, nil)

	select {
	case err := <-touched:
		if err != nil {
			t.Fatalf("touch failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("last_used_at touch never attempted")
	}

	tok := reloadToken(t, deps, id)
	if dto.NullableLastUsed(tok.LastUsedAt) == nil {
		t.Errorf("last_used_at still never-used sentinel: %v", tok.LastUsedAt)
	}
	if d := time.Since(tok.LastUsedAt); d < -time.Minute || d > time.Minute {
		t.Errorf("last_used_at = %v, want ~now", tok.LastUsedAt)
	}
}

// TestTokenValidatorThrottlesRecentTouch asserts a token used within
// lastUsedThrottle is NOT re-stamped. The throttle decision runs synchronously
// inside validate, so a correct implementation spawns no touch goroutine at
// all and the row stays byte-identical.
func TestTokenValidatorThrottlesRecentTouch(t *testing.T) {
	app, deps := newTestApp(t)
	touched := make(chan error, 4)
	recent := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	id, _, _ := setupValidatorTest(t, app, deps,
		func(ctx context.Context, tid uint, at time.Time) error {
			touched <- nil
			return nil
		},
		func(tokenID uint) {
			if err := deps.DB.Model(&model.Token{}).Where("id = ?", tokenID).
				UpdateColumn("last_used_at", recent).Error; err != nil {
				t.Fatalf("seed last_used_at: %v", err)
			}
		})

	// Decision happens before validate returns; nothing may have been spawned.
	select {
	case <-touched:
		t.Fatal("recently-used token was touched (throttle broken)")
	default:
	}
	tok := reloadToken(t, deps, id)
	if tok.LastUsedAt.Unix() != recent.Unix() {
		t.Errorf("last_used_at changed: got %v, want %v", tok.LastUsedAt, recent)
	}
}

// TestTokenValidatorTouchFailureKeepsAuth asserts a failing touch write never
// affects the auth result: validate still succeeds (twice), and the row simply
// keeps its sentinel.
func TestTokenValidatorTouchFailureKeepsAuth(t *testing.T) {
	app, deps := newTestApp(t)
	touched := make(chan struct{}, 4)
	id, plaintext, validate := setupValidatorTest(t, app, deps,
		func(ctx context.Context, tid uint, at time.Time) error {
			touched <- struct{}{}
			return errors.New("injected db failure")
		}, nil)

	select {
	case <-touched:
	case <-time.After(5 * time.Second):
		t.Fatal("touch never attempted")
	}

	// Row keeps the sentinel (the injected write failed)...
	tok := reloadToken(t, deps, id)
	if dto.NullableLastUsed(tok.LastUsedAt) != nil {
		t.Errorf("row unexpectedly stamped: %v", tok.LastUsedAt)
	}
	// ...and auth keeps working on subsequent requests.
	if _, ok := validate(plaintext); !ok {
		t.Errorf("second validate failed after touch failure")
	}
}
