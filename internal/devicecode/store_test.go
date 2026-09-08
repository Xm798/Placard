package devicecode

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Xm798/placard/internal/testutil"
)

// fixture is one Store plus the only backend-specific thing the suite below
// needs: a way to reach the far side of the 180s window and the poll gate
// without sleeping.
type fixture struct {
	store   Store
	advance func(time.Duration)
}

// testClock drives the SQL store, which reads wall time rather than letting a
// server expire keys for it.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// eachStore runs fn against every Store implementation. Both back the same
// endpoints in production — which one a deployment gets depends only on whether
// redis.addr is set — so every property below has to hold for both.
func eachStore(t *testing.T, fn func(*testing.T, fixture)) {
	t.Helper()
	t.Run("redis", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		fn(t, fixture{store: NewRedisStore(rdb), advance: mr.FastForward})
	})
	t.Run("sql", func(t *testing.T) {
		clock := &testClock{t: time.Now().UTC()}
		fn(t, fixture{
			store:   NewSQLStoreWithClock(testutil.OpenTestDB(t), clock.Now),
			advance: clock.advance,
		})
	})
}

func TestCreateShapesCodes(t *testing.T) {
	eachStore(t, func(t *testing.T, f fixture) {
		s := f.store
		got, err := s.Create(context.Background(), NewFlow{NameHint: "mac", CreatedIP: "**.*.*.*"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if len(got.DeviceCode) != DeviceCodeLen {
			t.Fatalf("device_code len = %d, want %d", len(got.DeviceCode), DeviceCodeLen)
		}
		if len(got.UserCode) != UserCodeLen {
			t.Fatalf("user_code len = %d, want %d", len(got.UserCode), UserCodeLen)
		}
		// 0/O/1/I are excluded so a human can copy the code without ambiguity.
		if strings.ContainsAny(got.UserCode, "0O1I") {
			t.Fatalf("user_code %q contains an ambiguous character", got.UserCode)
		}
	})
}

func TestExpiresAfterTTL(t *testing.T) {
	eachStore(t, func(t *testing.T, f fixture) {
		s := f.store
		iss, _ := s.Create(context.Background(), NewFlow{})
		f.advance(TTL + time.Second)
		if _, _, err := s.Poll(context.Background(), iss.DeviceCode); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Poll after TTL err = %v, want ErrNotFound", err)
		}
		if _, _, err := s.ByUserCode(context.Background(), iss.UserCode); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ByUserCode after TTL err = %v, want ErrNotFound", err)
		}
	})
}

func TestApproveThenConsumeIsOneShot(t *testing.T) {
	eachStore(t, func(t *testing.T, f fixture) {
		ctx := context.Background()
		s := f.store
		iss, _ := s.Create(ctx, NewFlow{NameHint: "mac"})

		if _, err := s.Consume(ctx, iss.DeviceCode); !errors.Is(err, ErrNotApproved) {
			t.Fatalf("Consume before approve = %v, want ErrNotApproved", err)
		}
		if err := s.Approve(ctx, iss.DeviceCode, Approval{
			AuthzID: "ou_alice", ApproverIP: "192.0.2.7", TokenTTL: "30d",
		}); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := s.Approve(ctx, iss.DeviceCode, Approval{
			AuthzID: "ou_mallory", ApproverIP: "198.51.100.9", TokenTTL: "180d",
		}); !errors.Is(err, ErrAlreadyApproved) {
			t.Fatalf("second Approve = %v, want ErrAlreadyApproved", err)
		}

		rec, err := s.Consume(ctx, iss.DeviceCode)
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
		if rec.AuthzID != "ou_alice" || rec.ApproverIP != "192.0.2.7" || rec.NameHint != "mac" {
			t.Fatalf("record = %+v", rec)
		}
		// The approver's lifetime choice must survive the store round trip: it is the
		// only channel carrying it to the unauthenticated redemption endpoint.
		if rec.TokenTTL != "30d" {
			t.Fatalf("TokenTTL = %q, want %q (the first approval's choice)", rec.TokenTTL, "30d")
		}
		if _, err := s.Consume(ctx, iss.DeviceCode); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second Consume = %v, want ErrNotFound (one-shot)", err)
		}
	})
}

func TestPollTooSoonSignalsSlowDown(t *testing.T) {
	eachStore(t, func(t *testing.T, f fixture) {
		ctx := context.Background()
		s := f.store
		iss, _ := s.Create(ctx, NewFlow{})

		if _, tooSoon, err := s.Poll(ctx, iss.DeviceCode); err != nil || tooSoon {
			t.Fatalf("first poll: tooSoon=%v err=%v, want false/nil", tooSoon, err)
		}
		if _, tooSoon, _ := s.Poll(ctx, iss.DeviceCode); !tooSoon {
			t.Fatal("immediate second poll: tooSoon=false, want true")
		}
		f.advance(PollInterval + time.Second)
		if _, tooSoon, _ := s.Poll(ctx, iss.DeviceCode); tooSoon {
			t.Fatal("poll after the interval: tooSoon=true, want false")
		}
	})
}

