package dto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestNullableExpirySentinel asserts the 9999 "never expires" sentinel maps to
// nil while a real future instant is preserved.
func TestNullableExpirySentinel(t *testing.T) {
	never := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if got := NullableExpiry(never); got != nil {
		t.Errorf("NullableExpiry(9999 sentinel) = %v, want nil", got)
	}

	real := time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)
	got := NullableExpiry(real)
	if got == nil || !got.Equal(real) {
		t.Errorf("NullableExpiry(real) = %v, want %v", got, real)
	}
}

// TestNullableLastUsedSentinel asserts the <=1970 "never used" sentinel maps to
// nil while a real instant is preserved.
func TestNullableLastUsedSentinel(t *testing.T) {
	sentinel := time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)
	if got := NullableLastUsed(sentinel); got != nil {
		t.Errorf("NullableLastUsed(1970 sentinel) = %v, want nil", got)
	}

	real := time.Date(2026, 5, 28, 9, 30, 0, 0, time.UTC)
	got := NullableLastUsed(real)
	if got == nil || !got.Equal(real) {
		t.Errorf("NullableLastUsed(real) = %v, want %v", got, real)
	}
}

// TestSentinelRoundTripNonUTC covers the F4 timezone-drift regression: even when
// the DB driver hands back the sentinel in a non-UTC location, the semantic
// t.Year() judgement still maps to JSON null.
func TestSentinelRoundTripNonUTC(t *testing.T) {
	type wrap struct {
		ExpiresAt  *time.Time `json:"expires_at"`
		LastUsedAt *time.Time `json:"last_used_at"`
	}

	// Two adversarial scenarios that broke the naive == / <= judgement:
	//  (a) the 9999 sentinel INSTANT viewed in a positive-offset zone rolls the
	//      wall-clock year to 10000 — defeats == 9999 AND time.MarshalJSON
	//      crashes with "year outside of range [0,9999]".
	//  (b) the 1970 sentinel INSTANT viewed in a negative-offset zone pulls the
	//      wall-clock day back to 1970-01-01 — still must map to null.
	cases := []struct {
		name string
		loc  *time.Location
	}{
		{"UTC", time.UTC},
		{"east+14", time.FixedZone("UTC+14", 14*3600)},
		{"east+08", time.FixedZone("UTC+08", 8*3600)},
		{"west-11", time.FixedZone("UTC-11", -11*3600)},
	}

	neverUTC := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	lastUsedUTC := time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)

	for _, c := range cases {
		// The driver hands back the same INSTANT located in the DSN loc.
		w := wrap{
			ExpiresAt:  NullableExpiry(neverUTC.In(c.loc)),
			LastUsedAt: NullableLastUsed(lastUsedUTC.In(c.loc)),
		}
		b, err := json.Marshal(w)
		if err != nil {
			t.Fatalf("[%s] marshal: %v", c.name, err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("[%s] unmarshal: %v", c.name, err)
		}
		if string(m["expires_at"]) != "null" {
			t.Errorf("[%s] expires_at = %s, want null", c.name, m["expires_at"])
		}
		if string(m["last_used_at"]) != "null" {
			t.Errorf("[%s] last_used_at = %s, want null", c.name, m["last_used_at"])
		}
	}
}

// TestExpiredResponseShape asserts the expired/miss state serializes to ONLY
// {"expired":true} — never render_url, never title.
func TestExpiredResponseShape(t *testing.T) {
	b, err := json.Marshal(ExpiredResponse{Expired: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if got != `{"expired":true}` {
		t.Errorf("ExpiredResponse = %s, want {\"expired\":true}", got)
	}
	for _, banned := range []string{"render_url", "title", "id"} {
		if strings.Contains(got, banned) {
			t.Errorf("ExpiredResponse leaked %q: %s", banned, got)
		}
	}
}

// TestMetaResponseOmitsEmptyRenderURL asserts render_url is omitted when empty
// (omitempty), so a hit without a signed URL never serializes a blank field.
func TestMetaResponseOmitsEmptyRenderURL(t *testing.T) {
	b, _ := json.Marshal(MetaResponse{ID: "abc", Title: "t", Expired: false})
	if strings.Contains(string(b), "render_url") {
		t.Errorf("MetaResponse with empty render_url should omit it: %s", b)
	}
}
