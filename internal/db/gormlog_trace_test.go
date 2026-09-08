package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/migrate"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/repo"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
)

// TestRequestIDSurvivesRepoToGormTrace is the end-to-end half of the unit tests
// above: everything there drives Trace directly, which proves the fields are
// emitted but not that a request-scoped ctx actually reaches GORM through a
// repo call. Here a real statement is issued through a real repo method and the
// correlation key set at the top must come back out on the log line.
//
// The error branch is the one exercised because it is the branch operators
// actually read, and a duplicate nano_id is a deterministic way to reach it
// without a timing-dependent slow query.
func TestRequestIDSurvivesRepoToGormTrace(t *testing.T) {
	// This package cannot use internal/testutil — testutil opens databases
	// through Open, so importing it from a test in package db would be an
	// import cycle. Open a scratch SQLite one the same way instead.
	cfg := &config.Config{}
	cfg.Database.Driver = config.DriverSQLite
	cfg.Database.SQLite.Path = filepath.Join(t.TempDir(), "trace.db")

	core, logs := observer.New(zapcore.DebugLevel)
	log := zap.New(core)
	base, err := Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := migrate.Run(base); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb := base.Session(&gorm.Session{
		Logger: newZapGormLoggerWith(func() *zap.Logger { return log }),
	})

	ctx := ctxlog.WithEntrypoint(
		ctxlog.WithRequestID(context.Background(), "req_gormtrace"),
		ctxlog.EntrypointHTTP,
	)

	never := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	newFile := func() *model.File {
		return &model.File{NanoID: "gtrace01", Title: "gorm trace", ObjectKey: "gtrace01-1.html",
			ExpiresAt: never, CreateUser: "u_owner", UpdateUser: "u_owner"}
	}

	files := repo.NewFileRepo(gdb)
	// ctx rides in on the *gorm.DB handle for tx methods (the one carrier rule
	// in the internal/repo package doc), which is exactly what must reach Trace.
	if err := files.Insert(gdb.WithContext(ctx), newFile()); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("successful insert must stay silent, got %v", logs.All())
	}

	if err := files.Insert(gdb.WithContext(ctx), newFile()); err == nil {
		t.Fatal("duplicate nano_id insert unexpectedly succeeded")
	}

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 log line for the failed insert, got %d: %v", len(entries), entries)
	}
	e := entries[0]
	if e.Level != zapcore.ErrorLevel {
		t.Errorf("level = %v, want error", e.Level)
	}
	got := e.ContextMap()
	if got["request_id"] != "req_gormtrace" {
		t.Errorf("request_id = %v, want req_gormtrace — the ctx did not survive repo -> GORM", got["request_id"])
	}
	if got["entrypoint"] != ctxlog.EntrypointHTTP {
		t.Errorf("entrypoint = %v, want %s", got["entrypoint"], ctxlog.EntrypointHTTP)
	}
	if _, ok := got["duration_ms"].(int64); !ok {
		t.Errorf("duration_ms = %#v, want int64", got["duration_ms"])
	}

	// Parameterized queries, end to end: the statement GORM built is the
	// template, so no column value shows up in the line.
	sql, _ := got["sql"].(string)
	if !strings.Contains(sql, "?") && !strings.Contains(sql, "$1") {
		t.Errorf("sql = %q, want bind placeholders — ParamsFilter is not in effect", sql)
	}
	if strings.Contains(sql, "gtrace01") || strings.Contains(sql, "u_owner") {
		t.Errorf("sql = %q leaks bound argument values into the log", sql)
	}
}
