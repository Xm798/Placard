package repo

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/model"
)

// AuditRepo is the append-only audit trail access layer.
//
// ⚠ HARD GATE (review-final §B, plan audit append-only): this type exposes ONLY
// Insert and Query. There is NO Update/Delete method and NO UPDATE/DELETE SQL
// against audit_log anywhere. Append-only is enforced at the application layer
// (DB GRANT is an optional defense-in-depth, not a prerequisite).
type AuditRepo struct {
	db *gorm.DB
}

// NewAuditRepo builds an AuditRepo bound to db.
func NewAuditRepo(db *gorm.DB) *AuditRepo {
	return &AuditRepo{db: db}
}

// Insert appends an audit entry. The only write path for audit_log.
func (r *AuditRepo) Insert(ctx context.Context, entry *model.AuditLog) error {
	db := r.db.WithContext(ctx)
	return db.Create(entry).Error
}

// AuditQuery narrows an audit Query. Zero-value fields are ignored.
type AuditQuery struct {
	Action       string
	Outcome      string
	ReasonCode   string
	AuthChannel  string
	Actor        string
	FileNanoID   string
	ResourceType string
	ResourceID   string
	From         time.Time
	To           time.Time
	BeforeID     uint
	Limit        int
}

// Query returns audit entries matching q, newest first. Read-only.
func (r *AuditRepo) Query(ctx context.Context, q AuditQuery) ([]model.AuditLog, error) {
	db := r.db.WithContext(ctx)
	tx := db.Model(&model.AuditLog{})
	if q.Action != "" {
		tx = tx.Where("action = ?", q.Action)
	}
	if q.Outcome != "" {
		tx = tx.Where("outcome = ?", q.Outcome)
	}
	if q.ReasonCode != "" {
		tx = tx.Where("reason_code = ?", q.ReasonCode)
	}
	if q.AuthChannel != "" {
		tx = tx.Where("auth_channel = ?", q.AuthChannel)
	}
	if q.Actor != "" {
		tx = tx.Where("actor = ?", q.Actor)
	}
	if q.FileNanoID != "" {
		tx = tx.Where("file_nano_id = ?", q.FileNanoID)
	}
	if q.ResourceType != "" {
		tx = tx.Where("resource_type = ?", q.ResourceType)
	}
	if q.ResourceID != "" {
		tx = tx.Where("resource_id = ?", q.ResourceID)
	}
	if !q.From.IsZero() {
		tx = tx.Where("create_time >= ?", model.Timestamp(q.From))
	}
	if !q.To.IsZero() {
		tx = tx.Where("create_time < ?", model.Timestamp(q.To))
	}
	if q.BeforeID > 0 {
		tx = tx.Where("id < ?", q.BeforeID)
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var entries []model.AuditLog
	err := tx.Order("create_time DESC, id DESC").Limit(limit).Find(&entries).Error
	if err != nil {
		return nil, err
	}
	return entries, nil
}
