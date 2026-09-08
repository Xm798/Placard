// Package model holds the GORM persistence models for Placard.
//
// ⚠ These are persistence models only. Never return them directly via c.JSON —
// every outbound response MUST go through an explicit DTO whitelist. Sensitive
// and internal fields (authz id keys, object_key, ip, user_agent, details,
// sentinel timestamps) are tagged json:"-" so a stray default serialization
// cannot leak uploader identity or internal state.
//
// Identity fields (CreateUser/UpdateUser/UserID/Viewer/Actor) store User.ID —
// never a username. Usernames can be recycled, and storing one would let
// owner-only queries hit another user's files after reuse (cross-user
// takeover).
//
// Index names share one namespace per database in SQLite rather than one per
// table, so every name below must be unique across the whole schema — two
// tables indexing a same-named column collide at migration time unless one of
// them qualifies its index (see idx_file_is_deleted / idx_token_is_deleted).
package model

import "time"

// Timestamp normalizes an instant to what every time column in this package
// stores: UTC, whole seconds.
//
// SQLite has no date type — it keeps a time.Time as the text the driver formats
// it into and compares it byte by byte. Two rows written in different zones, or
// at different sub-second precisions, then order by their spelling rather than
// by their instant ("…:04.5Z" sorts before "…:04Z"). One zone and one width is
// what makes text comparison agree with chronology, and it is also what the
// 9999 / 1970 sentinels are written as.
//
// Every timestamp that is not left to GORM's autoCreateTime/autoUpdateTime —
// those go through gorm.Config.NowFunc, which applies the same rule — passes
// through here, on the way into a column and into a WHERE bound against one.
//
// The one exception is the DDL defaults below ('9999-12-31 23:59:59',
// '1970-01-01 00:00:00'): the database writes those in its own spelling, which
// is not the driver's. Their leading year still orders them correctly against
// every real value, which is all the sentinels are compared for — but a future
// range or ORDER BY over a column that can hold a DDL-written default needs
// checking rather than assuming.
func Timestamp(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// Now is Timestamp(time.Now()), the value GORM's NowFunc hands the auto
// timestamp columns.
func Now() time.Time { return Timestamp(time.Now()) }

// File is an uploaded HTML file. Except NanoID/Title every field is json:"-".
//
// ObjectKey/SizeBytes/Title/Description are the transactional serving cache:
// copies of the currently-served file_version row (shared_version, or latest_version when
// shared_version=0). Every version-pointer mutation (publish/restore/pin)
// refreshes them in the SAME transaction — the public render path reads only
// this cache and never joins file_version.
type File struct {
	ID            uint   `gorm:"primaryKey;autoIncrement" json:"-"`
	NanoID        string `gorm:"column:nano_id;size:8;not null;uniqueIndex:uk_nano_id" json:"id"`
	Title         string `gorm:"size:255;not null;default:''" json:"title"`
	Description   string `gorm:"size:255;not null;default:''" json:"-"` // <meta name=description> snapshot; feeds og:description on the share shell
	ObjectKey     string `gorm:"column:object_key;size:255;not null" json:"-"`
	SizeBytes     int64  `gorm:"not null;default:0" json:"-"`
	LatestVersion int    `gorm:"column:latest_version;not null;default:1" json:"-"` // head version pointer, CAS-advanced (repo.AdvanceHead)
	SharedVersion int    `gorm:"column:shared_version;not null;default:0" json:"-"` // pinned serving version; 0 = follow latest
	Visibility    string `gorm:"size:16;not null;default:'link'" json:"-"`          // private/link; see ValidVisibility
	ViewCount     int64  `gorm:"not null;default:0" json:"-"`                       // denormalized cache, recomputed by periodic job; non-authoritative, owner-DTO only
	// ShareCodeEnc holds the 6-digit share code encrypted under
	// server.secret_key (internal/sharecode), empty when the page has none.
	// Encrypted rather than hashed because the owner's dialog has to show the
	// code back; a rotated key makes it undecryptable, which every reader
	// treats as "no code".
	ShareCodeEnc string `gorm:"column:share_code_enc;size:128;not null;default:''" json:"-"`
	// ShareCodeVersion increments on every generate and every clear. It is
	// bound into each unlock ticket, so one bump expires every ticket issued
	// under the previous code.
	ShareCodeVersion int       `gorm:"column:share_code_version;not null;default:0" json:"-"`
	ExpiresAt        time.Time `gorm:"not null;index:idx_expires_at;default:'9999-12-31 23:59:59'" json:"-"` // 9999 sentinel = never expires; DTO maps to null
	CreateTime       time.Time `gorm:"not null;autoCreateTime" json:"-"`
	CreateUser       string    `gorm:"size:64;not null;index:idx_create_user" json:"-"` // immutable authz key
	UpdateTime       time.Time `gorm:"not null;autoUpdateTime" json:"-"`
	UpdateUser       string    `gorm:"size:64;not null;default:''" json:"-"`                  // = authz id
	IsDeleted        int8      `gorm:"not null;default:0;index:idx_file_is_deleted" json:"-"` // 0=normal 1=deleted
}

func (File) TableName() string { return "file" }

// FileVersion is one immutable published version of a page. Rows are
// insert-only: restore copies an old row's content into a new head version;
// rows are never updated and only reclaimed together with the whole page.
type FileVersion struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"-"`
	NanoID      string    `gorm:"column:nano_id;size:8;not null;default:'';uniqueIndex:uk_nano_version,priority:1" json:"-"`
	Version     int       `gorm:"column:version;not null;default:0;uniqueIndex:uk_nano_version,priority:2" json:"version"`
	ObjectKey   string    `gorm:"column:object_key;size:255;not null;default:''" json:"-"`
	SizeBytes   int64     `gorm:"not null;default:0" json:"size_bytes"`
	ContentHash string    `gorm:"column:content_hash;size:64;not null;default:''" json:"-"` // SHA-256 hex; empty string on backfilled rows (hash unknown)
	Title       string    `gorm:"size:255;not null;default:''" json:"title"`                // <title> snapshot at publish time
	Description string    `gorm:"size:255;not null;default:''" json:"-"`                    // <meta name=description> snapshot at publish time
	CreateTime  time.Time `gorm:"not null;autoCreateTime" json:"create_time"`
	CreateUser  string    `gorm:"size:64;not null;default:''" json:"-"` // = authz id
}

func (FileVersion) TableName() string { return "file_version" }

// Visibility values for File.Visibility and User.DefaultVisibility. authz.View
// treats any other value as unknown and default-denies (see internal/authz/authz.go).
const (
	VisibilityPrivate = "private"
	VisibilityLink    = "link"
)

// ValidVisibility reports whether v is one of the defined visibility values.
func ValidVisibility(v string) bool {
	return v == VisibilityPrivate || v == VisibilityLink
}

// Never is the DATETIME "never happened" sentinel used across User's *_at
// columns (first/last login, last active) — mirrors the existing 9999/1970
// sentinel pattern (File.ExpiresAt's 9999-12-31; Token.LastUsedAt's own
// never-used sentinel is 1970-01-02, one day later, to stay distinguishable
// from this value).
var Never = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)

// IsNever reports whether t is the Never sentinel.
func IsNever(t time.Time) bool { return t.Equal(Never) }

// User is an account on this instance, keyed by an immutable nanoid. That id —
// never the username — is what every ownership/authz/audit column across the
// schema stores (File.CreateUser, Token.UserID, View.Viewer, AuditLog.Actor),
// because a username can be renamed or recycled and an owner-only query keyed
// on one would reach another user's files after reuse.
//
// PasswordHash is empty for an account that has no local password (an
// OIDC-only user); such a user authenticates through a UserIdentity row alone.
// Email is a pointer so "no email" is SQL NULL: the unique index tolerates any
// number of NULLs but only one of each concrete address.
type User struct {
	ID                string    `gorm:"column:id;size:32;primaryKey" json:"-"`
	Username          string    `gorm:"size:64;not null;uniqueIndex:uk_user_username" json:"-"`
	Email             *string   `gorm:"size:255;uniqueIndex:uk_user_email" json:"-"` // NULL when unset; audit/linking only, never authz
	DisplayName       string    `gorm:"column:display_name;size:128;not null;default:''" json:"-"`
	PasswordHash      string    `gorm:"column:password_hash;size:255;not null;default:''" json:"-"` // argon2id PHC string; empty = no local password
	IsAdmin           bool      `gorm:"column:is_admin;not null;default:false" json:"-"`
	Disabled          bool      `gorm:"not null;default:false" json:"-"`                                // refused at login and on every existing session/PAT
	AvatarKey         string    `gorm:"column:avatar_key;size:128;not null;default:''" json:"-"`        // object-store cache key; empty = not cached
	AvatarSourceURL   string    `gorm:"column:avatar_source_url;size:512;not null;default:''" json:"-"` // upstream source URL; change-detection only, never exposed to frontend
	DefaultVisibility string    `gorm:"column:default_visibility;size:16;not null;default:'link'" json:"-"`
	FirstLoginAt      time.Time `gorm:"column:first_login_at;not null;default:'1970-01-01 00:00:00'" json:"-"`
	LastLoginAt       time.Time `gorm:"column:last_login_at;not null;default:'1970-01-01 00:00:00'" json:"-"`
	LastActiveAt      time.Time `gorm:"column:last_active_at;not null;default:'1970-01-01 00:00:00'" json:"-"` // 5min-throttled touch
	CreateTime        time.Time `gorm:"not null;autoCreateTime" json:"-"`
	UpdateTime        time.Time `gorm:"not null;autoUpdateTime" json:"-"`
}

func (User) TableName() string { return "user" }

// UserIDLen is the length of the nanoid User.ID is generated with.
const UserIDLen = 16

// ProviderLocal is the UserIdentity.Provider value for a username+password
// account. Every other value is an OIDC provider's configured name, so the
// provider column alone says which credential a row belongs to.
const ProviderLocal = "local"

// UserIdentity is one credential a user can log in with: the local password
// account, or a subject at an OIDC provider. (Provider, Subject) is unique, so
// one upstream identity can never be claimed by two accounts.
//
// The index name is qualified (idx_identity_user_id) because index names share
// one namespace per database in SQLite — see the package doc.
type UserIdentity struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"-"`
	Provider   string    `gorm:"size:64;not null;uniqueIndex:uk_identity_provider_subject,priority:1" json:"-"`
	Subject    string    `gorm:"size:255;not null;uniqueIndex:uk_identity_provider_subject,priority:2" json:"-"`
	UserID     string    `gorm:"column:user_id;size:32;not null;index:idx_identity_user_id" json:"-"`
	CreateTime time.Time `gorm:"not null;autoCreateTime" json:"-"`
}

