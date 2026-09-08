// Package config loads Placard configuration from a YAML file with
// environment-variable overrides (viper). Every field has a SetDefault so env
// vars are picked up by Unmarshal even when absent from the file.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/Xm798/placard/internal/version"
)

type ServerConfig struct {
	Port           int      `mapstructure:"port"`
	ReadBufferSize int      `mapstructure:"read_buffer_size"`
	TrustedProxies []string `mapstructure:"trusted_proxies"`
	ProxyHeader    string   `mapstructure:"proxy_header"`
	BaseURL        string   `mapstructure:"base_url"` // canonical primary origin (no trailing slash); used to build /s/:id share URLs
	// SecretKey is the instance-wide 32-byte secret every keyed primitive
	// derives from (today: the PAT HMAC pepper). Left unset it is generated
	// once into <data_dir>/secret.key — see EnsureSecretKey. Rotating it
	// invalidates every value derived from it, personal access tokens included.
	SecretKey string `mapstructure:"secret_key"`
	// MinCLIVersion is the oldest `placard` CLI this instance answers. Empty
	// (the default) checks nothing. Set it after a breaking API change: a CLI
	// whose User-Agent reports an older release gets 426 with an upgrade
	// message instead of a confusing failure deeper in the request.
	MinCLIVersion string `mapstructure:"min_cli_version"`
}

// SQLiteConfig points at the single-file database. Path defaults to
// <data_dir>/placard.db.
type SQLiteConfig struct {
	Path string `mapstructure:"path"`
}

type DatabaseConfig struct {
	DSN          string       `mapstructure:"dsn"`    // postgres only; sqlite is addressed by SQLite.Path
	Driver       string       `mapstructure:"driver"` // sqlite (default) or postgres
	SQLite       SQLiteConfig `mapstructure:"sqlite"`
	AutoMigrate  bool         `mapstructure:"auto_migrate"`
	MaxOpenConns int          `mapstructure:"max_open_conns"`
	MaxIdleConns int          `mapstructure:"max_idle_conns"`
}

// Supported database.driver values.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// defaultBaseURL is what an instance started with no config at all serves
// itself as.
const defaultBaseURL = "http://localhost:8080"

// DefaultDataDir is the root every unset path derives from, so a self-hoster
// who writes no config at all still gets one directory holding the database,
// the objects and the secret key.
const DefaultDataDir = "./data"

// StorageConfig selects the object-store backend published pages live in.
// Type is "local" (a directory on disk) or "s3" (any S3-compatible service:
// AWS S3, Cloudflare R2, MinIO). An empty Type reads as "local".
type StorageConfig struct {
	Type  string             `mapstructure:"type"`
	Local LocalStorageConfig `mapstructure:"local"`
	S3    S3Config           `mapstructure:"s3"`
}

type LocalStorageConfig struct {
	Dir string `mapstructure:"dir"`
}

