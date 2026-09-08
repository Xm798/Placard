package ctxlog

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Outcome enum, aligned to the audit vocabulary the repo already writes:
// "success" / "denied" (internal/handler/authz.go, internal/linkpreview/audit.go)
// and "failure" (internal/handler/auth.go, all via auditLogin).
//
// It is "failure", not "error": the string "error" appears nowhere as an audit
// outcome, and introducing it here would fork the very vocabulary these fields
// exist to keep aligned.
const (
	OutcomeSuccess = "success"
	OutcomeDenied  = "denied"
	OutcomeFailure = "failure"
)

// DurMS reports the duration of a completed operation in whole milliseconds.
//
// These are constructors rather than field-name constants on purpose: a
// constant pins the name but not the type or the unit, so zap.Duration(name, d)
// would still compile and still emit float seconds. A constructor makes the
// wrong shape unrepresentable.
func DurMS(d time.Duration) zap.Field {
	return zap.Int64("duration_ms", d.Milliseconds())
}

// TTFBMS reports time-to-first-byte in whole milliseconds, for streaming reads
// where the operation is not finished when the call returns.
func TTFBMS(d time.Duration) zap.Field {
	return zap.Int64("ttfb_ms", d.Milliseconds())
}

// ReqID emits the correlation key carried by ctx (empty string when absent).
func ReqID(ctx context.Context) zap.Field {
	return zap.String("request_id", RequestID(ctx))
}

// Entry emits the entrypoint dimension carried by ctx (empty string when absent).
func Entry(ctx context.Context) zap.Field {
	return zap.String("entrypoint", Entrypoint(ctx))
}

// Op emits the logical operation name, e.g. "publish" or "storage.get_object".
func Op(op string) zap.Field {
	return zap.String("operation", op)
}

// NanoID emits the public id of a file. Acceptable on low-frequency business
// and event Info lines (the audit table stores it in plaintext already), never
// on a hot Debug path.
func NanoID(id string) zap.Field {
	return zap.String("nano_id", id)
}

// Outcome emits one of the Outcome* constants. Not free text.
func Outcome(o string) zap.Field {
	return zap.String("outcome", o)
}
