package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/storage"
	"github.com/Xm798/placard/internal/testutil"
)

// The negative regression test for the HTTP entrypoint — the one that matters
// most: HTTP is the only entrypoint that carries PATs and session cookies. Its
// counterpart lives with the entrypoint it drives (the cron round in
// internal/cleanup/cron_log_test.go); the driver is what differs between them,
// the assertion is not.
//
// It asserts nothing about which field is redacted — that is what
// middleware.TestAccessLogRedactsSensitiveQueryKeys does, one pattern over one
// field. This one drives a whole entrypoint end to end with hostile input and
// asserts that no layer it touched — access log, authn, business trail, GORM,
// the storage decorator — wrote a line containing a secret. That is the assertion
// that keeps holding after a refactor moves a log line, after someone adds a
// "just for debugging" header dump during an incident, and after a new
// middleware lands upstream of the handler.
var httpPathSecrets = []string{
	"pl_",      // PAT plaintext (sent as the Bearer credential and in the query)
	"u_actor",  // an authz id (in the session cookie value and the page body)
	"Bearer",   // any Authorization header value
	"wss://",   // websocket endpoint, credentials in its query string
	"https://", // any inbound URL — Referer, Origin, and the redirect query value
}

// layersThatMustSpeak are the log messages the driven request has to produce.
// Without them the loop below would iterate over lines that never came from the
// layers this test exists to cover, and pass by saying nothing.
var layersThatMustSpeak = []string{
	"access",             // internal/middleware.AccessLog
	"state change",       // internal/handler.logStateChange
	"gorm query",         // the GORM adapter (stand-in below)
	"storage put object", // internal/storage.LoggingClient
}

func TestNoSecretReachesTheLogsOnTheHTTPPath(t *testing.T) {
	lines, messages := driveHostileRequest(t)

	if len(lines) == 0 {
		t.Fatal("the request logged nothing, so it asserts nothing")
	}
	for _, msg := range layersThatMustSpeak {
		if !messages[msg] {
			t.Fatalf("no %q line captured; that layer is no longer covered (captured: %v)", msg, messages)
		}
	}

	for _, line := range lines {
		for _, secret := range httpPathSecrets {
			if strings.Contains(line, secret) {
				t.Errorf("log line leaked %q: %s", secret, line)
			}
		}
	}
}

// driveHostileRequest publishes one page over the production middleware chain
// with every credential-shaped input a request can carry, and returns every log
// line the whole chain wrote, plus the set of messages seen.
//
// Capture is the real file sink rather than a zap observer: the access logger
// is resolved through logger.Module, which reads the process-wide logger, and
// there is no seam to inject a core into it. Initializing that logger onto a
// temp file gives one sink for every layer at once — encoded by the production
// JSON encoder, so field names and values are both covered — which is what the
// assertion needs and is exactly the bytes ELK would receive.
func driveHostileRequest(t *testing.T) ([]string, map[string]bool) {
	t.Helper()

	logPath := filepath.Join(t.TempDir(), "placard.log")
	logger.Init(config.LogConfig{Level: "debug", File: logPath, MaxSize: 1, MaxBackups: 1})
	// Point the process logger back at the console before the temp dir goes
	// away (cleanups run LIFO, so this one runs first).
	t.Cleanup(func() { logger.Init(config.LogConfig{Level: "info"}) })

	db := testutil.OpenTestDB(t)
	db.Logger = gormObserver{}
	// This handle is this test's alone; hand its pool back rather than leaving
	// idle connections on the shared database for the rest of the run.
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	cfg := &config.Config{}
	cfg.Upload.MaxFileSize = 10 << 20
	cfg.Server.SecretKey = "test-secret-key"
	cfg.Token.MaxTTLDays = 365
	cfg.Server.BaseURL = "https://placard.example.com"
	cfg.CORS.AllowedOrigins = []string{cfg.Server.BaseURL}
	cfg.CSRF.AllowedOrigins = []string{cfg.Server.BaseURL}

	deps := Deps{
		DB:      db,
		Storage: storage.NewLoggingClient(storage.NewStubClient(), logger.Module("storage"), storage.DefaultSlowThreshold),
		Cfg:     cfg,

		Files:    repo.NewFileRepo(db),
		Tokens:   repo.NewTokenRepo(db),
		Views:    repo.NewViewRepo(db),
		Audit:    repo.NewAuditRepo(db),
		Pending:  repo.NewPendingObjectDeleteRepo(db),
		Versions: repo.NewFileVersionRepo(db),
		Users:    repo.NewUserRepo(db),
	}
	h := New(deps)

	// A PAT the middleware really accepts, so the request authenticates through
	// the production Bearer channel and reaches the handler. last_used_at is the
	// never-used sentinel, which also puts the async touch on this path.
	plaintext := patPrefix + strings.Repeat("a", patNanoIDLen)
	if err := deps.Tokens.Insert(context.Background(), &model.Token{
		TokenHash:  hashToken(cfg.Server.SecretKey, plaintext),
		UserID:     testAuthzID,
		Name:       "cli",
		ExpiresAt:  model.Timestamp(time.Now().Add(24 * time.Hour)),
		LastUsedAt: time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC),
		CreateUser: testAuthzID,
		UpdateUser: testAuthzID,
	}); err != nil {
		t.Fatalf("seed PAT: %v", err)
	}

	// The chain from main.go, in order.
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler, BodyLimit: int(cfg.Upload.MaxFileSize)})
	app.Use(middleware.RequestID())
	app.Use(middleware.AccessLog())
	app.Use(recover.New())
	app.Use(middleware.NewAuth(middleware.AuthOptions{
		TokenValidator: h.TokenValidator(),
		Sessions:       noSessions{},
		CookieName:     "__Host-placard_session",
	}))
	app.Use(middleware.CORS(cfg.CORS))
	app.Use(middleware.CSRF(cfg.CSRF))
	h.Mount(app)

	resp, err := app.Test(hostileRequest(t, plaintext), -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("publish through the real chain = %d, want 200 (the request must reach the handler)", resp.StatusCode)
	}

	logger.Sync()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read captured log: %v", err)
	}
	var lines []string
	messages := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("captured line is not JSON (%v): %s", err, line)
		}
		messages[entry.Msg] = true
		lines = append(lines, line)
	}
	return lines, messages
}