func (UserIdentity) TableName() string { return "user_identity" }

// Setting is one runtime-editable instance setting. The config file seeds these
// on a first start and the table is authoritative from then on, so an admin can
// change them without a restart — infrastructure configuration (database,
// storage, OIDC credentials) deliberately stays in the config file instead.
type Setting struct {
	Key       string    `gorm:"column:key;size:64;primaryKey" json:"-"`
	Value     string    `gorm:"size:1024;not null;default:''" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null" json:"-"`
}

func (Setting) TableName() string { return "setting" }

// Setting keys. Values are stored as text and parsed by the typed accessors on
// repo.SettingRepo.
const (
	SettingRegistrationOpen  = "auth.registration_open"
	SettingOIDCAutoProvision = "auth.oidc_auto_provision"
	SettingUploadMaxFileSize = "upload.max_file_size"
	// SettingBootstrapAt records when the instance's first account was created.
	// It is not seeded from config and not editable: registration writes it in
	// the same transaction as that account, and its primary key is what makes
	// "first" exclusive — see handler.createAccount.
	SettingBootstrapAt = "auth.bootstrapped_at"
)

// Token is an API access token. Stored as HMAC-SHA256(server.secret_key, token); never the plaintext.
type Token struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"-"`
	TokenHash  string    `gorm:"size:64;not null;uniqueIndex:uk_token_hash" json:"-"`        // HMAC-SHA256(server.secret_key, token)
	UserID     string    `gorm:"column:user_id;size:64;not null;index:idx_user_id" json:"-"` // = authz id
	Name       string    `gorm:"size:128;not null;default:''" json:"-"`
	ExpiresAt  time.Time `gorm:"not null;default:'9999-12-31 23:59:59'" json:"-"`        // 9999 sentinel; server enforces max TTL (<=365d); DTO maps null
	LastUsedAt time.Time `gorm:"not null;default:'1970-01-02 00:00:00'" json:"-"`        // 1970 sentinel = never used; DTO maps null; throttle updates (>5min) or async
	Revoked    int8      `gorm:"not null;default:0;index:idx_revoked" json:"-"`          // 0=valid 1=revoked
	IsDeleted  int8      `gorm:"not null;default:0;index:idx_token_is_deleted" json:"-"` // 0=normal 1=deleted
	CreateTime time.Time `gorm:"not null;autoCreateTime" json:"-"`
	CreateUser string    `gorm:"size:64;not null" json:"-"` // = authz id
	UpdateTime time.Time `gorm:"not null;autoUpdateTime" json:"-"`
	UpdateUser string    `gorm:"size:64;not null;default:''" json:"-"`
}

func (Token) TableName() string { return "token" }

// View is the access record (UPSERT, one row per viewer per file) — the single
// source of truth for view counts. Non-whitelist fields are json:"-"; /views
// DTO emits only viewer_name_snapshot, never Viewer (the authz id).
//
// The composite uniqueIndex uk_file_viewer (file_nano_id, viewer) is what the
// Upsert ON CONFLICT clause matches. Without it AutoMigrate would build a view
// table with no unique constraint, so every view would insert a new row instead
// of incrementing view_count.
type View struct {
	ID                 uint      `gorm:"primaryKey;autoIncrement" json:"-"`
	FileNanoID         string    `gorm:"column:file_nano_id;size:8;not null;index:idx_file_nano_id;uniqueIndex:uk_file_viewer,priority:1" json:"-"`
	Viewer             string    `gorm:"size:64;not null;uniqueIndex:uk_file_viewer,priority:2" json:"-"` // immutable authz key, never leaked, DTO emits snapshot
	ViewerNameSnapshot string    `gorm:"size:128;not null;default:''" json:"-"`                           // display-name snapshot at write time; display-only, may be stale
	ViewCount          int64     `gorm:"not null;default:1" json:"view_count"`                            // per-viewer-per-file count; via owner /views DTO
	LastViewedAt       time.Time `gorm:"not null;default:CURRENT_TIMESTAMP" json:"last_viewed_at"`
	CreateTime         time.Time `gorm:"not null;autoCreateTime" json:"-"`
	CreateUser         string    `gorm:"size:64;not null" json:"-"` // = authz id
	UpdateTime         time.Time `gorm:"not null;autoUpdateTime" json:"-"`
	UpdateUser         string    `gorm:"size:64;not null;default:''" json:"-"` // = authz id
}

func (View) TableName() string { return "view" }

// AuditLog is the append-only audit trail. Authentication failures may be
// anonymous, so Actor/CreateUser are empty until an identity is resolved.
// Sensitive/internal fields are never serialized directly.
type AuditLog struct {
	ID           uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Action       string    `gorm:"size:32;not null;index:idx_action;index:idx_audit_action_time,priority:1" json:"action"`
	Outcome      string    `gorm:"size:16;not null;default:'success'" json:"-"`
	ReasonCode   string    `gorm:"size:64;not null;default:''" json:"-"`
	AuthChannel  string    `gorm:"size:16;not null;default:''" json:"-"`
	Actor        string    `gorm:"size:64;not null;default:'';index:idx_actor;index:idx_audit_actor_time,priority:1" json:"-"` // = authz id when known; never leaked
	ActorName    string    `gorm:"size:128;not null;default:''" json:"-"`                                                      // display-name snapshot; display-only, may be stale
	FileNanoID   string    `gorm:"column:file_nano_id;size:8;not null;default:'';index:idx_audit_file_nano_id" json:"-"`
	ResourceType string    `gorm:"size:16;not null;default:'';index:idx_audit_resource,priority:1" json:"-"`
	ResourceID   string    `gorm:"size:64;not null;default:'';index:idx_audit_resource,priority:2" json:"-"`
	IP           string    `gorm:"column:ip;size:45;not null;default:''" json:"-"` // only the trusted-proxy hop; internal audit field
	UserAgent    string    `gorm:"size:255;not null;default:''" json:"-"`          // untrusted, truncate/strip control chars
	RequestID    string    `gorm:"size:64;not null;default:'';index:idx_audit_request_id" json:"-"`
	Details      string    `gorm:"size:2048;not null;default:''" json:"-"` // validated JSON; extracted selectively via DTO, never emitted whole
	CreateTime   time.Time `gorm:"not null;autoCreateTime;index:idx_audit_actor_time,priority:2;index:idx_audit_action_time,priority:2;index:idx_audit_resource,priority:3" json:"-"`
	CreateUser   string    `gorm:"size:64;not null;default:''" json:"-"` // = actor when known
}

func (AuditLog) TableName() string { return "audit_log" }

// PendingObjectDelete is the deferred object reclamation queue — orphans from PUT
// replacement and publish/update rollback, reclaimed idempotently by cron.
type PendingObjectDelete struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"-"`
	ObjectKey  string    `gorm:"column:object_key;size:255;not null;uniqueIndex:uk_object_key" json:"-"`
	Reason     string    `gorm:"size:32;not null;default:''" json:"-"` // put_replace / publish_rollback / nanoid_conflict / user_delete / version_conflict / expired
	RetryCount int8      `gorm:"not null;default:0" json:"-"`
	CreateTime time.Time `gorm:"not null;autoCreateTime;index:idx_create_time" json:"-"`
	CreateUser string    `gorm:"size:64;not null;default:''" json:"-"` // = authz id
	UpdateTime time.Time `gorm:"not null;autoUpdateTime" json:"-"`
	UpdateUser string    `gorm:"size:64;not null;default:''" json:"-"`
}

