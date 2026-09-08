package storage

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Xm798/placard/internal/ctxlog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// fakeClient is the inner Client under the decorator. delay makes the slow
// branch deterministic; err drives the error branch.
type fakeClient struct {
	delay time.Duration
	err   error
	body  io.ReadCloser

	putReader io.Reader // captured, never read
}

func (f *fakeClient) PutObject(_ context.Context, _ string, data io.Reader, _ string) error {
	f.putReader = data
	time.Sleep(f.delay)
	return f.err
}

func (f *fakeClient) GetObject(_ context.Context, _ string) (io.ReadCloser, error) {
	time.Sleep(f.delay)
	if f.err != nil {
		return nil, f.err
	}
	return f.body, nil
}

func (f *fakeClient) DeleteObject(_ context.Context, _ string) error {
	time.Sleep(f.delay)
	return f.err
}

// explodingReader fails the test if anything reads or closes it. The decorator
// must pass bodies straight through: it never tees, buffers or hashes them.
type explodingReader struct {
	t *testing.T
}

func (r *explodingReader) Read([]byte) (int, error) {
	r.t.Fatal("decorator read the request body; it must only ever see (key, err, duration)")
	return 0, io.EOF
}

func (r *explodingReader) Close() error {
	r.t.Fatal("decorator closed the body")
	return nil
}

// observed builds a decorator logging into an observer core at the given level,
// bypassing the global logger.
func observed(inner Client, level zapcore.Level, slow time.Duration) (Client, *observer.ObservedLogs) {
	core, logs := observer.New(level)
	return NewLoggingClient(inner, zap.New(core), slow), logs
}

// correlatedCtx is what a request-scoped ctx looks like at the storage layer.
func correlatedCtx() context.Context {
	ctx := ctxlog.WithRequestID(context.Background(), "req_abc123")
	return ctxlog.WithEntrypoint(ctx, ctxlog.EntrypointHTTP)
}

// fieldsOf flattens one observed entry's fields into name -> value.
func fieldsOf(t *testing.T, logs *observer.ObservedLogs) (observer.LoggedEntry, map[string]interface{}) {
	t.Helper()
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 log line, got %d: %v", len(entries), entries)
	}
	return entries[0], entries[0].ContextMap()
}

func TestErrorPathLogsAtErrorWithKeyAndCorrelation(t *testing.T) {
	boom := errors.New("storage exploded")
	c, logs := observed(&fakeClient{err: boom}, zapcore.DebugLevel, time.Hour)

	if err := c.DeleteObject(correlatedCtx(), "2026/07/abc-1.html"); !errors.Is(err, boom) {
		t.Fatalf("error must pass through unchanged, got %v", err)
	}

	e, f := fieldsOf(t, logs)
	if e.Level != zapcore.ErrorLevel {
		t.Errorf("level = %v, want Error", e.Level)
	}
	if e.Message != "storage delete object" {
		t.Errorf("message = %q", e.Message)
	}
	if f["key"] != "2026/07/abc-1.html" {
		t.Errorf("key = %v, want the object key on the error line", f["key"])
	}
	if f["request_id"] != "req_abc123" || f["entrypoint"] != ctxlog.EntrypointHTTP {
		t.Errorf("correlation missing: request_id=%v entrypoint=%v", f["request_id"], f["entrypoint"])
	}
	if f["error"] != boom.Error() {
		t.Errorf("error = %v, want %q", f["error"], boom.Error())
	}
	if _, ok := f["duration_ms"].(int64); !ok {
		t.Errorf("duration_ms missing or not int64: %#v", f["duration_ms"])
	}
}

