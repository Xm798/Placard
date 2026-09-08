package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// blankEnvVars clears (sets to "") the env vars viper's AutomaticEnv would bind
// to the keys these tests assert on, so Load tests are hermetic against a
// developer shell that happens to export them. Env overrides yaml in viper, so
// without this a stray PLACARD_DATA_DIR would mask a derivation assertion.
func blankEnvVars(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DATA_DIR", "DATABASE_DSN", "DATABASE_DRIVER", "DATABASE_SQLITE_PATH",
		"SERVER_SECRET_KEY", "SERVER_BASE_URL", "REDIS_ADDR",
		"STORAGE_TYPE", "STORAGE_LOCAL_DIR",
		"STORAGE_S3_ENDPOINT", "STORAGE_S3_REGION", "STORAGE_S3_BUCKET",
		"STORAGE_S3_ACCESS_KEY", "STORAGE_S3_SECRET_KEY",
	} {
		// Both spellings: the prefixed one viper binds today, and the bare one
		// it used to, which a test must also prove is now ignored.
		t.Setenv(EnvPrefix+"_"+k, "")
		t.Setenv(k, "")
	}
	// Read directly by ResolveEnv / ResolvePath rather than bound by viper.
	t.Setenv("APP_ENV", "")
	t.Setenv("PLACARD_CONFIG", "")
}

// validConfig returns a Config that passes Validate() under env=prod.
func validConfig() *Config {
	return &Config{
		Env:     "prod",
		DataDir: "./data",
		Server: ServerConfig{
			Port:    8080,
			BaseURL: "http://localhost:8080",
		},
		Database: DatabaseConfig{Driver: DriverSQLite, SQLite: SQLiteConfig{Path: "./data/placard.db"}},
		Auth: AuthConfig{
			Session: SessionConfig{CookieName: "placard_session", IdleTTL: time.Hour, AbsoluteTTL: 2 * time.Hour},
		},
		Cleanup: CleanupConfig{
			Enabled:         true,
			IntervalSeconds: 60,
			LockTTLSeconds:  300,
		},
	}
}

// A self-hoster must be able to start in an empty directory with no config
// file, no env vars and no flags — so the pure-defaults config has to validate.
func TestLoad_DefaultsValidateWithNoConfigFile(t *testing.T) {
	blankEnvVars(t)
	t.Chdir(t.TempDir())
	cfg, err := Load("", "prod")
	if err != nil {
		t.Fatalf("Load with no config file: %v", err)
	}
	if cfg.DataDir != DefaultDataDir {
		t.Errorf("data_dir = %q, want %q", cfg.DataDir, DefaultDataDir)
	}
	if cfg.Database.Driver != DriverSQLite {
		t.Errorf("database.driver = %q, want %q", cfg.Database.Driver, DriverSQLite)
	}
	if !cfg.Database.AutoMigrate {
		t.Error("database.auto_migrate = false, want true — an unattended instance migrates itself")
	}
}

// data_dir is the single knob that moves an instance: every unset path derives
// from it.
func TestLoad_DerivesPathsFromDataDir(t *testing.T) {
	blankEnvVars(t)
	yaml := "data_dir: /srv/placard\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join("/srv/placard", "placard.db"); cfg.Database.SQLite.Path != want {
		t.Errorf("database.sqlite.path = %q, want %q", cfg.Database.SQLite.Path, want)
	}
	if want := filepath.Join("/srv/placard", "objects"); cfg.Storage.Local.Dir != want {
		t.Errorf("storage.local.dir = %q, want %q", cfg.Storage.Local.Dir, want)
	}
	if want := filepath.Join("/srv/placard", "secret.key"); cfg.SecretKeyPath() != want {
		t.Errorf("secret key path = %q, want %q", cfg.SecretKeyPath(), want)
	}
}

