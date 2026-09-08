// cmd/placard/exit.go
package main

import (
	"errors"
	"strings"
)

// Error is the single error type every command returns. Code is what the
// --json envelope carries; Exit is the process exit code (spec §10.3:
// 0 ok / 1 generic / 2 usage / 3 auth).
type Error struct {
	Code    string
	Message string
	Exit    int
}

func (e *Error) Error() string { return e.Message }

func Usage(msg string) *Error         { return &Error{Code: "usage", Message: msg, Exit: 2} }
func NoCredentials(msg string) *Error { return &Error{Code: "no_credentials", Message: msg, Exit: 3} }
func BaseMismatch(msg string) *Error  { return &Error{Code: "base_mismatch", Message: msg, Exit: 3} }
func Network(msg string) *Error       { return &Error{Code: "network", Message: msg, Exit: 1} }
func LocalIO(msg string) *Error       { return &Error{Code: "local_io", Message: msg, Exit: 1} }
func Conflict(msg string) *Error      { return &Error{Code: "conflict", Message: msg, Exit: 1} }

// ServerTooOld reports a missing endpoint (404 with envelope code "error").
// exit is 3 for login and 1 everywhere else (spec §2.2).
func ServerTooOld(msg string, exit int) *Error {
	return &Error{Code: "server_too_old", Message: msg, Exit: exit}
}

// ServerError carries a server envelope code through verbatim so --json output
// reports the server's own code, not a CLI-invented one.
func ServerError(code, message string, exit int) *Error {
	return &Error{Code: code, Message: message, Exit: exit}
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Exit
	}
	return 1
}

// asError reports whether err is a *Error and, if so, stores it in target.
func asError(err error, target **Error) bool {
	var e *Error
	if errors.As(err, &e) {
		*target = e
		return true
	}
	return false
}

// errorEnvelope builds the --json error shape: {"error":{"code","message"}}.
func errorEnvelope(err error) any {
	code, msg := "error", err.Error()
	var e *Error
	if errors.As(err, &e) {
		code, msg = e.Code, e.Message
	}
	return map[string]any{"error": map[string]string{"code": code, "message": msg}}
}

// cobraUsagePhrases are cobra's own wordings for argument/flag misuse. cobra
// exposes no typed error for them, so matching its phrasing is the only hook.
// This is the ONLY place in the CLI that matches on message text, and it
// matches OUR dependency's text, never a server message (spec §13.1 forbids
// matching server messages such as "Cannot POST ...").
var cobraUsagePhrases = []string{
	"unknown command",
	"unknown flag",
	"unknown shorthand flag",
	"accepts ",
	"requires at least",
	"invalid argument",
	"flag needs an argument",
	"required flag",
}

func classifyCobraError(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	msg := err.Error()
	for _, p := range cobraUsagePhrases {
		if strings.Contains(msg, p) {
			return Usage(msg)
		}
	}
	return &Error{Code: "error", Message: msg, Exit: 1}
}
