package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/session"
)

// businessFields is the exact field set §5.7 fixes for a state-change line.
// Nothing else belongs on it — an actor, a grantee or a title here would put
// identity/content into ELK that the audit table already holds.
var businessFields = []string{
	"request_id", "entrypoint", "operation", "nano_id", "duration_ms", "outcome",
}

// captureBusiness swaps the state-change logger for an observer for one test.
func captureBusiness(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	prev := businessLogger
	businessLogger = func() *zap.Logger { return zap.New(core) }
	t.Cleanup(func() { businessLogger = prev })
	return logs
}

// businessLine returns the single state-change line for operation, asserting
// the level, the field set, the millisecond type of duration_ms, and that
// outcome is a member of the ctxlog enum rather than free text.
func businessLine(t *testing.T, logs *observer.ObservedLogs, operation string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, e := range logs.All() {
		m := e.ContextMap()
		if m["operation"] != operation {
			continue
		}
		if e.Level != zapcore.InfoLevel {
			t.Fatalf("%s logged at %v, want Info", operation, e.Level)
		}
		for _, f := range businessFields {
			if _, ok := m[f]; !ok {
				t.Fatalf("%s line missing field %q: %v", operation, f, m)
			}
		}
		if len(m) != len(businessFields) {
			t.Fatalf("%s line has unexpected fields: %v", operation, m)
		}
		if _, ok := m["duration_ms"].(int64); !ok {
			t.Fatalf("%s duration_ms = %T, want int64 milliseconds", operation, m["duration_ms"])
		}
		switch m["outcome"] {
		case ctxlog.OutcomeSuccess, ctxlog.OutcomeDenied, ctxlog.OutcomeFailure:
		default:
			t.Fatalf("%s outcome = %v, not a ctxlog.Outcome* value", operation, m["outcome"])
		}
		found = append(found, m)
	}
	if len(found) != 1 {
		t.Fatalf("want exactly 1 %q line, got %d (all: %v)", operation, len(found), logs.All())
	}
	return found[0]
}

func assertNoBusinessLines(t *testing.T, logs *observer.ObservedLogs) {
	t.Helper()
	if n := logs.Len(); n != 0 {
		t.Fatalf("read path emitted %d business lines, want 0: %v", n, logs.All())
	}
}

// Publish and republish each emit one correlated line naming the audit action.
func TestBusinessLogPublishFamily(t *testing.T) {
	app, _ := newTestApp(t)

	logs := captureBusiness(t)
	id := publishOne(t, app, "30d")

	m := businessLine(t, logs, "file.publish")
	if m["nano_id"] != id {
		t.Errorf("nano_id = %v, want %q", m["nano_id"], id)
	}
	if rid, _ := m["request_id"].(string); !strings.HasPrefix(rid, "req_") {
		t.Errorf("request_id = %v, want the middleware-minted req_* id", m["request_id"])
	}
	if m["entrypoint"] != ctxlog.EntrypointHTTP {
		t.Errorf("entrypoint = %v, want %q", m["entrypoint"], ctxlog.EntrypointHTTP)
	}

	code, b := doJSON(t, app, "POST", "/api/publish",
		`{"id":"`+id+`","html":"<!DOCTYPE html><html><body>v2</body></html>"}`)
	if code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}
	if m := businessLine(t, logs, "file.republish"); m["nano_id"] != id {
		t.Errorf("republish nano_id = %v, want %q", m["nano_id"], id)
	}
}

func TestBusinessLogDelete(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishOne(t, app, "30d")

	logs := captureBusiness(t)
	if code, b := doJSON(t, app, "DELETE", "/api/files/"+id, ""); code != fiber.StatusNoContent {
		t.Fatalf("delete = %d %s", code, b)
	}
	if m := businessLine(t, logs, "file.delete"); m["nano_id"] != id {
		t.Errorf("nano_id = %v, want %q", m["nano_id"], id)
	}
}

