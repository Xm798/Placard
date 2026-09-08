package storage

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidKey is returned when a key is not a clean, relative, slash-
// separated object path. Callers compare with errors.Is (the concrete error
// wraps it with the offending key).
var ErrInvalidKey = errors.New("storage: invalid object key")

// validateKey enforces the one key shape every backend accepts: a relative
// path of non-empty segments joined by "/", with no "." or ".." segment, no
// backslash and no NUL. The local backend joins keys onto a directory, so this
// is what keeps an object inside it; the other backends apply the same rule so
// a key accepted by one deployment is never rejected by another.
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	if strings.ContainsAny(key, "\\\x00") {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	for _, seg := range strings.Split(key, "/") {
		switch seg {
		case "", ".", "..":
			return fmt.Errorf("%w: %q", ErrInvalidKey, key)
		}
	}
	return nil
}
