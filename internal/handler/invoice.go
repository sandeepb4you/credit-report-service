package handler

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// SetInvoices wires the invoice endpoints onto the order routes. A setter
// rather than a constructor argument so the server's handler list did not have
// to grow for two routes that live under /orders anyway; unset, both answer
// 503 rather than 404, because the order exists and the invoice feature is
// what is missing.
func (h *OrderHandler) SetInvoices(svc *service.InvoiceService) { h.invoices = svc }

// ---- GET /api/orders/{orderId}/invoice --------------------------------------

// Invoice godoc
//
// @Summary      Download an order's invoice
// @Description  Returns a short-lived URL for the invoice PDF of one of the caller's paid orders, rendering and storing it first if this is the first request. The URL is presigned: open it directly, without the bearer token. `filename` is the name to save it under.
// @Tags         orders
// @Produce      json
// @Security     BearerAuth
// @Param        orderId  path      string  true  "Order id"
// @Success      200      {object}  service.InvoiceLink
// @Failure      400      {object}  apperr.ErrorBody  "orderId is not valid"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404      {object}  apperr.ErrorBody  "Order not found, or no invoice issued for it"
// @Failure      502      {object}  apperr.ErrorBody  "Could not prepare the download"
// @Failure      503      {object}  apperr.ErrorBody  "Invoice rendering or storage is not configured"
// @Router       /orders/{orderId}/invoice [get]
func (h *OrderHandler) Invoice(c *fiber.Ctx) error {
	accountID, orderUID, err := h.invoiceTarget(c)
	if err != nil {
		return err
	}
	link, err := h.invoices.Link(c.Context(), accountID, orderUID)
	if err != nil {
		return err
	}
	return c.JSON(link)
}

// ---- POST /api/orders/{orderId}/invoice/email ---------------------------------

type invoiceEmailReq struct {
	// Email is optional. Absent: the account's own address. Present and not
	// the account's: sent once to it and not saved (the design's "Send once,
	// don't save").
	Email string `json:"email,omitempty" example:"user@example.com"`
}

// EmailInvoice godoc
//
// @Summary      Email an order's invoice
// @Description  Sends the invoice PDF to the account's email address, or, when `email` is given and is another address, once to that address without saving it. 409 when neither exists, so the client can ask where to send it. One send per invoice per 30 seconds, and at most 3 a day to addresses the account does not hold; beyond either, 429 with Retry-After.
// @Tags         orders
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        orderId  path      string                   true   "Order id"
// @Param        body     body      handler.invoiceEmailReq  false  "Where to send it"
// @Success      200      {object}  service.InvoiceEmailResult
// @Failure      400      {object}  apperr.ErrorBody  "orderId or email is not valid"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404      {object}  apperr.ErrorBody  "Order not found, or no invoice issued for it"
// @Failure      409      {object}  apperr.ErrorBody  "No email on the account and none given"
// @Failure      429      {object}  apperr.ErrorBody  "Sent too recently, or too often to other addresses"
// @Failure      502      {object}  apperr.ErrorBody  "Could not send the email"
// @Failure      503      {object}  apperr.ErrorBody  "Invoice rendering or email is not configured"
// @Router       /orders/{orderId}/invoice/email [post]
func (h *OrderHandler) EmailInvoice(c *fiber.Ctx) error {
	accountID, orderUID, err := h.invoiceTarget(c)
	if err != nil {
		return err
	}
	var req invoiceEmailReq
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return apperr.NewValidation("invalid JSON body")
		}
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email != "" && !looksLikeEmail(req.Email) {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"email": "email must be valid"})
	}

	res, err := h.invoices.Email(c.Context(), accountID, orderUID, req.Email)
	var limited *service.InvoiceSendLimited
	switch {
	case err == nil:
		return c.JSON(res)
	case errors.Is(err, service.ErrInvoiceEmailMissing):
		return apperr.NewConflict("Add an email address, or enter one, to have the invoice sent.")
	case errors.As(err, &limited):
		c.Set(fiber.HeaderRetryAfter, strconv.Itoa(int(math.Ceil(limited.RetryAfter.Seconds()))))
		return fiber.NewError(fiber.StatusTooManyRequests, limited.Msg)
	default:
		return err
	}
}

func (h *OrderHandler) invoiceTarget(c *fiber.Ctx) (int64, string, error) {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return 0, "", apperr.NewUnauthorized("Not authenticated")
	}
	orderUID := c.Params("orderId")
	if !orderUIDRE.MatchString(orderUID) {
		return 0, "", apperr.NewValidationWith("Validation failed",
			map[string]string{"orderId": "orderId is not valid"})
	}
	if h.invoices == nil {
		return 0, "", apperr.NewServiceUnavailable("Invoices are not available right now.")
	}
	return accountID, orderUID, nil
}
