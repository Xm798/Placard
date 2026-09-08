package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/Xm798/placard/internal/model"
)

// gravatarBase is the avatar endpoint, without the hash. Kept as a constant so
// the one outbound request this file can produce is visible in one place.
const gravatarBase = "https://www.gravatar.com/avatar/"

// gravatarSize is the requested pixel width. It matches avatarMaxBytes' budget
// comfortably and is larger than any avatar the UI renders, so the cached
// object stays usable if a bigger element is added later.
const gravatarSize = "256"

// avatarSourceURL picks where a user's avatar should come from, in the order
// the product promises: the identity provider's picture, then Gravatar for a
// known address, then nothing — which is what makes the frontend render the
// initial letter.
//
// An empty result is a decision, not a failure: it means this account has no
// remote avatar to fetch, and nothing is requested for it.
func (h *Handlers) avatarSourceURL(user *model.User, picture string) string {
	if p := strings.TrimSpace(picture); p != "" {
		return p
	}
	if !h.gravatarEnabled() || user == nil || user.Email == nil {
		return ""
	}
	return gravatarURL(*user.Email)
}

// gravatarEnabled reports the avatar.gravatar_fallback switch. Absent config
// reads as off: the fallback discloses a hash of the user's email address to a
// third party, and a code path running without configuration must not be the
// thing that decides to do that.
func (h *Handlers) gravatarEnabled() bool {
	return h.deps.Cfg != nil && h.deps.Cfg.Avatar.GravatarFallback
}

// gravatarURL is the "the address has no Gravatar" form of the request: d=404
// makes the service answer 404 instead of serving a generated placeholder, so
// the fetch pipeline's non-200 rejection is what leaves the account with no
// cached avatar and the frontend with its initial letter.
//
// SHA-256 of the trimmed, lower-cased address is Gravatar's current hashing
// rule (MD5 is the legacy one it still accepts).
func gravatarURL(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return gravatarBase + hex.EncodeToString(sum[:]) + "?d=404&s=" + gravatarSize
}

// refreshAvatar kicks off the avatar cache for a login that just succeeded.
// cacheAvatarSync returns early when the source has not changed since the last
// successful fetch, so this costs one row read for a returning user.
func (h *Handlers) refreshAvatar(user *model.User, picture string) {
	h.cacheAvatarAsync(user.ID, h.avatarSourceURL(user, picture))
}
