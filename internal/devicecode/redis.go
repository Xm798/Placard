package devicecode

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Xm798/placard/internal/idgen"
)

const (
	// Key prefixes are fixed so a backlog can be observed with SCAN and wiped
	// in one shot during an incident.
	keyDevice    = "placard:devicecode:"
	keyUserCode  = "placard:devicecode:usercode:"
	keyChallenge = "placard:devicecode:challenge:"
	keyPoll      = "placard:devicecode:poll:"
	keyBacklog   = "placard:devicecode:outstanding"
)

// RedisStore is the Redis-backed Store.
type RedisStore struct {
	rdb *redis.Client
	now func() time.Time
}

// NewRedisStore builds a RedisStore bound to rdb.
func NewRedisStore(rdb *redis.Client) *RedisStore {
	return &RedisStore{rdb: rdb, now: time.Now}
}

// consumeScript atomically reads and deletes a key, returning the old value.
// Consume and ConsumeChallenge both need read-and-delete to be indivisible:
// two concurrent redemptions of one device_code must never both mint a PAT.
var consumeScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if not v then return false end
redis.call('DEL', KEYS[1])
return v
`)

// pruneScript drops backlog members whose device record has expired and
// returns the live count. Pruning is keyed on record EXISTENCE, not on a
// time.Now cutoff: Redis key expiry fires no callback, so the only signal that
// a flow aged out is that its device key is gone. Driving the cap off that
// signal (rather than a wall-clock score) is what makes the backlog self-heal
// without a decrement on the expiry path.
var pruneScript = redis.NewScript(`
local members = redis.call('ZRANGE', KEYS[1], 0, -1)
for _, m in ipairs(members) do
  if redis.call('EXISTS', ARGV[1] .. m) == 0 then
    redis.call('ZREM', KEYS[1], m)
  end
