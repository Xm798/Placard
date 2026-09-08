package storage

import (
	"context"
	"io"
	"sync/atomic"
	"time"

	"github.com/Xm798/placard/internal/ctxlog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// DefaultSlowThreshold is the wall-clock cutoff above which a storage call is
// logged at Warn. Higher than the GORM one (200ms): these are cross-network
// object-store calls over bodies up to the upload limit, so a few hundred
// milliseconds is normal rather than notable.
const DefaultSlowThreshold = time.Second

// LoggingClient decorates a Client with one log line per call.
//
// It sees only (key, err, duration) — never the object body. That is enforced
// by what this file does, not by convention: the io.Reader handed to PutObject
// is passed straight through, and the io.ReadCloser returned by GetObject is
// returned unwrapped. Wrapping either to measure bytes or full-body time would
// both put user HTML through this layer and, for the reader, entangle it with
// the ctx-lifetime rule on the render stream (see the Client doc comment).
//
// One logged duration covers the whole call, retries included: the S3 SDK
// retries a failed request internally (standard retryer, 3 attempts), so a fat
// duration next to a tiny server-side latency is retries or dial trouble
// rather than one slow response.
type LoggingClient struct {
	inner    Client
	log      *zap.Logger
	slow     time.Duration
	counters storageCounters
}

var _ Client = (*LoggingClient)(nil)

// NewLoggingClient wraps inner so every call emits one line: Error on failure,
// Warn when it takes longer than slow, Debug otherwise. The periodic liveness
// snapshot is NOT started here — StartSummary does that, so no caller can
// strand a ticker it has no way to stop.
func NewLoggingClient(inner Client, log *zap.Logger, slow time.Duration) *LoggingClient {
	return &LoggingClient{inner: inner, log: log, slow: slow}
}

func (l *LoggingClient) PutObject(ctx context.Context, key string, data io.Reader, contentType string) error {
	begin := time.Now()
	err := l.inner.PutObject(ctx, key, data, contentType)
	// duration_ms, not ttfb_ms: the SDK reads and closes the response before
	// returning, so the measured interval is the whole operation.
	elapsed := time.Since(begin)
	l.counters.puts.Add(1)
	l.record(ctx, "storage put object", key, elapsed, ctxlog.DurMS(elapsed), err)
	return err
}

func (l *LoggingClient) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	begin := time.Now()
	rc, err := l.inner.GetObject(ctx, key)
	// ttfb_ms, not duration_ms: GetObject returns the response body unread, so
	// what is measured here is response headers only. That is the honest name
	// for the number, not a bug to fix.
	elapsed := time.Since(begin)
	l.counters.gets.Add(1)
	l.record(ctx, "storage get object", key, elapsed, ctxlog.TTFBMS(elapsed), err)
	return rc, err
}

func (l *LoggingClient) DeleteObject(ctx context.Context, key string) error {
	begin := time.Now()
	err := l.inner.DeleteObject(ctx, key)
	elapsed := time.Since(begin)
	l.counters.deletes.Add(1)
	l.record(ctx, "storage delete object", key, elapsed, ctxlog.DurMS(elapsed), err)
	return err
}

// record emits the single line for a completed call. dur is the pre-built
// timing field, so the field *name* is the caller's decision (see above).
//
// The Debug branch goes through log.Check rather than log.Debug because
// log.Debug(msg, fields...) builds the variadic []zap.Field in the caller frame
// *before* zap tests the level: on /s/:id/render → GetObject that is a heap
// allocation per view, in prod, where Debug is suppressed and the line is then
// thrown away. Check returns nil first, so nothing is built. time.Since stays
// outside the check — the slow-threshold decision needs it either way — and
// AddCaller still works because it fires inside ce.Write.
//
// It also logs less: op + timing + correlation only. The key embeds nano_id and
// version, so it belongs on the rare Warn/Error lines, never on the hot one.
// Those two branches are unguarded on purpose: they are rare and always written.
func (l *LoggingClient) record(ctx context.Context, msg, key string, elapsed time.Duration, dur zap.Field, err error) {
	l.counters.observe(elapsed, err)
	switch {
	case err != nil:
		l.log.Error(msg, zap.String("key", key), dur, ctxlog.ReqID(ctx), ctxlog.Entry(ctx), zap.Error(err))
	case elapsed > l.slow:
		l.log.Warn(msg, zap.String("key", key), dur, ctxlog.ReqID(ctx), ctxlog.Entry(ctx))
	default:
		if ce := l.log.Check(zapcore.DebugLevel, msg); ce != nil {
			ce.Write(dur, ctxlog.ReqID(ctx), ctxlog.Entry(ctx))
		}
	}
}

