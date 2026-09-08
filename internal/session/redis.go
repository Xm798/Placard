package session

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyPrefix = "placard:session:"

// RedisStore keeps sessions in Redis: the sliding idle TTL is Redis EXPIRE, and
// the absolute cap is enforced on read from Data.CreatedAt (the key is
// proactively deleted when exceeded).
type RedisStore struct {
	rdb         *redis.Client
	idleTTL     time.Duration
	absoluteTTL time.Duration
}

func NewRedisStore(rdb *redis.Client, idleTTL, absoluteTTL time.Duration) *RedisStore {
	return &RedisStore{rdb: rdb, idleTTL: idleTTL, absoluteTTL: absoluteTTL}
}

func (s *RedisStore) Create(ctx context.Context, d Data) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	if err := s.rdb.Set(ctx, keyPrefix+id, raw, s.idleTTL).Err(); err != nil {
		return "", err
	}
	return id, nil
}

func (s *RedisStore) Get(ctx context.Context, id string) (Data, error) {
	key := keyPrefix + id
	raw, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return Data{}, ErrNotFound
	}
	if err != nil {
		return Data{}, err // infrastructure error — caller maps to 503, never 401
	}
	var d Data
	if err := json.Unmarshal(raw, &d); err != nil {
		// Corrupt value: fail-closed as no-session, drop the key.
		_ = s.rdb.Del(ctx, key).Err()
		return Data{}, ErrNotFound
	}
	if time.Since(d.CreatedAt) > s.absoluteTTL {
		_ = s.rdb.Del(ctx, key).Err()
		return Data{}, ErrNotFound
	}
	// Threshold renewal: only write when remaining TTL fell below idle/2.
	if ttl, err := s.rdb.TTL(ctx, key).Result(); err == nil && ttl > 0 && ttl < s.idleTTL/2 {
		_ = s.rdb.Expire(ctx, key, s.idleTTL).Err()
	}
	return d, nil
}

// Update overwrites the stored Data for id without touching its remaining
// TTL (redis.KeepTTL — a plain Set with an explicit expiration would reset the
// idle window on every write, defeating the sliding-expiry design). Mode XX
// makes the write conditional on the key already existing:
// if id is unknown (expired/never existed), Redis's SET NX/XX semantics
// return a nil reply, which go-redis surfaces as redis.Nil — mapped here to
// ErrNotFound, consistent with Get/Delete. Update must never be the point
// that silently mints a new, TTL-less session key.
func (s *RedisStore) Update(ctx context.Context, id string, d Data) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	err = s.rdb.SetArgs(ctx, keyPrefix+id, raw, redis.SetArgs{Mode: "XX", KeepTTL: true}).Err()
	if errors.Is(err, redis.Nil) {
		return ErrNotFound
	}
	return err
}

func (s *RedisStore) Delete(ctx context.Context, id string) error {
	return s.rdb.Del(ctx, keyPrefix+id).Err()
}
