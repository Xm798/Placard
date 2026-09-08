package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func newBufOut(jsonMode bool) (*Out, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	return NewOut(&stdout, &stderr, jsonMode, false, 100), &stdout, &stderr
}

func TestJSONModeSuppressesHumanStdout(t *testing.T) {
	out, stdout, stderr := newBufOut(true)
	out.Printf("Published: %s\n", "abc")
	out.Table([]string{"ID"}, [][]string{{"abc"}})
	out.Warnf("warning: the page has its own <title>\n")
	if stdout.Len() != 0 {
		t.Fatalf("json mode stdout must stay empty until Emit, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "warning:") {
		t.Fatalf("warnings must reach stderr in json mode, got %q", stderr.String())
	}
}

func TestEmitProducesValidJSON(t *testing.T) {
	out, stdout, _ := newBufOut(true)
	if err := out.Emit(map[string]any{"id": "abc", "deleted": true}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("stdout is not valid JSON: %q", stdout.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["id"] != "abc" || got["deleted"] != true {
		t.Fatalf("got %v", got)
	}
}

func TestEmitIsNoOpInHumanMode(t *testing.T) {
	out, stdout, _ := newBufOut(false)
	if err := out.Emit(map[string]any{"id": "abc"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("human mode Emit must not write, got %q", stdout.String())
	}
}

func TestEmitRawPassesServerBytesThrough(t *testing.T) {
	out, stdout, _ := newBufOut(true)
	raw := []byte(`{"shared_version":0,"latest_version":3}`)
	if err := out.EmitRaw(raw); err != nil {
		t.Fatalf("EmitRaw: %v", err)
	}
	// Byte-for-byte passthrough: shared_version stays 0, never "latest".
	if strings.TrimSpace(stdout.String()) != string(raw) {
		t.Fatalf("got %q, want %q", stdout.String(), raw)
	}
}

func TestEmitRawRejectsNonJSON(t *testing.T) {
	out, stdout, _ := newBufOut(true)
	if err := out.EmitRaw([]byte("<html>oops</html>")); err == nil {
		t.Fatal("EmitRaw must refuse to write non-JSON to stdout in --json mode")
	}
	if stdout.Len() != 0 {
		t.Fatalf("nothing may reach stdout, got %q", stdout.String())
	}
}

func TestEmitErrorJSONGoesToStdout(t *testing.T) {
	out, stdout, stderr := newBufOut(true)
	out.EmitError(NoCredentials("not logged in for this base"))
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("error path stdout must be valid JSON, got %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "placard:") {
		t.Fatalf("human prefix leaked into json stdout: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("json mode must not duplicate the error on stderr, got %q", stderr.String())
	}
}

func TestEmitErrorHumanGoesToStderr(t *testing.T) {
	out, stdout, stderr := newBufOut(false)
	out.EmitError(errors.New("boom"))
	if stdout.Len() != 0 {
		t.Fatalf("human error must not touch stdout, got %q", stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), "placard: boom") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestTableIsDeterministic(t *testing.T) {
	out, stdout, _ := newBufOut(false)
	out.Table([]string{"ID", "TITLE"}, [][]string{
		{"abc", "Q3 Report"},
		{"defghi", "x"},
	})
	want := "ID      TITLE\nabc     Q3 Report\ndefghi  x\n"
	if stdout.String() != want {
		t.Fatalf("got:\n%q\nwant:\n%q", stdout.String(), want)
	}
}

func TestColorDisabledMeansNoANSI(t *testing.T) {
	out, _, _ := newBufOut(false)
	if got := out.Bold("hi"); got != "hi" {
		t.Fatalf("Bold with Color=false = %q, want plain", got)
	}
	colored := NewOut(nil, nil, false, true, 100)
	if got := colored.Bold("hi"); got != "\x1b[1mhi\x1b[0m" {
		t.Fatalf("Bold with Color=true = %q", got)
	}
}

func TestTableWidthFromEnv(t *testing.T) {
	if got := tableWidthFromEnv(func(string) string { return "" }); got != 100 {
		t.Fatalf("default width = %d, want 100", got)
	}
	if got := tableWidthFromEnv(func(k string) string {
		if k == "PLACARD_TABLE_WIDTH" {
			return "60"
		}
		return ""
	}); got != 60 {
		t.Fatalf("env width = %d, want 60", got)
	}
	if got := tableWidthFromEnv(func(string) string { return "not-a-number" }); got != 100 {
		t.Fatalf("bad env width must fall back to 100, got %d", got)
	}
}
