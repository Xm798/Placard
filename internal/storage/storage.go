// Package storage wraps the object store Placard keeps published pages in.
//
// Rendering goes through a same-origin backend proxy: the viewer shell at
// /s/:id loads its iframe from GET /s/:id/render, which streams the HTML object
// back and sets Content-Disposition: inline itself. This is required because a
// backend need not persist the Content-Disposition supplied to PutObject, so an
// inline render can only be guaranteed by a proxy that owns the response
// headers. Buckets stay private: no backend here ever hands out a presigned
// URL.
//
// Isolation does not rely on a cross-origin object URL. The user HTML executes
// inside the shell's sandboxed iframe (sandbox="allow-scripts", an opaque
// origin) and the render response additionally carries a CSP sandbox header, so
// even a direct hit on the render URL lands in an opaque origin — never the
// primary origin. Objects are stored with Content-Type "text/html; charset=utf-8".
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned by GetObject when the key does not exist. Callers
// compare with errors.Is (the concrete error may wrap it with the key).
var ErrNotFound = errors.New("storage: object not found")

// TempSweeper is implemented by backends that stage a write through a temporary
// file the process can be killed in the middle of, stranding it: nothing
// references it, so neither the pending-delete queue nor any other reclaim path
// can see it. The cleanup cron calls it to keep those from accumulating.
//
// Only the local backend needs this. An interrupted s3 upload is the service's
// to garbage-collect, and the stub holds nothing on disk.
type TempSweeper interface {
	// SweepTemp removes staged files last modified before olderThan and
	// reports how many it deleted. The cutoff is what makes it safe to run
	// while writes are in flight, so callers must choose one comfortably
	// longer than the slowest possible PutObject.
	SweepTemp(ctx context.Context, olderThan time.Time) (int, error)
}

// Client is the storage surface the handlers depend on. The local (on-disk)
// and s3 backends satisfy it, selectable via configuration, as does the
// in-memory stub used by tests.
//
// The ctx is honoured, not decorative: the s3 backend hands it to
// aws-sdk-go-v2, which carries it into the outgoing request, so cancelling it
// aborts the in-flight HTTP call. Two rules follow from that.
//
// Rule 1 — a GetObject body outlives the call, but not its ctx. The returned
// io.ReadCloser stays bound to the ctx that produced it, so that ctx must stay
// live until the body has been fully copied. Never pass a ctx from
// context.WithTimeout with a `defer cancel()` alongside it: cancel() fires when
// the calling function returns, which for a streamed response is mid-body, and
// the reader dies silently — a truncated page, no error, no log line.
// handler/render.go is the call site this protects: it hands the ReadCloser to
// SendStream and fasthttp drains it after the handler has returned.
// (handler/versions.go and handler/user_avatar.go io.ReadAll inside the handler
// and are structurally immune.) If a GetObject deadline is ever wanted, the
// mechanism is to transfer ownership of cancel into the returned
// ReadCloser.Close() — handler/link_relay.go's withLinkRelay already wraps the
// render body and fasthttp guarantees Close() is called — never `defer`.
//
// Rule 2 — in a fiber handler, pass c.UserContext(), never c.Context().
// c.Context() is the pooled *fasthttp.RequestCtx, which fasthttp resets and
// returns to its pool once the response is written; handing it to the SDK is a
// use-after-return race on a pooled object. (main.go contains a
// context.WithTimeout(c.Context(), …) that reads like precedent. It is not.)
//
// PutObject and DeleteObject carry neither constraint — the SDK reads and
// closes their response before returning — so they are the safe place to hang
// future deadlines.
type Client interface {
	// PutObject stores data at key with the given Content-Type.
	PutObject(ctx context.Context, key string, data io.Reader, contentType string) error
	// GetObject returns a reader over the object stored at key. The caller is
	// responsible for closing the returned ReadCloser. A missing key returns an
	// error satisfying errors.Is(err, ErrNotFound). ctx must outlive the read of
	// the returned body — see Rule 1 above.
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
	// DeleteObject removes key. Idempotent: a not-found object is treated as
	// success (cron reclaim must never stick on an already-gone key).
	DeleteObject(ctx context.Context, key string) error
}
