package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/userctx"
)

func TestBrowserOnly(t *testing.T) {
	tests := []struct {
		name           string
		channel        string
		wantStatusCode int
	}{
		// Allowed channels
		{"session channel", "session", 200},
		{"dev_mock channel", "dev_mock", 200},
		// Anonymous is allowed: share links are open to visitors with no
		// account, and the handler behind this decides what they may see.
		{"empty channel (anonymous)", "", 200},

		// Rejected channels
		{"pat channel", "pat", 403},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
			app.Get("/test", func(c *fiber.Ctx) error {
				if tt.channel != "" {
					userctx.Set(c, userctx.Identity{AuthChannel: tt.channel})
				}
				return c.Next()
			}, BrowserOnly(), func(c *fiber.Ctx) error {
				return c.SendString("ok")
			})

			req := httptest.NewRequest("GET", "/test", nil)
			resp, _ := app.Test(req)
			if resp.StatusCode != tt.wantStatusCode {
				t.Errorf("status code: got %d, want %d", resp.StatusCode, tt.wantStatusCode)
			}
		})
	}
}

func TestSessionOnly(t *testing.T) {
	tests := []struct {
		name           string
		channel        string
		wantStatusCode int
	}{
		// Allowed channels
		{"session channel", "session", 200},
		{"dev_mock channel", "dev_mock", 200},

		// Rejected channels
		{"pat channel", "pat", 403},
		{"empty channel (unauthenticated)", "", 403},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
			app.Get("/test", func(c *fiber.Ctx) error {
				if tt.channel != "" {
					userctx.Set(c, userctx.Identity{AuthChannel: tt.channel})
				}
				return c.Next()
			}, SessionOnly(), func(c *fiber.Ctx) error {
				return c.SendString("ok")
			})

			req := httptest.NewRequest("GET", "/test", nil)
			resp, _ := app.Test(req)
			if resp.StatusCode != tt.wantStatusCode {
				t.Errorf("status code: got %d, want %d", resp.StatusCode, tt.wantStatusCode)
			}
		})
	}
}
