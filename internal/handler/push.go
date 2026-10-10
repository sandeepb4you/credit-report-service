package handler

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/push"
	"credit-report-service/internal/repository"
	"credit-report-service/internal/server/middleware"
)

// PushHandler owns device push-token registration and the admin send.
type PushHandler struct {
	sessions *repository.SessionRepo
	accounts *repository.AccountRepo
	sender   *push.Sender
}

func NewPushHandler(
	sessions *repository.SessionRepo,
	accounts *repository.AccountRepo,
	sender *push.Sender,
) *PushHandler {
	return &PushHandler{sessions: sessions, accounts: accounts, sender: sender}
}

// RegisterToken godoc
//
// @Summary      Register the calling device's FCM push token
// @Description  Attaches the token to the CALLER'S OWN session (the `sid` claim in the access token), stealing it from any other session that still holds it — signing out and back in mints a new session on the same device with the same token. Revoking the session (logout, password reset, deletion) silences the device; there is nothing separate to clean up. Re-sent by the app on every sign-in and on FCM token rotation; repeating a registration is idempotent.
// @Tags         push
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      object{token=string}  true  "FCM registration token"
// @Success      204      "Registered"
// @Failure      400      {object}  apperr.ErrorBody  "Missing/oversized token"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated, or a pre-session token with no sid claim"
// @Router       /push-token [put]
func (h *PushHandler) RegisterToken(c *fiber.Ctx) error {
	if _, ok := middleware.AccountID(c); !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	sessionID := middleware.SessionID(c)
	if sessionID == 0 {
		// A token minted before session ids rode in access tokens. Refresh gets
		// a modern one; registering against row zero would arm nothing.
		return apperr.NewUnauthorized("Session expired. Please sign in again.")
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := c.BodyParser(&in); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	in.Token = strings.TrimSpace(in.Token)
	// FCM tokens run ~150-300 chars; 4KB bounds hostile input without guessing
	// at the format, which Firebase does not document as stable.
	if in.Token == "" || len(in.Token) > 4096 {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"token": "a non-empty token under 4KB is required"})
	}
	if err := h.sessions.SetPushToken(c.Context(), sessionID, in.Token); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.NewUnauthorized("Session expired. Please sign in again.")
		}
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// UnregisterToken godoc
//
// @Summary      Detach the calling device's push token
// @Description  Explicit opt-out. Not required on logout — revoking the session already silences the device — but lets a client clear itself without ending the session.
// @Tags         push
// @Security     BearerAuth
// @Success      204  "Cleared"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /push-token [delete]
func (h *PushHandler) UnregisterToken(c *fiber.Ctx) error {
	if _, ok := middleware.AccountID(c); !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	if sessionID := middleware.SessionID(c); sessionID != 0 {
		if err := h.sessions.ClearPushToken(c.Context(), sessionID); err != nil {
			return err
		}
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// adminNotifyBody is the admin send: what the customer's lock screen shows.
type adminNotifyBody struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// ImageURL is optional and must be https when present — FCM drops cleartext
	// images on Android, so accepting http would "send" a picture nobody sees.
	ImageURL string `json:"imageUrl"`
	// Route is an optional in-app destination the tap opens (simple route name,
	// e.g. "Reports"). Travels in the data payload; the app validates it.
	Route string `json:"route"`
}

// AdminNotify godoc
//
// @Summary      Send a push notification to one customer's devices
// @Description  Fans the message out to every live session of the account that has a registered push token. The send is BEST-EFFORT per device (FCM outages and dead tokens are logged, dead tokens dropped); the response reports how many devices were addressed, where zero means the user has no push-capable session — not an error, but worth showing the operator. Requires notification:send (admin).
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        accountId  path      int              true  "Account id"
// @Param        request    body      adminNotifyBody  true  "Message"
// @Success      200        {object}  object{devices=int}  "Number of push-capable devices addressed"
// @Failure      400        {object}  apperr.ErrorBody  "Missing title/body, or a non-https image URL"
// @Failure      403        {object}  apperr.ErrorBody  "Missing notification:send"
// @Failure      404        {object}  apperr.ErrorBody  "No such account"
// @Failure      503        {object}  apperr.ErrorBody  "Push is not configured on this server (stub sender)"
// @Router       /admin/accounts/{accountId}/notify [post]
func (h *PushHandler) AdminNotify(c *fiber.Ctx) error {
	accountID, err := c.ParamsInt("accountId")
	if err != nil || accountID <= 0 {
		return apperr.NewValidation("accountId must be a positive integer")
	}
	var in adminNotifyBody
	if err := c.BodyParser(&in); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	details := map[string]string{}
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(in.Body)
	in.ImageURL = strings.TrimSpace(in.ImageURL)
	in.Route = strings.TrimSpace(in.Route)
	if in.Title == "" || len(in.Title) > 200 {
		details["title"] = "required, at most 200 characters"
	}
	if in.Body == "" || len(in.Body) > 1000 {
		details["body"] = "required, at most 1000 characters"
	}
	if in.ImageURL != "" && !strings.HasPrefix(in.ImageURL, "https://") {
		details["imageUrl"] = "must be an https URL"
	}
	if len(details) > 0 {
		return apperr.NewValidationWith("Validation failed", details)
	}
	// Honest about an unconfigured server, rather than a success that notified
	// nobody and taught the operator the feature is flaky.
	if h.sender.IsStub() {
		return apperr.NewServiceUnavailable(
			"Push notifications are not configured on this server.")
	}
	if _, err := h.accounts.FindByID(c.Context(), int64(accountID)); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.NewNotFound("Account not found")
		}
		return err
	}
	tokens, err := h.sessions.LivePushTokens(c.Context(), int64(accountID))
	if err != nil {
		return err
	}

	var data map[string]string
	if in.Route != "" {
		data = map[string]string{"route": in.Route}
	}
	// Detached from the request context: the operator's HTTP request ending must
	// not cancel sends mid-fan-out. Bounded so a wedged FCM cannot leak goroutines.
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), 60*time.Second)
	go func() {
		defer cancel()
		h.sender.NotifyAccount(sendCtx, int64(accountID), push.Notification{
			Title: in.Title, Body: in.Body, Image: in.ImageURL, Data: data,
		})
	}()
	return c.JSON(fiber.Map{"devices": len(tokens)})
}
