package handler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/dto"
	"github.com/Xm798/placard/internal/idgen"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/middleware"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/userctx"
)

// tokenCreateRequest is the POST /api/tokens body.
type tokenCreateRequest struct {
	Name   string `json:"name"`
	Expiry string `json:"expiry"`
}

// AuditMeta carries everything the audit trail needs, explicitly.
//
// Deliberately NOT a *fiber.Ctx: issueToken must be callable from the
// unauthenticated device-code exchange, where the ctx carries no identity at
// all. Taking a ctx would quietly reintroduce the very coupling this type
// exists to remove.
type AuditMeta struct {
	// Channel is the audit_log.auth_channel value: the request's real channel
	// ("session"/"pat"/"dev_mock") for the web path, and "device" for the
	// device-code exchange.
	Channel   string
	ActorName string
	// ApproverIP is the browser IP that clicked Approve (device flow), or the
	// caller's IP on the web path.
	ApproverIP string
	// ExchangeIP is the CLI IP that redeemed the code — device flow only.
	// Both IPs land on ONE token.create row so "who approved" and "who
	// collected" can be compared afterwards; that comparison is the forensic
	// handle for the social-engineering attack this flow is exposed to.
	ExchangeIP string
	UserAgent  string
	RequestID  string
}

// issueToken mints a PAT for authzid without touching the Fiber context:
// generates the plaintext, stores HMAC-SHA256(pepper, token), inserts the row
// and writes the token.create audit entry. Both CreateToken (cookie/PAT
// channel) and the device-code exchange call it.
//
// expiresAt is absolute and is used as given — callers, not this function,
// decide the policy. The device flow passes now+180d directly and must never
// route through parseTokenExpiry: "", "never" and "permanent" all hit its
// cap branch and would silently stretch an unauthenticated endpoint's
// credential to MaxTTLDays (365 by default).
func (h *Handlers) issueToken(ctx context.Context, authzid, name string, expiresAt time.Time, meta AuditMeta) (string, *model.Token, error) {
	plaintext := patPrefix + idgen.Generate(patNanoIDLen)
	tok := &model.Token{
		TokenHash:  hashToken(h.deps.Cfg.Server.SecretKey, plaintext),
		UserID:     authzid,
		Name:       name,
		ExpiresAt:  expiresAt,
		CreateUser: authzid,
		UpdateUser: authzid,
	}
	if err := h.deps.Tokens.Insert(ctx, tok); err != nil {
		return "", nil, err
	}

	if h.deps.Audit != nil {
		details := ""
		if meta.ExchangeIP != "" {
			details = truncateString(fmt.Sprintf(
				`{"channel":%q,"approver_ip":%q,"exchange_ip":%q}`,
				meta.Channel, meta.ApproverIP, meta.ExchangeIP), 2048)
		}
		entry := &model.AuditLog{
			Action:       "token.create",
			Outcome:      "success",
			AuthChannel:  meta.Channel,
			Actor:        authzid,
			ActorName:    meta.ActorName,
			ResourceType: "token",
			ResourceID:   strconv.FormatUint(uint64(tok.ID), 10),
			IP:           meta.ApproverIP,
			UserAgent:    truncateUA(meta.UserAgent),
			RequestID:    truncateString(meta.RequestID, 64),
			Details:      details,
			CreateUser:   authzid,
		}
		if meta.ExchangeIP != "" {
			entry.IP = meta.ExchangeIP
		}
		if err := h.deps.Audit.Insert(ctx, entry); err != nil {
			logger.Module("audit").Error("insert failed",
				zap.String("action", entry.Action), zap.Error(err))
		}
	}
	return plaintext, tok, nil
}

// CreateToken handles POST /api/tokens. It generates a one-time plaintext PAT
// (pl_<nanoid32>), stores HMAC-SHA256(pepper, token), enforces max TTL (never /
// >365d truncated), and returns the plaintext exactly once. A thin shell over
// issueToken since the device-code exchange needed a ctx-free version.
func (h *Handlers) CreateToken(c *fiber.Ctx) error {
	authzid := userctx.AuthzID(c)
	if authzid == "" {
		return apperr.Unauthorized()
	}

	var req tokenCreateRequest
	_ = c.BodyParser(&req)

	expiresAt, err := parseTokenExpiry(req.Expiry, h.deps.Cfg.Token.MaxTTLDays)
	if err != nil {
		return apperr.Validation("invalid expiry")
	}

	plaintext, tok, err := h.issueToken(c.UserContext(), authzid, req.Name, expiresAt, AuditMeta{
		Channel:    authChannel(c),
		ActorName:  displayName(c),
		ApproverIP: clientIPOf(c),
		UserAgent:  c.Get(fiber.HeaderUserAgent),
		RequestID:  middleware.RequestIDFromCtx(c),
	})
	if err != nil {
		return apperr.Internal("could not create token")
	}

	logStateChange(c, "token.create", "", ctxlog.OutcomeSuccess)

	return c.JSON(dto.TokenCreateResponse{
		ID:        tok.ID,
		Token:     plaintext,
		Name:      tok.Name,
		ExpiresAt: dto.NullableExpiry(tok.ExpiresAt),
	})
}

