package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/testutil"
	"github.com/Xm798/placard/internal/userctx"
)

func getPrefs(t *testing.T, app *fiber.App) (int, []byte) {
	t.Helper()
	return doJSON(t, app, "GET", "/api/prefs", "")
}

func putPrefs(t *testing.T, app *fiber.App, body string) (int, []byte) {
	t.Helper()
	return doJSON(t, app, "PUT", "/api/prefs", body)
}

func putPrefsValue(t *testing.T, app *fiber.App, vis string) (int, []byte) {
	t.Helper()
	body, _ := json.Marshal(struct {
		DefaultVisibility string `json:"default_visibility"`
	}{DefaultVisibility: vis})
	return putPrefs(t, app, string(body))
}

// --- GET /api/prefs ---

func TestGetNoUserRow(t *testing.T) {
	app, deps := newTestApp(t)

	code, b := getPrefs(t, app)
	if code != fiber.StatusOK {
		t.Fatalf("get prefs status = %d, want 200", code)
	}

	var resp struct {
		DefaultVisibility string `json:"default_visibility"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.DefaultVisibility != "link" {
		t.Errorf("default_visibility = %q, want 'link'", resp.DefaultVisibility)
	}

	_, err := deps.Users.Get(context.Background(), testAuthzID)
	if err != gorm.ErrRecordNotFound {
		t.Errorf("Users.Get should still return ErrRecordNotFound, got %v", err)
	}
}

func TestGetUserRowPrivate(t *testing.T) {
	app, deps := newTestApp(t)

	seedUserRow(t, deps.DB, model.User{ID: testAuthzID, DisplayName: "Alice"})

	if err := deps.DB.Model(&model.User{}).
		Where("id = ?", testAuthzID).
		Update("default_visibility", "private").Error; err != nil {
		t.Fatalf("set to private: %v", err)
	}

	code, b := getPrefs(t, app)
	if code != fiber.StatusOK {
		t.Fatalf("get prefs status = %d, want 200", code)
	}

	var resp struct {
		DefaultVisibility string `json:"default_visibility"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.DefaultVisibility != "private" {
		t.Errorf("default_visibility = %q, want 'private'", resp.DefaultVisibility)
	}
}

// --- PUT /api/prefs ---

func TestPutValidPrivate(t *testing.T) {
	app, deps := newTestApp(t)

	seedUserRow(t, deps.DB, model.User{ID: testAuthzID, DisplayName: "Alice"})

	code, b := putPrefsValue(t, app, "private")
	if code != fiber.StatusNoContent {
		t.Fatalf("put prefs status = %d, body = %s, want 204", code, b)
	}

	u, err := deps.Users.Get(context.Background(), testAuthzID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if u.DefaultVisibility != "private" {
		t.Errorf("persisted default_visibility = %q, want 'private'", u.DefaultVisibility)
	}
}

func TestPutValidLink(t *testing.T) {
	app, deps := newTestApp(t)

	seedUserRow(t, deps.DB, model.User{ID: testAuthzID, DisplayName: "Alice"})

	code, b := putPrefsValue(t, app, "link")
	if code != fiber.StatusNoContent {
		t.Fatalf("put prefs status = %d, body = %s, want 204", code, b)
	}

	u, err := deps.Users.Get(context.Background(), testAuthzID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if u.DefaultVisibility != "link" {
		t.Errorf("persisted default_visibility = %q, want 'link'", u.DefaultVisibility)
	}
}

// TestPutSameValueTwiceIsIdempotent regression-tests the RowsAffected==0
// ambiguity: without clientFoundRows, re-saving the value the row already
// has changes zero columns (default_visibility unchanged, and UpdateTime is
// second-resolution so it can also land unchanged within the same second).
// Both PUTs fire back-to-back with no sleep, so this reproduces the race
// deterministically instead of relying on second-boundary timing luck. Both
// must return 204, never the missing-row 503.
func TestPutSameValueTwiceIsIdempotent(t *testing.T) {
	app, deps := newTestApp(t)

	seedUserRow(t, deps.DB, model.User{ID: testAuthzID, DisplayName: "Alice"})

	// New row's default_visibility already defaults to 'link' (DDL default),
	// so this first PUT itself is already a same-value save relative to the
	// freshly inserted row.
	for i := 0; i < 2; i++ {
		code, b := putPrefsValue(t, app, "link")
		if code != fiber.StatusNoContent {
			t.Fatalf("put #%d prefs status = %d, body = %s, want 204", i+1, code, b)
		}
	}

	u, err := deps.Users.Get(context.Background(), testAuthzID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if u.DefaultVisibility != "link" {
		t.Errorf("persisted default_visibility = %q, want 'link'", u.DefaultVisibility)
	}
}

func TestPutRestrictedIsError(t *testing.T) {
	app, deps := newTestApp(t)

	seedUserRow(t, deps.DB, model.User{ID: testAuthzID, DisplayName: "Alice"})

	code, b := putPrefsValue(t, app, "restricted")
	if code != fiber.StatusBadRequest {
		t.Fatalf("put prefs with 'restricted' status = %d, want 400; body = %s", code, b)
	}

	var errResp dto.ErrorResponse
	if err := json.Unmarshal(b, &errResp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if errResp.Code != "validation" {
		t.Errorf("error code = %q, want 'validation'", errResp.Code)
	}
}

func TestPutInvalidValueIs400(t *testing.T) {
	app, deps := newTestApp(t)

	seedUserRow(t, deps.DB, model.User{ID: testAuthzID, DisplayName: "Alice"})

	code, b := putPrefsValue(t, app, "bogus")
	if code != fiber.StatusBadRequest {
		t.Fatalf("put prefs with 'bogus' status = %d, want 400; body = %s", code, b)
	}
}

func TestPutNoUserRowIs503(t *testing.T) {
	app, deps := newTestApp(t)

	code, b := putPrefsValue(t, app, "private")
	if code != fiber.StatusServiceUnavailable {
		t.Fatalf("put prefs with no user row status = %d, body = %s, want 503", code, b)
	}

	var errResp dto.ErrorResponse
	if err := json.Unmarshal(b, &errResp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if errResp.Code != "unavailable" {
		t.Errorf("error code = %q, want 'unavailable'", errResp.Code)
	}

	_, err := deps.Users.Get(context.Background(), testAuthzID)
	if err != gorm.ErrRecordNotFound {
		t.Errorf("Users.Get should return ErrRecordNotFound, got %v", err)
	}
}

// --- /api/me extension ---

func TestMeResponseHasAuthzIDAndRestrictedEnabled(t *testing.T) {
	app, _ := newTestApp(t)

	code, b := doJSON(t, app, "GET", "/api/me", "")
	if code != fiber.StatusOK {
		t.Fatalf("get /api/me status = %d, want 200", code)
	}

	var resp struct {
		DisplayName string `json:"display_name"`
		AvatarURL   string `json:"avatar_url"`
		AuthzID     string `json:"authz_id"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("unmarshal /api/me response: %v", err)
	}

	if resp.AuthzID != testAuthzID {
		t.Errorf("authz_id = %q, want %q", resp.AuthzID, testAuthzID)
	}

	if resp.DisplayName != "Alice" {
		t.Errorf("display_name = %q, want 'Alice'", resp.DisplayName)
	}

	if resp.AvatarURL == "" {
		t.Logf("avatar_url is empty (expected for test user)")
	}
}

func TestMePATLoadsLatestDisplayProfile(t *testing.T) {
	db := testutil.OpenTestDB(t)
	users := repo.NewUserRepo(db)
	seedUserRow(t, db, model.User{
		ID:              testAuthzID,
		DisplayName:     "Alice Updated",
		AvatarSourceURL: "https://example.com/alice.png",
	})

	h := New(Deps{Users: users, Tokens: &repo.TokenRepo{}})
	app := fiber.New()
	app.Get("/me", func(c *fiber.Ctx) error {
		userctx.Set(c, userctx.Identity{
			AuthzID:     testAuthzID,
			AuthChannel: userctx.ChannelPAT,
		})
		return h.Me(c)
	})

	code, b := doJSON(t, app, "GET", "/me", "")
	if code != fiber.StatusOK {
		t.Fatalf("get /me status = %d, want 200", code)
	}
	var resp struct {
		DisplayName string `json:"display_name"`
		AvatarURL   string `json:"avatar_url"`
		AuthzID     string `json:"authz_id"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("unmarshal /me response: %v", err)
	}
	if resp.DisplayName != "Alice Updated" {
		t.Fatalf("PAT display profile = %+v, want the row's display name", resp)
	}
	// avatar_source_url is an address the server fetches from — under the
	// Gravatar fallback, one derived from the account's email — so /me must
	// never echo it. The frontend builds the proxy path from authz_id.
	if resp.AvatarURL != "" {
		t.Fatalf("avatar_url = %q, want empty: the source URL is never handed out", resp.AvatarURL)
	}
	if resp.AuthzID != testAuthzID {
		t.Fatalf("authz_id = %q, want %q", resp.AuthzID, testAuthzID)
	}
}
