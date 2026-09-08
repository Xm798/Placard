package middleware

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/gofiber/fiber/v2"
)

// requestIDLocalsKey is the fiber.Locals key holding the per-request id.
const requestIDLocalsKey = "request_id"

// RequestID assigns a request id used to correlate all log lines for a request
// in ELK. An inbound X-Request-ID is honored; otherwise a new id is generated
// from crypto/rand with a "req_" prefix. The id is echoed back in the
// X-Request-ID response header and published two ways:
//
//   - on fiber Locals, for RequestIDFromCtx and its existing callers;
//   - on the UserContext, so it reaches everything downstream of the handler
//     (repo -> GORM -> storage), which Locals cannot.
//
// Invariant — the UserContext must never become cancellable. What is installed
// here is a pure context.WithValue chain over the background context, and the
// HTTP middleware chain is forbidden from introducing context.WithCancel,
// WithTimeout or WithDeadline on it. GET /s/:id/render streams a storage body bound
// to this context and streams it after the handler has returned; a cancellable
// UserContext would cut that stream when the handler returns, truncating the
// rendered page with no error and no log line.
func RequestID() fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Get(fiber.HeaderXRequestID)
		if id == "" {
			b := make([]byte, 16)
			_, _ = rand.Read(b)
			id = "req_" + hex.EncodeToString(b)
		}
		c.Locals(requestIDLocalsKey, id)
		c.SetUserContext(ctxlog.WithEntrypoint(ctxlog.WithRequestID(c.UserContext(), id), ctxlog.EntrypointHTTP))
		c.Set(fiber.HeaderXRequestID, id)
		return c.Next()
	}
}

// RequestIDFromCtx returns the request id stored on the context, or "".
func RequestIDFromCtx(c *fiber.Ctx) string {
	if v, ok := c.Locals(requestIDLocalsKey).(string); ok {
		return v
	}
	return ""
}
