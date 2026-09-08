package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/testutil"
	"github.com/Xm798/placard/internal/userctx"
)

type recordingAudit struct {
	mu        sync.Mutex
	entries   []model.AuditLog
	insertErr error
}

func (a *recordingAudit) Insert(_ context.Context, entry *model.AuditLog) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.insertErr != nil {
		return a.insertErr
	}
	a.entries = append(a.entries, *entry)
	return nil
}

func (a *recordingAudit) Query(_ context.Context, q repo.AuditQuery) ([]model.AuditLog, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var entries []model.AuditLog
	for _, entry := range a.entries {
		if q.Action != "" && entry.Action != q.Action {
			continue
		}
		if q.Actor != "" && entry.Actor != q.Actor {
			continue
		}
		if q.FileNanoID != "" && entry.FileNanoID != q.FileNanoID {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (a *recordingAudit) last(t *testing.T) model.AuditLog {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.entries) == 0 {
		t.Fatal("expected an audit entry")
	}
	return a.entries[len(a.entries)-1]
}

// newAuthApp mounts the session-cookie routes (logout, logged-out) plus an
// /api/me with a test-injected identity, over a real session store.
func newAuthApp(t *testing.T) (*fiber.App, session.Store, *recordingAudit) {
	t.Helper()
	store := newEphemeral(t, testutil.OpenTestDB(t), time.Hour, 2*time.Hour).sessions
	audit := &recordingAudit{}

	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://ps.example.com"
	cfg.Auth.Session.IdleTTL = time.Hour
	cfg.Auth.Session.AbsoluteTTL = 2 * time.Hour

	h := New(Deps{
		Cfg:           cfg,
		Tokens:        &repo.TokenRepo{},
		Sessions:      store,
		SessionCookie: "__Host-placard_session",
		Audit:         audit,
	})
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Use(middleware.RequestID())
	app.Post("/auth/logout", h.AuthLogout)
	app.Get("/auth/logged-out", h.AuthLoggedOut)
	app.Get("/api/me", func(c *fiber.Ctx) error { // identity injected by test
		userctx.Set(c, userctx.Identity{AuthzID: "u_x", DisplayName: "沈"})
		return h.Me(c)
	})
	return app, store, audit
}

func TestLogoutDeletesSession(t *testing.T) {
	app, store, audit := newAuthApp(t)
	sid, _ := store.Create(context.Background(),
		session.Data{AuthzID: "u_x", DisplayName: "沈", CreatedAt: time.Now()})

	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: sid})
	resp, _ := app.Test(req)
	if resp.StatusCode != 204 {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
	if _, err := store.Get(context.Background(), sid); err != session.ErrNotFound {
		t.Fatalf("session must be deleted, got %v", err)
	}
	auditEntry := audit.last(t)
	if auditEntry.Action != "auth.logout" || auditEntry.Outcome != "success" ||
		auditEntry.Actor != "u_x" || auditEntry.ActorName != "沈" || auditEntry.ResourceID != "" {
		t.Fatalf("logout audit = %+v", auditEntry)
	}
}

// Logging out without a cookie still clears client state and never touches the
// store — the browser may be holding a cookie the server already forgot.
func TestLogoutWithoutCookieIsNoContent(t *testing.T) {
	app, _, _ := newAuthApp(t)
	resp, _ := app.Test(httptest.NewRequest("POST", "/auth/logout", nil))
	if resp.StatusCode != 204 {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
	if cleared := cookieVal(resp, "__Host-placard_session"); cleared != "" {
		t.Fatalf("session cookie must be cleared, got %q", cleared)
	}
}

func TestMe(t *testing.T) {
	app, _, _ := newAuthApp(t)
	resp, _ := app.Test(httptest.NewRequest("GET", "/api/me", nil))
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		DisplayName string `json:"display_name"`
		AuthzID     string `json:"authz_id"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != 200 || out.DisplayName != "沈" || out.AuthzID != "u_x" {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

// cookieVal extracts a Set-Cookie value by name from a response.
func cookieVal(resp *http.Response, name string) string {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}
