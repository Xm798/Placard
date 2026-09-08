package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Xm798/placard/internal/admincli"
	"github.com/Xm798/placard/internal/cleanup"
	"github.com/Xm798/placard/internal/config"
	appdb "github.com/Xm798/placard/internal/db"
	"github.com/Xm798/placard/internal/devicecode"
	"github.com/Xm798/placard/internal/handler"
	"github.com/Xm798/placard/internal/httpx"
	"github.com/Xm798/placard/internal/lock"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/migrate"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/oidc"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/session"
	"github.com/Xm798/placard/internal/storage"
	"github.com/Xm798/placard/internal/version"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func main() {
	// `admin` is dispatched before the server's own flags are parsed: it has
	// its own per-command flag sets, and it must run on a host where the
	// server cannot be signed into at all.
	if len(os.Args) > 1 && os.Args[1] == admincli.Name {
		runAdminCLI(os.Args[2:])
		return
	}

	// Config selection lives in config.ResolveEnv/ResolvePath, shared by every
	// binary so they agree on the precedence. The file itself is optional: an
	// instance running on defaults and env vars needs none.
	configFlag := flag.String("config", "", "path to config file (default: $PLACARD_CONFIG, else "+config.DefaultConfigPath+")")
	flag.Parse()

	cfg, err := config.Load(config.ResolvePath(*configFlag), config.ResolveEnv())
	if err != nil {
		// logger may not be initialized yet; use the fallback logger.
		logger.L().Fatal("load config", zap.Error(err))
	}
	// Reads or, on a first start, generates <data_dir>/secret.key. Must run
	// before any keyed primitive (PAT hashing) is used.
	if err := cfg.EnsureSecretKey(); err != nil {
		logger.L().Fatal("resolve secret key", zap.Error(err))
	}

	logger.Init(cfg.Log)
	defer logger.Sync()
	log := logger.Module("main")

	warnUntrustedProxyConfig(cfg, log)
	warnPlainHTTP(cfg, log)

	db, err := openDB(cfg)
	if err != nil {
		log.Fatal("open database", zap.Error(err))
	}

	// redis.addr empty is the zero-dependency deployment: sessions and device
	// flows go to SQL, the rate-limit counters and the cron lock live in this
	// process. Everything below reads the backend off this one handle.
	rdb := openRedis(cfg)
	if rdb != nil {
		defer func() { _ = rdb.Close() }()
	}

	app := newFiberApp(cfg)
	registerHealthRoutes(app, rdb, db)

	// Repositories (single source of truth for persistence).
	files := repo.NewFileRepo(db)
	tokens := repo.NewTokenRepo(db)
	views := repo.NewViewRepo(db)
	auditRepo := repo.NewAuditRepo(db)
	pending := repo.NewPendingObjectDeleteRepo(db)
	versions := repo.NewFileVersionRepo(db)
	users := repo.NewUserRepo(db)
	identities := repo.NewUserIdentityRepo(db)
	settings := repo.NewSettingRepo(db)

	// The config file seeds the runtime settings once; the table is
	// authoritative from then on, so an admin's later change is not reverted by
	// the next restart.
	if err := settings.SeedDefaults(context.Background(), map[string]string{
		model.SettingRegistrationOpen:  strconv.FormatBool(cfg.Auth.RegistrationOpen),
		model.SettingOIDCAutoProvision: strconv.FormatBool(cfg.Auth.OIDCAutoProvision),
		model.SettingUploadMaxFileSize: strconv.FormatInt(cfg.Upload.MaxFileSize, 10),
	}); err != nil {
		log.Fatal("seed instance settings", zap.Error(err))
	}

	// dev_mock injects a fixed identity ahead of every credential check, and
	// everything downstream of authentication (preferences, ownership, the
	// avatar proxy) reads a real user row. Materializing it here is what makes
	// the local-development identity an account like any other.
	if cfg.Auth.DevMock.Enabled {
		if err := ensureDevMockUser(context.Background(), users, cfg.Auth.DevMock); err != nil {
			log.Fatal("seed dev_mock account", zap.Error(err))
		}
	}

	// Serializes the cleanup cron: across replicas when Redis is configured,
	// within this process when it is not.
	var locker lock.Locker = lock.NewMemoryLocker()
	if rdb != nil {
		locker = lock.NewRedisLocker(rdb)
	}

	// Rate-limit counters. The in-process one is exact for this replica and
	// blind to the others, which is why a multi-replica deployment needs Redis.
	var counter middleware.Counter = middleware.NewMemoryCounter()
	if rdb != nil {
		counter = middleware.NewRedisCounter(rdb)
	}

	// Ephemeral login state. The SQL stores accumulate expired rows, which the
	// cleanup cron sweeps (step4); the Redis ones expire their own keys.
	var (
		sessions    session.Store
		deviceCodes devicecode.Store
		purgers     []cleanup.Purger
	)
	if rdb != nil {
		sessions = session.NewRedisStore(rdb, cfg.Auth.Session.IdleTTL, cfg.Auth.Session.AbsoluteTTL)
		deviceCodes = devicecode.NewRedisStore(rdb)
	} else {
		sqlSessions := session.NewSQLStore(db, cfg.Auth.Session.IdleTTL, cfg.Auth.Session.AbsoluteTTL)
		sqlDeviceCodes := devicecode.NewSQLStore(db)
		sessions, deviceCodes = sqlSessions, sqlDeviceCodes
		purgers = []cleanup.Purger{sqlSessions, sqlDeviceCodes}
	}

	// Lifetime of the process's background workers (cleanup cron, periodic log
	// summaries). Cancelled on shutdown, before the server is drained.
	rootCtx, rootCancel := context.WithCancel(context.Background())

	// Object storage client: the backend named by storage.type (local or s3).
	// Wrapped in the logging decorator either way — logger.Init has already run
	// above, so a plain handle is enough here (no provider indirection needed).
	rawStorage, err := storage.NewFromConfig(cfg.Storage)
	if err != nil {
		log.Fatal("storage client", zap.Error(err))
	}
	loggingStorage := storage.NewLoggingClient(rawStorage, logger.Module("storage"), storage.DefaultSlowThreshold)
	// The per-call lines are Debug on the normal path, so a healthy object
	// store is silent; this is the line that says it is being called at all.
	loggingStorage.StartSummary(rootCtx)
	objectStore := storage.Client(loggingStorage)

	// Upload rate limiter applied ONLY to POST /api/publish (never /s/:id/meta).
	publishLimiter := middleware.UploadRateLimit(middleware.RateLimitOptions{
		Counter:  counter,
		Limit:    cfg.RateLimit.UploadsPerHour,
		FailOpen: false,
	})

	// Per-IP mint limiter for the unauthenticated device-code endpoint.
	deviceLimiter := middleware.NewIPLimiter(counter, handler.DeviceCodeIPRateLimit, "devicecode")

	// Per-IP budget for anonymous requests to the share-link routes. AnonOnly
	// exempts a signed-in visitor, so an owner never competes with the visitors
	// of a page they published from behind the same address.
	anonRenderLimiter := middleware.AnonOnly(
		middleware.NewIPLimiter(counter, cfg.RateLimit.AnonRenderPerMinute, "anonrender").Handler())

	// Per-file+IP budget for WRONG share-code submissions. A 6-digit code is a
	// million guesses; this is what stands between an attacker and all of them.
	shareCodeLimiter := middleware.NewIPLimiter(counter, cfg.RateLimit.ShareCodeFailPerMinute, "sharecode")

	sessionCookie := handler.SessionCookieName(cfg.Server.BaseURL, cfg.Auth.Session.CookieName)

	failLimiter := middleware.NewIPLimiter(counter, cfg.RateLimit.AuthFailPerMinute, "authfail")

	// Counts every call to the password endpoints, not only the failures: each
	// one costs an argon2 hash before it can be judged.
	authLimiter := middleware.NewIPLimiter(counter, cfg.RateLimit.AuthPerMinute, "authroute")

	// last_active_at toucher: per-replica throttled (5min), detached async write.
	// Wired into every auth channel below via AuthOptions.TouchActive.
	activeToucher := middleware.NewActiveToucher(users.TouchLastActive)

	// Identity providers. Discovery is lazy inside the registry, so a provider
	// that is unreachable at boot costs nothing here and is retried on the
	// first login attempt that needs it.
	oidcRegistry := oidc.NewRegistry(cfg.Auth.OIDC,
		strings.TrimRight(cfg.Server.BaseURL, "/")+handler.OIDCCallbackPath, nil)

	h := handler.New(handler.Deps{
		DB:               db,
		Storage:          objectStore,
		Cfg:              cfg,
		Files:            files,
		Tokens:           tokens,
		Views:            views,
		Audit:            auditRepo,
		Pending:          pending,
		Versions:         versions,
		Users:            users,
		Identities:       identities,
		Settings:         settings,
		OIDC:             oidcRegistry,
		FailLimiter:      failLimiter,
		AuthLimiter:      authLimiter.Handler(),
		PublishLimiter:   publishLimiter,
		DeviceCodes:      deviceCodes,
		DeviceLimiter:    deviceLimiter.Handler(),
		ShareCodeLimiter: shareCodeLimiter,
		Sessions:         sessions,
		SessionCookie:    sessionCookie,

		AnonRenderLimiter: anonRenderLimiter,
		CLIRelease:        handler.GitHubCLIRelease(),
	})

	// Middleware chain (order matters): request id → access log → recover →
	// min CLI version → auth (dev_mock / PAT / session) → CORS → CSRF
	// (cookie-channel writes only).
	// recover.New() sits INSIDE AccessLog so a downstream panic is converted to
	// an error that unwinds through the access logger — the request that 500s
	// the client is recorded instead of vanishing from the access log.
	// RequestID/AccessLog themselves are panic-free plumbing; the panic surface
	// (handlers, DB, storage) is all below recover.
	app.Use(middleware.RequestID())
	app.Use(middleware.AccessLog())
	app.Use(recover.New())
	// Ahead of auth: an outdated CLI must be told to upgrade rather than have
	// its credential evaluated by an API it no longer speaks.
	app.Use(middleware.MinCLIVersion(cfg.Server.MinCLIVersion))
	app.Use(middleware.NewAuth(middleware.AuthOptions{
		DevMock:        cfg.Auth.DevMock,
		TokenValidator: h.TokenValidator(),
		Sessions:       sessions,
		CookieName:     sessionCookie,
		FailLimiter:    failLimiter,
		TouchActive:    activeToucher.Touch,
		AccountStatus:  users.Active,
	}))
	app.Use(middleware.CORS(cfg.CORS))
	app.Use(middleware.CSRF(cfg.CSRF))

	h.Mount(app)

	// In-process cleanup cron, on the same rootCtx as the storage summary above:
	// cancelling on shutdown lets both goroutines exit cleanly, and the locker
	// keeps exactly one round running at a time.
	if cfg.Cleanup.Enabled {
		go cleanup.Scheduler(rootCtx, cleanup.Deps{
			DB: db, Storage: objectStore, Locker: locker,
			Files: files, Views: views, Pending: pending, Audit: auditRepo,
			Versions: versions, Purgers: purgers,
			Cfg: cleanup.Config{
				Interval:            time.Duration(cfg.Cleanup.IntervalSeconds) * time.Second,
				LockTTL:             time.Duration(cfg.Cleanup.LockTTLSeconds) * time.Second,
				RetryMax:            cfg.Cleanup.RetryMax,
				ViewRecomputeEvery:  cfg.Cleanup.ViewRecomputeEvery,
				UserDeleteRetention: time.Duration(cfg.Cleanup.UserDeleteRetentionDays) * 24 * time.Hour,
			},
		})
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		addr := serverAddr(cfg)
		log.Info("starting server",
			zap.String("addr", addr),
			zap.String("version", version.Version),
			zap.String("git_branch", version.GitBranch),
			zap.String("git_commit", version.GitCommit),
			zap.String("build_time", version.BuildTime))
		if err := app.Listen(addr); err != nil {
			log.Fatal("server listen", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutting down")
	rootCancel() // stop the cleanup scheduler and the storage summary before draining

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(ctx); err != nil {
		log.Error("graceful shutdown", zap.Error(err))
	}
}

// runAdminCLI executes the `admin` subcommand and exits with its result.
//
// It opens the database through the same openDB the server boots with, so
// migrations are applied here too — creating the first admin on an empty SQLite
// file is one of the situations the command exists for. Errors print bare: this
// is a terminal, not a log stream.
//
// EnsureSecretKey is deliberately NOT called: no admin command needs the key
// (argon2 is unkeyed and no token is minted here), and it would create
// <data_dir>/secret.key owned by whoever ran the command — which on the very
// host this exists for is often root rather than the account the server runs
// as, leaving a key the server cannot read at boot.
func runAdminCLI(args []string) {
	err := admincli.Run(context.Background(), args, admincli.Env{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
		OpenDB: func(configPath string) (*gorm.DB, error) {
			cfg, err := config.Load(config.ResolvePath(configPath), config.ResolveEnv())
			if err != nil {
				return nil, err
			}
			return openDB(cfg)
		},
	})
	if err != nil {
		// flag.ContinueOnError and the usage paths have already said their
		// piece on stderr; anything else is a message worth one line.
		if !errors.Is(err, admincli.ErrUsage) && !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

// ensureDevMockUser materializes the dev_mock identity as a real account, so
// local development runs against the same user row every other channel
// authenticates into. Existing rows are left alone: the developer may have
// edited the profile, and this must never reset a password or clear the admin
// flag on a database someone is actually using.
func ensureDevMockUser(ctx context.Context, users *repo.UserRepo, m config.DevMockConfig) error {
	id := middleware.Identity(m)
	if id.AuthzID == "" {
		return fmt.Errorf("auth.dev_mock.uid is required when dev_mock is enabled")
	}
	switch _, err := users.Get(ctx, id.AuthzID); {
	case err == nil:
		return nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return err
	}

	username := m.User
	if username == "" {
		username = "devmock"
	}
	user := &model.User{
		ID:                id.AuthzID,
		Username:          username,
		DisplayName:       id.DisplayName,
		IsAdmin:           true,
		DefaultVisibility: model.VisibilityLink,
		FirstLoginAt:      model.Never,
		LastLoginAt:       model.Never,
		LastActiveAt:      model.Never,
	}
	if m.Email != "" {
		email := m.Email
		user.Email = &email
	}
	return users.Create(ctx, user)
}

// warnUntrustedProxyConfig flags the one misconfiguration that degrades
// silently. Behind a reverse proxy with server.trusted_proxies empty, Fiber's
// c.IP() resolves to the proxy for every request, so every per-IP limiter
// (auth failures, device codes) shares a single bucket and stops limiting
// anyone. It is not fatal because a directly exposed instance is a legitimate
// setup that needs no trusted proxies at all.
func warnUntrustedProxyConfig(cfg *config.Config, log *zap.Logger) {
	if len(cfg.Server.TrustedProxies) == 0 {
		log.Warn("server.trusted_proxies is empty: behind a reverse proxy every client shares one per-IP rate-limit bucket")
	}
}

// warnPlainHTTP reports the cookie attributes a plain-HTTP base_url costs. The
// instance works — the session and share-code cookies drop the prefix that
// would make a browser discard them — but nothing then stops a neighbouring
// host under the same parent domain from writing a cookie this one accepts.
// Loopback is silent: it has no neighbouring hosts, and it is what the
// quickstart in docker-compose.yaml ships.
func warnPlainHTTP(cfg *config.Config, log *zap.Logger) {
	if !strings.HasPrefix(strings.ToLower(cfg.Server.BaseURL), "http://") {
		return
	}
	if u, err := url.Parse(cfg.Server.BaseURL); err == nil && isLoopbackHost(u.Hostname()) {
		return
	}
	log.Warn("server.base_url is plain http: session cookies are issued without Secure or the __Host- prefix")
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func serverAddr(cfg *config.Config) string {
	port := cfg.Server.Port
	if port == 0 {
		port = 8080
	}
	return ":" + strconv.Itoa(port)
}

// openDB connects GORM via the shared opener (driver selection, pragmas, pool
// sizing — see internal/db) and, when AutoMigrate is enabled, brings the schema
// up to date (internal/migrate).
func openDB(cfg *config.Config) (*gorm.DB, error) {
	db, err := appdb.Open(cfg)
	if err != nil {
		return nil, err
	}

	if cfg.Database.AutoMigrate {
		if err := migrate.Run(db); err != nil {
			return nil, err
		}
	}

	return db, nil
}

// redisMinIdleConns keeps warm connections to avoid cold-dial latency on burst.
const redisMinIdleConns = 5

// openRedis returns nil when redis.addr is unset — the signal every caller in
// main reads to pick the single-binary backend instead. go-redis would happily
// dial localhost:6379 for an empty Addr, so the check has to happen here rather
// than being left to the client.
func openRedis(cfg *config.Config) *redis.Client {
	if cfg.Redis.Addr == "" {
		return nil
	}
	return redis.NewClient(&redis.Options{
		Addr:         cfg.Redis.Addr,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DB,
		MinIdleConns: redisMinIdleConns,
	})
}

// newFiberApp builds the Fiber app: a 16KB read buffer and trusted-proxy
// handling so c.IP() reflects the trusted hop (X-Real-IP) rather than a
// client-spoofable value.
func newFiberApp(cfg *config.Config) *fiber.App {
	readBuf := cfg.Server.ReadBufferSize
	if readBuf == 0 {
		readBuf = 16384
	}
	proxyHeader := cfg.Server.ProxyHeader
	if proxyHeader == "" {
		proxyHeader = "X-Real-IP"
	}

	app := fiber.New(fiber.Config{
		ReadBufferSize:          readBuf,
		ProxyHeader:             proxyHeader,
		EnableTrustedProxyCheck: true,
		TrustedProxies:          cfg.Server.TrustedProxies,
		DisableStartupMessage:   true,
		// BodyLimit matches the configured upload cap so the handler's
		// MaxFileSize check is the real gate (the default 4MB would reject
		// 4–10MB uploads that the configured 10MB cap intends to allow).
		BodyLimit: int(cfg.Upload.MaxFileSize),
		// ErrorHandler is the single translation point for errors returned from
		// handlers/middleware and for panics caught by recover.New. See
		// httpx.ErrorHandler for the apperr/fiber-error mapping.
		ErrorHandler: httpx.ErrorHandler,
	})
	return app
}

// registerHealthRoutes wires the internal probes (unauthenticated built-in skips).
// /api/version returns master-<commit> for master builds and the regular build
// version otherwise. Build time remains available only in the startup log.
//
// /api/health is the cheap liveness probe (process is up). /api/ready pings the
// configured dependencies under a ~1s budget, but only the database gates
// readiness: virtually every route (including the public /s/:id share links)
// needs it, so a replica that lost the database serves nothing useful and must
// leave the rotation. Redis is REPORTED but never fails the probe — it is
// shared, so a blip would fail every replica at once and the LB would pull the
// whole service (share links included) out of rotation, amplifying a partial
// failure into a full outage. Session auth already degrades to per-request
// 503s, which the access log records. A nil rdb is a deployment configured
// without Redis, which has nothing to report rather than something unreachable.
// Probes bypass the error envelope, so a plain {"status":...,"reason":...}
// matches the codebase style without routing through httpx.ErrorHandler.
func registerHealthRoutes(app *fiber.App, rdb *redis.Client, db *gorm.DB) {
	probeLog := logger.Module("probe")
	app.Get("/api/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})
	app.Get("/api/ready", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), time.Second)
		defer cancel()

		sqlDB, err := db.DB()
		if err == nil {
			err = sqlDB.PingContext(ctx)
		}
		if err != nil {
			return c.Status(fiber.StatusServiceUnavailable).
				JSON(fiber.Map{"status": "not_ready", "reason": "database"})
		}
		if rdb != nil {
			if err := rdb.Ping(ctx).Err(); err != nil {
				probeLog.Warn("redis unreachable in readiness probe", zap.Error(err))
				return c.JSON(fiber.Map{"status": "degraded", "reason": "redis"})
			}
		}
		return c.JSON(fiber.Map{"status": "ready"})
	})
	app.Get("/api/version", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"version": version.Public()})
	})
}
