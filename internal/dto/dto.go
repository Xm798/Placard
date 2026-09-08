// Package dto holds the explicit outbound response shapes for Placard.
//
// ORM models must never be returned via c.JSON — their fields default-serialize
// and would leak uploader identity / internal fields / sentinel timestamps.
// Every endpoint constructs a DTO here.
//
// Sentinel mapping (F4): judged by field SEMANTICS, not exact-time equality, so
// it stays stable under loc drift / sub-second skew.
//   - expires_at with t.Year() == 9999  → null ("never expires")
//   - last_used_at with t.Year() <= 1970 → null ("never used")
//
// The model import is for the one mapper here (AdminUserItemFrom), which two
// callers share so they cannot report the same account differently. It does not
// license returning a model type: the rule above stands.
package dto

import (
	"time"

	"github.com/Xm798/placard/internal/model"
)

// MetaResponse is the public meta payload on a HIT for GET /s/:id/meta.
// Only the minimal render set is exposed: no uploader, no view_count, no
// internal fields. render_url is the same-origin proxy path /s/:id/render.
type MetaResponse struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Expired   bool   `json:"expired"`
	RenderURL string `json:"render_url,omitempty"`
}

// ExpiredResponse is returned for expired / missing / deleted files, and for a
// live one the caller may not view. It carries ONLY {expired:true} — never
// render_url, never title, never any date field — so a viewer learns nothing
// (no existence confirmation, and no way to tell a private page from a page
// that does not exist).
type ExpiredResponse struct {
	Expired bool `json:"expired"`
}

// ErrorResponse is the unified error envelope returned by the Fiber ErrorHandler
// and by any handler/middleware that aborts with an apperr.Error. Code is the
// stable machine code; Message is user-safe. Error mirrors Message as a
// compatibility field for legacy callers that read "error"; it is kept until
// all consumers migrate to "code". No Cause is ever serialized here.
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

// PublishResponse is the POST /api/publish success body, also returned by the
// restore endpoint (invariant: restore responses are publish-shaped). ExpiresAt
// is a pointer so the 9999 sentinel maps to JSON null. Version is the version
// this publish resolved to (1 for a new page; unchanged on an idempotent
// identical-content re-publish). SkillVersion is the server's current SKILL.md
// version, letting installed agents detect a stale local skill copy.
type PublishResponse struct {
	ID        string     `json:"id"`
	URL       string     `json:"url"`
	Title     string     `json:"title"`
	Version   int        `json:"version"`
	ExpiresAt *time.Time `json:"expires_at"`
	// ShareCode is the plaintext share code, present only on the one response
	// that mints it (publish --password auto). It is never returned again:
	// afterwards only the owner's own file list carries it.
	ShareCode    string    `json:"share_code,omitempty"`
	CreateTime   time.Time `json:"create_time"`
	SkillVersion int       `json:"skill_version"`
}

// ShareCodeResponse is the body of POST /api/files/:id/share-code — the
// freshly minted plaintext code, shown to the owner who asked for it.
type ShareCodeResponse struct {
	ShareCode string `json:"share_code"`
}

