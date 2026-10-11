package handler

import (
	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// BusinessLoanHandler serves the business-loan lender list.
type BusinessLoanHandler struct {
	svc *service.BusinessLoanService
}

func NewBusinessLoanHandler(svc *service.BusinessLoanService) *BusinessLoanHandler {
	return &BusinessLoanHandler{svc: svc}
}

// List godoc
//
// @Summary      Business-loan lenders, with the caller's match status
// @Description  The lender list behind Explore → Business loans. Shown to every account, paid or not: matchStatus is excellent, good or not_match when a scored report exists, and no_score otherwise. The rules behind the status are deliberately not part of the response.
// @Tags         loans
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  service.BusinessLoanList
// @Router       /loans/business [get]
func (h *BusinessLoanHandler) List(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	out, err := h.svc.List(c.Context(), accountID)
	if err != nil {
		return err
	}
	return c.JSON(out)
}
