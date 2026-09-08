// Package session is the login session store. Expiry is two-tier: a sliding
// idle TTL refreshed on read, and an absolute cap measured from Data.CreatedAt,
// past which the record is dropped rather than renewed.
//
// Two backends implement Store with the same semantics: RedisStore, which a
// multi-replica deployment needs so every replica sees the same session, and
// SQLStore, which lets a single binary run with no Redis at all. main.go picks
// one by whether redis.addr is configured.
package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"
)

var ErrNotFound = errors.New("session: not found")

type Data struct {
	AuthzID     string    `json:"authz_id"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	CreatedAt   time.Time `json:"created_at"`

	// OIDC carries the in-flight authorization request while the browser is
	// away at the identity provider, and is cleared by the callback that
	// consumes it. It rides on the session because that is the one piece of
	// per-browser server-side state Placard already has, and because it must
	// not be readable or writable by the page that started the flow.
	OIDC *OIDCFlow `json:"oidc,omitempty"`
}

// OIDCFlow is one pending authorization request.
//
// A session holding one may be either of two things, told apart by
// Data.AuthzID rather than by a flag here: a PENDING session (empty AuthzID),
// minted for a visitor who is signing in and carrying no identity the auth
// middleware would accept, or a live session whose owner is linking another
// provider to the account they are already signed into.
//
// State is what the callback compares the query parameter against, Nonce what
// it compares the id token's claim against, and Provider is why the callback
// URL needs no provider parameter of its own — the server already knows which
// flow this browser started.
type OIDCFlow struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	// Redirect is the same-origin path to land on afterwards, already
	// validated by the handler that stored it.
	Redirect string `json:"redirect,omitempty"`
}

// Store is the login-session persistence seam.
//
// Get returns ErrNotFound for an unknown, idle-expired or absolutely-expired
// session, and an infrastructure error for anything else — the auth middleware
// maps the first to 401 and the second to 503, so an implementation must never
// collapse a backend outage into ErrNotFound (that would log everyone out).
//
// Update rewrites a session's Data without extending its idle window, and is
// ErrNotFound for a session that no longer exists: it must never be the point
// that mints a new, unexpiring session.
type Store interface {
	Create(ctx context.Context, d Data) (string, error)
	Get(ctx context.Context, id string) (Data, error)
	Update(ctx context.Context, id string, d Data) error
	Delete(ctx context.Context, id string) error
}

// newID returns 32 bytes of crypto/rand as base64url (43 chars, 256-bit).
func newID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