func TestLoad_ExplicitPathsOverrideDataDir(t *testing.T) {
	blankEnvVars(t)
	yaml := `
data_dir: /srv/placard
database:
  sqlite:
    path: /db/other.sqlite
storage:
  local:
    dir: /mnt/objects
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Database.SQLite.Path != "/db/other.sqlite" {
		t.Errorf("database.sqlite.path = %q, want the configured value", cfg.Database.SQLite.Path)
	}
	if cfg.Storage.Local.Dir != "/mnt/objects" {
		t.Errorf("storage.local.dir = %q, want the configured value", cfg.Storage.Local.Dir)
	}
}

// Setting server.base_url alone has to be enough: the web UI talks to its own
// origin, and a CSRF allowlist that does not contain it 403s every publish,
// settings save and logout.
func TestLoad_OriginsFollowBaseURL(t *testing.T) {
	blankEnvVars(t)
	t.Chdir(t.TempDir())
	t.Setenv("PLACARD_SERVER_BASE_URL", "https://p.example.com/")

	cfg, err := Load("", "prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"https://p.example.com"}
	if !reflect.DeepEqual(cfg.CSRF.AllowedOrigins, want) {
		t.Errorf("csrf.allowed_origins = %v, want %v", cfg.CSRF.AllowedOrigins, want)
	}
	if !reflect.DeepEqual(cfg.CORS.AllowedOrigins, want) {
		t.Errorf("cors.allowed_origins = %v, want %v", cfg.CORS.AllowedOrigins, want)
	}
}

func TestLoad_ConfiguredOriginsWinOverBaseURL(t *testing.T) {
	blankEnvVars(t)
	yaml := `
server:
  base_url: https://p.example.com
csrf:
  allowed_origins: ["https://csrf.example.com"]
cors:
  allowed_origins: ["https://cors.example.com"]
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.CSRF.AllowedOrigins; !reflect.DeepEqual(got, []string{"https://csrf.example.com"}) {
		t.Errorf("csrf.allowed_origins = %v, want the configured value", got)
	}
	if got := cfg.CORS.AllowedOrigins; !reflect.DeepEqual(got, []string{"https://cors.example.com"}) {
		t.Errorf("cors.allowed_origins = %v, want the configured value", got)
	}
}