// ListTokens handles GET /api/tokens. Returns owner-only token metadata; never
// the plaintext or hash. Sentinels map to null via the DTO.
func (h *Handlers) ListTokens(c *fiber.Ctx) error {
	authzid := userctx.AuthzID(c)
	if authzid == "" {
		return apperr.Unauthorized()
	}

	tokens, err := h.deps.Tokens.ListByUser(c.UserContext(), authzid)
	if err != nil {
		return apperr.Internal("could not list tokens")
	}

	out := make([]dto.TokenResponse, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, dto.TokenResponse{
			ID:         t.ID,
			Name:       t.Name,
			LastUsedAt: dto.NullableLastUsed(t.LastUsedAt),
			CreateTime: t.CreateTime,
			ExpiresAt:  dto.NullableExpiry(t.ExpiresAt),
			Revoked:    t.Revoked != 0,
		})
	}
	return c.JSON(fiber.Map{"tokens": out})
}

// RevokeToken handles DELETE /api/tokens/:id. Owner-only; sets revoked=1.
func (h *Handlers) RevokeToken(c *fiber.Ctx) error {
	authzid := userctx.AuthzID(c)
	if authzid == "" {
		return apperr.Unauthorized()
	}

	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.Validation("invalid token id")
	}

	if err := h.deps.Tokens.Revoke(c.UserContext(), uint(id), authzid); err != nil {
		// Owner-only miss → 404, never confirming existence.
		return apperr.NotFound("not found")
	}

	h.auditToken(c, authzid, "token.revoke", uint(id))
	logStateChange(c, "token.revoke", "", ctxlog.OutcomeSuccess)
	return c.SendStatus(fiber.StatusNoContent)
}

// DeleteRevokedToken handles DELETE /api/tokens/:id/permanent. It soft-deletes
// an owner-scoped token only after it has been revoked.
func (h *Handlers) DeleteRevokedToken(c *fiber.Ctx) error {
	authzid := userctx.AuthzID(c)
	if authzid == "" {
		return apperr.Unauthorized()
	}

	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.Validation("invalid token id")
	}

	if err := h.deps.Tokens.DeleteRevoked(c.UserContext(), uint(id), authzid); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Active, unknown, and non-owned tokens are intentionally indistinguishable.
			return apperr.NotFound("not found")
		}
		return apperr.Internal("could not delete token")
	}

	h.auditToken(c, authzid, "token.delete", uint(id))
	logStateChange(c, "token.delete", "", ctxlog.OutcomeSuccess)
	return c.SendStatus(fiber.StatusNoContent)
}

// auditToken records a token.* audit entry (best-effort).
func (h *Handlers) auditToken(c *fiber.Ctx, authzid, action string, tokenID uint) {
	h.auditBestEffort(c, &model.AuditLog{
		Action:       action,
		Actor:        authzid,
		ActorName:    displayName(c),
		ResourceType: "token",
		ResourceID:   strconv.FormatUint(uint64(tokenID), 10),
	})
}

const (
	// lastUsedThrottle bounds how often last_used_at is rewritten on token auth.
	lastUsedThrottle = 5 * time.Minute
	// lastUsedTouchTimeout bounds the detached async last_used_at write.
	lastUsedTouchTimeout = 5 * time.Second
)

// lastUsedToucher stamps token.last_used_at after a successful Bearer auth.
type lastUsedToucher struct {
	touch func(ctx context.Context, id uint, at time.Time) error
	log   *zap.Logger
}

// newLastUsedToucher builds a toucher around a TouchLastUsed-shaped write. The
// logger is resolved once here, at wiring time (after logger.Init in main).
func newLastUsedToucher(touch func(ctx context.Context, id uint, at time.Time) error) *lastUsedToucher {
	return &lastUsedToucher{touch: touch, log: logger.Module("token")}
}

// maybeTouch stamps last_used_at asynchronously, throttled by lastUsedThrottle
// judged on the value just read (the 1970 never-used sentinel is always stale
// — no extra query). The write runs detached from the request context so a
// slow or failed write can never affect the auth result; failures are only
// logged. Concurrent requests may occasionally double-touch — benign, so no
// locking.
func (u *lastUsedToucher) maybeTouch(t *model.Token) {
	if time.Since(t.LastUsedAt) <= lastUsedThrottle {
		return
	}
	id := t.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), lastUsedTouchTimeout)
		defer cancel()
		if err := u.touch(ctx, id, time.Now()); err != nil {
			u.log.Warn("touch last_used_at failed",
				zap.Uint("token_id", id), zap.Error(err))
		}
	}()
}

// TokenValidator returns a validator compatible with middleware.TokenValidator.
// It hashes the bearer token, looks it up, and validates fail-closed (unrevoked
// + unexpired). The resolved Identity.AuthzID is the immutable authz id
// (token.user_id). Supplied to the auth middleware for the Bearer channel.
func (h *Handlers) TokenValidator() middleware.TokenValidator {
	return func(token string) (userctx.Identity, bool) {
		hash := hashToken(h.deps.Cfg.Server.SecretKey, token)
		t, ok := h.deps.Tokens.GetValidByHash(context.Background(), hash)
		if !ok {
			return userctx.Identity{}, false
		}
		h.lastUsed.maybeTouch(t)
		return userctx.Identity{
			AuthzID:     t.UserID,
			AuthChannel: userctx.ChannelPAT,
		}, true
	}
}
