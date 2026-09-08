package middleware

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// recordingTouch counts invocations and captures the last authzID; err, when
// set, is returned by every call (to exercise the log-only failure path).
type recordingTouch struct {
	calls int32
	last  atomic.Value
	err   error
	done  chan struct{}
}

func newRecordingTouch(err error) *recordingTouch {
	return &recordingTouch{err: err, done: make(chan struct{}, 16)}
}

func (r *recordingTouch) fn(_ context.Context, authzID string, _ time.Time) error {
	atomic.AddInt32(&r.calls, 1)
	r.last.Store(authzID)
	r.done <- struct{}{}
	return r.err
}

func (r *recordingTouch) waitCall(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("touch func never invoked")
	}
}

func (r *recordingTouch) assertNoCall(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
		t.Fatal("touch func invoked, want no call (throttled)")
	case <-time.After(100 * time.Millisecond):
	}
}

// A nil *ActiveToucher must no-op — AuthOptions.TouchActive is expected to be
// left unset in tests/configs that don't wire one.
func TestActiveToucherNilReceiverNoop(t *testing.T) {
	var a *ActiveToucher
	a.Touch("on_x") // must not panic
}

// An empty authzID must never trigger a write (mirrors the middleware's
// fail-closed empty-identity discipline).
func TestActiveToucherEmptyAuthzIDNoop(t *testing.T) {
	rt := newRecordingTouch(nil)
	a := NewActiveToucher(rt.fn)
	a.Touch("")
	rt.assertNoCall(t)
}

// First touch for a fresh authzID always fires (goes through the detached
// goroutine — waitCall confirms it happened without racing the throttle map).
func TestActiveToucherFirstTouchFires(t *testing.T) {
	rt := newRecordingTouch(nil)
	a := NewActiveToucher(rt.fn)
	a.Touch("on_x")
	rt.waitCall(t)
	if got := rt.last.Load(); got != "on_x" {
		t.Fatalf("authzID passed to touch = %v, want on_x", got)
	}
}

// A second touch for the same authzID within the throttle window must NOT
// fire — this is the whole point of the per-pod in-memory throttle.
func TestActiveToucherThrottlesWithinWindow(t *testing.T) {
	old := ActiveTouchThrottle
	ActiveTouchThrottle = time.Hour // long enough that the test can't outrun it
	defer func() { ActiveTouchThrottle = old }()

	rt := newRecordingTouch(nil)
	a := NewActiveToucher(rt.fn)
	a.Touch("on_x")
	rt.waitCall(t)

	a.Touch("on_x") // within the window
	rt.assertNoCall(t)
}

// Once the throttle window elapses, a repeat touch for the same authzID
// fires again. Uses the package var override the spec calls for.
func TestActiveToucherFiresAgainAfterWindowElapses(t *testing.T) {
	old := ActiveTouchThrottle
	ActiveTouchThrottle = 20 * time.Millisecond
	defer func() { ActiveTouchThrottle = old }()

	rt := newRecordingTouch(nil)
	a := NewActiveToucher(rt.fn)
	a.Touch("on_x")
	rt.waitCall(t)

	time.Sleep(40 * time.Millisecond)
	a.Touch("on_x")
	rt.waitCall(t)

	if got := atomic.LoadInt32(&rt.calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

// Distinct authzIDs are throttled independently.
func TestActiveToucherPerAuthzIDIndependent(t *testing.T) {
	old := ActiveTouchThrottle
	ActiveTouchThrottle = time.Hour
	defer func() { ActiveTouchThrottle = old }()

	rt := newRecordingTouch(nil)
	a := NewActiveToucher(rt.fn)
	a.Touch("on_x")
	rt.waitCall(t)
	a.Touch("on_y")
	rt.waitCall(t)

	if got := atomic.LoadInt32(&rt.calls); got != 2 {
		t.Fatalf("calls = %d, want 2 (independent throttle per authzID)", got)
	}
}

// A failing write must not panic or propagate — best-effort, log-only.
func TestActiveToucherWriteFailureIsSwallowed(t *testing.T) {
	rt := newRecordingTouch(errors.New("db down"))
	a := NewActiveToucher(rt.fn)
	a.Touch("on_x")
	rt.waitCall(t) // reaching here without panicking is the assertion
}

// A nil touch func (no writer configured) must no-op, not panic.
func TestActiveToucherNilTouchFuncNoop(t *testing.T) {
	a := NewActiveToucher(nil)
	a.Touch("on_x") // must not panic
}
