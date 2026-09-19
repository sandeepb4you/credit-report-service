package handler

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/repository"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// EarningsHandler serves the referral-money surface: the user's own dashboard
// (summary, referral list, bank account, withdrawals) and the admin's
// manual-payout queue. Money never moves here — a user request goes PENDING
// and an admin marks it PAID after paying off-app.
type EarningsHandler struct {
	svc *service.EarningsService
}

func NewEarningsHandler(svc *service.EarningsService) *EarningsHandler {
	return &EarningsHandler{svc: svc}
}

// ---- GET /api/referrals/summary -------------------------------------------

// Summary godoc
//
// @Summary      Referral earnings summary
// @Description  The balance card: flat reward per referral, total earned, withdrawn (paid), locked in pending requests, available, and successful-vs-pending counts. Available = earned − paid − pending, so a pending request locks its amount. Also returns the caller's own referral code (minted on first read).
// @Tags         referrals
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  models.EarningsSummary
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /referrals/summary [get]
func (h *EarningsHandler) Summary(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	out, err := h.svc.Summary(c.Context(), accountID)
	return jsonOrErr(c, out, err)
}

// ---- GET /api/referrals/mine ----------------------------------------------

// MyReferrals godoc
//
// @Summary      My referred signups
// @Description  Every signup through the caller's code, oldest first, with masked contacts (phone like +91 98xxx xx041, email like r****l@gmail.com), signup/purchase dates and paid-vs-pending status. Full details stay on the admin report.
// @Tags         referrals
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.MyReferralItem
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /referrals/mine [get]
func (h *EarningsHandler) MyReferrals(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	out, err := h.svc.MyReferrals(c.Context(), accountID)
	return jsonOrErr(c, out, err)
}

// ---- GET / PUT /api/referrals/bank-account --------------------------------

// GetBank godoc
//
// @Summary      My payout bank account
// @Description  The withdrawal destination, masked (holder name, last 4, IFSC). 404 when none is saved yet — the app then shows the "Change bank account" sheet.
// @Tags         referrals
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  models.PayoutBankAccount
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "No bank account saved"
// @Router       /referrals/bank-account [get]
func (h *EarningsHandler) GetBank(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	bank, err := h.svc.MyBank(c.Context(), accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.NewNotFound("No bank account saved yet")
	}
	return jsonOrErr(c, bank, err)
}

type saveBankReq struct {
	HolderName    string `json:"holderName"`
	AccountNumber string `json:"accountNumber"`
	ConfirmNumber string `json:"confirmNumber"`
	IFSC          string `json:"ifsc"`
}

// SaveBank godoc
//
// @Summary      Save payout bank account
// @Description  Upserts the withdrawal destination. The number must be typed twice and match; IFSC must match the standard shape. Withdrawals snapshot this row at request time, so changing it later never rewrites a pending request.
// @Tags         referrals
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      saveBankReq  true  "Bank account"
// @Success      200      {object}  models.PayoutBankAccount
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /referrals/bank-account [put]
func (h *EarningsHandler) SaveBank(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req saveBankReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	out, err := h.svc.SaveBank(c.Context(), accountID, service.SaveBankInput{
		HolderName:    req.HolderName,
		AccountNumber: req.AccountNumber,
		ConfirmNumber: req.ConfirmNumber,
		IFSC:          req.IFSC,
	})
	return jsonOrErr(c, out, err)
}

// ---- GET / POST /api/referrals/withdrawals --------------------------------

// MyWithdrawals godoc
//
// @Summary      My withdrawal requests
// @Description  The caller's own requests, newest first, with PENDING/PAID/REJECTED status.
// @Tags         referrals
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.Withdrawal
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /referrals/withdrawals [get]
func (h *EarningsHandler) MyWithdrawals(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	out, err := h.svc.MyWithdrawals(c.Context(), accountID)
	return jsonOrErr(c, out, err)
}

type requestWithdrawalReq struct {
	// AmountPaise is the requested amount in paise (100 = ₹1).
	AmountPaise int `json:"amountPaise" example:"12500"`
}

// RequestWithdrawal godoc
//
// @Summary      Request a withdrawal
// @Description  Creates a PENDING request, locking its amount out of the available balance. Requires a saved bank account and an amount within 1..available. Paying happens off-app; an admin marks it PAID afterwards.
// @Tags         referrals
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      requestWithdrawalReq  true  "Amount in paise"
// @Success      201      {object}  models.Withdrawal
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /referrals/withdrawals [post]
func (h *EarningsHandler) RequestWithdrawal(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req requestWithdrawalReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	w, err := h.svc.RequestWithdrawal(c.Context(), accountID, req.AmountPaise)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(w)
}

// ---- GET /api/admin/withdrawals -------------------------------------------

