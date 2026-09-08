// Package devicecode is the state store for the RFC 8628-shaped CLI login
// flow. It holds ONLY identity and status — never a token plaintext: the PAT is
// minted inside the CLI's redemption request, not at approval time, precisely
// so a long-lived credential never sits next to sessions, rate limits and cron
// locks.
//
// Every record lives for TTL (180s) — short enough that a phished user_code is
// only useful for three minutes, and short enough that an abuse backlog on the
// unauthenticated mint endpoint drains on its own.
//
// Two backends implement Store with the same semantics: RedisStore, which a
// multi-replica deployment needs so an approval on one replica is visible to
// the poll that lands on another, and SQLStore, which lets a single binary run
// with no Redis at all.
package devicecode

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Xm798/placard/internal/idgen"
)

const (
	// TTL bounds every device flow. Same number as the phishing window in the
	// design: a code a social engineer talked someone into is dead in 180s.
	TTL = 180 * time.Second
	// ExpiresIn is TTL in seconds, echoed to the CLI as expires_in.
	ExpiresIn = 180
	// PollInterval is the minimum spacing between two token polls for one
	// device_code; a poll that beats it gets slow_down instead of an answer.
	PollInterval = 5 * time.Second
	// IntervalSecs is PollInterval in seconds, echoed to the CLI as interval.
	IntervalSecs = 5
	// MaxOutstanding caps concurrently-unfinished flows store-wide. Per-IP
	// limiting alone cannot protect the store here: office NAT forces the per-IP
	// budget to be loose (see the auth middleware's session-miss comment), and an
	// unbounded backlog crowds out the sessions sharing that store, which logs
	// the whole site out.
	MaxOutstanding = 1000

	// DeviceCodeLen matches the PAT body length: 62^32 ≈ 190 bits. device_code
	// is the "whoever holds it gets the token" credential — it never passes in
	// front of a human, so it is sized like a secret, not like a code.
	DeviceCodeLen = 32
	// UserCodeLen is what a human compares against their terminal before
	// confirming — or types by hand on the manual-entry fallback page.
	UserCodeLen = 8
	// UserCodeAlphabet drops 0/O/1/I so the code cannot be mis-copied.
	UserCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

	// StatusPending / StatusApproved are exported because the redemption
	// handler branches on Record.Status — a bare "approved" string literal
	// across the package boundary is exactly the kind of drift that compiles
	// fine and then silently never matches.
	StatusPending  = "pending"
	StatusApproved = "approved"

	// mintRetries bounds user_code collision retries (32^8 ≈ 1.1e12 keyspace,
	// so a collision needs an adversary, not luck).
	mintRetries = 5
)

var (
	ErrNotFound        = errors.New("devicecode: not found")
	ErrCapacity        = errors.New("devicecode: too many outstanding flows")
	ErrAlreadyApproved = errors.New("devicecode: already approved")
	ErrNotApproved     = errors.New("devicecode: not approved yet")
)

// Record is everything persisted for one flow. Deliberately carries no token
// plaintext — see the package doc.
type Record struct {
	UserCode   string    `json:"user_code"`
	Status     string    `json:"status"`
	AuthzID    string    `json:"authz_id,omitempty"`
	NameHint   string    `json:"name_hint,omitempty"`
	CreatedIP  string    `json:"created_ip,omitempty"`
	CreatedUA  string    `json:"created_ua,omitempty"`
	ApproverIP string    `json:"approver_ip,omitempty"`
	ApprovedAt time.Time `json:"approved_at,omitzero"`
	NextPollAt time.Time `json:"next_poll_at,omitzero"`
	// TokenTTL is the PAT lifetime the APPROVER picked on the confirmation page,
	// written at Approve time and resolved back at redemption.
	//
	// It travels through the store rather than on the wire precisely because the
	// redemption endpoint is unauthenticated: a TTL taken from the CLI's request
	// body would let whoever holds a device_code choose how long the credential
	// it mints lives. Here the only writer is the authenticated approve handler,
	// which validates against its own allowlist.
	//
	// It is the handler's allowlist KEY (e.g. "7d"), not a duration, so that a
	// lifetime outside the published set is unrepresentable here rather than
	// merely rejected on read: redemption resolves the key through the same
	// allowlist that rendered the page, and an unknown key resolves to nothing.
	// It also keeps the record legible during an incident — "7d" says what was
	// approved where 604800000000000 has to be decoded.
	//
	// Empty means "the approver's choice is unknown" — a record written by a
	// replica predating this field, mid-rollout. Redemption falls back to its default;
	// the store itself holds no opinion about what that default is, nor about
	// which keys are valid.
	TokenTTL string `json:"token_ttl,omitempty"`
}

