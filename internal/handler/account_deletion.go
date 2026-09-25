package handler

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
)

// The public account-deletion endpoints, behind https://myscorr.com/delete-account.
//
// Unauthenticated by requirement, not by oversight: Google Play expects a user
// who has already uninstalled the app to be able to erase their account from a
// web page, so there is no bearer token to lean on and the OTP is the whole of
// the authentication. Everything that makes that safe lives in the service —
// the per-purpose challenge, the attempt cap, the single-use grant, and the
// fourteen-day window a sign-in cancels.
//
// Three calls rather than one for the same reason password reset has three:
// the page should be able to move the user to the consequences screen the
// moment the code checks out, without holding a live OTP while they read it.

// ---- POST /api/auth/account-deletion/send -------------------------------

type accountDeletionSendReq struct {
	// Identifier is the phone number or email address on the account. One
	// field rather than two because the page asks one question, and because
	// the app is phone-first — most accounts have no email address at all.
	Identifier string `json:"identifier" example:"+919000000777"`
}

// SendAccountDeletionOTP godoc
//
//	@Summary		Send an account-deletion code
//	@Description	Sends a one-time code to the phone number or email address on the account.
//	@Description	Always answers 200 for an identifier with no account: this route is public, and
//	@Description	an honest answer would let anyone sort a list of contacts into registered and not.
//	@Tags			account-deletion
//	@Accept			json
//	@Produce		json
//	@Param			payload	body		accountDeletionSendReq	true	"Phone or email"
//	@Success		200		{object}	map[string]string
//	@Failure		400		{object}	apperr.ErrorBody
//	@Failure		429		{object}	apperr.ErrorBody
//	@Router			/auth/account-deletion/send [post]
func (h *AuthHandler) SendAccountDeletionOTP(c *fiber.Ctx) error {
	var req accountDeletionSendReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("Invalid request body")
	}
	if err := h.svc.RequestAccountDeletionCode(c.Context(), req.Identifier); err != nil {
		return err
	}
	// Deliberately says "if"; see the service. The page repeats the same
	// hedge, so a user who mistyped their number is not left believing a code
	// is on its way to it.
	return c.JSON(fiber.Map{
		"message": "If that phone number or email has an account, we've sent a code to it.",
	})
}

// ---- POST /api/auth/account-deletion/verify -----------------------------

type accountDeletionVerifyReq struct {
	Identifier string `json:"identifier" example:"+919000000777"`
	OTP        string `json:"otp"        example:"1234"`
}

// VerifyAccountDeletionOTP godoc
//
//	@Summary		Verify an account-deletion code
//	@Description	Exchanges the code for a single-use grant, valid 15 minutes. Nothing is
//	@Description	deleted or scheduled by this call.
//	@Tags			account-deletion
//	@Accept			json
//	@Produce		json
//	@Param			payload	body		accountDeletionVerifyReq	true	"Identifier and code"
//	@Success		200		{object}	service.AccountDeletionGrant
//	@Failure		400		{object}	apperr.ErrorBody
//	@Failure		422		{object}	apperr.ErrorBody
//	@Router			/auth/account-deletion/verify [post]
func (h *AuthHandler) VerifyAccountDeletionOTP(c *fiber.Ctx) error {
	var req accountDeletionVerifyReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("Invalid request body")
	}
	otp := strings.TrimSpace(req.OTP)
	if !otpCodeRE.MatchString(otp) {
		return apperr.NewValidationWith("Validation failed", map[string]string{
			"otp": "Enter the code we sent you.",
		})
	}
	grant, err := h.svc.VerifyAccountDeletionCode(c.Context(), req.Identifier, otp)
	if err != nil {
		return err
	}
	return c.JSON(grant)
}

// ---- POST /api/auth/account-deletion/confirm ----------------------------

type accountDeletionConfirmReq struct {
	DeletionToken string `json:"deletionToken" example:"adt_xxx"`
}

// ConfirmAccountDeletion godoc
//
//	@Summary		Schedule the account for deletion
//	@Description	Redeems the grant, schedules the purge 14 days out and revokes every session.
//	@Description	Signing in before that date cancels it. Confirming twice returns the original
//	@Description	date rather than moving it.
//	@Tags			account-deletion
//	@Accept			json
//	@Produce		json
//	@Param			payload	body		accountDeletionConfirmReq	true	"The grant from /verify"
//	@Success		200		{object}	service.AccountDeletionSchedule
//	@Failure		400		{object}	apperr.ErrorBody
//	@Failure		422		{object}	apperr.ErrorBody
//	@Router			/auth/account-deletion/confirm [post]
func (h *AuthHandler) ConfirmAccountDeletion(c *fiber.Ctx) error {
	var req accountDeletionConfirmReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("Invalid request body")
	}
	schedule, err := h.svc.ConfirmAccountDeletion(c.Context(), req.DeletionToken)
	if err != nil {
		return err
	}
	return c.JSON(schedule)
}