func TestResolvePath(t *testing.T) {
	t.Setenv("PLACARD_CONFIG", "")
	t.Chdir(t.TempDir())

	if got := ResolvePath("/explicit.yaml"); got != "/explicit.yaml" {
		t.Errorf("explicit flag: got %q", got)
	}
	// No config.yaml in the working directory: the server must start on
	// defaults rather than fail looking for a file nobody asked for.
	if got := ResolvePath(""); got != "" {
		t.Errorf("no override and no file: got %q, want \"\"", got)
	}
	if err := os.WriteFile(DefaultConfigPath, []byte("server:\n  port: 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolvePath(""); got != DefaultConfigPath {
		t.Errorf("no override with a file present: got %q, want %q", got, DefaultConfigPath)
	}
	t.Setenv("PLACARD_CONFIG", "/env.yaml")
	if got := ResolvePath(""); got != "/env.yaml" {
		t.Errorf("PLACARD_CONFIG: got %q", got)
	}
	if got := ResolvePath("/explicit.yaml"); got != "/explicit.yaml" {
		t.Errorf("flag must beat PLACARD_CONFIG: got %q", got)
	}
}

// EnsureSecretKey is what makes an unconfigured instance's PATs survive a
// restart: the key it generates has to land on disk and be read back verbatim.
func TestEnsureSecretKey_GeneratesThenReuses(t *testing.T) {
	dir := t.TempDir()
	c := validConfig()
	c.DataDir = filepath.Join(dir, "data")

	if err := c.EnsureSecretKey(); err != nil {
		t.Fatalf("first EnsureSecretKey: %v", err)
	}
	generated := c.Server.SecretKey
	if len(generated) != secretKeyBytes*2 {
		t.Fatalf("generated key is %d chars, want %d hex chars", len(generated), secretKeyBytes*2)
	}

	info, err := os.Stat(c.SecretKeyPath())
	if err != nil {
		t.Fatalf("stat secret key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("secret.key mode = %04o, want 0600", perm)
	}

	next := validConfig()
	next.DataDir = c.DataDir
	if err := next.EnsureSecretKey(); err != nil {
		t.Fatalf("second EnsureSecretKey: %v", err)
	}
	if next.Server.SecretKey != generated {
		t.Errorf("restart read back %q, want the generated %q", next.Server.SecretKey, generated)
	}

	// The key is staged in a temp file and linked into place; nothing may be
	// left behind for an operator to mistake for a second key.
	entries, err := os.ReadDir(c.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "secret.key" {
			t.Errorf("data dir holds %q besides secret.key", e.Name())
		}
	}
}

func TestEnsureSecretKey_ConfiguredValueWins(t *testing.T) {
	c := validConfig()
	c.DataDir = t.TempDir()
	c.Server.SecretKey = "configured"
	if err := c.EnsureSecretKey(); err != nil {
		t.Fatalf("EnsureSecretKey: %v", err)
	}
	if c.Server.SecretKey != "configured" {
		t.Errorf("secret key = %q, want the configured value", c.Server.SecretKey)
	}
	if _, err := os.Stat(c.SecretKeyPath()); !os.IsNotExist(err) {
		t.Errorf("secret.key was written even though the key was configured: %v", err)
	}
}

// dev_mock injects a fixed identity ahead of every credential check, so it is
// the one setting whose legality depends on APP_ENV.
func TestValidate_DevMockOnlyUnderLocal(t *testing.T) {
	for _, env := range []string{"prod", "staging", ""} {
		c := validConfig()
		c.Env = env
		c.Auth.DevMock.Enabled = true
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "auth.dev_mock.enabled") {
			t.Errorf("env=%q: err = %v, want a dev_mock rejection", env, err)
		}
	}
	c := validConfig()
	c.Env = "local"
	c.Auth.DevMock.Enabled = true
	if err := c.Validate(); err != nil {
		t.Errorf("env=local: unexpected error: %v", err)
	}
}

func TestValidate_Driver(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(c *Config)
		wantSub string // "" = must validate
	}{
		{"sqlite", func(c *Config) { c.Database.Driver = DriverSQLite }, ""},
		{"empty_reads_as_sqlite", func(c *Config) { c.Database.Driver = "" }, ""},
		{"postgres_with_dsn", func(c *Config) {
			c.Database.Driver = DriverPostgres
			c.Database.DSN = "host=db user=placard dbname=placard"
		}, ""},
		{"postgres_without_dsn", func(c *Config) { c.Database.Driver = DriverPostgres }, "database.dsn required"},
		{"unknown", func(c *Config) { c.Database.Driver = "mongodb" }, "database.driver must be sqlite or postgres"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(c)
			err := c.Validate()
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestValidate_MinCLIVersion(t *testing.T) {
	for _, v := range []string{"", "1.2.0", "0.1.0-rc.1"} {
		c := validConfig()
		c.Server.MinCLIVersion = v
		if err := c.Validate(); err != nil {
			t.Errorf("min_cli_version=%q: unexpected error: %v", v, err)
		}
	}
	// A version the gate cannot compare would silently disable it, so it is
	// rejected at boot instead.
	for _, v := range []string{"v1.2.0", "latest", "1.2.0-dirty"} {
		c := validConfig()
		c.Server.MinCLIVersion = v
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "server.min_cli_version") {
			t.Errorf("min_cli_version=%q: err = %v, want a rejection", v, err)
		}
	}
}

func TestValidate_PortRange(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 70000} {
		c := validConfig()
		c.Server.Port = port
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "server.port") {
			t.Errorf("port=%d: expected server.port error, got %v", port, err)
		}
	}
	// boundaries 1 and 65535 are valid.
	for _, port := range []int{1, 65535} {
		c := validConfig()
		c.Server.Port = port
		if err := c.Validate(); err != nil {
			t.Errorf("port=%d: unexpected error %v", port, err)
		}
	}
}

func TestValidate_CleanupParams(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(c *Config)
		wantSub string
	}{
		{"interval", func(c *Config) { c.Cleanup.IntervalSeconds = 0 }, "cleanup.interval_seconds"},
		{"lock_ttl", func(c *Config) { c.Cleanup.LockTTLSeconds = 0 }, "cleanup.lock_ttl_seconds"},
		{"user_delete_retention", func(c *Config) { c.Cleanup.UserDeleteRetentionDays = -1 }, "cleanup.user_delete_retention_days"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(c)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("%s: err = %v", tc.name, err)
			}
		})
	}
	// Disabled cleanup skips the cadence checks.
	c := validConfig()
	c.Cleanup.Enabled = false
	c.Cleanup.IntervalSeconds = 0
	c.Cleanup.LockTTLSeconds = 0
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled cleanup should skip cadence checks: %v", err)
	}
}

// A path someone actually named must fail loudly when it is missing, so a typo
// in --config or PLACARD_CONFIG cannot silently boot on defaults.
func TestLoad_MissingNamedConfigFileIsFatal(t *testing.T) {
	blankEnvVars(t)
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), "prod")
	if err == nil || !strings.Contains(err.Error(), "read config file") {
		t.Fatalf("err = %v, want a read config file error", err)
	}
}

