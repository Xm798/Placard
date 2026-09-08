package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Xm798/placard/internal/config"
)

const htmlType = "text/html; charset=utf-8"

// backend is one Client implementation under the shared contract below. new is
// called per subtest so cases never share state.
type backend struct {
	name string
	new  func(t *testing.T) Client
}

// contractBackends returns every Client implementation this build can exercise.
// The s3 backend needs a live S3-compatible service (MinIO in CI): it joins the
// list only when TEST_S3_ENDPOINT / TEST_S3_BUCKET / TEST_S3_ACCESS_KEY /
// TEST_S3_SECRET_KEY are all set, and is skipped otherwise.
func contractBackends() []backend {
	backends := []backend{
		{
			name: "stub",
			new:  func(*testing.T) Client { return NewStubClient() },
		},
		{
			name: "local",
			new: func(t *testing.T) Client {
				c, err := NewLocalClient(config.LocalStorageConfig{Dir: t.TempDir()})
				if err != nil {
					t.Fatalf("NewLocalClient: %v", err)
				}
				return c
			},
		},
	}

	s3cfg := config.S3Config{
		Endpoint:  os.Getenv("TEST_S3_ENDPOINT"),
		Region:    envOrDefault("TEST_S3_REGION", "us-east-1"),
		Bucket:    os.Getenv("TEST_S3_BUCKET"),
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
		PathStyle: true,
	}
	if s3cfg.Endpoint != "" && s3cfg.Bucket != "" && s3cfg.AccessKey != "" && s3cfg.SecretKey != "" {
		backends = append(backends, backend{
			name: "s3",
			new: func(t *testing.T) Client {
				c, err := NewS3Client(s3cfg)
				if err != nil {
					t.Fatalf("NewS3Client: %v", err)
				}
				return c
			},
		})
	}
	return backends
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// runContract runs fn against every available backend as a named subtest.
func runContract(t *testing.T, fn func(t *testing.T, c Client)) {
	t.Helper()
	for _, b := range contractBackends() {
		t.Run(b.name, func(t *testing.T) {
			fn(t, b.new(t))
		})
	}
}

// TestContract_PutGetRoundTrip asserts GetObject serves back exactly the bytes
// PutObject was given — the same-origin render proxy streams them to the
// viewer unchanged, so any rewriting here would corrupt published pages.
func TestContract_PutGetRoundTrip(t *testing.T) {
	runContract(t, func(t *testing.T, c Client) {
		const key = "2026/07/abc-1.html"
		const body = "<!DOCTYPE html><html><body>hi</body></html>"

		if err := c.PutObject(context.Background(), key, strings.NewReader(body), htmlType); err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		if got := mustGet(t, c, key); string(got) != body {
			t.Errorf("round-trip body = %q, want %q", got, body)
		}
	})
}

// TestContract_PutOverwrites asserts a second Put on the same key replaces the
// object rather than appending to or keeping the first body. Avatars rely on
// this: they live at a fixed avatars/{id} key that every refresh overwrites.
func TestContract_PutOverwrites(t *testing.T) {
	runContract(t, func(t *testing.T, c Client) {
		const key = "avatars/u_alice"
		ctx := context.Background()
		if err := c.PutObject(ctx, key, strings.NewReader("first"), htmlType); err != nil {
			t.Fatalf("PutObject first: %v", err)
		}
		if err := c.PutObject(ctx, key, strings.NewReader("second"), htmlType); err != nil {
			t.Fatalf("PutObject second: %v", err)
		}
		if got := mustGet(t, c, key); string(got) != "second" {
			t.Errorf("body after overwrite = %q, want %q", got, "second")
		}
	})
}

// TestContract_GetMissIsErrNotFound asserts an unknown key is reported as
// ErrNotFound, which is what the render/avatar handlers turn into a clean 404
// instead of a 500.
func TestContract_GetMissIsErrNotFound(t *testing.T) {
	runContract(t, func(t *testing.T, c Client) {
		_, err := c.GetObject(context.Background(), "2026/07/missing-1.html")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetObject miss err = %v, want errors.Is ErrNotFound", err)
		}
	})
}

