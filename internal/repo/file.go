// Package repo holds the GORM data-access layer for Placard.
//
// Ownership/authz keys are always the immutable authz id (userctx.AuthzID()).
// Owner-only queries enforce create_user = authzid so a non-owner gets a clean
// miss (handlers map that to 404, never 403, avoiding existence confirmation).
//
// AuditRepo is deliberately append-only: it exposes ONLY Insert and Query. No
// UPDATE/DELETE method or SQL against audit_log exists anywhere in this package.
//
// Telemetry-only writes (last_used_at touch, view_count recompute) leave
// update_time alone: they record activity, not a change to the row.
//
// One ctx carrier: context.Context travels either as an explicit first
// parameter or inside a *gorm.DB handle — never both on the same call. A
// function that already receives tx *gorm.DB receives its ctx through it.
// Concretely, non-transactional methods here take ctx and open with
// db := r.db.WithContext(ctx); methods taking tx *gorm.DB take no ctx, because
// the caller already applied WithContext at the transaction entrypoint. Do not
// "fix" the apparently missing ctx on a tx method — a second carrier would give
// two sources with undefined precedence and no benefit.
package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Xm798/placard/internal/model"
)

// FileRepo is the data-access surface for the file table.
type FileRepo struct {
	db *gorm.DB
}

// NewFileRepo builds a FileRepo bound to db.
func NewFileRepo(db *gorm.DB) *FileRepo {
	return &FileRepo{db: db}
}

// Insert persists a new file row within tx. create_time/update_time come from
// GORM's autoCreateTime/autoUpdateTime, which also write the values back onto f
// — publish echoes create_time straight back to the caller.
func (r *FileRepo) Insert(tx *gorm.DB, f *model.File) error {
	return tx.Create(f).Error
}