// TestValidate_Storage asserts the backend selection is validated in every
// environment: an s3 backend missing its bucket, region or credentials must
// fail at boot rather than at the first publish.
func TestValidate_Storage(t *testing.T) {
	validS3 := func() StorageConfig {
		return StorageConfig{
			Type: "s3",
			S3: S3Config{
				Endpoint:  "https://s3.example",
				Region:    "auto",
				Bucket:    "placard",
				AccessKey: "ak",
				SecretKey: "sk",
			},
		}
	}
	cases := []struct {
		name    string
		storage StorageConfig
		wantSub string // "" = must validate
	}{
		{"unset_reads_as_local", StorageConfig{}, ""},
		{"explicit_local", StorageConfig{Type: "local", Local: LocalStorageConfig{Dir: "/var/lib/placard/objects"}}, ""},
		{"complete_s3", validS3(), ""},
		{"unknown_type", StorageConfig{Type: "gcs"}, "storage.type must be local or s3"},
		{"s3_no_bucket", func() StorageConfig { c := validS3(); c.S3.Bucket = ""; return c }(), "storage.s3.bucket required"},
		{"s3_no_region", func() StorageConfig { c := validS3(); c.S3.Region = ""; return c }(), "storage.s3.region required"},
		{"s3_no_access_key", func() StorageConfig { c := validS3(); c.S3.AccessKey = ""; return c }(), "storage.s3.access_key required"},
		{"s3_no_secret_key", func() StorageConfig { c := validS3(); c.S3.SecretKey = ""; return c }(), "storage.s3.secret_key required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, env := range []string{"local", "prod"} {
				c := validConfig()
				c.Env = env
				c.Storage = tc.storage
				err := c.Validate()
				if tc.wantSub == "" {
					if err != nil {
						t.Fatalf("env=%s: unexpected error: %v", env, err)
					}
					continue
				}
				if err == nil {
					t.Fatalf("env=%s: expected error containing %q, got nil", env, tc.wantSub)
				}
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("env=%s: error = %q, want substring %q", env, err.Error(), tc.wantSub)
				}
			}
		})
	}
}

