package session

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/model"
)

// SQLStore keeps sessions in the database, so a single-binary deployment needs
// no Redis. The two-tier expiry Redis gets from EXPIRE is explicit here: the
// idle deadline is the expires_at column, pushed out on read, and the absolute
// cap is measured against created_at.
//
// Expired rows are dropped on the read that finds them, and swept in bulk by
// the cleanup cron (PurgeExpired) for sessions nobody comes back for. Every
// read filters on expires_at, so an unswept row is never served.
//
// Sessions are per-process only in the sense that the database is shared:
// several replicas pointed at one Postgres see the same sessions.
type SQLStore struct {
	db          *gorm.DB
	idleTTL     time.Duration
	absoluteTTL time.Duration
	now         func() time.Time
}

func NewSQLStore(db *gorm.DB, idleTTL, absoluteTTL time.Duration) *SQLStore {
	return NewSQLStoreWithClock(db, idleTTL, absoluteTTL, time.Now)
}

// NewSQLStoreWithClock is NewSQLStore with an injected clock. It exists for
// tests, which need to reach an idle or absolute deadline without sleeping for
// a week (the Redis store's equivalent is miniredis.FastForward).
func NewSQLStoreWithClock(db *gorm.DB, idleTTL, absoluteTTL time.Duration, now func() time.Time) *SQLStore {
	return &SQLStore{db: db, idleTTL: idleTTL, absoluteTTL: absoluteTTL, now: now}
}

func (s *SQLStore) Create(ctx context.Context, d Data) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	now := s.now()
	row := model.Session{
		ID:          id,
		AuthzID:     d.AuthzID,
		DisplayName: d.DisplayName,
		AvatarURL:   d.AvatarURL,
		OIDCFlow:    marshalFlow(d.OIDC),
		CreatedAt:   model.Timestamp(d.CreatedAt),
		ExpiresAt:   model.Timestamp(now.Add(s.idleTTL)),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return "", err
	}
	return id, nil
}

func (s *SQLStore) Get(ctx context.Context, id string) (Data, error) {
	var row model.Session
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Data{}, ErrNotFound
	}
	if err != nil {
		return Data{}, err // infrastructure error — caller maps to 503, never 401
	}

	now := s.now()
	if !now.Before(row.ExpiresAt) || now.Sub(row.CreatedAt) > s.absoluteTTL {
		_ = s.Delete(ctx, id)
		return Data{}, ErrNotFound
	}
	// Threshold renewal, as in the Redis store: write only once the remaining
	// idle window has fallen below half, so an active session costs one UPDATE
	// per idle/2 rather than one per request.
	if row.ExpiresAt.Sub(now) < s.idleTTL/2 {
		_ = s.db.WithContext(ctx).Model(&model.Session{}).Where("id = ?", id).
			Update("expires_at", model.Timestamp(now.Add(s.idleTTL))).Error
	}
	return Data{
		AuthzID:     row.AuthzID,
		DisplayName: row.DisplayName,
		AvatarURL:   row.AvatarURL,
		OIDC:        unmarshalFlow(row.OIDCFlow),
		CreatedAt:   row.CreatedAt,
	}, nil
}

// Update rewrites the row's data columns and deliberately leaves expires_at
// alone: touching it here would restart the idle window on every profile
// refresh. The expires_at predicate is what keeps an already-expired session
// from being resurrected by an in-flight update — the SQL counterpart of the
// Redis store's SET XX.
func (s *SQLStore) Update(ctx context.Context, id string, d Data) error {
	res := s.db.WithContext(ctx).Model(&model.Session{}).
		Where("id = ? AND expires_at > ?", id, model.Timestamp(s.now())).
		Updates(map[string]interface{}{
			"authz_id":     d.AuthzID,
			"display_name": d.DisplayName,
			"avatar_url":   d.AvatarURL,
			"oidc_flow":    marshalFlow(d.OIDC),
			"created_at":   model.Timestamp(d.CreatedAt),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Delete(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Session{}).Error
}

// PurgeExpired deletes sessions past either deadline and returns how many rows
// went. The cleanup cron calls it; reads do not depend on it having run.
func (s *SQLStore) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	res := s.db.WithContext(ctx).
		Where("expires_at <= ? OR created_at <= ?",
			model.Timestamp(now), model.Timestamp(now.Add(-s.absoluteTTL))).
		Delete(&model.Session{})
	return res.RowsAffected, res.Error
}

// marshalFlow renders the pending authorization request for the oidc_flow
// column. A flow that cannot be marshalled is stored as "no flow": the
// callback then rejects the login rather than proceeding on state it cannot
// verify, which is the safe direction for an error that cannot happen with
// this struct anyway.
func marshalFlow(f *OIDCFlow) string {
	if f == nil {
		return ""
	}
	b, err := json.Marshal(f)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalFlow reads the column back, treating unreadable content as no flow
// in flight — same direction as marshalFlow.
func unmarshalFlow(raw string) *OIDCFlow {
	if raw == "" {
		return nil
	}
	var f OIDCFlow
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return nil
	}
	return &f
}
