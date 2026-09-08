package apperr

import (
	"errors"
	"testing"
)

func TestError_NoCause(t *testing.T) {
	e := NotFound("missing")
	if e.Code != "not_found" || e.HTTPStatus != 404 || e.Message != "missing" {
		t.Fatalf("unexpected error: %+v", e)
	}
	if got := e.Error(); got != "not_found: missing" {
		t.Errorf("Error() = %q", got)
	}
	if e.Unwrap() != nil {
		t.Errorf("Unwrap should be nil without Cause")
	}
}

func TestError_WithCause(t *testing.T) {
	cause := errors.New("db conn refused")
	e := Wrap(cause, "internal", 500, "storage down")
	if e.Cause != cause {
		t.Fatalf("Cause not retained")
	}
	// Error() includes the cause for logs.
	if got := e.Error(); got != "internal: storage down: db conn refused" {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(e, cause) {
		t.Errorf("errors.Is(e, cause) = false, want true")
	}
}

func TestErrorsAs(t *testing.T) {
	// The ErrorHandler relies on errors.As to detect *Error through wrapping.
	cause := errors.New("boom")
	wrapped := Wrap(cause, "storage_failed", 502, "upload failed")
	var target *Error
	if !errors.As(wrapped, &target) {
		t.Fatalf("errors.As did not match *Error")
	}
	if target.HTTPStatus != 502 || target.Code != "storage_failed" {
		t.Fatalf("wrong target: %+v", target)
	}
}

func TestPredefinedDefaults(t *testing.T) {
	cases := []struct {
		e    *Error
		code string
		stat int
		msg  string
	}{
		{Unauthorized(), "unauthorized", 401, "unauthorized"},
		{PermissionDenied(), "permission_denied", 403, "permission denied"},
		{NotFound(), "not_found", 404, "not found"},
		{Validation("bad input"), "validation", 400, "bad input"},
		{RateLimited(), "rate_limited", 429, "rate limited"},
		{UpgradeRequired("too old"), "upgrade_required", 426, "too old"},
		{Storage("oops"), "storage_failed", 502, "oops"},
		{Internal("boom"), "internal", 500, "boom"},
	}
	for _, tc := range cases {
		if tc.e.Code != tc.code || tc.e.HTTPStatus != tc.stat || tc.e.Message != tc.msg {
			t.Errorf("got %+v, want code=%s stat=%d msg=%s", tc.e, tc.code, tc.stat, tc.msg)
		}
	}
}

func TestFirstOverride(t *testing.T) {
	if got := Unauthorized("custom message"); got.Message != "custom message" {
		t.Errorf("override lost: %q", got.Message)
	}
	// Empty override string falls back to default.
	if got := Unauthorized(""); got.Message != "unauthorized" {
		t.Errorf("empty override should fall back: %q", got.Message)
	}
}

func TestUnavailable(t *testing.T) {
	e := Unavailable()
	if e.Code != "unavailable" || e.HTTPStatus != 503 || e.Message != "service unavailable" {
		t.Fatalf("got %+v", e)
	}
	if m := Unavailable("redis down").Message; m != "redis down" {
		t.Fatalf("override msg: got %q", m)
	}
}
