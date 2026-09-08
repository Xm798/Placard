package session

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRedisStore(rdb, 7*24*time.Hour, 30*24*time.Hour), mr
}

func TestCreateGetDelete(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	id, err := s.Create(ctx, Data{AuthzID: "on_abc", DisplayName: "沈", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if len(id) < 40 { // 32 random bytes base64url ≈ 43 chars
		t.Fatalf("session id too short: %q", id)
	}

	d, err := s.Get(ctx, id)
	if err != nil || d.AuthzID != "on_abc" || d.DisplayName != "沈" {
		t.Fatalf("get: %+v err=%v", d, err)
	}

	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, id); err != ErrNotFound {
		t.Fatalf("after delete want ErrNotFound, got %v", err)
	}
}

func TestGetUnknownIsNotFound(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Get(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// Renewal only fires when remaining TTL dropped below idle/2 (write-amplification guard).
func TestSlidingRenewalThreshold(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	id, _ := s.Create(ctx, Data{AuthzID: "u", CreatedAt: time.Now().UTC()})
	key := "placard:session:" + id

	// Fresh session: TTL ≈ idle, above half → Get must NOT extend.
	mr.SetTTL(key, 6*24*time.Hour) // above 3.5d threshold
	if _, err := s.Get(ctx, id); err != nil {
		t.Fatal(err)
	}
	if ttl := mr.TTL(key); ttl != 6*24*time.Hour {
		t.Fatalf("TTL should be untouched, got %v", ttl)
	}

	// Below half → Get renews to full idle.
	mr.SetTTL(key, time.Hour)
	if _, err := s.Get(ctx, id); err != nil {
		t.Fatal(err)
	}
	if ttl := mr.TTL(key); ttl != 7*24*time.Hour {
		t.Fatalf("TTL should be renewed to idle, got %v", ttl)
	}
}

// Update must rewrite Data without resetting the key's remaining TTL — a
// profile refresh should never re-extend (or shrink) the idle-expiry clock.
func TestUpdateKeepsTTL(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	id, _ := s.Create(ctx, Data{AuthzID: "u1", CreatedAt: time.Now().UTC()})
	key := "placard:session:" + id
	mr.SetTTL(key, 3*time.Hour) // below the sliding-renewal threshold, so a stray Get wouldn't mask this

	if err := s.Update(ctx, id, Data{
		AuthzID: "u1", DisplayName: "renamed", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if ttl := mr.TTL(key); ttl != 3*time.Hour {
		t.Fatalf("Update must not change TTL, got %v", ttl)
	}

	d, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.DisplayName != "renamed" {
		t.Fatalf("Update did not persist new fields: %+v", d)
	}
}

// Update against an unknown/expired id must error ErrNotFound, not silently
// mint a new TTL-less key (redis SET ... XX KEEPTTL on a missing key is a
// no-op reply from Redis; the store maps that reply to ErrNotFound).
func TestUpdateUnknownIsNotFound(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	if err := s.Update(ctx, "nope", Data{AuthzID: "x"}); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if mr.Exists("placard:session:nope") {
		t.Fatal("Update on unknown id must not create a key")
	}
}

// Sessions older than absoluteTTL are hard-expired: DEL + ErrNotFound.
func TestAbsoluteCap(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	id, _ := s.Create(ctx, Data{AuthzID: "u", CreatedAt: time.Now().UTC().Add(-31 * 24 * time.Hour)})
	if _, err := s.Get(ctx, id); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if mr.Exists("placard:session:" + id) {
		t.Fatal("key must be deleted on absolute-cap expiry")
	}
}

// The two backends must agree on the pending flow, so the SQL store's
// round-trip has a twin here.
func TestOIDCFlowRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	flow := &OIDCFlow{Provider: "corp", State: "st", Nonce: "nc", Redirect: "/settings"}
	id, err := s.Create(ctx, Data{CreatedAt: time.Now().UTC(), OIDC: flow})
	if err != nil {
		t.Fatal(err)
	}

	d, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.AuthzID != "" {
		t.Errorf("authz_id = %q, want empty on a pending session", d.AuthzID)
	}
	if d.OIDC == nil || *d.OIDC != *flow {
		t.Fatalf("flow = %+v, want %+v", d.OIDC, flow)
	}

	d.OIDC = nil
	if err := s.Update(ctx, id, d); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.OIDC != nil {
		t.Errorf("flow = %+v after clearing, want nil", after.OIDC)
	}
}
