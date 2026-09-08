package repo

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Xm798/placard/internal/model"
)

// UserIdentityRepo is the data-access surface for user_identity — the
// credentials a user can log in with. A local account carries a row with
// provider "local" and its own user id as the subject, so "which credentials
// does this account have" is one query rather than a special case for
// passwords plus a query for everything else.
type UserIdentityRepo struct {
	db *gorm.DB
}

// NewUserIdentityRepo builds a UserIdentityRepo bound to db.
func NewUserIdentityRepo(db *gorm.DB) *UserIdentityRepo {
	return &UserIdentityRepo{db: db}
}

// Create inserts an identity. A second row for the same (provider, subject)
// surfaces as the driver's unique violation — that constraint is what stops an
// upstream identity from being claimed by a second account.
func (r *UserIdentityRepo) Create(ctx context.Context, id *model.UserIdentity) error {
	return r.db.WithContext(ctx).Create(id).Error
}

// CreateTx is Create inside a caller-supplied transaction, for the
// user+identity pair that registration writes atomically.
func (r *UserIdentityRepo) CreateTx(tx *gorm.DB, id *model.UserIdentity) error {
	return tx.Create(id).Error
}

// GetByProviderSubject resolves one upstream identity to its row. Returns
// gorm.ErrRecordNotFound on miss.
func (r *UserIdentityRepo) GetByProviderSubject(ctx context.Context, provider, subject string) (*model.UserIdentity, error) {
	var id model.UserIdentity
	if err := r.db.WithContext(ctx).
		Where("provider = ? AND subject = ?", provider, subject).
		First(&id).Error; err != nil {
		return nil, err
	}
	return &id, nil
}

// ErrLastLoginMethod means the delete would have left the account with no way
// to sign in, so nothing was removed.
var ErrLastLoginMethod = errors.New("repo: identity is the account's last login method")

// DeleteUnlessLast removes one identity from a user, refusing to remove the
// last credential the account can sign in with. It is scoped by user id as well
// as by provider, so a request can only ever unbind a credential from the
// account that holds it, and reports whether a row was actually removed so the
// caller can tell "unbound" from "there was nothing bound".
//
// The count and the delete are one transaction over rows locked FOR UPDATE,
// because a handler that counted first and deleted afterwards would let two
// concurrent unlinks of two different providers each see two credentials and
// each delete one — leaving zero, which is exactly the state this refuses and
// which nothing in the product can undo. (SQLite's driver ignores the lock
// clause and serializes writers itself; Postgres needs it.)
func (r *UserIdentityRepo) DeleteUnlessLast(ctx context.Context, userID, provider string, hasPassword bool) (bool, error) {
	var removed bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []model.UserIdentity
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).
			Find(&rows).Error; err != nil {
			return err
		}

		methods := 0
		if hasPassword {
			methods++
		}
		found := false
		for _, row := range rows {
			if row.Provider == model.ProviderLocal {
				continue // the password itself, already counted
			}
			methods++
			if row.Provider == provider {
				found = true
			}
		}
		if !found {
			return nil
		}
		if methods <= 1 {
			return ErrLastLoginMethod
		}

		res := tx.Where("user_id = ? AND provider = ?", userID, provider).
			Delete(&model.UserIdentity{})
		if res.Error != nil {
			return res.Error
		}
		removed = res.RowsAffected > 0
		return nil
	})
	return removed, err
}

// ListByUser returns every identity bound to a user, oldest first.
func (r *UserIdentityRepo) ListByUser(ctx context.Context, userID string) ([]model.UserIdentity, error) {
	var ids []model.UserIdentity
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("id ASC").
		Find(&ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// DeleteByUser removes every credential bound to a user inside the caller's
// transaction. DeleteUnlessLast's "keep one login method" rule deliberately
// does not apply: the account itself is being removed alongside these rows.
func (r *UserIdentityRepo) DeleteByUser(tx *gorm.DB, userID string) error {
	return tx.Where("user_id = ?", userID).Delete(&model.UserIdentity{}).Error
}

// EnsureLocal gives userID its "local" credential row if it has none, so a
// password set on an account that had only an OIDC identity is a login method
// rather than a column nothing reads. An existing row is left alone.
func (r *UserIdentityRepo) EnsureLocal(ctx context.Context, userID string) error {
	_, err := r.GetByProviderSubject(ctx, model.ProviderLocal, userID)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return err
	}
	return r.Create(ctx, &model.UserIdentity{
		Provider: model.ProviderLocal,
		Subject:  userID,
		UserID:   userID,
	})
}