// ReviewQueue godoc
//
// @Summary      Withdrawal review queue (admin only)
// @Description  The manual-payout queue with each requester's identity and full bank destination, oldest first. Narrow with status=PENDING/PAID/REJECTED; omit for everything. Needs the 'withdrawal:review' permission.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        status  query     string  false  "PENDING, PAID or REJECTED"
// @Param        limit   query     int     false  "Max rows (default 50, max 200)"
// @Param        offset  query     int     false  "Rows to skip (default 0)"
// @Success      200     {array}   models.AdminWithdrawalItem
// @Failure      400     {object}  apperr.ErrorBody  "Bad status"
// @Failure      401     {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403     {object}  apperr.ErrorBody  "Missing the 'withdrawal:review' permission"
// @Router       /admin/withdrawals [get]
func (h *EarningsHandler) ReviewQueue(c *fiber.Ctx) error {
	if _, ok := middleware.AccountID(c); !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	status := strings.TrimSpace(c.Query("status"))
	var statusPtr *string
	if status != "" {
		statusPtr = &status
	}
	limit, err := queryInt(c, "limit")
	if err != nil {
		return err
	}
	offset, err := queryInt(c, "offset")
	if err != nil {
		return err
	}
	items, _, err := h.svc.ReviewQueue(c.Context(), statusPtr, limit, offset)
	if err != nil {
		return err
	}
	return c.JSON(items)
}

// ---- POST /api/admin/withdrawals/:id/pay ----------------------------------

// MarkPaid godoc
//
// @Summary      Mark a withdrawal paid (admin only)
// @Description  Records that the admin paid the request off-app (bank transfer). Only a PENDING request moves; anything else is 404, so a double-click or two admins cannot pay twice. Needs the 'withdrawal:review' permission.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        id    path      int  true  "Withdrawal id"
// @Success      200   {object}  map[string]string  "{\"message\": \"Marked paid\"}"
// @Failure      401   {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403   {object}  apperr.ErrorBody  "Missing the 'withdrawal:review' permission"
// @Failure      404   {object}  apperr.ErrorBody  "No pending withdrawal with that id"
// @Router       /admin/withdrawals/{id}/pay [post]
func (h *EarningsHandler) MarkPaid(c *fiber.Ctx) error {
	adminID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	id, err := withdrawalID(c)
	if err != nil {
		return err
	}
	if err := h.svc.MarkPaid(c.Context(), adminID, id); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"message": "Marked paid"})
}

type rejectWithdrawalReq struct {
	Reason string `json:"reason"`
}

// Reject godoc
//
// @Summary      Reject a withdrawal (admin only)
// @Description  Frees the request's locked amount back into the user's available balance. A reason is required — it is shown to the user. Only a PENDING request moves. Needs the 'withdrawal:review' permission.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      int                  true  "Withdrawal id"
// @Param        request  body      rejectWithdrawalReq  true  "Reason"
// @Success      200      {object}  map[string]string  "{\"message\": \"Rejected\"}"
// @Failure      400      {object}  apperr.ErrorBody  "Reason required"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Missing the 'withdrawal:review' permission"
// @Failure      404      {object}  apperr.ErrorBody  "No pending withdrawal with that id"
// @Router       /admin/withdrawals/{id}/reject [post]
func (h *EarningsHandler) Reject(c *fiber.Ctx) error {
	adminID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	id, err := withdrawalID(c)
	if err != nil {
		return err
	}
	var req rejectWithdrawalReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	if err := h.svc.Reject(c.Context(), adminID, id, req.Reason); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"message": "Rejected"})
}

func withdrawalID(c *fiber.Ctx) (int64, error) {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.NewValidationWith("Validation failed",
			map[string]string{"id": "expected a positive withdrawal id"})
	}
	return id, nil
}

// ---- GET /api/admin/referral-settings -------------------------------------

// GetSettings godoc
//
// @Summary      Referral programme economics (admin only)
// @Description  The flat credit per converted referral and the minimum withdrawal, both in paise. Needs the 'referral:manage' permission.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  models.ReferralSettings
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'referral:manage' permission"
// @Router       /admin/referral-settings [get]
func (h *EarningsHandler) GetSettings(c *fiber.Ctx) error {
	if _, ok := middleware.AccountID(c); !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	out, err := h.svc.GetSettings(c.Context())
	return jsonOrErr(c, out, err)
}

type updateSettingsReq struct {
	// Either may be omitted to leave that value alone; both omitted is a 400.
	RewardPaise        *int `json:"rewardPaise" example:"12500"`
	MinWithdrawalPaise *int `json:"minWithdrawalPaise" example:"50000"`
}

// UpdateSettings godoc
//
// @Summary      Reprice the referral programme (admin only)
// @Description  Changes the flat credit and/or the minimum withdrawal going forward. Credited rows snapshot the amount, so history is never rewritten — only future conversions and requests see the new values. Needs the 'referral:manage' permission.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      updateSettingsReq  true  "New economics"
// @Success      200      {object}  models.ReferralSettings
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Missing the 'referral:manage' permission"
// @Router       /admin/referral-settings [put]
func (h *EarningsHandler) UpdateSettings(c *fiber.Ctx) error {
	adminID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req updateSettingsReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	out, err := h.svc.UpdateSettings(c.Context(), adminID, service.UpdateSettingsInput{
		RewardPaise:        req.RewardPaise,
		MinWithdrawalPaise: req.MinWithdrawalPaise,
	})
	return jsonOrErr(c, out, err)
}

// jsonOrErr renders a service result. Services return empty slices (never
// nil), so an empty list encodes as [] rather than null.
func jsonOrErr(c *fiber.Ctx, out any, err error) error {
	if err != nil {
		return err
	}
	return c.JSON(out)
}
