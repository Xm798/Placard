package repo

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Xm798/placard/internal/model"
)

// ViewRepo is the data-access surface for the view table — the single
// authoritative source for view counts.
type ViewRepo struct {
	db *gorm.DB
}

// NewViewRepo builds a ViewRepo bound to db.
func NewViewRepo(db *gorm.DB) *ViewRepo {
	return &ViewRepo{db: db}
}

// Upsert records a view of fileNanoID by viewer (an authz id). It first verifies the
// file exists and is not deleted (defends against counting deleted/missing
// files), then UPSERTs the (file_nano_id, viewer) row: first insert sets the
// timestamps explicitly (avoids 0000-00-00), conflict increments view_count and
// refreshes last_viewed_at/update_time. viewer_name_snapshot is preserved on
// conflict (keeps first-seen snapshot, avoids hot-row write amplification).
//
// Returns gorm.ErrRecordNotFound when the file is absent/deleted.
func (r *ViewRepo) Upsert(ctx context.Context, fileNanoID, viewer, displayName string) error {
	db := r.db.WithContext(ctx)
	var count int64
	if err := db.Model(&model.File{}).
		Where("nano_id = ? AND is_deleted = 0", fileNanoID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}

	now := model.Now()
	view := model.View{
		FileNanoID:         fileNanoID,
		Viewer:             viewer,
		ViewerNameSnapshot: displayName,
		ViewCount:          1,
		LastViewedAt:       now,
		UpdateTime:         now,
		CreateUser:         viewer,
		UpdateUser:         viewer,
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "file_nano_id"}, {Name: "viewer"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			// Table-qualified and double-quoted: bare view_count is ambiguous
			// inside Postgres' DO UPDATE (it could mean the proposed row), and
			// double quotes are how both dialects spell an identifier that is
			// also a keyword.
			"view_count":     gorm.Expr(`"view".view_count + 1`),
			"last_viewed_at": now,
			"update_time":    now,
			"update_user":    viewer,
		}),
	}).Create(&view).Error
}

// DeleteByFile removes every view row for fileNanoID inside tx, run in the same
// transaction as the file soft-delete (step1) so counts can't dangle behind a
// reclaimed file.
func (r *ViewRepo) DeleteByFile(tx *gorm.DB, fileNanoID string) error {
	return tx.Where("file_nano_id = ?", fileNanoID).Delete(&model.View{}).Error
}

// ListByFile returns every per-viewer view row for fileNanoID (owner-only
// consumption — callers are expected to have already verified ownership of
// the file before calling this, e.g. the /files/:id/views endpoint).
func (r *ViewRepo) ListByFile(ctx context.Context, fileNanoID string) ([]model.View, error) {
	db := r.db.WithContext(ctx)
	var rows []model.View
	err := db.Where("file_nano_id = ?", fileNanoID).Find(&rows).Error
	return rows, err
}
