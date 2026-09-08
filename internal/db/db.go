// Package db opens the shared GORM handle for Placard binaries. The server
// (main.go) and any operator tool must open the database the same way — driver
// selection, pragmas, pool sizing — or an operator tool could behave
// differently from the app it is operating on. Schema migration is deliberately
// NOT here: it lives in internal/migrate, which the server applies at start.
package db

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Xm798/placard/internal/config"
	"github.com/Xm798/placard/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// sqliteParams are appended to the SQLite path.
//
// WAL lets readers run while a write is in flight, and busy_timeout absorbs the
// lock contention a concurrent publish causes instead of failing the request.
//
// _txlock=immediate is what makes busy_timeout sufficient. SQLite otherwise
// begins a transaction deferred and takes the write lock only at the first
// write, so two transactions that each read then write deadlock on the upgrade
// — a conflict no timeout can resolve, because neither side can yield. Taking
// the lock at BEGIN turns that into ordinary contention the busy handler waits
// out. Read-only queries outside a transaction are unaffected and still run in
// parallel, which is most of the render path.
const sqliteParams = "_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"

// Open connects GORM to the database named by cfg.Database.Driver.
func Open(cfg *config.Config) (*gorm.DB, error) {
	gormCfg := &gorm.Config{
		// Every auto timestamp is UTC at second resolution — see model.Timestamp
		// for why the two dialects need the same one.
		NowFunc: model.Now,
		// Parameterized queries — logged SQL is the template, never the bound
		// argument values — are enforced by the logger, not by a field here:
		// gorm.Config has no ParameterizedQueries (that field belongs to
		// gorm/logger.Config, which only the built-in Printf logger reads).
		// GORM asks the logger instead, via the gorm.ParamsFilter interface;
		// see zapGormLogger.ParamsFilter in gormlog.go, which is what keeps
		// user data out of every slow/error line. Do not drop it.
		Logger: newZapGormLogger(),
		// Map each driver's own constraint-violation error onto GORM's
		// gorm.ErrDuplicatedKey, so a caller that has to tell "already taken"
		// from a real failure (account registration) tests one sentinel
		// instead of matching SQLite and Postgres message text separately.
		TranslateError: true,
	}

	var (
		gdb *gorm.DB
		err error
	)
	switch cfg.Database.Driver {
	case "", config.DriverSQLite:
		gdb, err = openSQLite(cfg.Database.SQLite.Path, gormCfg)
	case config.DriverPostgres:
		gdb, err = gorm.Open(postgres.Open(cfg.Database.DSN), gormCfg)
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", cfg.Database.Driver)
	}
	if err != nil {
		return nil, err
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, err
	}
	if cfg.Database.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	}
	if cfg.Database.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	}
	return gdb, nil
}

// openSQLite creates the database file's parent directory before opening, so a
// first start under a fresh data_dir succeeds without an operator mkdir.
func openSQLite(path string, gormCfg *gorm.Config) (*gorm.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("database.sqlite.path is empty")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create database dir %s: %w", dir, err)
		}
	}
	return gorm.Open(sqlite.Open(path+"?"+sqliteParams), gormCfg)
}
