// Package httpx is the single translation point between returned errors and
// Fiber HTTP responses. It wires internal/apperr (typed errors) and internal/dto
// (response envelopes) to Fiber, so the apperr→envelope mapping — including the
// transport-level body-too-large 413→400 remap — lives in exactly one place
// shared by main.newFiberApp and tests.
package httpx

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/apperr"
	"github.com/Xm798/placard/internal/dto"
)

// ErrorHandler translates a returned error into the unified dto.ErrorResponse
// envelope. It is assigned as fiber.Config.ErrorHandler.
//
//   - *apperr.Error → its HTTPStatus + envelope (Code/Message from the error).
//   - *fiber.Error → Fiber's own typed errors, notably the transport-level
//     body-too-large (Fiber maps fasthttp.ErrBodyTooLarge to 413). An oversize
//     upload is a client content error, so it is surfaced as 400 validation
//     (unified contract) instead of 413, without leaking the underlying message.
//     Other Fiber errors keep their status with the unified envelope.
//   - anything else (untyped errors, panics caught by recover.New) → a fixed
//     500 with no internal detail leaked.
//
// apperr.Error() (which includes Cause) is never serialized — it is for logs.
func ErrorHandler(c *fiber.Ctx, err error) error {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return c.Status(ae.HTTPStatus).JSON(dto.ErrorResponse{
			Code: ae.Code, Message: ae.Message, Error: ae.Message,
		})
	}
	var fe *fiber.Error
	if errors.As(err, &fe) {
		if fe.Code == fiber.StatusRequestEntityTooLarge {
			return c.Status(fiber.StatusBadRequest).JSON(dto.ErrorResponse{
				Code: "validation", Message: "file too large", Error: "file too large",
			})
		}
		return c.Status(fe.Code).JSON(dto.ErrorResponse{
			Code: "error", Message: fe.Message, Error: fe.Message,
		})
	}
	return c.Status(fiber.StatusInternalServerError).JSON(dto.ErrorResponse{
		Code: "internal", Message: "internal error", Error: "internal error",
	})
}