func (PendingObjectDelete) TableName() string { return "pending_object_delete" }

// Reasons enqueued onto the PendingObjectDelete queue. Shared as constants so the
// producer (handler enqueue) and consumer (repo retention filter) cannot drift.
const (
	ReasonPutReplace      = "put_replace"
	ReasonPublishRollback = "publish_rollback"
	ReasonNanoidConflict  = "nanoid_conflict"
	ReasonUserDelete      = "user_delete"
	ReasonVersionConflict = "version_conflict" // lost publish race: orphaned candidate version object
	ReasonExpired         = "expired"          // expiry reclamation, enqueued inside the soft-delete tx
)

// Session is one login session, for deployments that run without Redis. The
// two-tier expiry the session package enforces is split across two columns:
// ExpiresAt is the sliding idle deadline (pushed out on read), CreatedAt is
// what the absolute cap is measured from.
//
// Rows outlive their expiry until the cleanup cron sweeps them; every read
// filters on ExpiresAt, so a stale row is never served.
type Session struct {
	ID          string `gorm:"column:id;size:64;primaryKey" json:"-"`
	AuthzID     string `gorm:"column:authz_id;size:64;not null;default:''" json:"-"`
	DisplayName string `gorm:"column:display_name;size:128;not null;default:''" json:"-"`
	AvatarURL   string `gorm:"column:avatar_url;size:512;not null;default:''" json:"-"`
	// OIDCFlow is the pending OpenID Connect authorization request as JSON
	// (session.OIDCFlow), empty when none is in flight. It lives on the
	// session row rather than in its own table because it is per-browser state
	// with exactly the session's lifetime — the callback that consumes it
	// clears it, and an abandoned flow goes when the session does.
	OIDCFlow string `gorm:"column:oidc_flow;size:1024;not null;default:''" json:"-"`
	// autoCreateTime is off on both time columns: the store is their only
	// writer, and the absolute cap is measured against exactly what it wrote.
	CreatedAt time.Time `gorm:"column:created_at;not null;autoCreateTime:false" json:"-"`
	ExpiresAt time.Time `gorm:"column:expires_at;not null;index:idx_session_expires_at" json:"-"`
}