func TestSlowCallLogsAtWarnWithoutError(t *testing.T) {
	c, logs := observed(&fakeClient{delay: 5 * time.Millisecond}, zapcore.DebugLevel, time.Millisecond)

	if err := c.PutObject(correlatedCtx(), "2026/07/abc-1.html", nil, "text/html"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	e, f := fieldsOf(t, logs)
	if e.Level != zapcore.WarnLevel {
		t.Errorf("level = %v, want Warn", e.Level)
	}
	if f["key"] != "2026/07/abc-1.html" {
		t.Errorf("key = %v, want the object key on the slow line", f["key"])
	}
	if _, ok := f["error"]; ok {
		t.Errorf("slow line must carry no error field: %v", f["error"])
	}
	if ms, ok := f["duration_ms"].(int64); !ok || ms < 1 {
		t.Errorf("duration_ms = %#v, want the measured milliseconds", f["duration_ms"])
	}
	if f["request_id"] != "req_abc123" || f["entrypoint"] != ctxlog.EntrypointHTTP {
		t.Errorf("correlation missing: request_id=%v entrypoint=%v", f["request_id"], f["entrypoint"])
	}
}

func TestNormalCallLogsAtDebugWithoutKey(t *testing.T) {
	c, logs := observed(&fakeClient{body: io.NopCloser(nil)}, zapcore.DebugLevel, time.Hour)

	if _, err := c.GetObject(correlatedCtx(), "2026/07/abc-1.html"); err != nil {
		t.Fatalf("GetObject: %v", err)
	}

	e, f := fieldsOf(t, logs)
	if e.Level != zapcore.DebugLevel {
		t.Errorf("level = %v, want Debug", e.Level)
	}
	if e.Message != "storage get object" {
		t.Errorf("message = %q", e.Message)
	}
	// The key embeds nano_id + version, so it stays off the hot line.
	if _, ok := f["key"]; ok {
		t.Errorf("hot Debug line must not carry key: %v", f["key"])
	}
	if _, ok := f["nano_id"]; ok {
		t.Errorf("hot Debug line must not carry nano_id: %v", f["nano_id"])
	}
	if f["request_id"] != "req_abc123" || f["entrypoint"] != ctxlog.EntrypointHTTP {
		t.Errorf("correlation missing: request_id=%v entrypoint=%v", f["request_id"], f["entrypoint"])
	}
}

// TestTimingFieldNamePerOperation pins the §0-verified names: GetObject returns
// the body unread, so its measurement is time-to-first-byte; the other two are
// complete when the call returns.
func TestTimingFieldNamePerOperation(t *testing.T) {
	cases := []struct {
		name  string
		call  func(Client) error
		field string
		other string
	}{
		{
			name:  "get",
			call:  func(c Client) error { _, err := c.GetObject(context.Background(), "k"); return err },
			field: "ttfb_ms",
			other: "duration_ms",
		},
		{
			name:  "put",
			call:  func(c Client) error { return c.PutObject(context.Background(), "k", nil, "text/html") },
			field: "duration_ms",
			other: "ttfb_ms",
		},
		{
			name:  "delete",
			call:  func(c Client) error { return c.DeleteObject(context.Background(), "k") },
			field: "duration_ms",
			other: "ttfb_ms",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Debug branch.
			c, logs := observed(&fakeClient{body: io.NopCloser(nil)}, zapcore.DebugLevel, time.Hour)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			_, f := fieldsOf(t, logs)
			if _, ok := f[tc.field].(int64); !ok {
				t.Errorf("debug line: %s missing, got %v", tc.field, f)
			}
			if _, ok := f[tc.other]; ok {
				t.Errorf("debug line: unexpected %s", tc.other)
			}

			// Error branch must use the same name.
			c, logs = observed(&fakeClient{err: errors.New("boom")}, zapcore.DebugLevel, time.Hour)
			_ = tc.call(c)
			_, f = fieldsOf(t, logs)
			if _, ok := f[tc.field].(int64); !ok {
				t.Errorf("error line: %s missing, got %v", tc.field, f)
			}
			if _, ok := f[tc.other]; ok {
				t.Errorf("error line: unexpected %s", tc.other)
			}
		})
	}
}

