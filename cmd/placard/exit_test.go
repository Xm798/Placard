package main

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"usage", Usage("unknown flag --nope"), 2},
		{"no_credentials", NoCredentials("not logged in"), 3},
		{"base_mismatch", BaseMismatch("token is bound to another base"), 3},
		{"network", Network("dial tcp: timeout"), 1},
		{"local_io", LocalIO("open page.html: no such file"), 1},
		{"conflict", Conflict("grantee list exceeds 200"), 1},
		{"server_too_old_login", ServerTooOld("no device endpoint", 3), 3},
		{"server_too_old_other", ServerTooOld("no resolve endpoint", 1), 1},
		{"server_error", ServerError("not_found", "not found", 1), 1},
		{"plain error", errors.New("boom"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCode(tc.err); got != tc.want {
				t.Fatalf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestErrorCodes(t *testing.T) {
	cases := []struct {
		err  *Error
		code string
	}{
		{Usage("x"), "usage"},
		{NoCredentials("x"), "no_credentials"},
		{BaseMismatch("x"), "base_mismatch"},
		{Network("x"), "network"},
		{LocalIO("x"), "local_io"},
		{Conflict("x"), "conflict"},
		{ServerTooOld("x", 1), "server_too_old"},
	}
	for _, tc := range cases {
		if tc.err.Code != tc.code {
			t.Errorf("code = %q, want %q", tc.err.Code, tc.code)
		}
	}
}

func TestErrorEnvelopeIsValidJSON(t *testing.T) {
	b, err := json.Marshal(errorEnvelope(Conflict("2 user_id(s) could not be resolved")))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error.Code != "conflict" || got.Error.Message != "2 user_id(s) could not be resolved" {
		t.Fatalf("envelope = %s", b)
	}
}

func TestErrorEnvelopeForPlainError(t *testing.T) {
	b, _ := json.Marshal(errorEnvelope(errors.New("boom")))
	if string(b) != `{"error":{"code":"error","message":"boom"}}` {
		t.Fatalf("envelope = %s", b)
	}
}

func TestClassifyCobraError(t *testing.T) {
	usageish := []string{
		"unknown command \"blah\" for \"placard\"",
		"unknown flag: --nope",
		"unknown shorthand flag: 'z' in -z",
		"accepts 1 arg(s), received 0",
		"requires at least 1 arg(s), only received 0",
		"flag needs an argument: --base",
		"invalid argument \"x\" for \"--json\"",
	}
	for _, msg := range usageish {
		got := classifyCobraError(errors.New(msg))
		if ExitCode(got) != 2 {
			t.Errorf("classify(%q) exit = %d, want 2", msg, ExitCode(got))
		}
	}
	if got := classifyCobraError(Network("timeout")); ExitCode(got) != 1 {
		t.Errorf("classify must pass *Error through unchanged")
	}
	if got := classifyCobraError(errors.New("something else")); ExitCode(got) != 1 {
		t.Errorf("non-usage error should be exit 1")
	}
}
