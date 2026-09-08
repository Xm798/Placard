package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/version"
)

// cliUserAgentPrefix is what the CLI sends on every request
// (`User-Agent: placard-cli/<version>`). It is the only thing that identifies
// the caller as a CLI, so the two sides have to agree on it.
const cliUserAgentPrefix = "placard-cli/"

// MinCLIVersion answers a `placard` CLI older than min with 426, so a
// self-hoster can force clients to upgrade after a breaking API change. An
// empty min disables the gate.
//
// It admits every caller whose version is not a published release — browsers
// and curl, which send no CLI User-Agent at all, and developers running a
// local build, whose version string is not comparable with a release number.
// A version claim in a User-Agent is not a credential; the point here is to
// give an outdated client an actionable message, not to keep anyone out.
func MinCLIVersion(min string) fiber.Handler {
	enabled := version.IsRelease(min)
	return func(c *fiber.Ctx) error {
		if !enabled {
			return c.Next()
		}
		got, ok := strings.CutPrefix(c.Get(fiber.HeaderUserAgent), cliUserAgentPrefix)
		if !ok || !version.IsRelease(got) || version.Compare(got, min) >= 0 {
			return c.Next()
		}
		return apperr.UpgradeRequired("this Placard server requires placard CLI " + min +
			" or newer, and you are running " + got + " — upgrade with: placard update")
	}
}
