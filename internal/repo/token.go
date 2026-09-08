package repo

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/model"
)

// TokenRepo is the data-access surface for the token table.
type TokenRepo struct {
	db *gorm.DB
}

// NewTokenRepo builds a TokenRepo bound to db.
func NewTokenRepo(db *gorm.DB) *TokenRepo {
	return &TokenRepo{db: db}
}

// Insert persists a new token row.
func (r *TokenRepo) Insert(ctx context.Context, t *model.Token) error {
	db := r.db.WithContext(ctx)
	return db.Create(t).Error
}

// GetValidByHash looks up a token by its HMAC hash and validates it
// fail-closed: it must be not deleted, not revoked AND not expired. Expiry
// judgement is fail-closed — a zero/out-of-range/<=1970 expires_at is treated
// as invalid, never as "never expires". The 9999 sentinel is the only
// legitimate never-expires marker and is preserved (never truncated).
//
// Returns (token, true) only when valid; (nil, false) otherwise.
func (r *TokenRepo) GetValidByHash(ctx context.Context, hash string) (*model.Token, bool) {
	db := r.db.WithContext(ctx)
	var t model.Token
	err := db.Where("token_hash = ? AND is_deleted = 0", hash).First(&t).Error
	if err != nil {
		return nil, false
	}
	if t.Revoked != 0 {
		return nil, false
	}
	if !tokenUnexpired(t.ExpiresAt) {
		return nil, false
	}
	return &t, true
}

// tokenUnexpired returns true only when exp is a valid future instant. The 9999
// sentinel means never-expires (valid). Any <=1970 / zero value is ambiguous
// and treated fail-closed as expired.
func tokenUnexpired(exp time.Time) bool {
	if exp.Year() == 9999 {
		return true
	}
	if exp.Year() <= 1970 {
		return false
	}
	return exp.After(time.Now())
}

// TouchLastUsed stamps last_used_at on a token row without moving update_time —
// last_used_at is telemetry, not a mutation of the token itself. UpdateColumn
// is what keeps update_time still: it bypasses GORM hooks and autoUpdateTime
// tracking.
func (r *TokenRepo) TouchLastUsed(ctx context.Context, id uint, at time.Time) error {
	db := r.db.WithContext(ctx)
	return db.
		Model(&model.Token{}).
		Where("id = ?", id).
		UpdateColumn("last_used_at", model.Timestamp(at)).Error
}

// ListByUser returns all non-deleted tokens owned by authzid,
// newest first.
func (r *TokenRepo) ListByUser(ctx context.Context, authzid string) ([]model.Token, error) {
	db := r.db.WithContext(ctx)
	var tokens []model.Token
	err := db.
		Where("user_id = ? AND is_deleted = 0", authzid).
		Order("create_time DESC").
		Find(&tokens).Error
	if err != nil {
		return nil, err
	}
	return tokens, nil
}

// Revoke sets revoked=1 on a non-deleted token owned by authzid (owner-only).
// Returns gorm.ErrRecordNotFound when no owned row matched.
func (r *TokenRepo) Revoke(ctx context.Context, id uint, authzid string) error {
	db := r.db.WithContext(ctx)
	res := db.Model(&model.Token{}).
		Where("id = ? AND user_id = ? AND is_deleted = 0", id, authzid).
		Updates(map[string]interface{}{
			"revoked":     1,
			"update_user": authzid,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeleteRevoked soft-deletes a revoked token owned by authzid, mirroring the
// file table's is_deleted convention. Active tokens must be revoked first.
// Returns gorm.ErrRecordNotFound when no matching, not-yet-deleted revoked row
// exists, preserving owner-scope miss semantics.
func (r *TokenRepo) DeleteRevoked(ctx context.Context, id uint, authzid string) error {
	db := r.db.WithContext(ctx)
	res := db.Model(&model.Token{}).
		Where("id = ? AND user_id = ? AND revoked = ? AND is_deleted = 0", id, authzid, 1).
		Updates(map[string]interface{}{
			"is_deleted":  1,
			"update_user": authzid,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// GetOwned returns a non-deleted token owned by authzid, for revoke validation
// if needed. A deleted token is a miss (mirrors File.GetOwned semantics).
func (r *TokenRepo) GetOwned(ctx context.Context, id uint, authzid string) (*model.Token, error) {
	db := r.db.WithContext(ctx)
	var t model.Token
	err := db.Where("id = ? AND user_id = ? AND is_deleted = 0", id, authzid).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, err
	}
	return &t, nil
}

// DeleteByUser removes every token belonging to a user inside the caller's
// transaction. Hard delete, unlike DeleteRevoked's is_deleted marker: the
// account these tokens authenticate into is going away in the same
// transaction, and a soft-deleted row would leave a credential hash on a
// user_id nothing answers for.
func (r *TokenRepo) DeleteByUser(tx *gorm.DB, userID string) error {
	return tx.Where("user_id = ?", userID).Delete(&model.Token{}).Error
}