// GetActiveByNanoID returns a live file: not deleted and not expired
// (expires_at >= NOW()). Used by the public meta path before signing
// render_url. Returns gorm.ErrRecordNotFound on miss.
func (r *FileRepo) GetActiveByNanoID(ctx context.Context, nanoID string) (*model.File, error) {
	db := r.db.WithContext(ctx)
	var f model.File
	err := db.
		Where("nano_id = ? AND is_deleted = 0 AND expires_at >= ?", nanoID, model.Now()).
		First(&f).Error
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// GetOwned returns a file owned by authzid (owner-only). It enforces
// create_user = authzid and is_deleted = 0, so a non-owner id yields a miss.
// Returns gorm.ErrRecordNotFound on miss.
func (r *FileRepo) GetOwned(ctx context.Context, nanoID, authzid string) (*model.File, error) {
	db := r.db.WithContext(ctx)
	var f model.File
	err := db.
		Where("nano_id = ? AND create_user = ? AND is_deleted = 0", nanoID, authzid).
		First(&f).Error
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// GetOwnedForUpdate is GetOwned's tx variant, taking a SELECT ... FOR UPDATE
// row lock. Callers that must resolve a version pointer (e.g. "follow latest")
// and write it back atomically use this instead of GetOwned to close the
// TOCTOU window between reading latest_version and writing shared_version —
// the lock is acquired and released entirely within one DB-only tx, so it
// never spans an external call. Returns gorm.ErrRecordNotFound on miss.
//
// SQLite has no FOR UPDATE and ignores the clause; there the same ordering
// comes from the single-connection pool internal/db.Open configures, which
// admits one transaction at a time.
func (r *FileRepo) GetOwnedForUpdate(tx *gorm.DB, nanoID, authzid string) (*model.File, error) {
	var f model.File
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("nano_id = ? AND create_user = ? AND is_deleted = 0", nanoID, authzid).
		First(&f).Error
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// DB exposes the underlying handle for transactional callers.
func (r *FileRepo) DB() *gorm.DB {
	return r.db
}

// ListExpired returns expired, not-yet-soft-deleted files (paged, id-ordered).
// The 9999 sentinel is always >= NOW so "never" files never match; the bound
// value is normalized so the comparison cannot be dragged across a TZ boundary
// and mistake a sentinel for an expired row (R6).
func (r *FileRepo) ListExpired(ctx context.Context, now time.Time, limit int) ([]model.File, error) {
	db := r.db.WithContext(ctx)
	var fs []model.File
	err := db.Where("expires_at < ? AND is_deleted = 0", model.Timestamp(now)).
		Order("id").Limit(limit).Find(&fs).Error
	return fs, err
}

// MarkDeleted soft-deletes one file inside tx. Idempotent: an already-deleted
// row matches nothing (RowsAffected=0) and returns no error.
func (r *FileRepo) MarkDeleted(tx *gorm.DB, nanoID, actor string) error {
	return tx.Model(&model.File{}).
		Where("nano_id = ? AND is_deleted = 0", nanoID).
		Updates(map[string]interface{}{"is_deleted": 1, "update_user": actor}).Error
}

// AdvanceHead CAS-advances latest_version from next-1 to next inside tx, and —
// only when the share follows Latest (shared_version = 0) — refreshes the
// serving-cache columns (object_key/size_bytes/title/description) to the new
// head row in the SAME statement. Returns RowsAffected: 0 means the head moved
// since the caller's unlocked read (lost race — re-read and retry). A publish
// is a real mutation, so update_time moves with it.
//
// CASE rather than a dialect-specific conditional: the same statement has to
// compile on SQLite and Postgres.
func (r *FileRepo) AdvanceHead(tx *gorm.DB, nanoID string, next int, serving *model.FileVersion, actor string) (int64, error) {
	res := tx.Exec(`UPDATE file
		SET latest_version = ?,
		    object_key  = CASE WHEN shared_version = 0 THEN ? ELSE object_key END,
		    size_bytes  = CASE WHEN shared_version = 0 THEN ? ELSE size_bytes END,
		    title       = CASE WHEN shared_version = 0 THEN ? ELSE title END,
		    description = CASE WHEN shared_version = 0 THEN ? ELSE description END,
		    update_user = ?,
		    update_time = ?
		WHERE nano_id = ? AND is_deleted = 0 AND latest_version = ?`,
		next, serving.ObjectKey, serving.SizeBytes, serving.Title, serving.Description,
		actor, model.Now(), nanoID, next-1)
	return res.RowsAffected, res.Error
}

// SetSharedVersion pins (sharedVersion>0) or unpins (0 = follow latest) the
// served version inside tx, refreshing the serving cache to `serving` — the
// resolved version row (the pinned row, or the latest row when unpinning) —
// in the same statement. The caller resolves `serving`; this method never
// decides which row wins.
func (r *FileRepo) SetSharedVersion(tx *gorm.DB, nanoID string, sharedVersion int, serving *model.FileVersion, actor string) error {
	return tx.Model(&model.File{}).
		Where("nano_id = ? AND is_deleted = 0", nanoID).
		Updates(map[string]interface{}{
			"shared_version": sharedVersion,
			"object_key":     serving.ObjectKey,
			"size_bytes":     serving.SizeBytes,
			"title":          serving.Title,
			"description":    serving.Description,
			"update_user":    actor,
		}).Error
}

// SetVisibility updates a file's visibility inside tx. Callers must already
// hold ownership (typically via GetOwnedForUpdate in the same tx) — this
// method itself enforces no owner scope beyond is_deleted = 0.
func (r *FileRepo) SetVisibility(tx *gorm.DB, nanoID, visibility, actor string) error {
	return tx.Model(&model.File{}).
		Where("nano_id = ? AND is_deleted = 0", nanoID).
		Updates(map[string]interface{}{
			"visibility":  visibility,
			"update_user": actor,
		}).Error
}

// SetShareCode stores enc as the file's share code (empty clears it) inside tx
// and increments share_code_version in the same statement — a generate and a
// clear both bump it, which is what expires every unlock ticket minted under
// the previous code. Callers must already hold ownership; this method enforces
// no owner scope beyond is_deleted = 0.
func (r *FileRepo) SetShareCode(tx *gorm.DB, nanoID, enc, actor string) error {
	return tx.Model(&model.File{}).
		Where("nano_id = ? AND is_deleted = 0", nanoID).
		Updates(map[string]interface{}{
			"share_code_enc":     enc,
			"share_code_version": gorm.Expr("share_code_version + 1"),
			"update_user":        actor,
		}).Error
}

// RecomputeViewCounts (step3) rebuilds the denormalized file.view_count cache
// from the authoritative view table. A correlated subquery rather than an
// UPDATE ... JOIN: joining in an UPDATE is spelled differently in every
// dialect, and this form compiles unchanged on SQLite and Postgres. Returns
// rows affected.
//
// update_time is deliberately absent from the SET list — this is a telemetry
// write (see package doc), and the row itself did not change.
func (r *FileRepo) RecomputeViewCounts(ctx context.Context) (int64, error) {
	db := r.db.WithContext(ctx)
	res := db.Exec(`UPDATE file
		SET view_count = COALESCE(
			(SELECT SUM(v.view_count) FROM view v WHERE v.file_nano_id = file.nano_id), 0)
		WHERE is_deleted = 0`)
	return res.RowsAffected, res.Error
}

// ownedScope narrows a query to authzid's non-deleted files. Centralizing the
// owner-only predicate keeps ListOwned/CountOwned in lockstep — a divergence
// here would be a silent authorization bug.
func ownedScope(db *gorm.DB, authzid string) *gorm.DB {
	return db.Where("create_user = ? AND is_deleted = 0", authzid)
}

// ListOwned returns authzid's non-deleted files, newest first (create_time
// DESC, id DESC), offset-paginated. Owner-only: enforces create_user = authzid
// so it can never surface another user's files.
func (r *FileRepo) ListOwned(ctx context.Context, authzid string, offset, limit int) ([]model.File, error) {
	db := r.db.WithContext(ctx)
	var fs []model.File
	err := ownedScope(db, authzid).
		Order("create_time DESC, id DESC").
		Offset(offset).Limit(limit).
		Find(&fs).Error
	return fs, err
}

// CountOwned returns the total count of authzid's non-deleted files (for pager).
func (r *FileRepo) CountOwned(ctx context.Context, authzid string) (int64, error) {
	db := r.db.WithContext(ctx)
	var n int64
	err := ownedScope(db.Model(&model.File{}), authzid).
		Count(&n).Error
	return n, err
}
