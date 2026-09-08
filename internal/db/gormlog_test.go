package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/ctxlog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// observedGormLogger builds the adapter over an observer core, bypassing the
// global logger so the assertions here never depend on logger.Init.
func observedGormLogger() (gormlogger.Interface, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	log := zap.New(core)
	return newZapGormLoggerWith(func() *zap.Logger { return log }), logs
}

// correlatedCtx is what a request-scoped ctx looks like by the time it reaches
// GORM (middleware → handler → repo → db.WithContext).
func correlatedCtx() context.Context {
	ctx := ctxlog.WithRequestID(context.Background(), "req_abc123")
	return ctxlog.WithEntrypoint(ctx, ctxlog.EntrypointHTTP)
}

func TestTraceSlowQueryWarnsWithCorrelation(t *testing.T) {
	gl, logs := observedGormLogger()

	begin := time.Now().Add(-2 * slowThreshold)
	gl.Trace(correlatedCtx(), begin, func() (string, int64) {
		return "SELECT * FROM `file` WHERE nano_id = ?", 3
	}, nil)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 log line, got %d: %v", len(entries), entries)
	}
	e := entries[0]
	if e.Level != zapcore.WarnLevel {
		t.Errorf("level = %v, want warn", e.Level)
	}
	got := e.ContextMap()
	if got["request_id"] != "req_abc123" {
		t.Errorf("request_id = %v, want req_abc123", got["request_id"])
	}
	if got["entrypoint"] != ctxlog.EntrypointHTTP {
		t.Errorf("entrypoint = %v, want %s", got["entrypoint"], ctxlog.EntrypointHTTP)
	}
	if got["rows"] != int64(3) {
		t.Errorf("rows = %#v, want int64(3)", got["rows"])
	}
	if got["sql"] != "SELECT * FROM `file` WHERE nano_id = ?" {
		t.Errorf("sql = %v", got["sql"])
	}
}

// TestTraceEmitsDurationMSAsInt64 pins the §10 rename: the field is
// duration_ms in whole milliseconds, not "took", and not a float-seconds
// zap.Duration. A wrong-typed field still logs, so only the type check catches
// a regression here.
func TestTraceEmitsDurationMSAsInt64(t *testing.T) {
	gl, logs := observedGormLogger()

	begin := time.Now().Add(-500 * time.Millisecond)
	gl.Trace(correlatedCtx(), begin, func() (string, int64) { return "SELECT 1", 0 }, nil)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 log line, got %d", len(entries))
	}
	got := entries[0].ContextMap()
	if _, ok := got["took"]; ok {
		t.Errorf("field took still emitted; it was renamed to duration_ms")
	}
	ms, ok := got["duration_ms"].(int64)
	if !ok {
		t.Fatalf("duration_ms = %#v (%T), want int64", got["duration_ms"], got["duration_ms"])
	}
	if ms < 500 || ms > 5000 {
		t.Errorf("duration_ms = %d, want ~500 (whole milliseconds)", ms)
	}
}

func TestTraceErrorLogsAtErrorWithCorrelation(t *testing.T) {
	gl, logs := observedGormLogger()

	wantErr := errors.New("UNIQUE constraint failed: file.nano_id")
	gl.Trace(correlatedCtx(), time.Now(), func() (string, int64) {
		return "INSERT INTO `file` (`nano_id`) VALUES (?)", 0
	}, wantErr)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 log line, got %d", len(entries))
	}
	e := entries[0]
	if e.Level != zapcore.ErrorLevel {
		t.Errorf("level = %v, want error", e.Level)
	}
	got := e.ContextMap()
	if got["request_id"] != "req_abc123" {
		t.Errorf("request_id = %v, want req_abc123", got["request_id"])
	}
	if got["entrypoint"] != ctxlog.EntrypointHTTP {
		t.Errorf("entrypoint = %v, want %s", got["entrypoint"], ctxlog.EntrypointHTTP)
	}
	if _, ok := got["duration_ms"].(int64); !ok {
		t.Errorf("duration_ms = %#v, want int64", got["duration_ms"])
	}
	if got["error"] != wantErr.Error() {
		t.Errorf("error = %v, want %v", got["error"], wantErr)
	}
}

