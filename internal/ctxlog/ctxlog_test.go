package ctxlog

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
)

func TestRequestIDRoundTrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req_abc")
	if got := RequestID(ctx); got != "req_abc" {
		t.Fatalf("RequestID = %q, want %q", got, "req_abc")
	}
}

func TestEntrypointRoundTrip(t *testing.T) {
	ctx := WithEntrypoint(context.Background(), EntrypointEvent)
	if got := Entrypoint(ctx); got != "event" {
		t.Fatalf("Entrypoint = %q, want %q", got, "event")
	}
}

func TestBothKeysCoexist(t *testing.T) {
	ctx := WithEntrypoint(WithRequestID(context.Background(), "evt_1"), EntrypointEvent)
	if got := RequestID(ctx); got != "evt_1" {
		t.Errorf("RequestID = %q, want %q", got, "evt_1")
	}
	if got := Entrypoint(ctx); got != "event" {
		t.Errorf("Entrypoint = %q, want %q", got, "event")
	}
}

// Absent values must read as "" without any diagnostic — background paths
// legitimately carry neither key.
func TestAbsentValuesAreEmpty(t *testing.T) {
	ctx := context.Background()
	if got := RequestID(ctx); got != "" {
		t.Errorf("RequestID on bare ctx = %q, want empty", got)
	}
	if got := Entrypoint(ctx); got != "" {
		t.Errorf("Entrypoint on bare ctx = %q, want empty", got)
	}
	//nolint:staticcheck // a nil ctx must not panic: this is the fail-open contract.
	if got := RequestID(nil); got != "" {
		t.Errorf("RequestID(nil) = %q, want empty", got)
	}
	//nolint:staticcheck // ditto.
	if got := Entrypoint(nil); got != "" {
		t.Errorf("Entrypoint(nil) = %q, want empty", got)
	}
}

// A wrong-typed value under the key must not panic, and must read as absent.
func TestWrongTypedValueIsEmpty(t *testing.T) {
	ctx := context.WithValue(context.Background(), requestIDKey{}, 42)
	if got := RequestID(ctx); got != "" {
		t.Errorf("RequestID = %q, want empty", got)
	}
}

func TestDurMSIsInt64Milliseconds(t *testing.T) {
	f := DurMS(1500 * time.Millisecond)
	if f.Key != "duration_ms" {
		t.Errorf("key = %q, want duration_ms", f.Key)
	}
	if f.Type != zapcore.Int64Type {
		t.Errorf("type = %v, want Int64Type", f.Type)
	}
	if f.Integer != 1500 {
		t.Errorf("value = %d, want 1500", f.Integer)
	}
}

func TestDurMSTruncatesSubMillisecond(t *testing.T) {
	if f := DurMS(999 * time.Microsecond); f.Integer != 0 {
		t.Errorf("value = %d, want 0", f.Integer)
	}
}

func TestTTFBMSIsInt64Milliseconds(t *testing.T) {
	f := TTFBMS(2 * time.Second)
	if f.Key != "ttfb_ms" {
		t.Errorf("key = %q, want ttfb_ms", f.Key)
	}
	if f.Type != zapcore.Int64Type {
		t.Errorf("type = %v, want Int64Type", f.Type)
	}
	if f.Integer != 2000 {
		t.Errorf("value = %d, want 2000", f.Integer)
	}
}

func TestContextFieldConstructors(t *testing.T) {
	ctx := WithEntrypoint(WithRequestID(context.Background(), "req_1"), EntrypointHTTP)

	if f := ReqID(ctx); f.Key != "request_id" || f.String != "req_1" || f.Type != zapcore.StringType {
		t.Errorf("ReqID = %+v", f)
	}
	if f := Entry(ctx); f.Key != "entrypoint" || f.String != "http" || f.Type != zapcore.StringType {
		t.Errorf("Entry = %+v", f)
	}
	// Absent ctx values still produce the field, with an empty value.
	if f := ReqID(context.Background()); f.Key != "request_id" || f.String != "" {
		t.Errorf("ReqID on bare ctx = %+v", f)
	}
}

func TestStringFieldConstructors(t *testing.T) {
	cases := []struct {
		name      string
		got       func() (key, val string)
		wantKey   string
		wantValue string
	}{
		{"Op", func() (string, string) { f := Op("publish"); return f.Key, f.String }, "operation", "publish"},
		{"NanoID", func() (string, string) { f := NanoID("abc123"); return f.Key, f.String }, "nano_id", "abc123"},
		{"Outcome", func() (string, string) { f := Outcome(OutcomeDenied); return f.Key, f.String }, "outcome", "denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, val := tc.got()
			if key != tc.wantKey {
				t.Errorf("key = %q, want %q", key, tc.wantKey)
			}
			if val != tc.wantValue {
				t.Errorf("value = %q, want %q", val, tc.wantValue)
			}
		})
	}
}

// The outcome enum must stay byte-identical to what the audit table already
// writes — in particular "failure", not "error".
func TestOutcomeEnumMatchesAuditVocabulary(t *testing.T) {
	if OutcomeSuccess != "success" || OutcomeDenied != "denied" || OutcomeFailure != "failure" {
		t.Fatalf("outcome enum drifted: %q %q %q", OutcomeSuccess, OutcomeDenied, OutcomeFailure)
	}
}