// ${VAR} placeholders left unset by os.ExpandEnv must expand to "" rather than
// reaching a backend as a literal.
func TestLoad_ExpandsUnsetPlaceholderToEmpty(t *testing.T) {
	blankEnvVars(t)
	yaml := `
storage:
  type: "local"
  s3:
    access_key: "${STORAGE_S3_ACCESS_KEY}"
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "local")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.S3.AccessKey != "" {
		t.Errorf("storage.s3.access_key = %q, want \"\"", cfg.Storage.S3.AccessKey)
	}
}

func TestLoadAuthDefaults(t *testing.T) {
	blankEnvVars(t)
	cfg, err := Load("", "local")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Session.CookieName != "placard_session" || cfg.Auth.Session.IdleTTL != 168*time.Hour || cfg.Auth.Session.AbsoluteTTL != 720*time.Hour {
		t.Fatalf("session defaults: %+v", cfg.Auth.Session)
	}
	if cfg.RateLimit.AuthFailPerMinute != 20 {
		t.Fatalf("auth ratelimit defaults: %+v", cfg.RateLimit)
	}
	// An unset budget must not read as 0: that is what protects a 6-digit
	// share code, and zero would refuse every attempt including correct ones.
	if cfg.RateLimit.ShareCodeFailPerMinute != 10 {
		t.Fatalf("share code ratelimit default = %d, want 10", cfg.RateLimit.ShareCodeFailPerMinute)
	}
}

func TestLoadOIDCProvidersFromFile(t *testing.T) {
	blankEnvVars(t)
	t.Setenv("SSO_SECRET", "from-the-environment")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
auth:
  oidc:
    - name: Corp
      display_name: Company SSO
      issuer: https://sso.example.com
      client_id: placard
      client_secret: ${SSO_SECRET}
      scopes: [openid, email]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path, "prod")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Auth.OIDC) != 1 {
		t.Fatalf("providers = %+v, want one", cfg.Auth.OIDC)
	}
	p := cfg.Auth.OIDC[0]
	if p.ClientSecret != "from-the-environment" {
		t.Errorf("client_secret = %q, want the expanded ${SSO_SECRET}", p.ClientSecret)
	}
	if p.Label() != "Company SSO" {
		t.Errorf("label = %q, want the display name", p.Label())
	}
	if !reflect.DeepEqual(p.Scopes, []string{"openid", "email"}) {
		t.Errorf("scopes = %v", p.Scopes)
	}
}

func TestValidateOIDC(t *testing.T) {
	complete := OIDCProviderConfig{
		Name: "corp", Issuer: "https://sso.example.com",
		ClientID: "placard", ClientSecret: "s3cret",
	}
	withName := func(name string) OIDCProviderConfig {
		p := complete
		p.Name = name
		return p
	}

	cases := []struct {
		name      string
		providers []OIDCProviderConfig
		wantErr   string
	}{
		{name: "complete", providers: []OIDCProviderConfig{complete}},
		{name: "none"},
		{name: "missing name", providers: []OIDCProviderConfig{withName("")}, wantErr: "name required"},
		{name: "reserved name", providers: []OIDCProviderConfig{withName("local")}, wantErr: `must not be "local"`},
		{
			name:      "duplicate name",
			providers: []OIDCProviderConfig{complete, withName("CORP")},
			wantErr:   "configured twice",
		},
		{
			name: "missing secret",
			providers: []OIDCProviderConfig{{
				Name: "corp", Issuer: "https://sso.example.com", ClientID: "placard",
			}},
			wantErr: "client_secret required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Auth.OIDC = tc.providers
			err := cfg.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want an error mentioning %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("Validate() = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadAvatarDefault(t *testing.T) {
	blankEnvVars(t)
	cfg, err := Load("", "local")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Avatar.GravatarFallback {
		t.Error("avatar.gravatar_fallback default = false, want true")
	}
}

// Every server setting is addressed as PLACARD_<KEY>. The prefix is what keeps
// a container's generic DATA_DIR / SERVER_PORT — names anything else in the
// same environment may already own — from silently reconfiguring the instance.
func TestLoad_EnvOverridesRequireThePlacardPrefix(t *testing.T) {
	blankEnvVars(t)
	t.Chdir(t.TempDir())
	t.Setenv("PLACARD_SERVER_PORT", "9090")
	t.Setenv("PLACARD_DATA_DIR", "/srv/placard")
	t.Setenv("SERVER_PORT", "1234")

	cfg, err := Load("", "prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("server.port = %d, want the PLACARD_SERVER_PORT value 9090", cfg.Server.Port)
	}
	if cfg.DataDir != "/srv/placard" {
		t.Errorf("data_dir = %q, want the PLACARD_DATA_DIR value", cfg.DataDir)
	}
}

// The []string keys are reachable from the environment as a comma-separated
// list — viper's default decoder runs StringToSliceHookFunc(","). The docs
// promise it, so a decoder change that silently dropped the hook would leave a
// reverse-proxy deployment configuring trusted_proxies into the void.
func TestLoad_StringSliceKeysAcceptCommaSeparatedEnv(t *testing.T) {
	blankEnvVars(t)
	t.Chdir(t.TempDir())
	t.Setenv("PLACARD_SERVER_TRUSTED_PROXIES", "10.0.0.1,10.0.0.2")
	t.Setenv("PLACARD_CSRF_ALLOWED_ORIGINS", "https://a.example.com,https://b.example.com")

	cfg, err := Load("", "prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"10.0.0.1", "10.0.0.2"}; !reflect.DeepEqual(cfg.Server.TrustedProxies, want) {
		t.Errorf("server.trusted_proxies = %v, want %v", cfg.Server.TrustedProxies, want)
	}
	if want := []string{"https://a.example.com", "https://b.example.com"}; !reflect.DeepEqual(cfg.CSRF.AllowedOrigins, want) {
		t.Errorf("csrf.allowed_origins = %v, want %v", cfg.CSRF.AllowedOrigins, want)
	}
	// Untouched by the environment, so it still derives from base_url.
	if want := []string{defaultBaseURL}; !reflect.DeepEqual(cfg.CORS.AllowedOrigins, want) {
		t.Errorf("cors.allowed_origins = %v, want %v", cfg.CORS.AllowedOrigins, want)
	}
}

// auth.oidc is a slice of STRUCTS, which no env encoding reaches — the one key
// that genuinely requires a config file, and the reason an SSO instance needs
// one at all. An attempt to set it from the environment is refused at startup
// rather than half-decoded into a provider nobody configured.
func TestLoad_OIDCProvidersAreFileOnly(t *testing.T) {
	blankEnvVars(t)
	t.Chdir(t.TempDir())
	t.Setenv("PLACARD_AUTH_OIDC", "corp")

	if _, err := Load("", "prod"); err == nil {
		t.Fatal("Load accepted auth.oidc from the environment, want a parse error")
	}
}