// TestBodyIsNeverTouched covers both directions: the upload reader is handed to
// the inner client untouched, and the download body comes back unwrapped.
func TestBodyIsNeverTouched(t *testing.T) {
	body := &explodingReader{t: t}
	inner := &fakeClient{body: body}
	c, _ := observed(inner, zapcore.DebugLevel, time.Hour)

	up := &explodingReader{t: t}
	if err := c.PutObject(context.Background(), "k", up, "text/html"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if inner.putReader != io.Reader(up) {
		t.Errorf("PutObject reader was substituted: %#v", inner.putReader)
	}

	got, err := c.GetObject(context.Background(), "k")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if got != io.ReadCloser(body) {
		t.Errorf("GetObject body was wrapped: %#v", got)
	}
}

// TestDebugBranchSilentAtInfoLevel is the behavioural half of the allocation
// claim: at prod's level the normal path produces no output at all.
func TestDebugBranchSilentAtInfoLevel(t *testing.T) {
	c, logs := observed(&fakeClient{body: io.NopCloser(nil)}, zapcore.InfoLevel, time.Hour)

	if _, err := c.GetObject(correlatedCtx(), "2026/07/abc-1.html"); err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if n := logs.Len(); n != 0 {
		t.Fatalf("normal path wrote %d lines at Info level, want 0: %v", n, logs.All())
	}
}

// TestNormalBranchAllocatesNothingAtInfoLevel is the other half: silence proves
// nothing was written, not that nothing was built. log.Debug(msg, fields...)
// would allocate the variadic slice in this frame before zap ever checks the
// level; log.Check does not. On /s/:id/render that is one allocation per view.
func TestNormalBranchAllocatesNothingAtInfoLevel(t *testing.T) {
	// A body value that is already an interface-shaped pointer, so the inner
	// client itself contributes no allocation to the measurement.
	inner := &fakeClient{body: io.NopCloser(nil)}
	core, _ := observer.New(zapcore.InfoLevel)
	c := NewLoggingClient(inner, zap.New(core), time.Hour)
	ctx := correlatedCtx()

	allocs := testing.AllocsPerRun(200, func() {
		_, _ = c.GetObject(ctx, "2026/07/abc-1.html")
	})
	if allocs != 0 {
		t.Errorf("normal branch allocated %.1f times per call at Info level, want 0", allocs)
	}
}

// TestSummaryTickReportsCountersAndBuckets drives one call into each branch and
// then lets the real ticker fire: the snapshot must be produced by the tick, not
// by a test calling logSummary directly, because a ticker that never fires is
// the failure mode this whole line exists to rule out.
func TestSummaryTickReportsCountersAndBuckets(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	c := NewLoggingClient(&fakeClient{}, zap.New(core), time.Hour)

	ctx := correlatedCtx()
	if err := c.PutObject(ctx, "2026/07/abc-1.html", &explodingReader{t: t}, "text/html"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if _, err := c.GetObject(ctx, "2026/07/abc-1.html"); err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	failing := &fakeClient{err: errors.New("storage unavailable")}
	c2 := NewLoggingClient(failing, zap.NewNop(), time.Hour)
	if err := c2.DeleteObject(ctx, "2026/07/abc-1.html"); err == nil {
		t.Fatal("DeleteObject: want error")
	}
	if err := c.DeleteObject(ctx, "2026/07/abc-1.html"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}

	tickCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.runSummary(tickCtx, time.Millisecond)

	entry := waitForEntry(t, logs, "storage summary")
	got := entry.ContextMap()

	for field, want := range map[string]uint64{
		"puts_total":    1,
		"gets_total":    1,
		"deletes_total": 1,
		"errors_total":  0, // the failure went to a different decorator
		"le_50":         3, // put + get + delete, all instant
		"le_200":        0,
		"le_1000":       0,
		"slower":        0,
	} {
		if v, ok := got[field].(uint64); !ok || v != want {
			t.Errorf("%s = %#v, want %d", field, got[field], want)
		}
	}

	// The reason deletes_total exists: every op observe() buckets must also be
	// counted by op, or the emitted fields do not reconcile and an operator
	// diffing two snapshots reads the excess as a broken counter.
	ops := got["puts_total"].(uint64) + got["gets_total"].(uint64) + got["deletes_total"].(uint64)
	buckets := got["le_50"].(uint64) + got["le_200"].(uint64) + got["le_1000"].(uint64) + got["slower"].(uint64)
	if ops != buckets {
		t.Errorf("per-op totals sum to %d but the buckets sum to %d; the snapshot does not reconcile: %v", ops, buckets, got)
	}
	if v, ok := got["max_ms"].(int64); !ok || v != 0 {
		t.Errorf("max_ms = %#v, want int64(0)", got["max_ms"])
	}

	// The snapshot is an aggregate. A per-object identifier on it would turn a
	// line meant to be grouped into one line per page.
	for _, forbidden := range []string{"key", "nano_id"} {
		if _, present := got[forbidden]; present {
			t.Errorf("summary line carries %q: %v", forbidden, got)
		}
	}
}

// TestSummaryCountsErrorsAndSlowBuckets pins the two branches the happy-path
// test cannot reach without waiting a second.
func TestSummaryCountsErrorsAndSlowBuckets(t *testing.T) {
	c := NewLoggingClient(&fakeClient{err: errors.New("storage unavailable")}, zap.NewNop(), time.Hour)
	for i := 0; i < 3; i++ {
		_ = c.PutObject(context.Background(), "k", nil, "text/html")
	}
	// Synthesized rather than slept: the bucket edges are the contract, and a
	// test that sleeps past 200ms to prove it is a test nobody will keep.
	c.counters.observe(120*time.Millisecond, nil)
	c.counters.observe(900*time.Millisecond, nil)
	c.counters.observe(3*time.Second, nil)

	if got := c.counters.errors.Load(); got != 3 {
		t.Errorf("errors_total = %d, want 3", got)
	}
	if got := c.counters.le200.Load(); got != 1 {
		t.Errorf("le_200 = %d, want 1", got)
	}
	if got := c.counters.le1000.Load(); got != 1 {
		t.Errorf("le_1000 = %d, want 1", got)
	}
	if got := c.counters.slower.Load(); got != 1 {
		t.Errorf("slower = %d, want 1", got)
	}
	if got := c.counters.maxMS.Load(); got != 3000 {
		t.Errorf("max_ms = %d, want 3000", got)
	}

	// max_ms is a window maximum, not a high-water mark: reading it resets it,
	// so one bad minute does not describe every minute after it.
	c.logSummary()
	if got := c.counters.maxMS.Load(); got != 0 {
		t.Errorf("max_ms after a snapshot = %d, want 0", got)
	}
}

// waitForEntry blocks until the observer captures a line with msg, so the
// assertion depends on the ticker firing rather than on a sleep being long
// enough.
func waitForEntry(t *testing.T, logs *observer.ObservedLogs, msg string) observer.LoggedEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if entries := logs.FilterMessage(msg).All(); len(entries) > 0 {
			return entries[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no %q line within the deadline", msg)
	return observer.LoggedEntry{}
}
