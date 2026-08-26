package handler

import (
	"strconv"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
	"github.com/gofiber/fiber/v2"
)

// AdminAccountHandler carries the account-administration routes that are not
// KYC review — currently just the reset that walks an account back to signup.
type AdminAccountHandler struct {
	svc *service.AccountResetService
}

func NewAdminAccountHandler(svc *service.AccountResetService) *AdminAccountHandler {
	return &AdminAccountHandler{svc: svc}
}

// AccountLookupResponse is an account plus what resetting it would remove.
type AccountLookupResponse struct {
	Account *models.Account           `json:"account"`
	Removes models.AccountResetCounts `json:"removes"`
}

// AccountResetRequest confirms the target by naming it.
type AccountResetRequest struct {
	// Confirm must be the phone number or email address registered on the
	// account being reset. The admin has already been authorised; this is here
	// so a mistyped account id cannot delete a stranger's paid reports.
	Confirm string `json:"confirm" example:"+919876543210"`
}

// LookupAccount godoc
//
// @Summary      Find an account by phone or email, with a reset preview
// @Description  Resolves an account from a mobile number or email address and reports what an account reset would remove from it — reports, orders (and how many were paid for), statements, coupon redemptions and live sessions. Intended to be shown before the reset is confirmed, because those counts are the warning.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        identifier  query     string  true  "Mobile number or email address"
// @Success      200  {object}  AccountLookupResponse
// @Failure      400  {object}  apperr.ErrorBody  "Missing or unparseable identifier"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'account:reset' permission"
// @Failure      404  {object}  apperr.ErrorBody  "No account with that phone number or email address"
// @Router       /admin/accounts/lookup [get]
func (h *AdminAccountHandler) LookupAccount(c *fiber.Ctx) error {
	overview, err := h.svc.Lookup(c.Context(), c.Query("identifier"))
	if err != nil {
		return err
	}
	return c.JSON(AccountLookupResponse{
		Account: overview.Account,
		Removes: *overview.Counts,
	})
}

// ResetAccount godoc
//
// @Summary      Reset an account back to signup
// @Description  Deletes everything the account did after signing up — PAN/KYC, credit reports (and their stored PDFs), orders and payment history, bank statements, prefill lookups, OTP challenges and referral credit — clears the profile name and date of birth, and revokes every session. The login survives: the same phone number or email signs straight back in and lands on PAN verification with the paywall restored, which is the point. Role is untouched, so an admin resetting their own account is still an admin afterwards. This is destructive and it is enabled in production; the body must name the account's own phone or email to confirm.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        accountId  path      int                  true  "Account to reset"
// @Param        request    body      AccountResetRequest  true  "Confirmation"
// @Success      200  {object}  models.AccountResetResult
// @Failure      400  {object}  apperr.ErrorBody  "accountId must be an integer / confirmation does not match"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'account:reset' permission"
// @Failure      404  {object}  apperr.ErrorBody  "Account not found"
// @Router       /admin/accounts/{accountId}/reset [post]
func (h *AdminAccountHandler) ResetAccount(c *fiber.Ctx) error {
	actorID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	targetID, err := strconv.ParseInt(c.Params("accountId"), 10, 64)
	if err != nil {
		return apperr.NewValidation("accountId must be an integer")
	}
	var req AccountResetRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}

	result, err := h.svc.Reset(c.Context(), actorID, targetID, req.Confirm)
	if err != nil {
		return err
	}
	return c.JSON(result)
}
