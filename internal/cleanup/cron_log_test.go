package cleanup

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	postgresdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/storage"
)

// errNoDatabase is what every statement in these tests fails with. The point of
// the round here is the ctx that reaches GORM, not the rows that come back, so
// a driver that refuses to connect exercises the whole repo → GORM path without
// a real database (that path is covered in the integration tests).
var errNoDatabase = errors.New("cleanup log test: no database")

type deadConnector struct{}

func (deadConnector) Connect(context.Context) (driver.Conn, error) { return nil, errNoDatabase }

func (deadConnector) Driver() driver.Driver { return deadDriver{} }

type deadDriver struct{}

func (deadDriver) Open(string) (driver.Conn, error) { return nil, errNoDatabase }

// grantingLocker always hands out the lock: the distributed mutex has its own
// contract and is not what these tests are about.
type grantingLocker struct{}

func (grantingLocker) Acquire(context.Context, string, time.Duration) (func(context.Context), error) {
	return func(context.Context) {}, nil
}

// silentStorage succeeds at everything; step2 never reaches it anyway once its
// listing query fails.
type silentStorage struct{ storage.Client }

func (silentStorage) DeleteObject(context.Context, string) error { return nil }

// tracingGormLogger stands in for internal/db's adapter, logging off the ctx it
// is handed exactly like the real one does. If a cron step ever hands GORM a
// context.Background again, these lines lose their request_id and the assertion
// below fails — which is precisely what step3 used to do.
type tracingGormLogger struct{ log *zap.Logger }

func (l tracingGormLogger) LogMode(gormlogger.LogLevel) gormlogger.Interface { return l }

func (l tracingGormLogger) Info(ctx context.Context, msg string, _ ...interface{}) {
	l.log.Info(msg, ctxlog.ReqID(ctx), ctxlog.Entry(ctx))
}

func (l tracingGormLogger) Warn(ctx context.Context, msg string, _ ...interface{}) {
	l.log.Warn(msg, ctxlog.ReqID(ctx), ctxlog.Entry(ctx))
}

func (l tracingGormLogger) Error(ctx context.Context, msg string, _ ...interface{}) {
	l.log.Error(msg, ctxlog.ReqID(ctx), ctxlog.Entry(ctx))
}

func (l tracingGormLogger) Trace(ctx context.Context, _ time.Time, fc func() (string, int64), _ error) {
	statement, rows := fc()
	l.log.Info("gorm trace",
		ctxlog.ReqID(ctx), ctxlog.Entry(ctx),
		zap.String("sql", statement), zap.Int64("rows", rows))
}

// driveRound runs one cron round against the dead database and returns
// everything it logged, GORM's Trace lines included.
func driveRound(t *testing.T, round int) *observer.ObservedLogs {
	t.Helper()

	sqlDB := sql.OpenDB(deadConnector{})
	t.Cleanup(func() { _ = sqlDB.Close() })

	core, logs := observer.New(zapcore.DebugLevel)
	log := zap.New(core)
	gdb, err := gorm.Open(
		// Postgres rather than the default SQLite dialector: SQLite's
		// Initialize reads the library version off the connection, which a
		// connector that refuses to connect cannot answer. Which dialect
		// compiles the statement is irrelevant here — only the ctx that
		// reaches GORM is. DisableAutomaticPing keeps Open from dialling.
		postgresdriver.New(postgresdriver.Config{Conn: sqlDB}),
		&gorm.Config{Logger: tracingGormLogger{log: log}, DisableAutomaticPing: true},
	)
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	Run(context.Background(), Deps{
		DB: gdb, Storage: silentStorage{}, Locker: grantingLocker{},
		Files: repo.NewFileRepo(gdb), Views: repo.NewViewRepo(gdb),
		Pending: repo.NewPendingObjectDeleteRepo(gdb), Versions: repo.NewFileVersionRepo(gdb),
		Audit: repo.NewAuditRepo(gdb),
		Cfg: Config{
			RetryMax: 3, ViewRecomputeEvery: 1,
			UserDeleteRetention: 24 * time.Hour,
		},
	}, round, log)

	return logs
}

// The cron entrypoint's half of the correlation guarantee: every statement a
// round issues carries that round's id, step3's included. step3 is the reason
// this test exists — it took no ctx at all, so its UPDATE reached GORM with a
// background context and an empty request_id.
func TestCronRoundCorrelatesEveryStatement(t *testing.T) {
	logs := driveRound(t, 7)

	traces := logs.FilterMessage("gorm trace").All()
	if len(traces) == 0 {
		t.Fatal("the round issued no statements, so it asserts nothing")
	}

	var sawRecompute bool
	for _, entry := range traces {
		fields := entry.ContextMap()
		if fields["request_id"] != "cron_7" {
			t.Errorf("request_id = %v, want cron_7 (sql: %v)", fields["request_id"], fields["sql"])
		}
		if fields["entrypoint"] != ctxlog.EntrypointCron {
			t.Errorf("entrypoint = %v, want %s", fields["entrypoint"], ctxlog.EntrypointCron)
		}
		if statement, _ := fields["sql"].(string); strings.Contains(statement, "SET view_count") {
			sawRecompute = true
		}
	}
	if !sawRecompute {
		t.Error("step3's recompute never reached GORM; the ctx assertion above never covered it")
	}
}

// The negative regression test for the cron entrypoint (the HTTP request is in
// internal/handler/leak_test.go — each is owned by the package that owns the
// entrypoint it drives). It asserts nothing about which field is redacted —
// only that a whole entrypoint, driven end to end, produced no line containing
// a secret.
func TestNoSecretReachesTheLogsOnTheCronPath(t *testing.T) {
	logs := driveRound(t, 7)
	if logs.Len() == 0 {
		t.Fatal("the round logged nothing, so it asserts nothing")
	}

	secrets := []string{
		"pl_",      // PAT plaintext
		"u_actor",  // an authz id
		"Bearer",   // any Authorization header value
		"wss://",   // websocket endpoint, credentials in its query string
		"https://", // any inbound URL, whose query may carry a capability token
	}
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	for _, entry := range logs.All() {
		buf, err := encoder.EncodeEntry(entry.Entry, entry.Context)
		if err != nil {
			t.Fatalf("encode entry: %v", err)
		}
		line := buf.String()
		for _, secret := range secrets {
			if strings.Contains(line, secret) {
				t.Errorf("log line leaked %q: %s", secret, line)
			}
		}
	}
}
