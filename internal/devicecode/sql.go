package devicecode

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Xm798/placard/internal/idgen"
	"github.com/Xm798/placard/internal/model"
)

// SQLStore keeps device flows in the database, so a single-binary deployment
// needs no Redis. Every key Redis expires becomes an expires_at column, and
// every read filters on it — an unswept row is never served, and the cleanup
// cron (PurgeExpired) reclaims the space afterwards.
//
// The Redis store spends a sorted set and a Lua prune script to keep
// MaxOutstanding honest, because Redis key expiry fires no callback it could
// hook. Here the same cap is a COUNT over live rows, which is self-healing for
// the same reason: an aged-out flow stops matching the expires_at predicate.
type SQLStore struct {
	db  *gorm.DB
	now func() time.Time
}

func NewSQLStore(db *gorm.DB) *SQLStore {
	return NewSQLStoreWithClock(db, time.Now)
}

// NewSQLStoreWithClock is NewSQLStore with an injected clock. It exists for
// tests, which need to cross the 180s window or the poll gate without sleeping
// (the Redis store's equivalent is miniredis.FastForward).
func NewSQLStoreWithClock(db *gorm.DB, now func() time.Time) *SQLStore {
	return &SQLStore{db: db, now: now}
}

func (s *SQLStore) Create(ctx context.Context, in NewFlow) (Issued, error) {
	now := s.now()
	var outstanding int64
	if err := s.db.WithContext(ctx).Model(&model.DeviceCode{}).
		Where("expires_at > ?", model.Timestamp(now)).Count(&outstanding).Error; err != nil {
		return Issued{}, err
	}
	if outstanding >= MaxOutstanding {
		return Issued{}, ErrCapacity
	}

	deviceCode := idgen.Generate(DeviceCodeLen)
	for attempt := 0; attempt < mintRetries; attempt++ {
		row := model.DeviceCode{
			DeviceCode: deviceCode,
			UserCode:   mintUserCode(),
			Status:     StatusPending,
			NameHint:   in.NameHint,
			CreatedIP:  in.CreatedIP,
			CreatedUA:  in.CreatedUA,
			CreatedAt:  model.Timestamp(now),
			ExpiresAt:  model.Timestamp(now.Add(TTL)),
		}
		// DO NOTHING + RowsAffected is SETNX in SQL: a user_code already taken
		// by a live flow leaves the insert a no-op, and we mint another. The
		// unique index is the authority either way, so two replicas racing on
		// one code cannot both get it.
		res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if res.Error != nil {
			return Issued{}, res.Error
		}
		if res.RowsAffected == 0 {
			continue // collision, mint another
		}
		return Issued{DeviceCode: deviceCode, UserCode: row.UserCode}, nil
	}
	return Issued{}, ErrCapacity
}

func (s *SQLStore) ByUserCode(ctx context.Context, userCode string) (string, Record, error) {
	var row model.DeviceCode
	err := s.db.WithContext(ctx).
		Where("user_code = ? AND expires_at > ?", userCode, model.Timestamp(s.now())).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", Record{}, ErrNotFound
	}
	if err != nil {
		return "", Record{}, err
	}
	return row.DeviceCode, recordOf(row), nil
}

