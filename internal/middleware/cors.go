package middleware

import (
	"strings"

	"github.com/Xm798/placard/internal/config"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

// CORS configures cross-origin access with an explicit allowlist (main origin
// only) and credentials enabled. It NEVER uses "*": a wildcard origin combined
// with credentials is forbidden, and the rendering path is a same-origin proxy
// rather than a cross-origin /api call, so no other origin belongs in the
// allowlist.
func CORS(cfg config.CORSConfig) fiber.Handler {
	return cors.New(cors.Config{
		AllowOrigins:     strings.Join(cfg.AllowedOrigins, ","),
		AllowCredentials: true,
		AllowMethods:     "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization,X-Requested-With",
	})
}
