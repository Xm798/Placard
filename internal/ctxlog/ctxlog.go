// Package ctxlog carries the log correlation keys — request_id and entrypoint —
// through context.Context, and exports the cross-package zap field constructors
// that emit them. Call sites import this one package instead of pairing a
// context helper with a field-name constant table.
//
// Field rules this package encodes, and which hand-written fields elsewhere
// must follow too:
//
//   - snake_case field names;
//   - the unit belongs in the name (_ms, _bytes);
//   - numbers are logged as numeric types, so ELK can aggregate them;
//   - zap.Duration is never used anywhere — it serializes as float seconds,
//     which is ambiguous next to a millisecond field and useless to aggregate.
//
// The last rule is enforced by a test (noduration_test.go), not by convention.
package ctxlog

import "context"

// requestIDKey and entrypointKey are unexported struct types, so no other
// package can collide with these context values.
type requestIDKey struct{}

type entrypointKey struct{}

// Entrypoint kinds. This is the low-cardinality dimension that distinguishes
// the four kinds of work unit producing log lines; request_id stays the single
// correlation key across all of them.
const (
	EntrypointHTTP  = "http"
	EntrypointEvent = "event"
	EntrypointCron  = "cron"
	EntrypointCLI   = "cli"
)

// WithRequestID returns a copy of ctx carrying the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request id carried by ctx, or "" when absent.
// Absence is fail-open and deliberately silent: background paths legitimately
// have no request id, so warning here would be pure noise.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// WithEntrypoint returns a copy of ctx carrying the entrypoint kind, one of the
// Entrypoint* constants above.
func WithEntrypoint(ctx context.Context, kind string) context.Context {
	return context.WithValue(ctx, entrypointKey{}, kind)
}

// Entrypoint returns the entrypoint kind carried by ctx, or "" when absent.
// Fail-open and silent, for the same reason as RequestID.
func Entrypoint(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	kind, _ := ctx.Value(entrypointKey{}).(string)
	return kind
}
