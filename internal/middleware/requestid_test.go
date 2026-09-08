package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/ctxlog"
)

// The generated id must reach both carriers: Locals (existing callers) and the
// UserContext (everything downstream of the handler).
func TestRequestIDPublishesToLocalsAndUserContext(t *testing.T) {
	var locals, ctxID, entry string

	app := fiber.New()
	app.Use(RequestID())
	app.Get("/x", func(c *fiber.Ctx) error {
		locals = RequestIDFromCtx(c)
		ctxID = ctxlog.RequestID(c.UserContext())
		entry = ctxlog.Entrypoint(c.UserContext())
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest("GET", "/x", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	header := resp.Header.Get(fiber.HeaderXRequestID)

	if locals == "" {
		t.Fatal("Locals request id is empty")
	}
	if ctxID != locals {
		t.Errorf("UserContext request id = %q, want %q (Locals)", ctxID, locals)
	}
	if header != locals {
		t.Errorf("X-Request-ID header = %q, want %q", header, locals)
	}
	if entry != ctxlog.EntrypointHTTP {
		t.Errorf("entrypoint = %q, want %q", entry, ctxlog.EntrypointHTTP)
	}
}

// An inbound X-Request-ID is honored on the UserContext too, not just Locals.
func TestRequestIDHonorsInboundHeader(t *testing.T) {
	var ctxID string

	app := fiber.New()
	app.Use(RequestID())
	app.Get("/x", func(c *fiber.Ctx) error {
		ctxID = ctxlog.RequestID(c.UserContext())
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(fiber.HeaderXRequestID, "req_inbound")
	if _, err := app.Test(req); err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if ctxID != "req_inbound" {
		t.Errorf("UserContext request id = %q, want %q", ctxID, "req_inbound")
	}
}

// Guards the invariant documented on RequestID: the UserContext must stay a
// pure WithValue chain. GET /s/:id/render streams an object-store body bound to it after
// the handler returns, so a cancellable UserContext would silently truncate
// rendered pages.
func TestUserContextIsNotCancellable(t *testing.T) {
	var deadlineOK, doneNil bool

	app := fiber.New()
	app.Use(RequestID())
	app.Get("/x", func(c *fiber.Ctx) error {
		ctx := c.UserContext()
		_, hasDeadline := ctx.Deadline()
		deadlineOK = !hasDeadline
		doneNil = ctx.Done() == nil
		return c.SendStatus(fiber.StatusNoContent)
	})

	if _, err := app.Test(httptest.NewRequest("GET", "/x", nil)); err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if !deadlineOK {
		t.Error("UserContext carries a deadline; see the invariant on RequestID")
	}
	if !doneNil {
		t.Error("UserContext is cancellable; see the invariant on RequestID")
	}
}

// Sanity check that the middleware builds on whatever UserContext it is given
// rather than replacing it, so an outer wrapper's values survive.
func TestRequestIDPreservesExistingContextValues(t *testing.T) {
	type outerKey struct{}
	var outer any

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(c.UserContext(), outerKey{}, "kept"))
		return c.Next()
	})
	app.Use(RequestID())
	app.Get("/x", func(c *fiber.Ctx) error {
		outer = c.UserContext().Value(outerKey{})
		return c.SendStatus(fiber.StatusNoContent)
	})

	if _, err := app.Test(httptest.NewRequest("GET", "/x", nil)); err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if outer != "kept" {
		t.Errorf("outer ctx value = %v, want %q", outer, "kept")
	}
}