func (Session) TableName() string { return "session" }

// DeviceCode is one in-flight CLI device-authorization flow, for deployments
// that run without Redis. It mirrors devicecode.Record and, like it, never
// holds a token plaintext: the PAT is minted at redemption, not at approval.
//
// NextPollAt is the poll-spacing gate (a too-soon poll gets slow_down) and
// ExpiresAt is the 180s window, absolute from mint time — approving or polling
// must never push it out.
type DeviceCode struct {
	DeviceCode string    `gorm:"column:device_code;size:64;primaryKey" json:"-"`
	UserCode   string    `gorm:"column:user_code;size:16;not null;uniqueIndex:uk_user_code" json:"-"`
	Status     string    `gorm:"size:16;not null;default:'pending'" json:"-"`
	AuthzID    string    `gorm:"column:authz_id;size:64;not null;default:''" json:"-"`
	NameHint   string    `gorm:"column:name_hint;size:64;not null;default:''" json:"-"`   // CLI-supplied hostname, untrusted
	CreatedIP  string    `gorm:"column:created_ip;size:64;not null;default:''" json:"-"`  // untrusted, corroboration only
	CreatedUA  string    `gorm:"column:created_ua;size:255;not null;default:''" json:"-"` // untrusted, truncated by the handler
	ApproverIP string    `gorm:"column:approver_ip;size:64;not null;default:''" json:"-"`
	TokenTTL   string    `gorm:"column:token_ttl;size:16;not null;default:''" json:"-"` // allowlist key (e.g. "7d"), never a duration
	ApprovedAt time.Time `gorm:"column:approved_at;not null;default:'1970-01-01 00:00:00'" json:"-"`
	NextPollAt time.Time `gorm:"column:next_poll_at;not null;default:'1970-01-01 00:00:00'" json:"-"`
	CreatedAt  time.Time `gorm:"column:created_at;not null;autoCreateTime:false" json:"-"`
	ExpiresAt  time.Time `gorm:"column:expires_at;not null;index:idx_device_code_expires_at" json:"-"`
}

func (DeviceCode) TableName() string { return "device_code" }

// DeviceChallenge is the synchronizer CSRF token rendered into the device
// confirmation page, for deployments that run without Redis.
//
// It is keyed by session id rather than by device_code because at render time
// the user has not typed a user_code yet — see devicecode.Store.PutChallenge.
type DeviceChallenge struct {
	SessionID string    `gorm:"column:session_id;size:64;primaryKey" json:"-"`
	Token     string    `gorm:"column:token;size:64;not null;default:''" json:"-"`
	ExpiresAt time.Time `gorm:"column:expires_at;not null;index:idx_device_challenge_expires_at" json:"-"`
}

func (DeviceChallenge) TableName() string { return "device_challenge" }
