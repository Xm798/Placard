package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/logger"

	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// slowThreshold is the default wall-clock cutoff above which a query is logged
// at Warn as a slow query. A constant rather than a config field: tuning has
// never been needed and a new key would widen the surface for no gain.
const slowThreshold = 200 * time.Millisecond

// zapGormLogger adapts gorm.io/gorm/logger.Interface to the project's global
// zap logger, routed through logger.Module("gorm") so DB logs share the
// repo-wide named-logger convention (zap logger_name "gorm") instead of a
// bespoke component field.
//
// This is a hand-rolled Interface impl rather than gorm.io/gorm/logger.New
// with a Printf writer: the latter flattens sql/rows/duration into a single
// string, whereas structured fields are the whole point of routing GORM
// through zap. The logger is resolved per call (log is a provider, not a
// handle) so a late logger.Init — after Open — takes effect without reopening
// the handle, so a binary that never calls Init still logs through the
// fallback.
type zapGormLogger struct {
	level gormlogger.LogLevel
	log   func() *zap.Logger
}

// GORM discovers ParamsFilter by type assertion, so a drifting signature would
// disable parameterized queries silently and start leaking bind values into the
// logs. Assert it at compile time instead.
var (
	_ gormlogger.Interface = (*zapGormLogger)(nil)
	_ gorm.ParamsFilter    = (*zapGormLogger)(nil)
)

// newZapGormLogger builds an adapter at Warn level (GORM's default).
// Record-not-found is silenced in Trace rather than via a field: repos
// already translate ErrRecordNotFound via errors.Is, so a line per miss
// would be noise.
func newZapGormLogger() gormlogger.Interface {
	return newZapGormLoggerWith(func() *zap.Logger { return logger.Module("gorm") })
}

// newZapGormLoggerWith is the injectable core so tests can supply an observer
// logger. The provider is re-read per call for the reason above.
func newZapGormLoggerWith(log func() *zap.Logger) gormlogger.Interface {
	return &zapGormLogger{level: gormlogger.Warn, log: log}
}

func (l *zapGormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	cp := *l
	cp.level = level
	return &cp
}

func (l *zapGormLogger) Info(ctx context.Context, format string, args ...interface{}) {
	if l.level < gormlogger.Info {
		return
	}
	l.log().Info(fmt.Sprintf(format, args...))
}

func (l *zapGormLogger) Warn(ctx context.Context, format string, args ...interface{}) {
	if l.level < gormlogger.Warn {
		return
	}
	l.log().Warn(fmt.Sprintf(format, args...))
}

func (l *zapGormLogger) Error(ctx context.Context, format string, args ...interface{}) {
	if l.level < gormlogger.Error {
		return
	}
	l.log().Error(fmt.Sprintf(format, args...))
}

// ParamsFilter implements gorm.ParamsFilter, GORM's parameterized-queries hook:
// callbacks.go asks the logger to filter the bind variables before building the
// string handed to Trace, and dropping them leaves the statement template
// ("... WHERE nano_id = ?") with no values interpolated.
//
// This is the equivalent of gorm/logger.Config.ParameterizedQueries, which is
// unreachable from here — that field is read only by GORM's own Printf logger,
// and gorm.Config has no such field. The interface is the supported path for a
// hand-rolled logger.
//
// It is a prerequisite for logging SQL at all, not an optimization: without it
// every slow/error line below prints the bound arguments — page titles, authz
// ids, token hashes, session values — into the log. Do not remove it.
func (l *zapGormLogger) ParamsFilter(ctx context.Context, sql string, params ...interface{}) (string, []interface{}) {
	return sql, nil
}

func (l *zapGormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	if l.level <= gormlogger.Silent {
		return
	}
	// err is a parameter, so check it before calling fc(): the common miss
	// path (owner-scope GetOwned → 404) then skips SQL formatting entirely.
	// Repos handle the miss via errors.Is; no log line wanted.
	if err != nil && errors.Is(err, gormlogger.ErrRecordNotFound) {
		return
	}

	elapsed := time.Since(begin)

	// Decide the outcome before building anything: the common case (success,
	// under the slow threshold, level below Info) logs nothing, and fc() forces
	// a full SQL-string rebuild that would otherwise be built and discarded.
	slow := elapsed > slowThreshold
	if err == nil && !slow && l.level < gormlogger.Info {
		return
	}

	sql, rows := fc()
	log := l.log()

	// request_id/entrypoint come from the ctx GORM was handed — via
	// db.WithContext(ctx) in the repos, so every Warn/Error line below joins
	// to the AccessLog line of the request that caused it. Empty on background
	// paths that carry no correlation key; that is fail-open by design.
	//
	// sql is the statement template, not the bound arguments — see
	// ParamsFilter above.
	fields := []zap.Field{
		zap.String("sql", sql),
		zap.Int64("rows", rows),
		ctxlog.DurMS(elapsed),
		ctxlog.ReqID(ctx),
		ctxlog.Entry(ctx),
	}

	switch {
	case err != nil:
		log.Error("gorm query", append(fields, zap.Error(err))...)
	case slow:
		log.Warn("gorm slow query", fields...)
	default: // l.level >= gormlogger.Info
		log.Info("gorm query", fields...)
	}
}
