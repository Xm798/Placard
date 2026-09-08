package repo

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Xm798/placard/internal/model"
)

// PendingObjectDeleteRepo queues storage objects for idempotent cron reclamation.
// Two enqueue disciplines coexist — do not mix them up:
//
//   - Rollback orphans (publish_rollback / nanoid_conflict / version_conflict /
//     put_replace): the object was uploaded but its referencing row never
//     committed. Enqueued best-effort via Insert OUTSIDE the failing tx —
//     inside it, the enqueue would roll back together with the failure it is
//     recording.
//
//   - Deletion reclamation (user_delete / expired): the referencing rows DO
//     exist. Enqueued via InsertBatch INSIDE the soft-delete tx, so the queue
//     rows and the soft-delete commit atomically and no crash window can leak
//     an object.
type PendingObjectDeleteRepo struct {
	db *gorm.DB
}

// NewPendingObjectDeleteRepo builds a PendingObjectDeleteRepo bound to db.
func NewPendingObjectDeleteRepo(db *gorm.DB) *PendingObjectDeleteRepo {
	return &PendingObjectDeleteRepo{db: db}
}

// Insert enqueues an object key for deferred deletion with the given reason
// (publish_rollback | nanoid_conflict | put_replace | user_delete). On
// uk_object_key conflict it is a no-op (idempotent), so repeated rollback attempts
// never error.
func (r *PendingObjectDeleteRepo) Insert(ctx context.Context, objectKey, reason, authzid string) error {
	db := r.db.WithContext(ctx)
	row := model.PendingObjectDelete{
		ObjectKey:  objectKey,
		Reason:     reason,
		CreateUser: authzid,
		UpdateUser: authzid,
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "object_key"}},
		DoNothing: true,
	}).Create(&row).Error
}

// InsertBatch enqueues many object keys in ONE multi-row INSERT round-trip inside
// tx (deletion paths enqueue atomically with the soft-delete). Idempotent per
// key via uk_object_key (ON DUPLICATE KEY no-op); version keys of one page are
// pairwise distinct, so batch inserts never self-conflict. Empty keys is a no-op.
func (r *PendingObjectDeleteRepo) InsertBatch(tx *gorm.DB, keys []string, reason, authzid string) error {
	if len(keys) == 0 {
		return nil
	}
	rows := make([]model.PendingObjectDelete, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, model.PendingObjectDelete{
			ObjectKey:  k,
			Reason:     reason,
			CreateUser: authzid,
			UpdateUser: authzid,
		})
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "object_key"}},
		DoNothing: true,
	}).Create(&rows).Error
}

// ListPending returns queued orphans below the retry ceiling (paged,
// id-ordered). Rows at maxRetry are intentionally excluded — they're poison and
// must be surfaced for human triage, not retried forever (R9). user_delete
// rows younger than userDeleteBefore are also excluded (retained for the
// configured retention window).
func (r *PendingObjectDeleteRepo) ListPending(ctx context.Context, maxRetry int, userDeleteBefore time.Time, limit int) ([]model.PendingObjectDelete, error) {
	db := r.db.WithContext(ctx)
	var rows []model.PendingObjectDelete
	err := db.Where("retry_count < ?", maxRetry).
		Where("reason <> ? OR create_time <= ?", model.ReasonUserDelete, model.Timestamp(userDeleteBefore)).
		Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// Delete removes a reconciled queue row (storage object confirmed gone).
func (r *PendingObjectDeleteRepo) Delete(ctx context.Context, id uint) error {
	db := r.db.WithContext(ctx)
	return db.Delete(&model.PendingObjectDelete{}, id).Error
}

// IncrRetry bumps retry_count after a failed reclaim attempt.
func (r *PendingObjectDeleteRepo) IncrRetry(ctx context.Context, id uint) error {
	db := r.db.WithContext(ctx)
	return db.Model(&model.PendingObjectDelete{}).Where("id = ?", id).
		Update("retry_count", gorm.Expr("retry_count + 1")).Error
}

// CountAtRetryMax returns how many rows have exhausted their retries
// (retry_count >= maxRetry). ListPending no longer surfaces these, so step2
// uses this count to alert for human triage (R9).
func (r *PendingObjectDeleteRepo) CountAtRetryMax(ctx context.Context, maxRetry int) (int64, error) {
	db := r.db.WithContext(ctx)
	var n int64
	err := db.Model(&model.PendingObjectDelete{}).Where("retry_count >= ?", maxRetry).Count(&n).Error
	return n, err
}
