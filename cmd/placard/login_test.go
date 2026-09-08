package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/dto"
)

// scriptedTokenServer replies with the given bodies in order, repeating the last.
func scriptedTokenServer(t *testing.T, bodies ...string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/device/token" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if _, ok := r.Header["Authorization"]; ok {
			t.Error("the polling endpoint is unauthenticated; no Bearer header may be sent")
		}
		body := bodies[len(bodies)-1]
		if calls < len(bodies) {
			body = bodies[calls]
		}
		calls++
		if strings.HasPrefix(body, "STATUS:") {
			parts := strings.SplitN(body, "|", 2)
			code := parts[0][len("STATUS:"):]
			w.WriteHeader(atoiOrDie(t, code))
			_, _ = w.Write([]byte(parts[1]))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	return srv, &calls
}

func atoiOrDie(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func newTestPoller(t *testing.T, srv *httptest.Server, sleeps *[]time.Duration) *devicePoller {
	t.Helper()
	now := testClock()
	return &devicePoller{
		Client:      NewClient(srv.URL, "", srv.Client()),
		Interval:    5 * time.Second,
		MaxInterval: pollMaxInterval,
		Deadline:    now.Add(190 * time.Second),
		Now:         func() time.Time { return now },
		Sleep: func(d time.Duration) {
			*sleeps = append(*sleeps, d)
			now = now.Add(d)
		},
	}
}

func TestPollerReturnsTokenOnApproved(t *testing.T) {
	srv, calls := scriptedTokenServer(t,
		`{"status":"pending"}`,
		`{"status":"approved","token":"pl_newtoken","name":"CLI on mac"}`)
	defer srv.Close()

	var sleeps []time.Duration
	p := newTestPoller(t, srv, &sleeps)
	resp, err := p.Poll(context.Background(), "dc_xxx")
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if resp.Token != "pl_newtoken" || resp.Status != "approved" {
		t.Fatalf("resp = %+v", resp)
	}
	if *calls != 2 {
		t.Fatalf("calls = %d, want 2", *calls)
	}
	if len(sleeps) != 2 || sleeps[0] != 5*time.Second || sleeps[1] != 5*time.Second {
		t.Fatalf("sleeps = %v, want [5s 5s]", sleeps)
	}
}

// spec §3.8: slow_down multiplies the interval by 1.5, caps at 30s, and NEVER
// lowers it again (ratchet).
func TestPollerSlowDownRatchets(t *testing.T) {
	srv, _ := scriptedTokenServer(t,
		`{"status":"pending"}`,
		`{"status":"slow_down"}`,
		`{"status":"slow_down"}`,
		`{"status":"pending"}`,
		`{"status":"approved","token":"pl_x"}`)
	defer srv.Close()

	var sleeps []time.Duration
	p := newTestPoller(t, srv, &sleeps)
	if _, err := p.Poll(context.Background(), "dc_xxx"); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	want := []time.Duration{
		5 * time.Second,          // before poll 1 (pending)
		5 * time.Second,          // before poll 2 (slow_down → 7.5s)
		7500 * time.Millisecond,  // before poll 3 (slow_down → 11.25s)
		11250 * time.Millisecond, // before poll 4 (pending: interval unchanged)
		11250 * time.Millisecond, // before poll 5 (approved)
	}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleeps[%d] = %v, want %v (full: %v)", i, sleeps[i], want[i], sleeps)
		}
	}
}

func TestPollerIntervalCapsAt30s(t *testing.T) {
	bodies := make([]string, 0, 20)
	for i := 0; i < 12; i++ {
		bodies = append(bodies, `{"status":"slow_down"}`)
	}
	bodies = append(bodies, `{"status":"approved","token":"pl_x"}`)
	srv, _ := scriptedTokenServer(t, bodies...)
	defer srv.Close()

	var sleeps []time.Duration
	p := newTestPoller(t, srv, &sleeps)
	p.Deadline = p.Now().Add(24 * time.Hour) // isolate the cap from the timeout
	if _, err := p.Poll(context.Background(), "dc_xxx"); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	for _, d := range sleeps {
		if d > pollMaxInterval {
			t.Fatalf("interval %v exceeds the 30s cap (all: %v)", d, sleeps)
		}
	}
	if sleeps[len(sleeps)-1] != pollMaxInterval {
		t.Fatalf("interval should have ratcheted up to the cap, got %v", sleeps[len(sleeps)-1])
	}
}

