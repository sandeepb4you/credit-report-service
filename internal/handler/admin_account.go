package handler

import (
	"strconv"
	"strings"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
	"github.com/gofiber/fiber/v2"
)

// AdminAccountHandler carries the account-administration routes that are not
// KYC review — currently just the reset that walks an account back to signup.
type AdminAccountHandler struct {
	svc  *service.AccountResetService
	list *service.AdminAccountsService
}

func NewAdminAccountHandler(
	svc *service.AccountResetService, list *service.AdminAccountsService,
) *AdminAccountHandler {
	return &AdminAccountHandler{svc: svc, list: list}
}

// ListAccounts godoc
//
// @Summary      List user accounts over a signup-date window
// @Description  The operator's customer list: who signed up in the window, with their latest credit score, whether they have ever paid, how many accounts they referred and how many of those bought. Contact details are NOT masked — the console is admin-only and the number is here to be called. Defaults to the last 30 whole UTC days, newest first.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        from    query     string  false  "First day, inclusive (YYYY-MM-DD)"
// @Param        to      query     string  false  "Last day, inclusive (YYYY-MM-DD)"
// @Param        status  query     string  false  "ACTIVE, INACTIVE, PENDING or SUSPENDED; omit for every status"
// @Param        q       query     string  false  "Matches name, phone or email, anywhere, case-insensitively"
// @Param        paid    query     bool    false  "Only accounts that have (or have never) paid"
// @Param        minScore query    int     false  "Lowest latest score to include"
// @Param        maxScore query    int     false  "Highest latest score to include"
// @Param        minReferred  query int    false  "Least number of accounts referred"
// @Param        minConverted query int    false  "Least number of referrals that bought"
// @Param        sort    query     string  false  "signedUp (default), name, phone, score, paid, referred or converted"
// @Param        desc    query     bool    false  "Sort descending (default true for signedUp)"
// @Param        limit   query     int     false  "Page size (default 50, max 200)"
// @Param        offset  query     int     false  "Rows to skip"
// @Success      200  {object}  models.AdminAccountPage
// @Failure      400  {object}  apperr.ErrorBody  "Unparseable date, backwards range, or unknown status"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'account:view' permission"
// @Router       /admin/accounts [get]
func (h *AdminAccountHandler) ListAccounts(c *fiber.Ctx) error {
	from, err := queryDate(c, "from")
	if err != nil {
		return err
	}
	to, err := queryDate(c, "to")
	if err != nil {
		return err
	}
	limit, err := queryInt(c, "limit")
	if err != nil {
		return err
	}
	offset, err := queryInt(c, "offset")
	if err != nil {
		return err
	}

	paid, err := queryBoolPtr(c, "paid")
	if err != nil {
		return err
	}
	minScore, err := queryIntPtr(c, "minScore")
	if err != nil {
		return err
	}
	maxScore, err := queryIntPtr(c, "maxScore")
	if err != nil {
		return err
	}
	minReferred, err := queryIntPtr(c, "minReferred")
	if err != nil {
		return err
	}
	minConverted, err := queryIntPtr(c, "minConverted")
	if err != nil {
		return err
	}
	// Descending unless the caller says otherwise: every column here is read
	// "most first" — newest signup, highest score, most referrals.
	desc, err := queryBoolPtr(c, "desc")
	if err != nil {
		return err
	}

	page, err := h.list.List(c.Context(), service.AccountQuery{
		From:         from,
		To:           to,
		Status:       c.Query("status"),
		Search:       c.Query("q"),
		Paid:         paid,
		MinScore:     minScore,
		MaxScore:     maxScore,
		MinReferred:  minReferred,
		MinConverted: minConverted,
		Sort:         c.Query("sort"),
		Desc:         desc == nil || *desc,
		Limit:        limit,
		Offset:       offset,
	})
	if err != nil {
		return err
	}
	return c.JSON(page)
}

// AccountDetail godoc
//
// @Summary      One account with its KYC record and recent score checks
// @Description  What opening a row on the user list shows: the account, the full KYC record (nil when nothing was submitted — the same record the PAN review queue shows, PAN included) and the five most recent successful score checks.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        accountId  path      int  true  "Account id"
// @Success      200  {object}  models.AdminAccountDetail
// @Failure      400  {object}  apperr.ErrorBody  "Unparseable account id"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'account:view' permission"
// @Failure      404  {object}  apperr.ErrorBody  "No account with that id"
// @Router       /admin/accounts/{accountId}/detail [get]
func (h *AdminAccountHandler) AccountDetail(c *fiber.Ctx) error {
	accountID, err := strconv.ParseInt(c.Params("accountId"), 10, 64)
	if err != nil || accountID <= 0 {
		return apperr.NewValidationWith("Validation failed", map[string]string{
			"accountId": "expected a positive account id",
		})
	}
	detail, err := h.list.Detail(c.Context(), accountID)
	if err != nil {
		return err
	}
	return c.JSON(detail)
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

// queryIntPtr parses an optional integer query parameter. Absent is nil — no
// filter — which is not the same as 0: "referred at least 0" is every account.
func queryIntPtr(c *fiber.Ctx, name string) (*int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{name: name + " must be an integer"})
	}
	return &v, nil
}

// queryBoolPtr parses an optional boolean. Absent is nil, meaning "either" —
// distinct from false, which means "only the ones that have not".
func queryBoolPtr(c *fiber.Ctx, name string) (*bool, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{name: name + " must be true or false"})
	}
	return &v, nil
}