// TestTraceNormalQuerySilent covers the hot path: at the default Warn level a
// fast, successful query must produce nothing at all.
func TestTraceNormalQuerySilent(t *testing.T) {
	gl, logs := observedGormLogger()

	gl.Trace(correlatedCtx(), time.Now(), func() (string, int64) { return "SELECT 1", 1 }, nil)

	if n := logs.Len(); n != 0 {
		t.Fatalf("want silence on the normal path, got %d line(s): %v", n, logs.All())
	}
}

// TestTraceRecordNotFoundSilent covers the owner-scope miss (GetOwned → 404),
// which repos translate via errors.Is and must not log — even when slow.
func TestTraceRecordNotFoundSilent(t *testing.T) {
	gl, logs := observedGormLogger()

	begin := time.Now().Add(-2 * slowThreshold)
	gl.Trace(correlatedCtx(), begin, func() (string, int64) {
		t.Error("fc() called on the record-not-found path; SQL formatting should be skipped")
		return "", 0
	}, gormlogger.ErrRecordNotFound)

	if n := logs.Len(); n != 0 {
		t.Fatalf("want silence on record-not-found, got %d line(s): %v", n, logs.All())
	}
}

// TestTraceInfoLevelLogsNormalQuery keeps the opt-in verbose path working: at
// Info level every statement is logged, with the same correlation fields.
func TestTraceInfoLevelLogsNormalQuery(t *testing.T) {
	gl, logs := observedGormLogger()
	gl = gl.LogMode(gormlogger.Info)

	gl.Trace(correlatedCtx(), time.Now(), func() (string, int64) { return "SELECT 1", 1 }, nil)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 log line, got %d", len(entries))
	}
	if entries[0].Level != zapcore.InfoLevel {
		t.Errorf("level = %v, want info", entries[0].Level)
	}
	if got := entries[0].ContextMap(); got["request_id"] != "req_abc123" {
		t.Errorf("request_id = %v, want req_abc123", got["request_id"])
	}
}

// TestParamsFilterDropsBindValues pins the parameterized-queries contract: the
// hook GORM calls before building the string for Trace must return no
// variables, so the logged statement stays a template and bound user data never
// reaches the log.
func TestParamsFilterDropsBindValues(t *testing.T) {
	gl, _ := observedGormLogger()

	filter, ok := gl.(gorm.ParamsFilter)
	if !ok {
		t.Fatalf("%T does not implement gorm.ParamsFilter; GORM would interpolate bind values into logged SQL", gl)
	}
	const stmt = "SELECT * FROM `token` WHERE token_hash = ?"
	sql, params := filter.ParamsFilter(correlatedCtx(), stmt, "secret-token-hash")
	if sql != stmt {
		t.Errorf("sql = %q, want it returned unchanged", sql)
	}
	if len(params) != 0 {
		t.Errorf("params = %v, want none (they would be interpolated into the log)", params)
	}
}

// TestTraceWithoutCorrelationEmitsEmptyFields pins the fail-open rule: a
// background ctx logs empty correlation values rather than dropping the line.
func TestTraceWithoutCorrelationEmitsEmptyFields(t *testing.T) {
	gl, logs := observedGormLogger()

	gl.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "DELETE FROM `file` WHERE id = ?", 1
	}, errors.New("boom"))

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 log line, got %d", len(entries))
	}
	got := entries[0].ContextMap()
	if got["request_id"] != "" {
		t.Errorf("request_id = %v, want empty", got["request_id"])
	}
	if got["entrypoint"] != "" {
		t.Errorf("entrypoint = %v, want empty", got["entrypoint"])
	}
}