// NewFlow is Create's input: everything the confirmation page shows about the
// requester. All three fields are attacker-controllable and are shown as
// corroboration only, never as a trust signal.
type NewFlow struct {
	NameHint  string
	CreatedIP string
	CreatedUA string
}

// Issued is Create's output.
type Issued struct {
	DeviceCode string
	UserCode   string
}

// Approval is Approve's input: what the authenticated approver decided.
//
// A struct rather than positional arguments because AuthzID and ApproverIP are
// both strings and TokenTTL grants a credential's lifetime — a transposed pair
// at a call site would compile fine and mis-attribute an approval.
type Approval struct {
	AuthzID    string
	ApproverIP string
	// TokenTTL is the allowlist key for the lifetime chosen on the confirmation
	// page (see Record.TokenTTL). The store records it verbatim; deciding which
	// keys exist and what they mean is the handler's job, not this package's.
	TokenTTL string
}

// Store is the device-flow persistence seam.
//
// Three of its methods carry the flow's security properties and an
// implementation owes all three: Approve is one-shot (a second approval of the
// same code is ErrAlreadyApproved), Consume is read-and-delete indivisibly (two
// concurrent redemptions of one device_code must never both mint a PAT), and
// no method may extend a record's 180s window.
type Store interface {
	// Create mints a device_code + user_code pair and stores a pending record.
	// ErrCapacity once MaxOutstanding flows are already in flight.
	Create(ctx context.Context, in NewFlow) (Issued, error)
	// ByUserCode resolves a human-typed user_code to its device_code + record.
	ByUserCode(ctx context.Context, userCode string) (string, Record, error)
	// Approve flips a pending record to approved, recording who approved it,
	// from which browser IP, and for how long the minted PAT should live.
	Approve(ctx context.Context, deviceCode string, in Approval) error
	// Poll returns the record and whether this poll arrived earlier than
	// PollInterval after the previous one (the caller answers slow_down then).
	// A too-soon poll must not push the deadline further out: a misbehaving
	// client must not be able to lock itself out permanently.
	Poll(ctx context.Context, deviceCode string) (Record, bool, error)
	// Consume deletes an approved record and returns it. An unapproved record
	// is left in place (ErrNotApproved) so the CLI can keep polling.
	Consume(ctx context.Context, deviceCode string) (Record, error)
	// PutChallenge stores the synchronizer CSRF token rendered into the
	// confirmation page, keyed by session id.
	//
	// Note it binds the SESSION, not the device_code: at render time the user
	// has not typed a user_code yet, so the server does not know which flow the
	// page will approve. The binding to device_code completes at approve time,
	// where this token is consumed and the record's pending→approved flip
	// supplies the per-device one-shot property.
	PutChallenge(ctx context.Context, sessionID, token string) error
	// ConsumeChallenge compares and one-shot-consumes the token for sessionID.
	// A mismatch leaves the stored token in place so a wrong guess cannot burn
	// a legitimate user's challenge.
	ConsumeChallenge(ctx context.Context, sessionID, token string) (bool, error)
}

// mintUserCode draws UserCodeLen characters from the unambiguous alphabet.
// idgen.Generate is crypto/rand but fixed to [A-Za-z0-9], so the alphabet is
// applied by re-mapping through a rejection-free index draw.
func mintUserCode() string {
	raw := idgen.Generate(UserCodeLen * 2)
	out := make([]byte, 0, UserCodeLen)
	for i := 0; len(out) < UserCodeLen && i < len(raw); i++ {
		out = append(out, UserCodeAlphabet[int(raw[i])%len(UserCodeAlphabet)])
	}
	return string(out)
}

// IsUserCode reports whether s has the exact shape mintUserCode produces.
//
// It lives next to the minter on purpose: it is the same fact (this alphabet,
// this length) read from the other direction, and callers elsewhere validating
// against the exported constants would silently stop matching newly-minted
// codes the day either constant changes.
//
// Callers must normalize first (upper-case, trimmed) — this is a predicate, not
// a parser. It gates presentation only; authorization stays with the store
// lookup, which is the sole authority on whether a code actually exists.
func IsUserCode(s string) bool {
	if len(s) != UserCodeLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(UserCodeAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}
