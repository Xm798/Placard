//go:build !integration

// Package testutil holds shared test helpers. It is imported only from _test.go
// files, so it is never linked into the production binary.
package testutil

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/config"
	appdb "github.com/Xm798/placard/internal/db"
	"github.com/Xm798/placard/internal/migrate"
)

// OpenTestDB opens a private SQLite database and applies the migrations. Every
// call gets its own, so tests start clean without truncating anything and need
// no external service.
//
// It goes through the production opener (internal/db.Open) so the pragmas,
// transaction locking and pool the tests run on are the ones a real instance
// runs on.
//
// A file under t.TempDir() rather than mode=memory: an in-memory database needs
// cache=shared to survive between statements, and shared cache serializes on
// table locks no busy handler retries — a test with concurrent transactions
// would fail there for a reason production never hits.
//
// Build with -tags=integration to run the same tests against Postgres instead;
// see db_integration.go.
func OpenTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Driver = config.DriverSQLite
	cfg.Database.SQLite.Path = filepath.Join(t.TempDir(), "placard-test.db")
	cfg.Database.MaxOpenConns = 10
	cfg.Database.MaxIdleConns = 10

	db, err := appdb.Open(cfg)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := migrate.Run(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}
