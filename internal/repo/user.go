package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/model"
)

// UserRepo is the data-access surface for the user table — accounts, their
// login credentials' owner row, and the display profile every snapshot column
// elsewhere is copied from.
//
// Reads here resolve an account; they are never an authorization decision.
// Ownership is still decided by comparing model.User.ID against the stored
// create_user / user_id / viewer / actor value, with no join (see
// internal/authz).
type UserRepo struct {
	db *gorm.DB
}

// NewUserRepo builds a UserRepo bound to db.
func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}

// Get returns one user row by id. Returns gorm.ErrRecordNotFound on miss.
func (r *UserRepo) Get(ctx context.Context, id string) (*model.User, error) {
	var u model.User
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByUsername returns the account with this username (case-insensitive, the
// same comparison Create's uniqueness check uses). Returns
// gorm.ErrRecordNotFound on miss.
func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	var u model.User
	if err := r.db.WithContext(ctx).
		Where("username = ?", NormalizeUsername(username)).
		First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByIdentifier resolves a login identifier, which may be either a username
// or an email address. Returns gorm.ErrRecordNotFound on miss.
//
// One query over both columns rather than "try username, then email": a user
// whose username is someone else's email address would otherwise decide which
// row a login attempt resolves to by the order the lookups happen to run in.
// Both columns are unique and normalized the same way on write, so at most one
// row can match either.
func (r *UserRepo) GetByIdentifier(ctx context.Context, identifier string) (*model.User, error) {
	id := strings.ToLower(strings.TrimSpace(identifier))
	if id == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var u model.User
	if err := r.db.WithContext(ctx).
		Where("username = ? OR email = ?", id, id).
		First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByEmail returns the account holding this address, normalized the way
// every write normalizes it. Returns gorm.ErrRecordNotFound on miss, and for a
// blank address — "no email" is not a lookup key, and matching it against the
// NULLs in the column would resolve an arbitrary account.
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	addr := NormalizeEmail(&email)
	if addr == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var u model.User
	if err := r.db.WithContext(ctx).Where("email = ?", *addr).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// Create inserts a user row. Username and Email are normalized here so every
// write goes through the same casing the unique indexes and GetByIdentifier
// compare on. A duplicate username or email surfaces as the driver's unique
// violation, which the caller maps to a conflict.
func (r *UserRepo) Create(ctx context.Context, u *model.User) error {
	u.Username = NormalizeUsername(u.Username)
	u.Email = NormalizeEmail(u.Email)
	return r.db.WithContext(ctx).Create(u).Error
}

// Active reports whether id names an account that may authenticate right now.
// A missing row and a disabled one are both false with a nil error: the auth
// middleware turns either into a 401, and only a real infrastructure failure
// (non-nil error) into a 503.
func (r *UserRepo) Active(ctx context.Context, id string) (bool, error) {
	var u model.User
	err := r.db.WithContext(ctx).
		Select("disabled").
		Where("id = ?", id).
		First(&u).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return !u.Disabled, nil
}

// SetDefaultVisibility updates id's default_visibility preference.
// Update-only: a missing user row is never created here. Returns a positive
// count when the row exists (whether or not the UPDATE changed anything) and 0
// only when the row is genuinely missing, so the handler can 503 without a
// false positive on a same-value save.
//
// SQLite and Postgres both report rows MATCHED, so a same-value save already
// returns 1. The existence check on the RowsAffected == 0 path is what keeps
// that from being load-bearing: an engine reporting rows CHANGED instead would
// return 0 for a save that changed nothing, and the handler would 503 on a
// row that exists. Never create a row here.
func (r *UserRepo) SetDefaultVisibility(ctx context.Context, id, v string) (int64, error) {
	db := r.db.WithContext(ctx)
	res := db.Model(&model.User{}).
		Where("id = ?", id).
		Update("default_visibility", v)
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected > 0 {
		return res.RowsAffected, nil
	}

	var exists int64
	if err := db.Model(&model.User{}).
		Where("id = ?", id).
		Count(&exists).Error; err != nil {
		return 0, err
	}
	if exists > 0 {
		return exists, nil // row present; this write was an idempotent no-op
	}
	return 0, nil
}

// SetAvatar updates the object-store avatar cache pointer (avatar_source_url
// change marker + avatar_key). Update-only, same discipline as
// SetDefaultVisibility.
func (r *UserRepo) SetAvatar(ctx context.Context, id, sourceURL, avatarKey string) error {
	return r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"avatar_source_url": sourceURL,
			"avatar_key":        avatarKey,
		}).Error
}

// StampLogin records a successful login: last_login_at always, first_login_at
// only while it still holds the Never sentinel. Both are stamped in one
// statement so a concurrent second login cannot land between a read and a
// write and move first_login_at forward.
func (r *UserRepo) StampLogin(ctx context.Context, id string, at time.Time) error {
	ts := model.Timestamp(at)
	return r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"last_login_at": ts,
			"first_login_at": gorm.Expr(
				"CASE WHEN first_login_at <= ? THEN ? ELSE first_login_at END",
				model.Timestamp(model.Never), ts),
		}).Error
}

