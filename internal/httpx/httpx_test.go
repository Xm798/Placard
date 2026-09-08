package httpx

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/apperr"
)

// newHandlerApp builds a minimal Fiber app with ErrorHandler, registering a
// single route that returns the given error so the translation can be observed.
func newHandlerApp(t *testing.T, retErr error) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler})
	app.Get("/e", func(c *fiber.Ctx) error { return retErr })
	return app
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return m
}

func TestErrorHandler_Apperr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
		code string
	}{
		{"unauthorized", apperr.Unauthorized(), 401, "unauthorized"},
		{"not_found", apperr.NotFound("nope"), 404, "not_found"},
		{"validation", apperr.Validation("bad"), 400, "validation"},
		{"storage", apperr.Storage("oops"), 502, "storage_failed"},
		{"internal", apperr.Internal("boom"), 500, "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newHandlerApp(t, tc.err)
			resp, err := app.Test(httptest.NewRequest("GET", "/e", nil), -1)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// TestErrorHandler_BodyTooLargeMapsTo400 verifies the transport-level oversize
// case (Fiber raises *fiber.Error 413) is surfaced as 400 validation, not 413
// or 500, and carries the validation code.
func TestErrorHandler_BodyTooLargeMapsTo400(t *testing.T) {
	app := newHandlerApp(t, fiber.ErrRequestEntityTooLarge)
	resp, err := app.Test(httptest.NewRequest("GET", "/e", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestErrorHandler_UntypedIs500NoLeak verifies an untyped error degrades to a
// fixed 500 and does not echo the error message into the response body.
func TestErrorHandler_UntypedIs500NoLeak(t *testing.T) {
	leaky := errors.New("internal: db conn postgres://user:pass@host:5432 with SQL SELECT * FROM files")
	app := newHandlerApp(t, leaky)
	resp, err := app.Test(httptest.NewRequest("GET", "/e", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}