end
return redis.call('ZCARD', KEYS[1])
`)

// Create keeps the outstanding-flow cap in a sorted set of device_codes, pruned
// on every call of members whose device record has expired — the cap self-heals
// without anyone decrementing it on the expiry path (Redis key expiry fires no
// callback we could hook).
func (s *RedisStore) Create(ctx context.Context, in NewFlow) (Issued, error) {
	now := s.now()
	n, err := pruneScript.Run(ctx, s.rdb, []string{keyBacklog}, keyDevice).Int()
	if err != nil {
		return Issued{}, err
	}
	if n >= MaxOutstanding {
		return Issued{}, ErrCapacity
	}

	deviceCode := idgen.Generate(DeviceCodeLen)
	rec := Record{
		UserCode:  "",
		Status:    StatusPending,
		NameHint:  in.NameHint,
		CreatedIP: in.CreatedIP,
		CreatedUA: in.CreatedUA,
	}

	for attempt := 0; attempt < mintRetries; attempt++ {
		userCode := mintUserCode()
		ok, err := s.rdb.SetNX(ctx, keyUserCode+userCode, deviceCode, TTL).Result()
		if err != nil {
			return Issued{}, err
		}
		if !ok {
			continue // collision, mint another
		}
		rec.UserCode = userCode
		raw, err := json.Marshal(rec)
		if err != nil {
			return Issued{}, err
		}
		if err := s.rdb.Set(ctx, keyDevice+deviceCode, raw, TTL).Err(); err != nil {
			_ = s.rdb.Del(ctx, keyUserCode+userCode).Err()
			return Issued{}, err
		}
		if err := s.rdb.ZAdd(ctx, keyBacklog,
			redis.Z{Score: float64(now.UnixNano()), Member: deviceCode}).Err(); err != nil {
			return Issued{}, err
		}
		// The backlog set itself must not outlive the flows it counts.
		_ = s.rdb.Expire(ctx, keyBacklog, TTL*2).Err()
		return Issued{DeviceCode: deviceCode, UserCode: userCode}, nil
	}
	return Issued{}, ErrCapacity
}

func (s *RedisStore) ByUserCode(ctx context.Context, userCode string) (string, Record, error) {
	deviceCode, err := s.rdb.Get(ctx, keyUserCode+userCode).Result()
	if errors.Is(err, redis.Nil) {
		return "", Record{}, ErrNotFound
	}
	if err != nil {
		return "", Record{}, err
	}
	rec, err := s.get(ctx, deviceCode)
	if err != nil {
		return "", Record{}, err
	}
	return deviceCode, rec, nil
}

// Approve reads then writes, so the one-shot property rests on Redis serving
// one command at a time — save's SET XX is what makes a record that expired
// between the two an ErrNotFound rather than a resurrection.
func (s *RedisStore) Approve(ctx context.Context, deviceCode string, in Approval) error {
	rec, err := s.get(ctx, deviceCode)
	if err != nil {
		return err
	}
	if rec.Status == StatusApproved {
		return ErrAlreadyApproved
	}
	rec.Status = StatusApproved
	rec.AuthzID = in.AuthzID
	rec.ApproverIP = in.ApproverIP
	rec.TokenTTL = in.TokenTTL
	rec.ApprovedAt = s.now()
	return s.save(ctx, deviceCode, rec)
}

// Poll gates spacing on a short-lived key SETNX'd with TTL=PollInterval: a poll
// that beats it sees the key still present and gets slow_down, and does not
// push the deadline further out.
func (s *RedisStore) Poll(ctx context.Context, deviceCode string) (Record, bool, error) {
	rec, err := s.get(ctx, deviceCode)
	if err != nil {
		return Record{}, false, err
	}
	ok, err := s.rdb.SetNX(ctx, keyPoll+deviceCode, "1", PollInterval).Result()
	if err != nil {
		return Record{}, false, err
	}
	if !ok {
		return rec, true, nil // too soon
	}
	rec.NextPollAt = s.now().Add(PollInterval)
	if err := s.save(ctx, deviceCode, rec); err != nil {
		return Record{}, false, err
	}
	return rec, false, nil
}

// Consume routes the delete through consumeScript so read-and-delete is one
// Redis command, and the loser of a concurrent redemption gets ErrNotFound.
func (s *RedisStore) Consume(ctx context.Context, deviceCode string) (Record, error) {
	rec, err := s.get(ctx, deviceCode)
	if err != nil {
		return Record{}, err
	}
	if rec.Status != StatusApproved {
		return Record{}, ErrNotApproved
	}
	raw, err := consumeScript.Run(ctx, s.rdb, []string{keyDevice + deviceCode}).Text()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return Record{}, ErrNotFound // lost the race to a concurrent redemption
		}
		return Record{}, err
	}
	var out Record
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Record{}, ErrNotFound
	}
	_ = s.rdb.Del(ctx, keyUserCode+out.UserCode).Err()
	_ = s.rdb.Del(ctx, keyPoll+deviceCode).Err()
	_ = s.rdb.ZRem(ctx, keyBacklog, deviceCode).Err()
	return out, nil
}

func (s *RedisStore) PutChallenge(ctx context.Context, sessionID, token string) error {
	return s.rdb.Set(ctx, keyChallenge+sessionID, token, TTL).Err()
}

func (s *RedisStore) ConsumeChallenge(ctx context.Context, sessionID, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	want, err := s.rdb.Get(ctx, keyChallenge+sessionID).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
		return false, nil
	}
	_, err = consumeScript.Run(ctx, s.rdb, []string{keyChallenge + sessionID}).Text()
	if err != nil && !errors.Is(err, redis.Nil) {
		return false, err
	}
	return true, nil
}

func (s *RedisStore) get(ctx context.Context, deviceCode string) (Record, error) {
	raw, err := s.rdb.Get(ctx, keyDevice+deviceCode).Bytes()
	if errors.Is(err, redis.Nil) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		_ = s.rdb.Del(ctx, keyDevice+deviceCode).Err()
		return Record{}, ErrNotFound
	}
	return rec, nil
}

// save rewrites a record WITHOUT resetting its TTL (KeepTTL): the 180s window
// is absolute from mint time — approving or polling must never extend it.
func (s *RedisStore) save(ctx context.Context, deviceCode string, rec Record) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	err = s.rdb.SetArgs(ctx, keyDevice+deviceCode, raw,
		redis.SetArgs{Mode: "XX", KeepTTL: true}).Err()
	if errors.Is(err, redis.Nil) {
		return ErrNotFound // expired between the read and this write
	}
	return err
}