// hostileRequest builds the one request. Every secret-shaped value sits where
// production puts it: the PAT in the Authorization header (and, as a caller
// mistake, in the query), an authz id inside the session cookie, inbound URLs in
// Referer/Origin and in a redirect parameter, and the page body carrying all of
// them at once.
//
// The redirect value is percent-encoded, as an inbound URL in a real query is:
// the access log records the query string by design (with sensitive keys
// redacted), so a raw "https://" there would be a deliberate design output, not
// a leak — while a layer that decodes the URL, or logs the Referer, or echoes
// the body, trips the assertion. Same reason the query carries no authz id: a
// non-sensitive key's value is logged verbatim on purpose.
func hostileRequest(t *testing.T, pat string) *http.Request {
	t.Helper()

	inbound := "https://intranet.example/wiki/p?token=" + pat + "&authorization=Bearer%20t0ken#u_actor"
	html := `<!DOCTYPE html><html><body><a href="` + inbound + `">wiki</a>` +
		`<script>const ws="wss://events.example.com/ws?token=` + pat + `";const me="u_actor";</script>` +
		`</body></html>`
	body, err := json.Marshal(map[string]string{"html": html, "title": "Q3 revenue", "expiry": "30d"})
	if err != nil {
		t.Fatalf("marshal publish body: %v", err)
	}

	query := "code=4%2Fsecret&ticket=tkt_9f3&token=" + pat +
		"&redirect=https%3A%2F%2Fintranet.example%2Fwiki%2Fp&page=2"

	req := httptest.NewRequest("POST", "/api/publish?"+query, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Cookie", "__Host-placard_session=sess_u_actor_9f3")
	req.Header.Set("Referer", inbound)
	req.Header.Set("Origin", "https://intranet.example")
	req.Header.Set("X-Forwarded-For", "10.1.2.3")
	req.Header.Set("User-Agent", "PlacardCLI/1.0")
	return req
}

// noSessions stands in for the Redis-backed session store. The request
// authenticates through the PAT channel, which returns before the cookie is
// read; this exists so a regression that falls through to the cookie path fails
// as an unauthenticated request rather than as a nil-store 503.
type noSessions struct{}

func (noSessions) Get(context.Context, string) (session.Data, error) {
	return session.Data{}, session.ErrNotFound
}

// gormObserver stands in for internal/db's adapter, which is unexported there.
// It logs through logger.Module("gorm") — the same sink the real one uses — at
// Info rather than Warn, so the statements a healthy request issues are part of
// what this test scans; production logs only slow and failing queries, which a
// green request produces none of.
//
// ParamsFilter mirrors the production adapter deliberately. GORM interpolates
// the bind values into the string handed to Trace unless the logger filters
// them, so a stand-in without it would log values production never logs and the
// test would be failing on its own harness. That the real adapter still carries
// it is internal/db's assertion (gormlog_test.go), not this one's.
type gormObserver struct{}

var (
	_ gormlogger.Interface = gormObserver{}
	_ gorm.ParamsFilter    = gormObserver{}
)

func (g gormObserver) LogMode(gormlogger.LogLevel) gormlogger.Interface { return g }

func (gormObserver) Info(_ context.Context, msg string, args ...interface{}) {
	logger.Module("gorm").Info(fmt.Sprintf(msg, args...))
}

func (gormObserver) Warn(_ context.Context, msg string, args ...interface{}) {
	logger.Module("gorm").Warn(fmt.Sprintf(msg, args...))
}

func (gormObserver) Error(_ context.Context, msg string, args ...interface{}) {
	logger.Module("gorm").Error(fmt.Sprintf(msg, args...))
}

func (gormObserver) ParamsFilter(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
	return sql, nil
}

func (gormObserver) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	statement, rows := fc()
	fields := []zap.Field{
		zap.String("sql", statement),
		zap.Int64("rows", rows),
		ctxlog.DurMS(time.Since(begin)),
		ctxlog.ReqID(ctx),
		ctxlog.Entry(ctx),
	}
	if err != nil {
		logger.Module("gorm").Error("gorm query", append(fields, zap.Error(err))...)
		return
	}
	logger.Module("gorm").Info("gorm query", fields...)
}
