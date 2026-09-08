package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"
)

// ClientIP returns the trusted client IP for auditing.
//
// Prefer the single-valued X-Real-IP set by the reverse proxy; if absent, take
// the RIGHTMOST non-empty entry of X-Forwarded-For — never the leftmost or the
// whole string, which the client can spoof.
//
// This assumes exactly one trusted inbound hop adjacent to the backend, so the
// rightmost XFF entry is the address that hop observed. Deployments with a
// second trusted hop (a mesh sidecar, say) need a fixed offset instead of the
// rightmost entry.
func ClientIP(c *fiber.Ctx) string {
	if realIP := strings.TrimSpace(c.Get("X-Real-IP")); realIP != "" {
		return realIP
	}

	xff := c.Get(fiber.HeaderXForwardedFor)
	if xff == "" {
		return ""
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if ip := strings.TrimSpace(parts[i]); ip != "" {
			return ip
		}
	}
	return ""
}