// TestContract_DeleteRemovesObject asserts Delete makes the object unreadable,
// so the cleanup cron actually reclaims space.
func TestContract_DeleteRemovesObject(t *testing.T) {
	runContract(t, func(t *testing.T, c Client) {
		const key = "2026/07/doomed-1.html"
		ctx := context.Background()
		if err := c.PutObject(ctx, key, strings.NewReader("bye"), htmlType); err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		if err := c.DeleteObject(ctx, key); err != nil {
			t.Fatalf("DeleteObject: %v", err)
		}
		if _, err := c.GetObject(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetObject after delete err = %v, want errors.Is ErrNotFound", err)
		}
	})
}

// TestContract_DeleteMissingIsIdempotent asserts deleting an absent key
// succeeds: the cron reclaim queue retries keys whose object is already gone
// and must never wedge on one.
func TestContract_DeleteMissingIsIdempotent(t *testing.T) {
	runContract(t, func(t *testing.T, c Client) {
		if err := c.DeleteObject(context.Background(), "2026/07/never-existed-1.html"); err != nil {
			t.Fatalf("DeleteObject on missing key: %v", err)
		}
	})
}

// TestContract_LargeObjectStreams asserts a body far past any internal buffer
// size round-trips byte-identically — publishes go up to the upload limit
// (10MB by default).
func TestContract_LargeObjectStreams(t *testing.T) {
	body := make([]byte, 5<<20)
	if _, err := rand.Read(body); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	runContract(t, func(t *testing.T, c Client) {
		const key = "2026/07/large-1.html"
		if err := c.PutObject(context.Background(), key, bytes.NewReader(body), htmlType); err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		if got := mustGet(t, c, key); !bytes.Equal(got, body) {
			t.Errorf("large round-trip differs: got %d bytes, want %d", len(got), len(body))
		}
	})
}

// TestContract_RejectsTraversalKeys asserts every backend refuses keys that
// escape the key namespace. On the local backend this is what keeps an object
// inside the configured directory; the other backends reject them too so a key
// that is rejected in one deployment is never silently accepted in another.
func TestContract_RejectsTraversalKeys(t *testing.T) {
	keys := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"absolute", "/etc/passwd"},
		{"parent_segment", "../outside.html"},
		{"embedded_parent", "2026/../../outside.html"},
		{"trailing_parent", "2026/07/.."},
		{"dot_segment", "./2026/07/a.html"},
		{"backslash", `2026\07\a.html`},
	}
	runContract(t, func(t *testing.T, c Client) {
		ctx := context.Background()
		for _, tc := range keys {
			t.Run(tc.name, func(t *testing.T) {
				if err := c.PutObject(ctx, tc.key, strings.NewReader("x"), htmlType); !errors.Is(err, ErrInvalidKey) {
					t.Errorf("PutObject(%q) err = %v, want errors.Is ErrInvalidKey", tc.key, err)
				}
				if _, err := c.GetObject(ctx, tc.key); !errors.Is(err, ErrInvalidKey) {
					t.Errorf("GetObject(%q) err = %v, want errors.Is ErrInvalidKey", tc.key, err)
				}
				if err := c.DeleteObject(ctx, tc.key); !errors.Is(err, ErrInvalidKey) {
					t.Errorf("DeleteObject(%q) err = %v, want errors.Is ErrInvalidKey", tc.key, err)
				}
			})
		}
	})
}

func mustGet(t *testing.T, c Client, key string) []byte {
	t.Helper()
	rc, err := c.GetObject(context.Background(), key)
	if err != nil {
		t.Fatalf("GetObject(%q): %v", key, err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll(%q): %v", key, err)
	}
	return got
}
