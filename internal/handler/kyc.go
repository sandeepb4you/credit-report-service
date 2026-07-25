package handler

import (
	"strconv"
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

// VerifyPAN godoc
//
// @Summary      Verify an account's PAN (admin only)
// @Description  Marks the named account's KYC row as PAN-verified. Required before that account can request credit analytics. The caller must be an admin.
// @Tags         kyc
// @Produce      json
// @Security     BearerAuth
// @Param        accountId  path      int  true  "Account id whose PAN is being verified"
// @Success      200        {object}  models.KYCRecord
// @Failure      400        {object}  apperr.ErrorBody  "accountId must be an integer"
// @Failure      401        {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403        {object}  apperr.ErrorBody  "Not an admin"
// @Failure      404        {object}  apperr.ErrorBody  "No PAN on file for this account"
// @Router       /admin/kyc/pan/{accountId}/verify [post]
func (h *KycHandler) VerifyPAN(c *fiber.Ctx) error {
	// RequireRole(admin) on the route guarantees the caller is an admin; the
	// account being verified comes from the path.
	if _, ok := middleware.AccountRole(c); !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	accountID, err := strconv.ParseInt(c.Params("accountId"), 10, 64)
	if err != nil {
		return apperr.NewValidation("accountId must be an integer")
	}
	rec, err := h.svc.VerifyPAN(c.Context(), accountID)
	if err != nil {
		return err
	}
	return c.JSON(rec)
}