func TestGlobalOutstandingCap(t *testing.T) {
	eachStore(t, func(t *testing.T, f fixture) {
		ctx := context.Background()
		s := f.store
		for i := 0; i < MaxOutstanding; i++ {
			if _, err := s.Create(ctx, NewFlow{}); err != nil {
				t.Fatalf("Create #%d: %v", i, err)
			}
		}
		if _, err := s.Create(ctx, NewFlow{}); !errors.Is(err, ErrCapacity) {
			t.Fatalf("Create over the cap = %v, want ErrCapacity", err)
		}
	})
}

func TestOutstandingCapDecaysWithTTL(t *testing.T) {
	eachStore(t, func(t *testing.T, f fixture) {
		ctx := context.Background()
		s := f.store
		for i := 0; i < MaxOutstanding; i++ {
			if _, err := s.Create(ctx, NewFlow{}); err != nil {
				t.Fatalf("Create #%d: %v", i, err)
			}
		}
		f.advance(TTL + time.Second)
		if _, err := s.Create(ctx, NewFlow{}); err != nil {
			t.Fatalf("Create after the backlog aged out: %v, want nil", err)
		}
	})
}

func TestChallengeIsOneShot(t *testing.T) {
	eachStore(t, func(t *testing.T, f fixture) {
		ctx := context.Background()
		s := f.store
		if err := s.PutChallenge(ctx, "sid-1", "tok-1"); err != nil {
			t.Fatalf("PutChallenge: %v", err)
		}
		if ok, _ := s.ConsumeChallenge(ctx, "sid-1", "wrong"); ok {
			t.Fatal("ConsumeChallenge with a wrong token = true, want false")
		}
		if ok, _ := s.ConsumeChallenge(ctx, "sid-1", "tok-1"); !ok {
			t.Fatal("ConsumeChallenge with the right token = false, want true")
		}
		if ok, _ := s.ConsumeChallenge(ctx, "sid-1", "tok-1"); ok {
			t.Fatal("replayed ConsumeChallenge = true, want false (one-shot)")
		}

		_ = s.PutChallenge(ctx, "sid-2", "tok-2")
		f.advance(TTL + time.Second)
		if ok, _ := s.ConsumeChallenge(ctx, "sid-2", "tok-2"); ok {
			t.Fatal("ConsumeChallenge after TTL = true, want false")
		}
	})
}

// TestIsUserCodeAcceptsWhatMintProduces is the anti-drift pin between the
// minter and the validator. They encode the same alphabet/length fact from
// opposite directions, so if either constant changes and only one side follows,
// freshly-minted codes stop validating — which downstream shows up as the
// confirmation page silently refusing to prefill a perfectly good code.
func TestIsUserCodeAcceptsWhatMintProduces(t *testing.T) {
	for i := 0; i < 200; i++ {
		code := mintUserCode()
		if !IsUserCode(code) {
			t.Fatalf("IsUserCode(mintUserCode()) = false for %q", code)
		}
	}
}

func TestIsUserCodeRejectsMalformed(t *testing.T) {
	cases := []struct {
		name, in string
	}{
		{"empty", ""},
		{"too short", strings.Repeat("A", UserCodeLen-1)},
		{"too long", strings.Repeat("A", UserCodeLen+1)},
		// The alphabet drops these precisely so they cannot be mis-copied; a
		// validator that accepts them is not validating the real alphabet.
		{"excluded 0", "ABCDEFG0"},
		{"excluded O", "ABCDEFGO"},
		{"excluded 1", "ABCDEFG1"},
		{"excluded I", "ABCDEFGI"},
		{"lower case (caller must normalize first)", "abcdefgh"},
		{"html metacharacters", `"><scrip`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsUserCode(tc.in) {
				t.Fatalf("IsUserCode(%q) = true, want false", tc.in)
			}
		})
	}
}

// Consume is the point where a device_code turns into a PAT, so two
// redemptions racing on one code must produce exactly one record — the rest get
// ErrNotFound. Sequential one-shot (above) does not prove this: it is the
// read-and-delete being indivisible that does.
func TestConcurrentConsumeYieldsOneRecord(t *testing.T) {
	eachStore(t, func(t *testing.T, f fixture) {
		ctx := context.Background()
		iss, err := f.store.Create(ctx, NewFlow{NameHint: "mac"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := f.store.Approve(ctx, iss.DeviceCode, Approval{AuthzID: "ou_alice", TokenTTL: "30d"}); err != nil {
			t.Fatalf("Approve: %v", err)
		}

		const racers = 8
		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			won   int
			other []error
		)
		wg.Add(racers)
		for i := 0; i < racers; i++ {
			go func() {
				defer wg.Done()
				rec, err := f.store.Consume(ctx, iss.DeviceCode)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					won++
					if rec.AuthzID != "ou_alice" {
						other = append(other, errors.New("winner got the wrong record"))
					}
				case errors.Is(err, ErrNotFound):
				default:
					other = append(other, err)
				}
			}()
		}
		wg.Wait()

		if won != 1 {
			t.Fatalf("%d redemptions succeeded, want exactly 1", won)
		}
		if len(other) > 0 {
			t.Fatalf("unexpected errors: %v", other)
		}
	})
}
