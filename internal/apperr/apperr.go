// Package apperr defines typed application errors that translate to HTTP
// responses via Fiber's ErrorHandler (see main.newFiberApp).
//
// A *Error carries a stable machine Code (for the frontend), an HTTPStatus, and
// a user-safe Message. The optional Cause is wrapped for logging only — it is
// never serialized into the response. Use New for a plain error and Wrap to
// attach a Cause.
//
// Security: the response body is built from Code+Message only. Error() (which
// includes Cause) must never be written to a response — it is for logs. Raw
// infrastructure errors (DB/Redis) may carry SQL fragments or connection
// strings; do NOT Wrap those — use Internal(msg) and drop the Cause.
package apperr

import "fmt"

// Error is a typed application error.
type Error struct {
	Code       string // stable machine code, e.g. "not_found"
	HTTPStatus int    // HTTP status code to return
	Message    string // user-safe message, safe to serialize
	Cause      error  // wrapped error, logged only, never serialized
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// New builds a plain typed error with no Cause.
func New(code string, status int, msg string) *Error {
	return &Error{Code: code, HTTPStatus: status, Message: msg}
}

// Wrap attaches a Cause. ONLY wrap already-sanitized errors — infrastructure
// errors (raw DB/Redis) may contain SQL fragments or connection strings that
// would leak into logs via Error(). For those, use Internal(msg) and drop the
// Cause.
func Wrap(err error, code string, status int, msg string) *Error {
	return &Error{Code: code, HTTPStatus: status, Message: msg, Cause: err}
}

// Predefined constructors. The variadic msg overrides the default message; omit
// it for the standard wording.
func Unauthorized(msg ...string) *Error {
	return New("unauthorized", 401, first(msg, "unauthorized"))
}
func PermissionDenied(msg ...string) *Error {
	return New("permission_denied", 403, first(msg, "permission denied"))
}
func NotFound(msg ...string) *Error {
	return New("not_found", 404, first(msg, "not found"))
}
func Validation(msg string) *Error {
	return New("validation", 400, msg)
}
func Conflict(msg ...string) *Error {
	return New("conflict", 409, first(msg, "already exists"))
}
func RateLimited(msg ...string) *Error {
	return New("rate_limited", 429, first(msg, "rate limited"))
}

// UpgradeRequired is the server.min_cli_version gate: the caller identified
// itself as a placard CLI older than this instance accepts. The message names
// both versions and the command that fixes it, because the CLI being rejected
// predates any knowledge of this code and can only print what it is handed.
func UpgradeRequired(msg string) *Error {
	return New("upgrade_required", 426, msg)
}
func Storage(msg string) *Error {
	return New("storage_failed", 502, msg)
}
func Unavailable(msg ...string) *Error {
	return New("unavailable", 503, first(msg, "service unavailable"))
}
func Internal(msg string) *Error {
	return New("internal", 500, msg)
}

// first returns the first non-empty msg, or def when none is provided.
func first(ss []string, def string) string {
	if len(ss) > 0 && ss[0] != "" {
		return ss[0]
	}
	return def
}
