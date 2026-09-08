// Package account holds the credential rules an account is created under.
//
// They live here rather than beside the registration handler because two
// surfaces create accounts: the HTTP register endpoint, and the server's
// `admin user create` subcommand, which an operator runs to get back into an
// instance whose admin is locked out. An account the subcommand created must be
// one the login form can resolve, so both go through exactly these rules.
package account

import (
	"errors"
	"net/mail"
	"strings"
)

// Credential shape limits. The username bounds are what a URL-safe, readable
// handle needs; the password minimum is the length below which argon2's cost
// stops mattering, and the maximum only bounds the work one request can ask
// for.
const (
	UsernameMinLen    = 3
	UsernameMaxLen    = 32
	PasswordMinLen    = 8
	PasswordMaxLen    = 128
	EmailMaxLen       = 255
	DisplayNameMaxLen = 128
)

// The rule violations callers report. Each message is user-safe: the HTTP
// surface returns it verbatim in a validation error, and the subcommand prints
// it.
var (
	ErrUsernameLength   = errors.New("username must be 3-32 characters")
	ErrUsernameCharset  = errors.New("username may contain only letters, digits, dot, dash and underscore")
	ErrUsernameLeading  = errors.New("username must start with a letter or digit")
	ErrEmailInvalid     = errors.New("invalid email address")
	ErrEmailLength      = errors.New("email is too long")
	ErrDisplayNameLong  = errors.New("display name is too long")
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")
	ErrPasswordTooLong  = errors.New("password must be at most 128 characters")
)

// ValidateUsername enforces the stored spelling and the character set.
//
// "@" is rejected on purpose: the login identifier accepts a username or an
// email address, and a username containing "@" would make what a caller typed
// ambiguous between the two.
func ValidateUsername(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if len(name) < UsernameMinLen || len(name) > UsernameMaxLen {
		return "", ErrUsernameLength
	}
	for _, r := range name {
		if !IsUsernameRune(r) {
			return "", ErrUsernameCharset
		}
	}
	if !IsAlphanumeric(rune(name[0])) {
		return "", ErrUsernameLeading
	}
	return name, nil
}

// ValidateEmail accepts a blank address as "no email" (nil, stored as NULL).
//
// What is returned is the ADDRESS mail.ParseAddress extracted, never the string
// as typed: the RFC 5322 grammar it implements also accepts display-name forms,
// so `alice <alice@example.com>` and `alice@example.com` are both valid and both
// name one mailbox. Storing them verbatim would leave uk_user_email unable to
// see that, and two accounts would end up holding the same real address — which
// is exactly the ambiguity linking an OIDC identity by email must not face.
func ValidateEmail(raw string) (*string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := mail.ParseAddress(trimmed)
	if err != nil {
		return nil, ErrEmailInvalid
	}
	addr := strings.ToLower(parsed.Address)
	if len(addr) > EmailMaxLen {
		return nil, ErrEmailLength
	}
	return &addr, nil
}

// ValidateDisplayName bounds the profile name and falls back to the username
// when none is given. The bound is in runes, not bytes: the column counts
// characters on Postgres, and truncating bytes would cut a multi-byte rune in
// half.
func ValidateDisplayName(raw, username string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return username, nil
	}
	if len([]rune(name)) > DisplayNameMaxLen {
		return "", ErrDisplayNameLong
	}
	return name, nil
}

// ValidatePassword bounds the password in bytes, which is what argon2 consumes.
func ValidatePassword(pw string) error {
	if len(pw) < PasswordMinLen {
		return ErrPasswordTooShort
	}
	if len(pw) > PasswordMaxLen {
		return ErrPasswordTooLong
	}
	return nil
}

// IsUsernameRune reports whether r may appear in a username.
func IsUsernameRune(r rune) bool {
	return IsAlphanumeric(r) || r == '.' || r == '-' || r == '_'
}

// IsAlphanumeric reports whether r is an ASCII letter or digit — the character
// class a username must start with.
func IsAlphanumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || isASCIIDigit(r)
}

// isASCIIDigit, not unicode.IsDigit: the latter admits U+0663 and friends,
// which ErrUsernameCharset does not promise and which would make the
// byte-based length bound measure something other than characters.
func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }
