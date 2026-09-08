package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/config"
)

func TestSessionCookieName(t *testing.T) {
	cases := []struct {
		baseURL string
		want    string
	}{
		{"https://placard.example.com", "__Host-placard_session"},
		// A LAN instance over plain HTTP: the prefix would make the browser
		// discard the cookie, so sign-in would never stick.
		{"http://192.168.1.10:8080", "placard_session"},
		{"HTTP://box.lan:8080", "placard_session"},
		{"HTTPS://PLACARD.EXAMPLE.COM", "__Host-placard_session"},
	}
	for _, tc := range cases {
		if got := SessionCookieName(tc.baseURL, "placard_session"); got != tc.want {
			t.Errorf("SessionCookieName(%q) = %q, want %q", tc.baseURL, got, tc.want)
		}
	}
}

// The __Host- prefix is honoured only alongside Secure, and the clearing
// cookie removes the original only when every attribute matches — so one
// predicate has to drive the name and both Set-Cookie headers.
func TestSessionCookieAttributesFollowBaseURL(t *testing.T) {
	cases := []struct {
		baseURL    string
		wantName   string
		wantSecure bool
	}{
		{"https://placard.example.com", "__Host-placard_session", true},
		{"http://192.168.1.10:8080", "placard_session", false},
	}
	for _, tc := range cases {
		cfg := &config.Config{}
		cfg.Server.BaseURL = tc.baseURL
		h := New(Deps{
			Cfg:           cfg,
			SessionCookie: SessionCookieName(tc.baseURL, "placard_session"),
		})

		app := fiber.New()
		app.Get("/set", func(c *fiber.Ctx) error {
			h.setSessionCookie(c, "sess_abc", 3600)
			return nil
		})
		app.Get("/clear", func(c *fiber.Ctx) error {
			h.clearSessionCookie(c)
			return nil
		})

		for _, path := range []string{"/set", "/clear"} {
			resp, err := app.Test(httptest.NewRequest("GET", path, nil))
			if err != nil {
				t.Fatalf("GET %s (%s): %v", path, tc.baseURL, err)
			}
			var found bool
			for _, c := range resp.Cookies() {
				if c.Name != tc.wantName {
					continue
				}
				found = true
				if c.Secure != tc.wantSecure {
					t.Errorf("%s on %s: Secure = %v, want %v", path, tc.baseURL, c.Secure, tc.wantSecure)
				}
				if c.Path != "/" {
					t.Errorf("%s on %s: Path = %q, want /", path, tc.baseURL, c.Path)
				}
				if !c.HttpOnly {
					t.Errorf("%s on %s: HttpOnly = false, want true", path, tc.baseURL)
				}
			}
			if !found {
				t.Errorf("%s on %s: no cookie named %q in %v", path, tc.baseURL, tc.wantName, resp.Cookies())
			}
		}
	}
}
