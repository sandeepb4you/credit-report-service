package handler

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	_ "credit-report-service/internal/models" // referenced by swag annotations (models.KYCRecord)
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// KycHandler exposes the PAN submission endpoint.
type KycHandler struct {
	svc *service.KycService
}

func NewKycHandler(svc *service.KycService) *KycHandler {
	return &KycHandler{svc: svc}
}

type submitPanReq struct {
	PAN string `json:"pan" example:"ABCDE1234F"`
}

// SubmitPAN godoc
//
// @Summary      Submit the authenticated account's PAN
// @Description  Accepts a PAN (Permanent Account Number), validates the format, and upserts it against the account's KYC record. A re-submission overwrites any existing PAN and resets verification to PENDING.
// @Tags         kyc
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      submitPanReq  true  "PAN to store"
// @Success      201      {object}  models.KYCRecord
// @Failure      400      {object}  apperr.ErrorBody  "Invalid JSON body / PAN format"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      409      {object}  apperr.ErrorBody  "PAN already linked to another account"
// @Router       /kyc/pan [post]
func (h *KycHandler) SubmitPAN(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}

	var req submitPanReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	if strings.TrimSpace(req.PAN) == "" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"pan": "pan is required"})
	}

	rec, err := h.svc.SubmitPAN(c.Context(), accountID, req.PAN)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(rec)
}