// TokenCreateResponse is the one-time POST /api/tokens body. Token is the
// plaintext, shown exactly once and never persisted/returned again.
type TokenCreateResponse struct {
	ID        uint       `json:"id"`
	Token     string     `json:"token"`
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// TokenResponse is a GET /api/tokens list item. Never includes the plaintext or
// the hash.
type TokenResponse struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreateTime time.Time  `json:"create_time"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Revoked    bool       `json:"revoked"`
}

// FileListItem is a GET /api/files list row (owner-only). ViewCount is the
// denormalized cache (may lag the authoritative view table). ExpiresAt maps the
// 9999 sentinel to null via NullableExpiry. LatestVersion/SharedVersion are the
// version pointers (shared_version 0 = follow latest).
type FileListItem struct {
	ID            string `json:"id"` // nano_id
	Title         string `json:"title"`
	URL           string `json:"url"`
	ViewCount     int64  `json:"view_count"`
	LatestVersion int    `json:"latest_version"`
	SharedVersion int    `json:"shared_version"`
	Visibility    string `json:"visibility"`
	// ShareCode is the page's plaintext share code, empty when it has none.
	// This list is owner-scoped, which is the ONLY reason the plaintext may
	// appear here — no other response, the admin surface included, carries it.
	ShareCode  string     `json:"share_code,omitempty"`
	CreateTime time.Time  `json:"create_time"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// FileListResponse is the GET /api/files body with offset pagination.
type FileListResponse struct {
	Files    []FileListItem `json:"files"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

// FileVersionItem is one row of the owner-only GET /api/files/:id/versions
// list. Whitelist only: no object_key, no content_hash, no creator identity.
type FileVersionItem struct {
	Version    int       `json:"version"`
	Title      string    `json:"title"`
	SizeBytes  int64     `json:"size_bytes"`
	CreateTime time.Time `json:"create_time"`
}

// FileVersionsResponse is the owner-only version history (newest first). It is
// NEVER served from the public /s/:id/meta path — MetaResponse must not leak
// version information.
type FileVersionsResponse struct {
	LatestVersion int               `json:"latest_version"`
	SharedVersion int               `json:"shared_version"` // 0 = follow latest
	Versions      []FileVersionItem `json:"versions"`
}

// PrefsResponse is the GET /api/prefs response body with the user's
// default visibility preference.
type PrefsResponse struct {
	DefaultVisibility string `json:"default_visibility"`
}

// DeviceCodeRequest is the POST /auth/device/code body.
//
// Exported (rather than a handler-package struct) because cmd/placard
// marshals it: §1.1 lets the CLI import internal/dto precisely so request and
// response shapes are never hand-copied on the client side.
//
// Hostname is OPTIONAL — an absent or empty value is legal and falls back to
// "unknown", so a client that sends no body at all still works. It is a
// display hint and is NEVER trusted: the server sanitizes it (see
// sanitizeHostname), because a client-side sanitization does not count.
type DeviceCodeRequest struct {
	Hostname string `json:"hostname"`
}

// DeviceCodeResponse is the POST /auth/device/code body — the RFC 8628-shaped
// start of the CLI login flow.
//
// Both URI forms are returned. VerificationURI stays bare so it can be read out
// loud or retyped on a phone; VerificationURIComplete is RFC 8628's optional
// field and carries ?user_code=, which is what the CLI actually opens so the
// user only has to confirm instead of transcribing the code.
//
// That convenience is a deliberate trade: it removes the transcription step
// that used to double as a phishing speed bump, so the confirmation page has to
// carry the weight instead — it shows the code being approved and the
// requester's (untrusted) hostname/IP/UA, and says to close the page if the
// user did not start a login. Keep both fields in sync with that page.
type DeviceCodeResponse struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	// VerificationURI is the bare confirmation page, with no code attached.
	VerificationURI string `json:"verification_uri"`
	// VerificationURIComplete embeds the user_code. Omitted (and the CLI must
	// not synthesize it) if a server ever wants the manual-entry flow back.
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// OpenURI is the URI a client should send the user to: the complete form when
// the server offered one, else the bare page.
//
// It lives on the type rather than in the CLI because it encodes a server-side
// decision: only the server chooses whether this login gets the one-click
// shape. A client must never build the ?user_code= query itself — a server that
// omits the field wants the manual-entry page, and synthesizing it would
// override that from the wrong end.
func (r DeviceCodeResponse) OpenURI() string {
	if r.VerificationURIComplete != "" {
		return r.VerificationURIComplete
	}
	return r.VerificationURI
}

// DeviceTokenRequest is the POST /auth/device/token body.
//
// Exported for the same reason as DeviceCodeRequest: cmd/placard marshals it
// directly (§1.1 permits importing internal/dto), so the wire shape has exactly
// one definition instead of a hand-copied client-side twin.
type DeviceTokenRequest struct {
	DeviceCode string `json:"device_code"`
}

// DeviceTokenResponse is the POST /auth/device/token body. Every outcome is
// HTTP 200 and is distinguished by Status ("pending"/"slow_down"/"expired"/
// "approved"), so the CLI can keep 404 reserved for "this server predates the
// endpoint". Token is the one and only time the plaintext PAT is transmitted.
//
// An unknown device_code answers "expired", identical to a real expiry — a
// distinct answer would make this endpoint an oracle for guessing codes.
type DeviceTokenResponse struct {
	Status    string     `json:"status"`
	Token     string     `json:"token,omitempty"`
	Name      string     `json:"name,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// NullableExpiry maps the 9999 "never expires" sentinel to nil. The judgement
// normalizes to UTC first (F4): a positive-offset loc can roll the sentinel's
// wall-clock year to 10000, which both defeats an == check AND makes
// time.MarshalJSON fail ("year outside of range [0,9999]"). UTC normalization
// plus >= keeps the mapping stable under loc drift.
func NullableExpiry(t time.Time) *time.Time {
	if t.UTC().Year() >= 9999 {
		return nil
	}
	tt := t
	return &tt
}

// NullableLastUsed maps the <=1970 "never used" sentinel to nil. The judgement
// normalizes to UTC first (F4): a negative-offset loc can pull 1970-01-02 back
// to 1970-01-01; UTC normalization keeps the <= judgement stable under drift.
func NullableLastUsed(t time.Time) *time.Time {
	if t.UTC().Year() <= 1970 {
		return nil
	}
	tt := t
	return &tt
}

// AuthStatusResponse is GET /api/auth/status, the only body served before
// anyone is authenticated. It carries instance policy only — never a count of
// users, a username, or anything else that would describe who is here.
//
// SetupRequired is true while the instance has no accounts at all, the state in
// which the next registration becomes the admin; RegistrationOpen already
// accounts for it, so a login page can render from that field alone.
type AuthStatusResponse struct {
	RegistrationOpen bool `json:"registration_open"`
	SetupRequired    bool `json:"setup_required"`
	// OIDCProviders is what the login page renders sign-in buttons from, in
	// configuration order. It names providers, never anything about who has
	// used them.
	OIDCProviders []OIDCProvider `json:"oidc_providers,omitempty"`
}

// OIDCProvider is one configured identity provider as the login page sees it:
// the key its start endpoint takes and the label its button carries. Issuer,
// client id and secret are deliberately absent — nothing here is a credential
// or an internal endpoint.
type OIDCProvider struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// IdentityItem is one credential bound to the authenticated account, for the
// settings page's identity list.
//
// The upstream subject is NEVER serialized: it is the credential half of the
// (provider, subject) pair, and the page only has to say which providers are
// bound, not what they call the user.
//
// CanUnlink is the server's verdict, not the page's: an account must keep at
// least one way to sign in, so the last remaining credential reports false and
// the endpoint refuses it too.
type IdentityItem struct {
	Provider    string    `json:"provider"`
	DisplayName string    `json:"display_name"`
	LinkedAt    time.Time `json:"linked_at"`
	CanUnlink   bool      `json:"can_unlink"`
}

// IdentitiesResponse is GET /api/auth/identities: what the authenticated
// account can sign in with, and which providers are still available to link.
type IdentitiesResponse struct {
	Identities []IdentityItem `json:"identities"`
	// HasPassword says whether a local password is set, which the settings
	// page needs to explain why unlinking the last provider is refused.
	HasPassword bool `json:"has_password"`
	// Available lists configured providers not yet bound to this account.
	Available []OIDCProvider `json:"available,omitempty"`
}

// AccountResponse is the authenticated account, returned by register and login.
// Email is omitted when the account has none. Nothing credential-shaped is ever
// serialized here — no password hash, no linked-identity subjects.
type AccountResponse struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	IsAdmin     bool   `json:"is_admin"`
}

// MeResponse is GET /api/me: who the caller is, for the app shell's header and
// its admin-only entry points. AvatarURL carries the session's snapshot and
// stays only for compatibility with callers that still read it — the frontend
// builds the same-origin proxy path from AuthzID instead and never embeds this
// value. It is never filled from user.avatar_source_url: that column is an
// address the server fetches from, and under the Gravatar fallback it encodes
// a hash of the account's email.
type MeResponse struct {
	AuthzID     string `json:"authz_id"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
	IsAdmin     bool   `json:"is_admin"`
}

// AdminUserItem is one row of GET /api/admin/users. It is a whitelist like
// every other DTO here: no password hash, no avatar source URL (which encodes
// a hash of the account's email under the Gravatar fallback), and nothing
// about the pages the user owns — an admin manages accounts here, not the
// content behind other people's share links.
//
// LastLoginAt/LastActiveAt carry the model.Never sentinel for an account that
// has never done either, and map to JSON null.
type AdminUserItem struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email,omitempty"`
	DisplayName  string     `json:"display_name"`
	IsAdmin      bool       `json:"is_admin"`
	Disabled     bool       `json:"disabled"`
	HasPassword  bool       `json:"has_password"`
	CreateTime   time.Time  `json:"create_time"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	LastActiveAt *time.Time `json:"last_active_at"`
}

// AdminUsersResponse is the GET /api/admin/users body with offset pagination,
// shaped like FileListResponse so both tables page the same way.
type AdminUsersResponse struct {
	Users    []AdminUserItem `json:"users"`
	Total    int64           `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	// Self is the calling admin's own id, which the page uses to grey out the
	// controls the server refuses on it (an admin may not disable or demote
	// themselves). Omitted where there is no caller — the `admin user list`
	// subcommand runs as the operator on the host, not as an account.
	Self string `json:"self,omitempty"`
}

// AdminSettingsResponse is the instance settings an admin edits at runtime.
// Infrastructure configuration (database, storage, OIDC credentials) stays in
// the config file and is deliberately absent.
type AdminSettingsResponse struct {
	RegistrationOpen  bool  `json:"registration_open"`
	OIDCAutoProvision bool  `json:"oidc_auto_provision"`
	UploadMaxFileSize int64 `json:"upload_max_file_size"`
}

// AdminUserItemFrom maps a user row to its admin-list shape. Both the HTTP
// endpoint and the server's `admin user list` subcommand build their output
// through this, so the two can never report different fields for the same
// account.
func AdminUserItemFrom(u model.User) AdminUserItem {
	item := AdminUserItem{
		ID:           u.ID,
		Username:     u.Username,
		DisplayName:  u.DisplayName,
		IsAdmin:      u.IsAdmin,
		Disabled:     u.Disabled,
		HasPassword:  u.PasswordHash != "",
		CreateTime:   u.CreateTime,
		LastLoginAt:  NullableLastUsed(u.LastLoginAt),
		LastActiveAt: NullableLastUsed(u.LastActiveAt),
	}
	if u.Email != nil {
		item.Email = *u.Email
	}
	return item
}
