package repo

import (
	"context"
	"errors"
	"strconv"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Xm798/placard/internal/model"
)

// SettingRepo is the data-access surface for the setting table — the runtime
// instance settings an admin edits without restarting the server.
//
// Reads go to the database on every call rather than through a cache: the
// settings are consulted on low-frequency paths (registration, an admin page)
// and a cache would be per-replica, so a change made on one replica would take
// effect on the others only after their TTLs happened to lapse.
type SettingRepo struct {
	db *gorm.DB
}

// NewSettingRepo builds a SettingRepo bound to db.
func NewSettingRepo(db *gorm.DB) *SettingRepo {
	return &SettingRepo{db: db}
}

// Get returns the stored value for key, or ("", false) when the key is unset.
func (r *SettingRepo) Get(ctx context.Context, key string) (string, bool, error) {
	var s model.Setting
	err := r.db.WithContext(ctx).Where("key = ?", key).First(&s).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return s.Value, true, nil
}

// GetBool returns key as a boolean, falling back to def when the key is unset
// or holds something strconv cannot parse. A corrupted value reading as the
// configured default keeps a bad row from turning into a 500 on the
// registration path.
func (r *SettingRepo) GetBool(ctx context.Context, key string, def bool) (bool, error) {
	raw, ok, err := r.Get(ctx, key)
	if err != nil || !ok {
		return def, err
	}
	v, perr := strconv.ParseBool(raw)
	if perr != nil {
		return def, nil
	}
	return v, nil
}

// GetInt64 returns key as an integer, with the same fallback rule as GetBool.
func (r *SettingRepo) GetInt64(ctx context.Context, key string, def int64) (int64, error) {
	raw, ok, err := r.Get(ctx, key)
	if err != nil || !ok {
		return def, err
	}
	v, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil {
		return def, nil
	}
	return v, nil
}

// Set writes key, overwriting any existing value.
func (r *SettingRepo) Set(ctx context.Context, key, value string) error {
	return r.SetTx(r.db.WithContext(ctx), key, value)
}

// SeedDefaults writes the values a first start takes from the config file, and
// leaves any key that already has a row alone. The config file is the seed; the
// table is authoritative from then on, so an admin's later change is not
// reverted by the next restart.
func (r *SettingRepo) SeedDefaults(ctx context.Context, defaults map[string]string) error {
	rows := make([]model.Setting, 0, len(defaults))
	now := model.Now()
	for k, v := range defaults {
		rows = append(rows, model.Setting{Key: k, Value: v, UpdatedAt: now})
	}
	if len(rows) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).
		Create(&rows).Error
}

// ClaimTx inserts key inside the caller's transaction and reports whether this
// transaction is the one that created it. An existing key leaves it untouched
// and returns false.
//
// ON CONFLICT DO NOTHING rather than an insert that fails: a concurrent
// inserter is waited for either way, but a raised unique violation would abort
// the surrounding transaction on Postgres, leaving the caller nothing it could
// still do with the answer.
func (r *SettingRepo) ClaimTx(tx *gorm.DB, key, value string) (bool, error) {
	res := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).
		Create(&model.Setting{Key: key, Value: value, UpdatedAt: model.Now()})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// SetTx is Set inside the caller's transaction.
func (r *SettingRepo) SetTx(tx *gorm.DB, key, value string) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&model.Setting{Key: key, Value: value, UpdatedAt: model.Now()}).Error
}