// The version family: pinning a serving version and restoring an old one.
func TestBusinessLogVersionFamily(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishOne(t, app, "30d")
	if code, b := doJSON(t, app, "POST", "/api/publish",
		`{"id":"`+id+`","html":"<!DOCTYPE html><html><body>v2</body></html>"}`); code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}

	logs := captureBusiness(t)
	if code, b := doJSON(t, app, "PATCH", "/api/files/"+id, `{"shared_version":1}`); code != fiber.StatusNoContent {
		t.Fatalf("pin = %d %s", code, b)
	}
	businessLine(t, logs, "file.pin_version")

	restoreLogs := captureBusiness(t)
	if code, b := doJSON(t, app, "POST", "/api/files/"+id+"/versions/1/restore", ""); code != fiber.StatusOK {
		t.Fatalf("restore = %d %s", code, b)
	}
	if m := businessLine(t, restoreLogs, "file.restore"); m["nano_id"] != id {
		t.Errorf("restore nano_id = %v, want %q", m["nano_id"], id)
	}
}

func TestBusinessLogTokenFamily(t *testing.T) {
	app, _ := newTestApp(t)

	createLogs := captureBusiness(t)
	tokenID, plaintext := createTestToken(t, app, "30d")
	m := businessLine(t, createLogs, "token.create")
	if m["nano_id"] != "" {
		t.Errorf("token.create nano_id = %v, want empty (no file involved)", m["nano_id"])
	}
	for _, line := range createLogs.All() {
		if strings.Contains(line.Message, plaintext) {
			t.Fatal("token plaintext reached a business log line")
		}
		for _, v := range line.ContextMap() {
			if s, ok := v.(string); ok && strings.Contains(s, "pl_") {
				t.Fatalf("token plaintext reached a business log field: %v", line.ContextMap())
			}
		}
	}

	revokeLogs := captureBusiness(t)
	if code, b := doJSON(t, app, "DELETE", "/api/tokens/"+strconv.FormatUint(uint64(tokenID), 10), ""); code != fiber.StatusNoContent {
		t.Fatalf("revoke = %d %s", code, b)
	}
	businessLine(t, revokeLogs, "token.revoke")
}

// Logout is the only auth-family state change left until local accounts land.
func TestBusinessLogLogout(t *testing.T) {
	app, store, _ := newAuthApp(t)
	sid, err := store.Create(context.Background(), session.Data{
		AuthzID: "u_x", DisplayName: "Alice", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	logs := captureBusiness(t)
	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-placard_session", Value: sid})
	resp, err := app.Test(req)
	if err != nil || resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("logout = %v %v", resp, err)
	}
	if m := businessLine(t, logs, "auth.logout"); m["outcome"] != ctxlog.OutcomeSuccess {
		t.Errorf("logout outcome = %v, want %q", m["outcome"], ctxlog.OutcomeSuccess)
	}
}

// Read paths stay silent: AccessLog already records one line per request, and
// a second line on the hottest paths would double their volume for nothing.
func TestBusinessLogSkipsReadPaths(t *testing.T) {
	app, _ := newTestApp(t)
	id := publishOne(t, app, "30d")

	logs := captureBusiness(t)
	if code, _ := doJSON(t, app, "GET", "/api/files", ""); code != fiber.StatusOK {
		t.Fatalf("list files failed")
	}
	if code, _ := doJSON(t, app, "GET", "/api/files/"+id+"/versions", ""); code != fiber.StatusOK {
		t.Fatalf("list versions failed")
	}
	if code, _ := doJSON(t, app, "GET", "/s/"+id+"/meta", ""); code != fiber.StatusOK {
		t.Fatalf("meta failed")
	}
	if resp := renderIframe(t, app, id); resp.StatusCode != fiber.StatusOK {
		t.Fatalf("render = %d", resp.StatusCode)
	}
	assertNoBusinessLines(t, logs)
}
