// Package migrate owns the database schema. Migrations are Go code applied by
// gormigrate at server start (database.auto_migrate), so a self-hoster never
// runs a separate schema step and the test helpers build the same schema the
// server does.
package migrate

import (
	"context"
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/model"
)

// Models is the full set of tables, in the order 0001 creates them. Exported
// so the test helpers can reset a schema they did not build.
func Models() []interface{} {
	return []interface{}{
		&model.File{},
		&model.FileVersion{},
		&model.Token{},
		&model.View{},
		&model.AuditLog{},
		&model.PendingObjectDelete{},
		&model.User{},
		&model.UserIdentity{},
		&model.Setting{},
		&model.Session{},
		&model.DeviceCode{},
		&model.DeviceChallenge{},
	}
}

// HistoryTable is where gormigrate records the ids it has applied.
const HistoryTable = "migrations"

// migrations is the ordered schema history. Until the first release, 0001 is
// edited in place rather than followed by a 0002: there is no deployed database
// to carry forward, so a schema change belongs in the initial migration.
func migrations() []*gormigrate.Migration {
	return []*gormigrate.Migration{
		{
			ID: "0001_initial_schema",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(Models()...)
			},
			Rollback: func(tx *gorm.DB) error {
				for _, m := range Models() {
					if err := tx.Migrator().DropTable(m); err != nil {
						return err
					}
				}
				return nil
			},
		},
	}
}

// advisoryLockKey identifies Placard's schema lock among every other advisory
// lock on the server. An arbitrary constant — only its uniqueness matters.
const advisoryLockKey int64 = 0x504c4143 // "PLAC"

// Run applies every pending migration. It is idempotent: gormigrate records
// applied ids and skips them on the next start.
//
// On Postgres it holds an advisory lock for the duration. auto_migrate is on by
// default, so two replicas restarting together both reach this; without the
// lock the loser's CREATE TABLE hits the winner's half-built schema, and a
// failed migration is fatal at boot (see main.openDB) — a crash loop until the
// winner happens to finish first. SQLite needs none: it is one file, served by
// one process.
func Run(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return run(db)
	}
	return withAdvisoryLock(db, run)
}

func run(db *gorm.DB) error {
	return gormigrate.New(db, gormigrate.DefaultOptions, migrations()).Migrate()
}

// withAdvisoryLock runs fn while holding Placard's schema lock on a dedicated
// connection. The lock is session-scoped, so it must be taken and released on
// one connection — fn itself runs on the pool as usual, since the lock only has
// to exclude the OTHER process.
func withAdvisoryLock(db *gorm.DB, fn func(*gorm.DB) error) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey) //nolint:errcheck // closing the connection releases it anyway

	return fn(db)
}
