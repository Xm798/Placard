// Package userctx carries the authenticated identity through the request
// lifecycle.
//
// The runtime authorization subject is the immutable authz id only. AuthzID()
// is the ONLY accessor permitted as an ownership/authz/audit key, and this
// package provides no accessor returning a mutable key (a username, an email).
// All owner-only WHERE clauses, ownership writes
// (create_user/update_user/user_id/viewer) and audit actors MUST source from
// AuthzID(), eliminating misuse at the root. AuthChannel() is a separate,
// non-authz accessor — read-only channel-routing information (session vs. PAT
// vs. dev_mock), never a key into anything.
//
// Display names live on Identity.DisplayName for UI/snapshot use only and must
// NEVER enter an ownership/authz/audit key.
package userctx

import "github.com/gofiber/fiber/v2"

// localsKey is the fiber.Locals key under which the Identity is stored.
const localsKey = "userctx.identity"

// Auth channel values for Identity.AuthChannel / AuthChannel(c) — see
// AuthChannel's doc for the read-only, non-authz nature of this field.
const (
	ChannelSession = "session"
	ChannelDevMock = "dev_mock"
	ChannelPAT     = "pat"
)

// Identity is the authenticated principal extracted by the auth middleware.
//
//	AuthzID      — the immutable authz id; the ONLY ownership/authz/audit key.
//	DisplayName  — display/snapshot only, never authz.
type Identity struct {
	AuthzID     string
	DisplayName string
	AvatarURL   string
	AuthChannel string
}

// Set stores the identity on the request context.
func Set(c *fiber.Ctx, id Identity) {
	c.Locals(localsKey, id)
}

// Get returns the stored identity and whether one was present.
func Get(c *fiber.Ctx) (Identity, bool) {
	v := c.Locals(localsKey)
	id, ok := v.(Identity)
	return id, ok
}

// AuthzID returns the immutable authz id for the request, or "" if absent.
// This is the only key permitted in ownership/authz/audit contexts.
func AuthzID(c *fiber.Ctx) string {
	id, ok := Get(c)
	if !ok {
		return ""
	}
	return id.AuthzID
}

// AuthChannel returns the authentication channel for the request (session,
// dev_mock, pat), or "" if no identity was present. It is a read-only accessor
// used for channel-based routing decisions (e.g. browser-only endpoints), not an
// authorization key.
func AuthChannel(c *fiber.Ctx) string {
	id, ok := Get(c)
	if !ok {
		return ""
	}
	return id.AuthChannel
}
