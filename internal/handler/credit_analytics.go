package handler

import (
	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	_ "credit-report-service/internal/models" // referenced by swag annotations
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// CreditAnalyticsHandler exposes the Digitap credit-analytics proxy endpoint.
type CreditAnalyticsHandler struct {
	svc *service.CreditAnalyticsService
}

func NewCreditAnalyticsHandler(svc *service.CreditAnalyticsService) *CreditAnalyticsHandler {
	return &CreditAnalyticsHandler{svc: svc}
}

// Request godoc
//
// @Summary      Request a credit analysis from Digitap
// @Description  Proxies the request body to the Digitap /credit_analytics/request API (spec V2.7 §1.4.1), persists the request and full upstream response against the authenticated account, and returns the stored row.
// @Tags         credit-analytics
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      service.CreditAnalyticsInput  true  "Credit-analytics request payload (Digitap §1.4.1)"
// @Success      201      {object}  models.CreditAnalyticsRequest
// @Failure      400      {object}  apperr.ErrorBody  "Invalid request body / validation failure / upstream 400"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated / upstream 401"
// @Failure      422      {object}  apperr.ErrorBody  "Upstream tradeline limit exceeded"
// @Router       /credit-analytics/request [post]
func (h *CreditAnalyticsHandler) Request(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}

	var in service.CreditAnalyticsInput
	if err := c.BodyParser(&in); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}

	row, err := h.svc.Request(c.Context(), accountID, in)
	if err != nil {
		// On upstream-mapped errors the row has already been persisted; surface
		// the error envelope as usual, and attach the row id via X-Request-Id so
		// the caller can reconcile.
		if row != nil {
			c.Set("X-Credit-Analytics-Id", itoaHandler(row.ID))
		}
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(row)
}

// itoaHandler is a tiny local int64->string to avoid pulling strconv into a
// handler file that otherwise doesn't need it.
func itoaHandler(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