// Approve flips pending→approved in one conditional UPDATE, so the one-shot
// property is the database's to enforce rather than a read-then-write window's.
// A miss is re-read only to tell an already-approved code from a gone one.
func (s *SQLStore) Approve(ctx context.Context, deviceCode string, in Approval) error {
	now := s.now()
	res := s.db.WithContext(ctx).Model(&model.DeviceCode{}).
		Where("device_code = ? AND status = ? AND expires_at > ?",
			deviceCode, StatusPending, model.Timestamp(now)).
		Updates(map[string]interface{}{
			"status":      StatusApproved,
			"authz_id":    in.AuthzID,
			"approver_ip": in.ApproverIP,
			"token_ttl":   in.TokenTTL,
			"approved_at": model.Timestamp(now),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	rec, err := s.get(ctx, deviceCode)
	if err != nil {
		return err
	}
	if rec.Status == StatusApproved {
		return ErrAlreadyApproved
	}
	return ErrNotFound
}

func (s *SQLStore) Poll(ctx context.Context, deviceCode string) (Record, bool, error) {
	rec, err := s.get(ctx, deviceCode)
	if err != nil {
		return Record{}, false, err
	}
	now := s.now()
	if now.Before(rec.NextPollAt) {
		return rec, true, nil // too soon; the deadline stays where it is
	}
	next := model.Timestamp(now.Add(PollInterval))
	res := s.db.WithContext(ctx).Model(&model.DeviceCode{}).
		Where("device_code = ? AND expires_at > ?", deviceCode, model.Timestamp(now)).
		Update("next_poll_at", next)
	if res.Error != nil {
		return Record{}, false, res.Error
	}
	if res.RowsAffected == 0 {
		return Record{}, false, ErrNotFound // expired between the read and this write
	}
	rec.NextPollAt = next
	return rec, false, nil
}

// Consume hands the record to whichever redemption wins the DELETE: the
// conditional delete's RowsAffected is what makes read-and-delete indivisible,
// so a concurrent redemption of the same device_code gets ErrNotFound rather
// than a second PAT.
func (s *SQLStore) Consume(ctx context.Context, deviceCode string) (Record, error) {
	rec, err := s.get(ctx, deviceCode)
	if err != nil {
		return Record{}, err
	}
	if rec.Status != StatusApproved {
		return Record{}, ErrNotApproved
	}
	res := s.db.WithContext(ctx).
		Where("device_code = ? AND status = ?", deviceCode, StatusApproved).
		Delete(&model.DeviceCode{})
	if res.Error != nil {
		return Record{}, res.Error
	}
	if res.RowsAffected == 0 {
		return Record{}, ErrNotFound // lost the race to a concurrent redemption
	}
	return rec, nil
}

func (s *SQLStore) PutChallenge(ctx context.Context, sessionID, token string) error {
	now := s.now()
	row := model.DeviceChallenge{
		SessionID: sessionID,
		Token:     token,
		ExpiresAt: model.Timestamp(now.Add(TTL)),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "session_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"token", "expires_at"}),
	}).Create(&row).Error
}

func (s *SQLStore) ConsumeChallenge(ctx context.Context, sessionID, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	var row model.DeviceChallenge
	err := s.db.WithContext(ctx).
		Where("session_id = ? AND expires_at > ?", sessionID, model.Timestamp(s.now())).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if subtle.ConstantTimeCompare([]byte(row.Token), []byte(token)) != 1 {
		return false, nil
	}
	if err := s.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Delete(&model.DeviceChallenge{}).Error; err != nil {
		return false, err
	}
	return true, nil
}

// PurgeExpired deletes flows and challenges past their 180s window and returns
// how many rows went. The cleanup cron calls it; reads do not depend on it
// having run.
func (s *SQLStore) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	cutoff := model.Timestamp(now)
	flows := s.db.WithContext(ctx).Where("expires_at <= ?", cutoff).Delete(&model.DeviceCode{})
	if flows.Error != nil {
		return 0, flows.Error
	}
	challenges := s.db.WithContext(ctx).Where("expires_at <= ?", cutoff).Delete(&model.DeviceChallenge{})
	return flows.RowsAffected + challenges.RowsAffected, challenges.Error
}

func (s *SQLStore) get(ctx context.Context, deviceCode string) (Record, error) {
	var row model.DeviceCode
	err := s.db.WithContext(ctx).
		Where("device_code = ? AND expires_at > ?", deviceCode, model.Timestamp(s.now())).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	return recordOf(row), nil
}

func recordOf(row model.DeviceCode) Record {
	return Record{
		UserCode:   row.UserCode,
		Status:     row.Status,
		AuthzID:    row.AuthzID,
		NameHint:   row.NameHint,
		CreatedIP:  row.CreatedIP,
		CreatedUA:  row.CreatedUA,
		ApproverIP: row.ApproverIP,
		ApprovedAt: row.ApprovedAt,
		NextPollAt: row.NextPollAt,
		TokenTTL:   row.TokenTTL,
	}
}
