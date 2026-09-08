package middleware

import (
	"net/url"
	"strings"
	"time"

	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/userctx"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// AccessLog logs one structured line per request: method/path/status/
// duration_ms/client_ip/request_id/entrypoint/ua/query. Both success and
// failure are recorded (level gates volume): <400 → Info, >=400 → Warn.
//
// It deliberately does NOT log the request/response body (avoids PII / internal
// HTML / sensitive data reaching ELK; correlate via request_id instead) and
// never logs sensitive headers — Authorization/Bearer, cookies, and render_url
// are not emitted at all.
//
// The query string IS logged, with known-sensitive keys redacted by
// redactQuery. That is not belt-and-braces: Placard does not control every
// inbound URL, and an SSO callback carrying a live authorization code in its
// query breaks the "callers must keep secrets out of query params" contract
// from the far side. Redaction keeps the key names visible — the shape of the
// request stays debuggable — and drops only the values.
//
// Must run after RequestID() so request_id and entrypoint are populated.
//
// Errors returned from downstream (apperr 401/429/502/503/400, etc.) are
// rendered by fiber.Config.ErrorHandler, which normally runs only AFTER the
// whole middleware chain has unwound — so reading c.Response().StatusCode()
// right after c.Next() would still see the default 200 and mislog every error
// as a success. To log the real status we mirror Fiber's own logger middleware:
// invoke the app ErrorHandler here so the response status is finalized before
// we read it, then return nil (the error is already handled; returning it would
// make Fiber invoke ErrorHandler a second time).
func AccessLog() fiber.Handler {
	return accessLog(logger.Module("access"))
}

// accessLog is the injectable core so tests can supply an observer logger.
func accessLog(log *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		chainErr := c.Next()
		if chainErr != nil {
			if err := c.App().ErrorHandler(c, chainErr); err != nil {
				_ = c.SendStatus(fiber.StatusInternalServerError)
			}
		}

		status := c.Response().StatusCode()
		identity, _ := userctx.Get(c)
		fields := []zap.Field{
			zap.String("method", c.Method()),
			zap.String("path", c.Path()),
			zap.Int("status", status),
			ctxlog.DurMS(time.Since(start)),
			zap.String("client_ip", ClientIP(c)),
			ctxlog.ReqID(c.UserContext()),
			ctxlog.Entry(c.UserContext()),
			zap.String("ua", c.Get(fiber.HeaderUserAgent)),
			zap.String("query", redactQuery(string(c.Request().URI().QueryString()))),
			zap.String("actor", identity.AuthzID),
			zap.String("auth_channel", identity.AuthChannel),
		}

		if status >= fiber.StatusBadRequest {
			log.Warn("access", fields...)
		} else {
			log.Info("access", fields...)
		}
		// Error already rendered above; returning it would double-invoke the
		// ErrorHandler and rewrite the body.
		return nil
	}
}

// redactedValue replaces a sensitive query value. A fixed marker rather than an
// empty string: otherwise "code=REDACTED" would be indistinguishable from a
// caller that genuinely sent no value.
const redactedValue = "REDACTED"

// sensitiveQueryKeys are the query parameter names whose VALUE never reaches a
// log line. Lowercased; the lookup lowercases the parsed key.
//
//	code, ticket   — OAuth / SSO authorization material (inbound callback)
//	token          — PATs and any bearer-ish value smuggled into a URL
//	access_key     — object-store / signed-URL credentials
//	authorization  — a header name, but seen as a query override in the wild
var sensitiveQueryKeys = map[string]bool{
	"access_key":    true,
	"authorization": true,
	"code":          true,
	"ticket":        true,
	"token":         true,
}

// redactQuery replaces the value of every sensitive key in a raw query string,
// leaving every other byte exactly as it arrived.
//
// It splits on the real delimiters and percent-decodes the KEY before matching,
// rather than running a regex over the raw string. A regex is the wrong tool
// twice over here: `token=[^&]*` also matches the tail of `access_token=`, and
// a key spelled `%74oken` slips past it silently. Splitting gets both right and
// — unlike parse-and-reencode through url.Values — preserves parameter order,
// repeated keys, and the original escaping of everything it does not touch, so
// the logged query stays a faithful picture of the request.
func redactQuery(raw string) string {
	if raw == "" || !strings.Contains(raw, "=") {
		return raw
	}
	pairs := strings.Split(raw, "&")
	for i, pair := range pairs {
		key, _, hasValue := strings.Cut(pair, "=")
		if !hasValue {
			continue
		}
		name, err := url.QueryUnescape(key)
		if err != nil {
			name = key // undecodable key: match on the raw form rather than skip
		}
		if sensitiveQueryKeys[strings.ToLower(name)] {
			pairs[i] = key + "=" + redactedValue
		}
	}
	return strings.Join(pairs, "&")
}
