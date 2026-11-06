package migrate_test

import (
	"testing"

	"github.com/Xm798/placard/internal/migrate"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/testutil"
)

// A database created by v0.1.0 has 0001 recorded and neither epoch column;
// 0002 has to add them rather than rely on 0001's AutoMigrate.
func TestSessionEpochMigrationUpgradesAnExistingDatabase(t *testing.T) {
	db := testutil.OpenTestDB(t)
	m := db.Migrator()
	if err := m.DropColumn(&model.Session{}, "Epoch"); err != nil {
		t.Fatal(err)
	}
	if err := m.DropColumn(&model.User{}, "SessionEpoch"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM "+migrate.HistoryTable+" WHERE id = ?", "0002_session_epoch").Error; err != nil {
		t.Fatal(err)
	}

	if err := migrate.Run(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !m.HasColumn(&model.User{}, "SessionEpoch") || !m.HasColumn(&model.Session{}, "Epoch") {
		t.Fatal("0002 left an epoch column missing")
	}
}
