//go:build integration

// Package testutil holds shared test helpers. It is imported only from _test.go
// files, so it is never linked into the production binary.
package testutil

import (
	"os"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/config"
	appdb "github.com/Xm798/placard/internal/db"
	"github.com/Xm798/placard/internal/migrate"
)

// defaultDSN is the docker-compose Postgres the integration tests run against.
// Override with TEST_DSN.
const defaultDSN = "host=127.0.0.1 port=5432 user=placard password=placard dbname=placard sslmode=disable TimeZone=UTC"

// allTables is truncated before each test, ordered child-before-parent (no FKs
// today, but keeps the list stable if any are added).
var allTables = []string{"view", "file_version", "file", "token", "audit_log", "pending_object_delete", "user_identity", "user", "setting", "session", "device_code", "device_challenge"}

// migrateOnce guards the per-process migration: the schema is identical across
// tests in a run, so migrating once avoids a round-trip storm on every setup.
var migrateOnce sync.Once

func dsn() string {
	if d := os.Getenv("TEST_DSN"); d != "" {
		return d
	}
	return defaultDSN
}

// OpenTestDB opens the compose Postgres, migrates once per process, then
// TRUNCATEs every table so the caller starts clean on the shared database.
// Because the database is shared, integration packages must run serialized
// (`go test -p 1`).
//
// It goes through the production opener (internal/db.Open) so the pool and
// GORM config the tests run on are the ones a real instance runs on.
func OpenTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Driver = config.DriverPostgres
	cfg.Database.DSN = dsn()
	cfg.Database.MaxOpenConns = 10
	cfg.Database.MaxIdleConns = 10

	db, err := appdb.Open(cfg)
	if err != nil {
		t.Fatalf("open postgres (is `docker compose up -d` running?): %v", err)
	}
	// Each test opens its own *gorm.DB. Tests run serially within a package and
	// never closed these handles, so the pooled connections accumulated across
	// the whole run and eventually tripped the server's connection limit.
	// Release each test's pool at cleanup; migrateOnce is a sync.Once
	// independent of any single handle, so closing here is safe.
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	migrateOnce.Do(func() {
		// Drop first, then migrate. The compose database survives across runs
		// and gormigrate skips an id it has already recorded, so an edited
		// 0001 (which is how the schema changes before the first release)
		// would never reach it — integration runs would silently keep testing
		// yesterday's columns while the SQLite runs used today's.
		tables := append(migrate.Models(), migrate.HistoryTable)
		if err := db.Migrator().DropTable(tables...); err != nil {
			t.Fatalf("drop schema: %v", err)
		}
		if err := migrate.Run(db); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	})
	for _, tbl := range allTables {
		if err := db.Exec(`TRUNCATE TABLE "` + tbl + `" RESTART IDENTITY CASCADE`).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	return db
}
