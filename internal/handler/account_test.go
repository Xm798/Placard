package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/model"
)

func TestAccountProfileShowsUsernameAndEmail(t *testing.T) {
	a := newLocalAuthApp(t)
	_, _, cookie := a.register(t, `{"username":"alice","email":"Alice@Example.com","password":"correct-horse","display_name":"Alice"}`)

	code, raw := a.get(t, "/api/account", cookie)
	if code != fiber.StatusOK {
		t.Fatalf("GET /api/account = %d (%s), want 200", code, raw)
	}
	var p dto.ProfileResponse
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if p.Username != "alice" || p.Email != "alice@example.com" || p.DisplayName != "Alice" || !p.HasPassword {
		t.Errorf("profile = %+v", p)
	}

	if code, _ := a.get(t, "/api/account", nil); code != fiber.StatusUnauthorized {
		t.Errorf("anonymous GET /api/account = %d, want 401", code)
	}
}

func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	a := newLocalAuthApp(t)
	_, _, cookie := a.register(t, `{"username":"alice","password":"correct-horse"}`)

	code, body, _ := a.write(t, "PUT", "/api/account/password",
		`{"current_password":"wrong-horse","new_password":"battery-staple"}`, cookie)
	if code != fiber.StatusForbidden {
		t.Fatalf("wrong current password = %d (%s), want 403", code, body)
	}
	code, body, _ = a.write(t, "PUT", "/api/account/password",
		`{"current_password":"correct-horse","new_password":"short"}`, cookie)
	if code != fiber.StatusBadRequest {
		t.Fatalf("too-short new password = %d (%s), want 400", code, body)
	}

	code, body, _ = a.write(t, "PUT", "/api/account/password",
		`{"current_password":"correct-horse","new_password":"battery-staple"}`, cookie)
	if code != fiber.StatusNoContent {
		t.Fatalf("change = %d (%s), want 204", code, body)
	}
	if code, _ := a.login(t, "alice", "correct-horse"); code != fiber.StatusUnauthorized {
		t.Errorf("login with the old password = %d, want 401", code)
	}
	if code, _ := a.login(t, "alice", "battery-staple"); code != fiber.StatusOK {
		t.Errorf("login with the new password = %d, want 200", code)
	}
}

// Wrong current passwords spend the same per-IP budget a failed login does,
// which admits `limit` failures and refuses once the count passes it.
func TestChangePasswordFeedsTheFailureBudget(t *testing.T) {
	a := newLocalAuthApp(t)
	_, _, cookie := a.register(t, `{"username":"alice","password":"correct-horse"}`)

	for i := 0; i < 4; i++ {
		a.write(t, "PUT", "/api/account/password",
			`{"current_password":"wrong-horse","new_password":"battery-staple"}`, cookie)
	}
	code, body, _ := a.write(t, "PUT", "/api/account/password",
		`{"current_password":"correct-horse","new_password":"battery-staple"}`, cookie)
	if code != fiber.StatusTooManyRequests {
		t.Fatalf("change after the budget is spent = %d (%s), want 429", code, body)
	}
}

func TestChangePasswordRefusesAPersonalAccessToken(t *testing.T) {
	a := newLocalAuthApp(t)
	_, _, cookie := a.register(t, `{"username":"alice","password":"correct-horse"}`)
	code, raw, _ := a.post(t, "/api/tokens", `{"name":"cli","expiry":"30d"}`, cookie)
	if code != fiber.StatusOK {
		t.Fatalf("create token = %d (%s)", code, raw)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil || tok.Token == "" {
		t.Fatalf("token body %s: %v", raw, err)
	}

	req := httptest.NewRequest("PUT", "/api/account/password",
		strings.NewReader(`{"current_password":"correct-horse","new_password":"battery-staple"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := a.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("PAT password change = %d, want 403", resp.StatusCode)
	}
}

// A password change signs out every other browser and keeps the one that made
// it signed in, under a new session id.
func TestChangePasswordEndsOtherSessions(t *testing.T) {
	a := newLocalAuthApp(t)
	_, _, here := a.register(t, `{"username":"alice","password":"correct-horse"}`)
	_, elsewhere := a.login(t, "alice", "correct-horse")

	code, body, renewed := a.write(t, "PUT", "/api/account/password",
		`{"current_password":"correct-horse","new_password":"battery-staple"}`, here)
	if code != fiber.StatusNoContent {
		t.Fatalf("change = %d (%s), want 204", code, body)
	}
	if renewed == nil || renewed.Value == "" || renewed.Value == here.Value {
		t.Fatalf("change set cookie %+v, want a new session id", renewed)
	}

	if code, _ := a.get(t, "/api/me", elsewhere); code != fiber.StatusUnauthorized {
		t.Errorf("other session after the change = %d, want 401", code)
	}
	if code, _ := a.get(t, "/api/me", here); code != fiber.StatusUnauthorized {
		t.Errorf("pre-change session of this browser = %d, want 401", code)
	}
	if code, _ := a.get(t, "/api/me", renewed); code != fiber.StatusOK {
		t.Errorf("renewed session = %d, want 200", code)
	}
	_, fresh := a.login(t, "alice", "battery-staple")
	if code, _ := a.get(t, "/api/me", fresh); code != fiber.StatusOK {
		t.Errorf("login after the change = %d, want 200", code)
	}
}

func TestAdminPasswordResetEndsTheUsersSessions(t *testing.T) {
	a := newLocalAuthApp(t)
	_, _, admin := a.register(t, `{"username":"alice","password":"correct-horse"}`)
	if err := a.settings.Set(context.Background(), model.SettingRegistrationOpen, "true"); err != nil {
		t.Fatalf("open registration: %v", err)
	}
	_, bob, bobSession := a.register(t, `{"username":"bob","password":"correct-horse"}`)

	code, body, _ := a.post(t, "/api/admin/users/"+bob.ID+"/password", `{"password":"battery-staple"}`, admin)
	if code != fiber.StatusNoContent {
		t.Fatalf("reset = %d (%s), want 204", code, body)
	}
	if code, _ := a.get(t, "/api/me", bobSession); code != fiber.StatusUnauthorized {
		t.Errorf("bob's session after the reset = %d, want 401", code)
	}
	if code, _ := a.get(t, "/api/me", admin); code != fiber.StatusOK {
		t.Errorf("admin's own session after the reset = %d, want 200", code)
	}
}
