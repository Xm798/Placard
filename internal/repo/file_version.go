package repo

import (
	"context"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/model"
)

// FileVersionRepo is the data-access surface for the immutable file_version
// table. Rows are insert-only: no Update/Delete method exists here — restore
// copies content into a NEW head version, and reclamation queues object keys via
// PendingObjectDeleteRepo instead of touching version rows.
type FileVersionRepo struct {
	db *gorm.DB
}

// NewFileVersionRepo builds a FileVersionRepo bound to db.
func NewFileVersionRepo(db *gorm.DB) *FileVersionRepo {
	return &FileVersionRepo{db: db}
}

// Insert persists a new version row within tx. A (nano_id, version) collision
// surfaces as a uk_nano_version unique-constraint error — the publish CAS loop
// treats it as a lost race and retries on a fresh head read.
func (r *FileVersionRepo) Insert(tx *gorm.DB, v *model.FileVersion) error {
	return tx.Create(v).Error
}

// ListByNanoID returns every version of nanoID, newest first (version DESC).
func (r *FileVersionRepo) ListByNanoID(ctx context.Context, nanoID string) ([]model.FileVersion, error) {
	db := r.db.WithContext(ctx)
	var vs []model.FileVersion
	err := db.Where("nano_id = ?", nanoID).Order("version DESC").Find(&vs).Error
	return vs, err
}

// ReclaimKeys returns the object keys of every version of nanoID, for callers
// that enqueue them all for object reclamation (user_delete / expired). Pre-
// versioning rows never got a backfilled version row, so an empty result
// falls back to fallbackKey (the file's serving-cache key) — the object is
// still reclaimed rather than silently skipped.
func (r *FileVersionRepo) ReclaimKeys(ctx context.Context, nanoID, fallbackKey string) ([]string, error) {
	versions, err := r.ListByNanoID(ctx, nanoID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(versions)+1)
	for _, v := range versions {
		keys = append(keys, v.ObjectKey)
	}
	if len(keys) == 0 {
		keys = append(keys, fallbackKey)
	}
	return keys, nil
}

// GetByVersion returns one version row. Returns gorm.ErrRecordNotFound on miss.
func (r *FileVersionRepo) GetByVersion(ctx context.Context, nanoID string, version int) (*model.FileVersion, error) {
	db := r.db.WithContext(ctx)
	var v model.FileVersion
	err := db.Where("nano_id = ? AND version = ?", nanoID, version).First(&v).Error
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetByVersionTx is GetByVersion's tx variant, for callers that must resolve a
// version row inside the same transaction that locks/updates the file row
// (e.g. PatchFile pin/unpin, via FileRepo.GetOwnedForUpdate). file_version
// rows are insert-only and never mutate, so no row lock is needed here — the
// caller's lock on the file row is what closes the race. Returns
// gorm.ErrRecordNotFound on miss.
func (r *FileVersionRepo) GetByVersionTx(tx *gorm.DB, nanoID string, version int) (*model.FileVersion, error) {
	var v model.FileVersion
	err := tx.Where("nano_id = ? AND version = ?", nanoID, version).First(&v).Error
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetLatest returns the highest version row of nanoID. Returns
// gorm.ErrRecordNotFound when no version row exists.
func (r *FileVersionRepo) GetLatest(ctx context.Context, nanoID string) (*model.FileVersion, error) {
	db := r.db.WithContext(ctx)
	var v model.FileVersion
	err := db.Where("nano_id = ?", nanoID).Order("version DESC").First(&v).Error
	if err != nil {
		return nil, err
	}
	return &v, nil
}