// storageCounters is the liveness state behind the periodic snapshot: counters and
// fixed buckets, never a rate, an average or a percentile. Those are a metrics
// system's job — a p95 cannot be recovered from counters, and a rate computed
// here would be a number nobody can re-aggregate across pods or windows. What
// ELK can do with these is exactly what it is good at: diff two snapshots.
//
// Every field is atomic because the hot path is concurrent (one publish and N
// renders at a time) while exactly one goroutine reads them, and because an
// atomic add is the only counter update that keeps the normal branch
// allocation-free (there is a test asserting that).
type storageCounters struct {
	// One counter per operation, deletes included: observe() buckets all three,
	// so without deletes_total an operator diffing puts_total + gets_total
	// against the buckets sees unexplained excess every time the cron reclaims
	// keys, and cannot tell that from a counter going wrong.
	puts    atomic.Uint64
	gets    atomic.Uint64
	deletes atomic.Uint64
	errors  atomic.Uint64

	// Disjoint latency buckets, NOT cumulative Prometheus `le` buckets: a call
	// lands in exactly one, so the four always sum to the number of calls in
	// the window — i.e. to the diff of puts_total + gets_total + deletes_total.
	// le_200 therefore means (50ms, 200ms], not "at most 200ms".
	le50   atomic.Uint64
	le200  atomic.Uint64
	le1000 atomic.Uint64
	slower atomic.Uint64

	maxMS atomic.Int64
}

// observe records one completed call. Allocation-free by construction: no
// field is built, nothing escapes, and err is already an interface value.
func (c *storageCounters) observe(elapsed time.Duration, err error) {
	if err != nil {
		c.errors.Add(1)
	}
	ms := elapsed.Milliseconds()
	switch {
	case ms <= 50:
		c.le50.Add(1)
	case ms <= 200:
		c.le200.Add(1)
	case ms <= 1000:
		c.le1000.Add(1)
	default:
		c.slower.Add(1)
	}
	for {
		max := c.maxMS.Load()
		if ms <= max || c.maxMS.CompareAndSwap(max, ms) {
			return
		}
	}
}

// SummaryInterval is how often StartSummary emits the liveness snapshot.
const SummaryInterval = time.Minute

// StartSummary runs the periodic snapshot until ctx is done, and returns
// immediately. It exists because the per-call lines answer "was this call
// slow?" but not "is storage being called at all?" — the normal branch is Debug,
// so in prod a perfectly healthy storage layer is silent.
//
// A /metrics endpoint would be the better answer (quantiles come out correct
// rather than approximated), and is deliberately not what this is: there is no
// scrape target to point at it.
func (l *LoggingClient) StartSummary(ctx context.Context) {
	go l.runSummary(ctx, SummaryInterval)
}

func (l *LoggingClient) runSummary(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.logSummary()
		}
	}
}

// logSummary emits one line per interval. No key and no nano_id: this line is
// about the storage layer, not about any page, and a per-object identifier on
// a line that exists to be aggregated is how a low-cardinality field turns into
// a high-cardinality one.
//
// Counters are cumulative since process start, so a missed or delayed tick
// costs nothing — the consumer diffs two lines. max_ms is the one exception:
// it is the window maximum, reset on read, because a cumulative maximum is a
// high-water mark that never comes back down and stops describing "now" after
// the first bad minute.
func (l *LoggingClient) logSummary() {
	l.log.Info("storage summary",
		zap.Uint64("puts_total", l.counters.puts.Load()),
		zap.Uint64("gets_total", l.counters.gets.Load()),
		zap.Uint64("deletes_total", l.counters.deletes.Load()),
		zap.Uint64("errors_total", l.counters.errors.Load()),
		zap.Int64("max_ms", l.counters.maxMS.Swap(0)),
		zap.Uint64("le_50", l.counters.le50.Load()),
		zap.Uint64("le_200", l.counters.le200.Load()),
		zap.Uint64("le_1000", l.counters.le1000.Load()),
		zap.Uint64("slower", l.counters.slower.Load()))
}