func TestPollerExpiredStopsWithExit3(t *testing.T) {
	srv, calls := scriptedTokenServer(t, `{"status":"expired"}`)
	defer srv.Close()

	var sleeps []time.Duration
	p := newTestPoller(t, srv, &sleeps)
	_, err := p.Poll(context.Background(), "dc_xxx")
	if err == nil {
		t.Fatal("expired must stop immediately")
	}
	if ExitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", ExitCode(err))
	}
	if !strings.Contains(err.Error(), "180") || !strings.Contains(err.Error(), "placard login") {
		t.Fatalf("message must state the 180s lifetime and the retry command, got %q", err.Error())
	}
	if *calls != 1 {
		t.Fatalf("calls = %d, want 1 (stop, do not keep polling)", *calls)
	}
}

// spec §3.8: a single failed poll is not fatal — retry up to 3 times with
// 1s/2s/4s backoff.
func TestPollerRetriesTransientNetworkFailures(t *testing.T) {
	srv, calls := scriptedTokenServer(t,
		`STATUS:502|{"code":"storage_failed","message":"upstream"}`,
		`STATUS:502|{"code":"storage_failed","message":"upstream"}`,
		`{"status":"approved","token":"pl_x"}`)
	defer srv.Close()

	var sleeps []time.Duration
	p := newTestPoller(t, srv, &sleeps)
	resp, err := p.Poll(context.Background(), "dc_xxx")
	if err != nil {
		t.Fatalf("two transient failures must not be fatal, got %v", err)
	}
	if resp.Token != "pl_x" {
		t.Fatalf("resp = %+v", resp)
	}
	if *calls != 3 {
		t.Fatalf("calls = %d, want 3", *calls)
	}
	var backoffs []time.Duration
	for _, d := range sleeps {
		if d == time.Second || d == 2*time.Second || d == 4*time.Second {
			backoffs = append(backoffs, d)
		}
	}
	if len(backoffs) != 2 || backoffs[0] != time.Second || backoffs[1] != 2*time.Second {
		t.Fatalf("backoffs = %v, want [1s 2s]", backoffs)
	}
}

func TestPollerGivesUpAfterThreeConsecutiveFailures(t *testing.T) {
	srv, calls := scriptedTokenServer(t, `STATUS:502|{"code":"storage_failed","message":"upstream"}`)
	defer srv.Close()

	var sleeps []time.Duration
	p := newTestPoller(t, srv, &sleeps)
	if _, err := p.Poll(context.Background(), "dc_xxx"); err == nil {
		t.Fatal("three consecutive failures must give up")
	}
	if *calls != 4 {
		t.Fatalf("calls = %d, want 4 (1 initial + 3 retries)", *calls)
	}
}

func TestPollerStopsAtDeadline(t *testing.T) {
	srv, _ := scriptedTokenServer(t, `{"status":"pending"}`)
	defer srv.Close()

	var sleeps []time.Duration
	p := newTestPoller(t, srv, &sleeps)
	p.Deadline = p.Now().Add(12 * time.Second) // ~2 polls
	_, err := p.Poll(context.Background(), "dc_xxx")
	if err == nil {
		t.Fatal("polling must stop at the deadline, not run forever")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %q", err.Error())
	}
}

func TestPollerHonoursContextCancel(t *testing.T) {
	srv, _ := scriptedTokenServer(t, `{"status":"pending"}`)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var sleeps []time.Duration
	p := newTestPoller(t, srv, &sleeps)
	p.Sleep = func(d time.Duration) { cancel() } // Ctrl-C arrives while waiting
	if _, err := p.Poll(ctx, "dc_xxx"); err == nil {
		t.Fatal("a cancelled context must end the poll")
	}
}

var _ = dto.DeviceTokenResponse{}
