package handler

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/logger"
)

// businessLogger resolves the logger carrying the state-change trail. A
// provider rather than a stored handle, for the same reason the GORM adapter
// uses one: a late logger.Init still takes effect, and tests can swap in an
// observer without threading a logger through Deps.
var businessLogger = func() *zap.Logger { return logger.Module("business") }

// logStateChange emits the one Info line that records a business state change:
// publish/republish/restore, delete, version pin, visibility change, grant
// add/remove, login/logout, token create/revoke.
//
// State changes ONLY. Read paths (list, meta, render, version list) are
// deliberately not logged here — AccessLog already records one line per
// request with status and duration, so a second line per read would double the
// hottest path's log volume for no new information. If a new call to this
// function lands on a per-request path, it does not belong there.
//
// operation reuses the audit table's action vocabulary verbatim ("file.publish",
// "grant.add", …) and outcome reuses the audit table's outcome vocabulary via
// the ctxlog.Outcome* enum, so a log line and its audit row can never describe
// the same event with two different names. The two mechanisms stay separate
// otherwise: this line is written whether or not the audit insert succeeds, and
// carries no actor, grantee or details — those live in the audit row.
//
// duration_ms is measured from the moment fasthttp handed the request to the
// handler chain. c.Context() is read for that timestamp and nothing else — the
// pooled *fasthttp.RequestCtx is never stored, and never handed to storage or any
// other callee as a context.Context (it is reset and reused after the response
// is written).
func logStateChange(c *fiber.Ctx, operation, nanoID, outcome string) {
	ctx := c.UserContext()
	businessLogger().Info("state change",
		ctxlog.ReqID(ctx),
		ctxlog.Entry(ctx),
		ctxlog.Op(operation),
		ctxlog.NanoID(nanoID),
		ctxlog.DurMS(time.Since(c.Context().Time())),
		ctxlog.Outcome(outcome),
	)
}