// TouchLastActive stamps last_active_at on an existing row (5min-throttled by
// the caller) without moving update_time — last_active_at is telemetry, not a
// mutation of the profile. Update-only: a missing row is a silent no-op, this
// must never become a row-creation point. UpdateColumn is what keeps
// update_time still: it bypasses GORM hooks and autoUpdateTime tracking — same
// pattern as TokenRepo.TouchLastUsed.
func (r *UserRepo) TouchLastActive(ctx context.Context, id string, at time.Time) error {
	return r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("id = ?", id).
		UpdateColumn("last_active_at", model.Timestamp(at)).Error
}

// NormalizeUsername is the single spelling a username is stored and compared
// under. Usernames are matched case-insensitively so "Alice" and "alice" are
// one account rather than two that look identical in every list.
func NormalizeUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// NormalizeEmail lowercases and trims an address, and maps a blank one to nil
// so it lands in the column as NULL — the unique index admits any number of
// NULLs but only one of each address.
func NormalizeEmail(e *string) *string {
	if e == nil {
		return nil
	}
	v := strings.ToLower(strings.TrimSpace(*e))
	if v == "" {
		return nil
	}
	return &v
}

// Any reports whether the instance has at least one account. It stops at the
// first row rather than counting them: the answer gates registration on an
// endpoint anyone can call, and a full COUNT(*) there is a sequential scan an
// anonymous caller could ask for in a loop.
func (r *UserRepo) Any(ctx context.Context) (bool, error) {
	var one int
	err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Select("1").
		Limit(1).
		Scan(&one).Error
	if err != nil {
		return false, err
	}
	return one == 1, nil
}

// List returns one page of accounts for the admin user table, newest first.
func (r *UserRepo) List(ctx context.Context, offset, limit int) ([]model.User, error) {
	var us []model.User
	err := r.db.WithContext(ctx).
		Order("create_time DESC, id DESC").
		Offset(offset).Limit(limit).
		Find(&us).Error
	return us, err
}

// Count returns how many accounts exist, for the admin table's pager. Unlike
// Any it really does count every row: an admin page has already established
// there is an admin, so this is not a surface an anonymous caller can drive.
func (r *UserRepo) Count(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.User{}).Count(&n).Error
	return n, err
}

// SetFlags updates the two account flags an admin can move, in one statement so
// a request changing both never lands half-applied. A nil argument leaves its
// column alone; both nil is a no-op that still reports whether the row exists.
//
// A disabled account is refused at login and on every credential that already
// exists, because the auth middleware re-reads that column on each request (see
// UserRepo.Active).
func (r *UserRepo) SetFlags(ctx context.Context, id string, isAdmin, disabled *bool) (int64, error) {
	fields := map[string]interface{}{}
	if isAdmin != nil {
		fields["is_admin"] = *isAdmin
	}
	if disabled != nil {
		fields["disabled"] = *disabled
	}
	return r.update(ctx, id, fields)
}

// SetAdmin grants or withdraws the admin flag.
func (r *UserRepo) SetAdmin(ctx context.Context, id string, isAdmin bool) (int64, error) {
	return r.update(ctx, id, map[string]interface{}{"is_admin": isAdmin})
}

// SetPasswordHash replaces the account's local password. The caller passes an
// already-encoded argon2id PHC string — this layer never sees a plaintext.
func (r *UserRepo) SetPasswordHash(ctx context.Context, id, hash string) (int64, error) {
	return r.update(ctx, id, map[string]interface{}{"password_hash": hash})
}

// DeleteTx removes the account row inside the caller's transaction. Every
// credential and owned resource must go in the same transaction — an orphaned
// token or identity would authenticate into an id no row answers for.
func (r *UserRepo) DeleteTx(tx *gorm.DB, id string) error {
	return tx.Where("id = ?", id).Delete(&model.User{}).Error
}

// update applies fields to one user row and reports whether the row exists,
// under the same discipline as SetDefaultVisibility: update-only, and a
// RowsAffected of 0 is confirmed against an existence check before it is
// reported as a missing row.
func (r *UserRepo) update(ctx context.Context, id string, fields map[string]interface{}) (int64, error) {
	db := r.db.WithContext(ctx)
	if len(fields) > 0 {
		res := db.Model(&model.User{}).Where("id = ?", id).Updates(fields)
		if res.Error != nil {
			return 0, res.Error
		}
		if res.RowsAffected > 0 {
			return res.RowsAffected, nil
		}
	}

	var exists int64
	if err := db.Model(&model.User{}).Where("id = ?", id).Count(&exists).Error; err != nil {
		return 0, err
	}
	return exists, nil
}
