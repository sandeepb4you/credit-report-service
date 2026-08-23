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
	// FullName as printed on the PAN card. Verified against the name the
	// provider holds for the account's mobile number, with a small edit-distance
	// tolerance — it does not have to match character for character.
	FullName string `json:"fullName" example:"JOHN DOE"`
}

// SubmitPAN godoc
//
// @Summary      Submit and verify the authenticated account's PAN
// @Description  Accepts a PAN and the full name printed on it, then verifies both against the mobile number registered on the account, using Digitap's Mobile to Prefill API. A match returns a VERIFIED record. A mismatch returns 422 and counts against a retry cap. If the provider holds no record for the number, the PAN is stored PENDING and 201 is returned — the user is not blocked for a gap in someone else's data. A re-submission overwrites any existing PAN and resets verification.
// @Tags         kyc
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      submitPanReq  true  "PAN and full name as printed on the card"
// @Success      201      {object}  models.KYCRecord
// @Failure      400      {object}  apperr.ErrorBody  "Invalid JSON body / PAN format"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      409      {object}  apperr.ErrorBody  "PAN already linked to another account"
// @Failure      422      {object}  apperr.ErrorBody  "PAN or name does not match the mobile number, or retries exhausted"
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
	missing := map[string]string{}
	if strings.TrimSpace(req.PAN) == "" {
		missing["pan"] = "pan is required"
	}
	if strings.TrimSpace(req.FullName) == "" {
		missing["fullName"] = "fullName is required"
	}
	if len(missing) > 0 {
		return apperr.NewValidationWith("Validation failed", missing)
	}

	rec, err := h.svc.SubmitPAN(c.Context(), accountID, req.PAN, req.FullName)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(rec)
}

// GetStatus godoc
//
// @Summary      Get the authenticated account's KYC status
// @Description  Returns the account's KYC state without exposing the full PAN (only the last 4 digits). An account that has never submitted a PAN gets 200 with status NOT_SUBMITTED — "no record" is a state, not an error — so a client can render the onboarding step directly from `status`. Possible values: NOT_SUBMITTED, PENDING, VERIFIED, REJECTED.
// @Tags         kyc
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  models.KYCStatus
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /kyc/status [get]
func (h *KycHandler) GetStatus(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	st, err := h.svc.Status(c.Context(), accountID)
	if err != nil {
		return err
	}
	return c.JSON(st)
}

// ListPending godoc
//
// @Summary      List pending KYC requests (admin only)
// @Description  Returns accounts that submitted a PAN and are awaiting verification, ordered newest activity first (a re-submitted PAN sorts back to the top). The rows carry the full PAN so a reviewer can check it, which is why the endpoint needs the 'kyc:verify' permission. Paged: `limit` defaults to 50 and is capped at 200, `offset` skips rows; `total` is the size of the whole queue, not of the page.
// @Tags         kyc
// @Produce      json
// @Security     BearerAuth
// @Param        limit   query     int  false  "Max rows to return (default 50, max 200)"
// @Param        offset  query     int  false  "Rows to skip (default 0)"
// @Success      200     {object}  models.KYCReviewPage
// @Failure      400     {object}  apperr.ErrorBody  "limit/offset must be integers"
// @Failure      401     {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403     {object}  apperr.ErrorBody  "Missing the 'kyc:verify' permission"
// @Router       /admin/kyc/pending [get]
func (h *KycHandler) ListPending(c *fiber.Ctx) error {
	// RequirePermission(kyc:verify) on the route is the authorization; this only
	// confirms the request carried an identity at all.
	if _, ok := middleware.AccountID(c); !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}

	limit, err := queryInt(c, "limit")
	if err != nil {
		return err
	}
	offset, err := queryInt(c, "offset")
	if err != nil {
		return err
	}

	page, err := h.svc.ListPending(c.Context(), limit, offset)
	if err != nil {
		return err
	}
	return c.JSON(page)
}

// queryInt parses an optional integer query parameter. An absent or empty
// value reads as 0 (the service turns that into its default); a non-numeric
// one is a 400 rather than being silently ignored, so a typo'd ?limit=fifty
// does not quietly serve a different page than the caller asked for.
func queryInt(c *fiber.Ctx, name string) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperr.NewValidationWith("Validation failed",
			map[string]string{name: name + " must be an integer"})
	}
	return v, nil
}

type rejectPanReq struct {
	Reason string `json:"reason" example:"PAN name does not match the profile name"`
}

// RejectPAN godoc
//
// @Summary      Reject an account's KYC submission (admin only)
// @Description  Marks the named account's KYC row as REJECTED and records why. The reason is required and is shown to the account holder via GET /api/kyc/status, so it should say what to correct. Rejecting an already-verified account withdraws its access — pan_verified is cleared, so it can no longer request credit analytics. The account can re-submit a PAN, which returns the row to PENDING and clears the reason.
// @Tags         kyc
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        accountId  path      int            true  "Account id whose KYC is being rejected"
// @Param        request    body      rejectPanReq   true  "Why the submission was rejected"
// @Success      200        {object}  models.KYCRecord
// @Failure      400        {object}  apperr.ErrorBody  "accountId must be an integer / reason missing or too long"
// @Failure      401        {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403        {object}  apperr.ErrorBody  "Missing the 'kyc:verify' permission"
// @Failure      404        {object}  apperr.ErrorBody  "No PAN on file for this account"
// @Router       /admin/kyc/pan/{accountId}/reject [post]
func (h *KycHandler) RejectPAN(c *fiber.Ctx) error {
	// RequirePermission(kyc:verify) on the route is the authorization; the id is
	// recorded on the row as the reviewer who made the call.
	reviewerID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	accountID, err := strconv.ParseInt(c.Params("accountId"), 10, 64)
	if err != nil {
		return apperr.NewValidation("accountId must be an integer")
	}

	var req rejectPanReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}

	rec, err := h.svc.RejectPAN(c.Context(), accountID, req.Reason, reviewerID)
	if err != nil {
		return err
	}
	return c.JSON(rec)
}

// VerifyPAN godoc
//
// @Summary      Verify an account's PAN (admin only)
// @Description  Marks the named account's KYC row as PAN-verified and clears any previous rejection reason. Required before that account can request credit analytics. Needs the 'kyc:verify' permission.
// @Tags         kyc
// @Produce      json
// @Security     BearerAuth
// @Param        accountId  path      int  true  "Account id whose PAN is being verified"
// @Success      200        {object}  models.KYCRecord
// @Failure      400        {object}  apperr.ErrorBody  "accountId must be an integer"
// @Failure      401        {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403        {object}  apperr.ErrorBody  "Missing the 'kyc:verify' permission"
// @Failure      404        {object}  apperr.ErrorBody  "No PAN on file for this account"
// @Router       /admin/kyc/pan/{accountId}/verify [post]
func (h *KycHandler) VerifyPAN(c *fiber.Ctx) error {
	// RequirePermission(kyc:verify) on the route is the authorization; the id is
	// recorded on the row as the reviewer who approved it. The account being
	// verified comes from the path.
	reviewerID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	accountID, err := strconv.ParseInt(c.Params("accountId"), 10, 64)
	if err != nil {
		return apperr.NewValidation("accountId must be an integer")
	}
	rec, err := h.svc.VerifyPAN(c.Context(), accountID, reviewerID)
	if err != nil {
		return err
	}
	return c.JSON(rec)
}
