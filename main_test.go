package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/version"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestVersionRouteReturnsMasterCommit(t *testing.T) {
	originalVersion, originalCommit, originalBranch := version.Version, version.GitCommit, version.GitBranch
	t.Cleanup(func() {
		version.Version, version.GitCommit, version.GitBranch = originalVersion, originalCommit, originalBranch
	})
	version.Version = "v0.4.0-1-g2189403"
	version.GitCommit = "2189403"
	version.GitBranch = "master"

	app := fiber.New()
	registerHealthRoutes(app, nil, nil)
	response, err := app.Test(httptest.NewRequest("GET", "/api/version", nil))
	if err != nil {
		t.Fatalf("GET /api/version: %v", err)
	}
	defer response.Body.Close()

	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Version != "master-2189403" {
		t.Fatalf("version = %q, want %q", body.Version, "master-2189403")
	}
}

// The warning is for an operator who can act on it: a base_url that other
// hosts share a parent domain with. Loopback — what the compose quickstart
// ships — has none, so it must stay quiet.
func TestWarnPlainHTTPSkipsLoopback(t *testing.T) {
	cases := []struct {
		baseURL  string
		wantWarn bool
	}{
		{"https://placard.example.com", false},
		{"http://localhost:8080", false},
		{"http://127.0.0.1:8080", false},
		{"http://[::1]:8080", false},
		{"http://192.168.1.10:8080", true},
		{"HTTP://box.lan:8080", true},
	}
	for _, tc := range cases {
		cfg := &config.Config{}
		cfg.Server.BaseURL = tc.baseURL

		core, logs := observer.New(zapcore.WarnLevel)
		warnPlainHTTP(cfg, zap.New(core))

		if got := logs.Len() > 0; got != tc.wantWarn {
			t.Errorf("warnPlainHTTP(%q) warned = %v, want %v", tc.baseURL, got, tc.wantWarn)
		}
	}
}