// S3Config addresses one S3-compatible bucket. Endpoint is empty for AWS S3
// (the SDK derives it from Region) and set for R2 / MinIO. R2 wants
// Region "auto". PathStyle is required by MinIO, which serves buckets as path
// segments rather than host prefixes.
type S3Config struct {
	Endpoint  string `mapstructure:"endpoint"`
	Region    string `mapstructure:"region"`
	Bucket    string `mapstructure:"bucket"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	PathStyle bool   `mapstructure:"path_style"`
}

// RedisConfig is optional. Leaving Addr empty runs the server with no Redis at
// all — the single-binary deployment. It is REQUIRED for more than one replica:
// without it each replica keeps its own sessions, rate-limit counters and cron
// lock, so a login is only valid on the replica that issued it, every replica
// hands out the full rate-limit budget, and every replica runs the cron.
type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// DevMockConfig is the fixed identity the auth middleware injects ahead of
// every credential check under APP_ENV=local (config.Validate rejects it
// anywhere else). Its fields mirror the account columns the rest of the app
// reads, because the server materializes a real user row from them at boot —
// see ensureDevMockUser: local development runs against the same kind of row
// every other login channel produces.
type DevMockConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	UID         string `mapstructure:"uid"` // becomes user.id, the ownership/audit key
	User        string `mapstructure:"user"`
	Email       string `mapstructure:"email"`
	DisplayName string `mapstructure:"display_name"`
}

type SessionConfig struct {
	CookieName  string        `mapstructure:"cookie_name"`
	IdleTTL     time.Duration `mapstructure:"idle_ttl"`
	AbsoluteTTL time.Duration `mapstructure:"absolute_ttl"`
}

// OIDCProviderConfig is one configured identity provider. Name is the stable
// key stored in user_identity.provider, so renaming it in the config orphans
// every identity already linked through it; DisplayName is the label the login
// button carries and may change freely.
//
// Issuer is the discovery base URL (no /.well-known suffix) — the value the
// provider also puts in the "iss" claim, which go-oidc compares them against.
// Scopes defaults to openid+profile+email when left empty; "openid" is added
// whether or not it is listed, since without it the provider returns no id
// token at all.
type OIDCProviderConfig struct {
	Name         string   `mapstructure:"name"`
	DisplayName  string   `mapstructure:"display_name"`
	Issuer       string   `mapstructure:"issuer"`
	ClientID     string   `mapstructure:"client_id"`
	ClientSecret string   `mapstructure:"client_secret"`
	Scopes       []string `mapstructure:"scopes"`
}

// Label is what a login button says: the configured display name, falling back
// to the provider key so a half-configured provider still renders as something
// a person can read.
func (p OIDCProviderConfig) Label() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Name
}

// AvatarConfig covers the avatar sources the server is allowed to reach out to.
// GravatarFallback is on by default: an account with an email but no provider
// picture gets the Gravatar for that address, and only accounts with neither
// fall through to the frontend's initial letter.
//
// Turning it off is a privacy switch — it stops the server from disclosing a
// hash of every user's email address to a third party.
type AvatarConfig struct {
	GravatarFallback bool `mapstructure:"gravatar_fallback"`
}

type AuthConfig struct {
	Session SessionConfig `mapstructure:"session"`
	DevMock DevMockConfig `mapstructure:"dev_mock"`

	// OIDC is the list of identity providers the login page offers. Empty
	// (the default) leaves the instance password-only.
	OIDC []OIDCProviderConfig `mapstructure:"oidc"`

	// RegistrationOpen and OIDCAutoProvision seed the setting table on a first
	// start and are not read again afterwards: the table is authoritative, so
	// an admin's change survives the next restart. See model.Setting.
	//
	// RegistrationOpen defaults to false. An instance with no accounts accepts
	// its first registration regardless — that account becomes the admin, and
	// without the exception a fresh install could never reach the switch.
	RegistrationOpen  bool `mapstructure:"registration_open"`
	OIDCAutoProvision bool `mapstructure:"oidc_auto_provision"`
}

type LogConfig struct {
	Level      string `mapstructure:"level"`
	File       string `mapstructure:"file"`
	MaxSize    int    `mapstructure:"max_size"`
	MaxBackups int    `mapstructure:"max_backups"`
	MaxAge     int    `mapstructure:"max_age"`
	Compress   bool   `mapstructure:"compress"`
}

type TokenConfig struct {
	MaxTTLDays int `mapstructure:"max_ttl_days"`
}

type CSRFConfig struct {
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

type CORSConfig struct {
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

type UploadConfig struct {
	MaxFileSize int64 `mapstructure:"max_file_size"`
}

type RateLimitConfig struct {
	UploadsPerHour int `mapstructure:"uploads_per_hour"`
	// AuthPerMinute caps requests to the password endpoints per IP, counting
	// every attempt rather than only the failures. Each one runs an argon2
	// verification (64 MiB, 3 passes) before it can know whether the password
	// was right, so without this an unauthenticated caller can exhaust the
	// instance's memory with correctly-shaped requests.
	AuthPerMinute     int `mapstructure:"auth_per_minute"`
	AuthFailPerMinute int `mapstructure:"auth_fail_per_minute"` // per-IP cap on failed authn attempts
	// AnonRenderPerMinute caps anonymous requests to the share-link routes per
	// IP. A signed-in visitor is exempt (middleware.AnonOnly): their traffic is
	// attributable to an account, while an anonymous caller is only ever an IP.
	// Rendering one page spends three (shell, meta, render), so the default
	// leaves a visitor 40 page views a minute.
	AnonRenderPerMinute int `mapstructure:"anon_render_per_minute"`
	// ShareCodeFailPerMinute caps wrong share-code submissions per file per IP.
	// A 6-digit code is only a million guesses, so this — not the code's
	// length — is what makes it a secret. The budget is partitioned by address,
	// which bounds one host to ~70 days of continuous guessing per page but
	// scales down with the number of hosts an attacker controls. A share code
	// therefore raises the cost of a leaked link; it is not an authentication
	// factor, and a page that must not be read by the wrong person belongs on
	// visibility private.
	ShareCodeFailPerMinute int `mapstructure:"share_code_fail_per_minute"`
}

// CleanupConfig drives the in-process cron. LockTTLSeconds MUST exceed the
// worst-case single round (R3); ViewRecomputeEvery is in rounds (step3 is low
// frequency). A value of 0 disables the step3 cadence (no recompute).
type CleanupConfig struct {
	Enabled                 bool `mapstructure:"enabled"`
	IntervalSeconds         int  `mapstructure:"interval_seconds"`           // default 3600
	LockTTLSeconds          int  `mapstructure:"lock_ttl_seconds"`           // default 300
	RetryMax                int  `mapstructure:"retry_max"`                  // default 5
	ViewRecomputeEvery      int  `mapstructure:"view_recompute_every"`       // default 24 (rounds)
	UserDeleteRetentionDays int  `mapstructure:"user_delete_retention_days"` // default 0
}

type Config struct {
	// Env is the deployment environment (APP_ENV), injected by Load and never
	// read from yaml. It gates one thing only: auth.dev_mock, which is
	// accepted under "local" and rejected everywhere else.
	Env string `mapstructure:"-"`
	// DataDir is the root of every derived path (database file, object
	// directory, secret key). Load fills the unset ones in from it.
	DataDir   string          `mapstructure:"data_dir"`
	Server    ServerConfig    `mapstructure:"server"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Storage   StorageConfig   `mapstructure:"storage"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Auth      AuthConfig      `mapstructure:"auth"`
	Log       LogConfig       `mapstructure:"log"`
	Token     TokenConfig     `mapstructure:"token"`
	CSRF      CSRFConfig      `mapstructure:"csrf"`
	CORS      CORSConfig      `mapstructure:"cors"`
	Upload    UploadConfig    `mapstructure:"upload"`
	Avatar    AvatarConfig    `mapstructure:"avatar"`
	RateLimit RateLimitConfig `mapstructure:"ratelimit"`
	Cleanup   CleanupConfig   `mapstructure:"cleanup"`
}

// ResolveEnv returns the deployment environment from APP_ENV, defaulting to
// "prod" — defaulting to the strictest validation is the safe failure mode.
// Shared by every binary so they agree on the default.
func ResolveEnv() string {
	if env := os.Getenv("APP_ENV"); env != "" {
		return env
	}
	return "prod"
}

// DefaultConfigPath is looked for in the working directory when nothing else
// names a config file.
const DefaultConfigPath = "config.yaml"

// EnvPrefix namespaces every environment override: server.base_url is read
// from PLACARD_SERVER_BASE_URL. It does not cover the ${VAR} references a
// config file may contain — those name whatever variable the operator chose.
const EnvPrefix = "PLACARD"

// ResolvePath resolves the config file path with the precedence every binary
// must share: explicit override (--config flag), then PLACARD_CONFIG, then
// DefaultConfigPath. A tool resolving this differently from the server could
// silently operate on a different database/bucket than intended.
//
// It returns "" — Load's "no config file, defaults and env only" mode — when
// nothing named a file and DefaultConfigPath does not exist. A path someone
// did name is returned whether or not it exists, so a typo in --config or
// PLACARD_CONFIG fails loudly instead of quietly booting on defaults.
func ResolvePath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("PLACARD_CONFIG"); p != "" {
		return p
	}
	if _, err := os.Stat(DefaultConfigPath); err != nil {
		return ""
	}
	return DefaultConfigPath
}

// Load reads configuration from path (optional) with env overrides, then runs
// Validate(). env (local/staging/prod) selects the validation strictness and is
// stored on the returned Config as cfg.Env. The caller is responsible for
// resolving env before invoking Load (see main.go).
func Load(path, env string) (*Config, error) {
	v := viper.New()

	// Every key is addressed as PLACARD_<KEY>. The prefix is load-bearing: the
	// unprefixed names this would otherwise bind (DATA_DIR, SERVER_PORT,
	// STORAGE_TYPE) are generic enough that another process in the same
	// container or unit file may already own them.
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_buffer_size", 16384)
	v.SetDefault("server.trusted_proxies", []string{})
	v.SetDefault("server.proxy_header", "X-Real-IP")
	v.SetDefault("server.base_url", defaultBaseURL)
	v.SetDefault("server.secret_key", "")
	v.SetDefault("server.min_cli_version", "")

	v.SetDefault("data_dir", DefaultDataDir)

	v.SetDefault("database.dsn", "")
	v.SetDefault("database.driver", DriverSQLite)
	v.SetDefault("database.sqlite.path", "")
	v.SetDefault("database.auto_migrate", true)
	v.SetDefault("database.max_open_conns", 100)
	v.SetDefault("database.max_idle_conns", 10)

	v.SetDefault("storage.type", "local")
	v.SetDefault("storage.local.dir", "")
	v.SetDefault("storage.s3.endpoint", "")
	v.SetDefault("storage.s3.region", "")
	v.SetDefault("storage.s3.bucket", "")
	v.SetDefault("storage.s3.access_key", "")
	v.SetDefault("storage.s3.secret_key", "")
	v.SetDefault("storage.s3.path_style", false)

	// Empty is the zero-dependency default: sessions and device flows go to SQL,
	// the rate-limit counters and the cron lock stay in-process. Setting an addr
	// moves all four to Redis, which is what running more than one replica
	// requires — see RedisConfig.
	v.SetDefault("redis.addr", "")
	v.SetDefault("redis.password", "")
	v.SetDefault("redis.db", 0)

	v.SetDefault("auth.session.cookie_name", "placard_session")
	v.SetDefault("auth.session.idle_ttl", "168h")
	v.SetDefault("auth.session.absolute_ttl", "720h")
	v.SetDefault("auth.registration_open", false)
	v.SetDefault("auth.oidc_auto_provision", true)
	// A slice default so an instance that configures no provider unmarshals to
	// an empty list rather than leaving the key absent.
	v.SetDefault("auth.oidc", []OIDCProviderConfig{})
	v.SetDefault("auth.dev_mock.enabled", false)
	v.SetDefault("auth.dev_mock.uid", "")
	v.SetDefault("auth.dev_mock.user", "")
	v.SetDefault("auth.dev_mock.email", "")
	v.SetDefault("auth.dev_mock.display_name", "")

	v.SetDefault("log.level", "info")
	v.SetDefault("log.file", "./logs/placard.log")
	v.SetDefault("log.max_size", 100) // MB
	v.SetDefault("log.max_backups", 5)
	v.SetDefault("log.max_age", 30) // days
	v.SetDefault("log.compress", true)

	v.SetDefault("token.max_ttl_days", 365) // quoted in the docs page (web/app.html) and skills/placard/SKILL.md

	// Left unset, both allowlists derive from server.base_url — see
	// deriveOrigins. A viper default here would win over that derivation and
	// silently pin every instance to defaultBaseURL.
	v.SetDefault("csrf.allowed_origins", []string{})
	v.SetDefault("cors.allowed_origins", []string{})

	v.SetDefault("upload.max_file_size", int64(10485760)) // 10MB; quoted in the docs page and SKILL.md

	v.SetDefault("avatar.gravatar_fallback", true)

	v.SetDefault("ratelimit.uploads_per_hour", 50) // quoted in the docs page and SKILL.md
	v.SetDefault("ratelimit.auth_per_minute", 30)
	v.SetDefault("ratelimit.auth_fail_per_minute", 20)
	v.SetDefault("ratelimit.anon_render_per_minute", 120)
	v.SetDefault("ratelimit.share_code_fail_per_minute", 10)

	v.SetDefault("cleanup.enabled", true)
	v.SetDefault("cleanup.interval_seconds", 3600)
	v.SetDefault("cleanup.lock_ttl_seconds", 300)
	v.SetDefault("cleanup.retry_max", 5)
	v.SetDefault("cleanup.view_recompute_every", 24)
	v.SetDefault("cleanup.user_delete_retention_days", 0)

	// An empty path means "defaults and env vars only" — see ResolvePath.
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config file: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	expandEnvInStruct(&cfg)
	cfg.deriveDataPaths()
	cfg.deriveOrigins()

	cfg.Env = env
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// deriveDataPaths fills every unset path from DataDir, so `data_dir` alone
// moves the whole instance. An explicitly configured path always wins.
func (c *Config) deriveDataPaths() {
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir
	}
	if c.Database.SQLite.Path == "" {
		c.Database.SQLite.Path = filepath.Join(c.DataDir, "placard.db")
	}
	if c.Storage.Local.Dir == "" {
		c.Storage.Local.Dir = filepath.Join(c.DataDir, "objects")
	}
}

// deriveOrigins fills the CSRF and CORS allowlists from server.base_url when
// they are unset. The web UI talks to its own origin, so setting base_url alone
// has to be enough — an allowlist that does not contain it makes
// middleware.CSRF reject every cookie-channel write with a 403.
func (c *Config) deriveOrigins() {
	origin := strings.TrimRight(c.Server.BaseURL, "/")
	if origin == "" {
		return
	}
	if len(c.CSRF.AllowedOrigins) == 0 {
		c.CSRF.AllowedOrigins = []string{origin}
	}
	if len(c.CORS.AllowedOrigins) == 0 {
		c.CORS.AllowedOrigins = []string{origin}
	}
}

// SecretKeyPath is where EnsureSecretKey persists a generated key.
func (c *Config) SecretKeyPath() string { return filepath.Join(c.DataDir, "secret.key") }

// secretKeyBytes is the generated key length; hex-encoded on disk.
const secretKeyBytes = 32

// EnsureSecretKey resolves server.secret_key: a configured value is kept as
// is, otherwise the key is read back from <data_dir>/secret.key, and on the
// very first start generated there (0600) so an instance run with no config
// still holds a stable secret across restarts.
//
// Losing or rotating the file invalidates everything derived from the key —
// personal access tokens included.
func (c *Config) EnsureSecretKey() error {
	if c.Server.SecretKey != "" {
		return nil
	}
	path := c.SecretKeyPath()
	switch key, err := os.ReadFile(path); {
	case err == nil:
		c.Server.SecretKey = strings.TrimSpace(string(key))
		if c.Server.SecretKey == "" {
			return fmt.Errorf("secret key file %s is empty", path)
		}
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("read secret key %s: %w", path, err)
	}

	raw := make([]byte, secretKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate secret key: %w", err)
	}
	key := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", filepath.Dir(path), err)
	}

	// Write the key somewhere else and link it into place, so secret.key is
	// only ever visible complete: two instances sharing a data_dir must agree
	// on the key, and a create-then-write would let the loser read the
	// winner's still-empty file. Link fails rather than overwrites, so the
	// loser reads the winner's key instead of replacing it.
	tmp, err := os.CreateTemp(filepath.Dir(path), "secret.key.*")
	if err != nil {
		return fmt.Errorf("create secret key %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if err := writeSecretKeyFile(tmp, key); err != nil {
		return fmt.Errorf("write secret key %s: %w", path, err)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return c.EnsureSecretKey()
		}
		return fmt.Errorf("create secret key %s: %w", path, err)
	}
	c.Server.SecretKey = key
	return nil
}

func writeSecretKeyFile(f *os.File, key string) error {
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.WriteString(key); err != nil {
		return err
	}
	return f.Sync()
}

// Validate runs fail-fast checks on the values a wrong setting would otherwise
// only reveal at the first request: port range, database driver, storage
// backend completeness, session TTL sanity and cleanup cadence. ${VAR}
// placeholders left unset by os.ExpandEnv expand to "", so the non-empty
// checks catch them without a literal "${" scan.
//
// Everything else is allowed to be absent: a self-hoster must be able to start
// the server in an empty directory with no config file at all. The one
// env-dependent rule is dev_mock, which injects a fixed identity ahead of every
// credential check and must never be reachable outside local.
func (c *Config) Validate() error {
	var errs []string

	// --- all environments ---
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, "server.port must be in 1..65535")
	}
	switch c.Database.Driver {
	case "", DriverSQLite:
	case DriverPostgres:
		if c.Database.DSN == "" {
			errs = append(errs, "database.dsn required for the postgres driver")
		}
	default:
		errs = append(errs, "database.driver must be sqlite or postgres")
	}
	// Storage is validated in every environment: an s3 backend missing its
	// bucket or credentials must fail at boot rather than at the first publish.
	switch c.Storage.Type {
	case "", "local":
	case "s3":
		if c.Storage.S3.Bucket == "" {
			errs = append(errs, "storage.s3.bucket required")
		}
		if c.Storage.S3.Region == "" {
			errs = append(errs, "storage.s3.region required (use \"auto\" for Cloudflare R2)")
		}
		if c.Storage.S3.AccessKey == "" {
			errs = append(errs, "storage.s3.access_key required")
		}
		if c.Storage.S3.SecretKey == "" {
			errs = append(errs, "storage.s3.secret_key required")
		}
	default:
		errs = append(errs, "storage.type must be local or s3")
	}
	if c.Server.MinCLIVersion != "" && !version.IsRelease(c.Server.MinCLIVersion) {
		errs = append(errs, "server.min_cli_version must be a release version such as 1.2.0")
	}
	if c.Auth.Session.IdleTTL <= 0 || c.Auth.Session.AbsoluteTTL < c.Auth.Session.IdleTTL {
		errs = append(errs, "auth.session: idle_ttl must be > 0 and absolute_ttl >= idle_ttl")
	}
	if c.Cleanup.Enabled {
		if c.Cleanup.IntervalSeconds <= 0 {
			errs = append(errs, "cleanup.interval_seconds must be > 0")
		}
		if c.Cleanup.LockTTLSeconds <= 0 {
			errs = append(errs, "cleanup.lock_ttl_seconds must be > 0")
		}
		if c.Cleanup.UserDeleteRetentionDays < 0 {
			errs = append(errs, "cleanup.user_delete_retention_days must be >= 0")
		}
	}

	errs = append(errs, c.validateOIDC()...)

	// dev_mock injects a fixed identity ahead of every credential check;
	// never permit it outside local.
	if c.Auth.DevMock.Enabled && c.Env != "local" {
		errs = append(errs, "auth.dev_mock.enabled must be false outside APP_ENV=local")
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation: %s", strings.Join(errs, "; "))
	}
	return nil
}

// validateOIDC checks every configured provider at boot rather than at the
// first login attempt.
//
// Names are the identity column's value, so they must be present, unique and
// never model.ProviderLocal — a provider named "local" would resolve to the
// password credentials of whatever account happened to share the subject.
// The name is not compared against that constant by import (config must not
// depend on model); the literal is repeated here deliberately.
func (c *Config) validateOIDC() []string {
	var errs []string
	seen := make(map[string]struct{}, len(c.Auth.OIDC))
	for i, p := range c.Auth.OIDC {
		where := fmt.Sprintf("auth.oidc[%d]", i)
		name := strings.ToLower(strings.TrimSpace(p.Name))
		switch {
		case name == "":
			errs = append(errs, where+".name required")
		case name == "local":
			errs = append(errs, where+`.name must not be "local" (reserved for password accounts)`)
		default:
			if _, dup := seen[name]; dup {
				errs = append(errs, where+".name "+name+" is configured twice")
			}
			seen[name] = struct{}{}
		}
		if p.Issuer == "" {
			errs = append(errs, where+".issuer required")
		}
		if p.ClientID == "" {
			errs = append(errs, where+".client_id required")
		}
		if p.ClientSecret == "" {
			errs = append(errs, where+".client_secret required")
		}
	}
	return errs
}

// expandEnvInStruct recursively expands ${VAR} references in all string fields.
//
// Slices are walked too, both the struct elements (auth.oidc[], whose client
// secrets are credentials like any other in the file) and the plain string
// ones: csrf.allowed_origins and cors.allowed_origins are lists of strings, and
// an unexpanded ${VAR} there fails silently — the origin simply never matches,
// and every cookie-channel write 403s with nothing saying why.
func expandEnvInStruct(v interface{}) {
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < val.NumField(); i++ {
		f := val.Field(i)
		switch f.Kind() {
		case reflect.String:
			if strings.Contains(f.String(), "${") {
				f.SetString(os.ExpandEnv(f.String()))
			}
		case reflect.Struct:
			expandEnvInStruct(f.Addr().Interface())
		case reflect.Slice:
			for j := 0; j < f.Len(); j++ {
				switch e := f.Index(j); e.Kind() {
				case reflect.Struct:
					expandEnvInStruct(e.Addr().Interface())
				case reflect.String:
					if strings.Contains(e.String(), "${") {
						e.SetString(os.ExpandEnv(e.String()))
					}
				}
			}
		}
	}
}
