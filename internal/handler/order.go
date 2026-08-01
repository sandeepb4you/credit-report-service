package handler

import (
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	_ "credit-report-service/internal/models" // referenced by swag annotations (models.Product, models.Order)
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// OrderHandler serves the product catalog, purchase, and payment-webhook
// endpoints.
type OrderHandler struct {
	svc *service.OrderService
}

func NewOrderHandler(svc *service.OrderService) *OrderHandler {
	return &OrderHandler{svc: svc}
}

// Cashfree order_id charset (also matches our UUIDs).
var orderUIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{3,45}$`)

// ---- GET /api/products ----------------------------------------------------

// ListProducts godoc
//
// @Summary      List purchasable products
// @Description  Returns the active product catalog with prices. Use a product's `code` as the `productCode` when creating an order.
// @Tags         orders
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.Product
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /products [get]
func (h *OrderHandler) ListProducts(c *fiber.Ctx) error {
	products, err := h.svc.ListProducts(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(products)
}

// ---- POST /api/orders -----------------------------------------------------

type createOrderReq struct {
	ProductCode string `json:"productCode" example:"CREDIT_ANALYSIS"`
}

// Create godoc
//
// @Summary      Create an order and start checkout
// @Description  Creates an order for the given product against the authenticated account and registers it with Cashfree. The response carries `paymentSessionId` and `mode`, which the frontend passes to the Cashfree JS SDK to open checkout.
// @Tags         orders
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      createOrderReq  true  "Product to purchase"
// @Success      201      {object}  service.PurchaseResult
// @Failure      400      {object}  apperr.ErrorBody  "Invalid JSON body / productCode missing or unknown"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      409      {object}  apperr.ErrorBody  "Product is not available for purchase"
// @Failure      502      {object}  apperr.ErrorBody  "Payment gateway could not create the order"
// @Router       /orders [post]
func (h *OrderHandler) Create(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req createOrderReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.ProductCode = strings.TrimSpace(req.ProductCode)
	if req.ProductCode == "" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"productCode": "productCode is required"})
	}
	res, err := h.svc.CreateOrder(c.Context(), accountID, req.ProductCode)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(res)
}

// ---- GET /api/orders ------------------------------------------------------

// List godoc
//
// @Summary      List the authenticated account's orders
// @Description  Returns every order placed by the authenticated account, newest first.
// @Tags         orders
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.Order
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /orders [get]
func (h *OrderHandler) List(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	orders, err := h.svc.ListOrders(c.Context(), accountID)
	if err != nil {
		return err
	}
	return c.JSON(orders)
}

// ---- GET /api/orders/:orderId ----------------------------------------------

// Get godoc
//
// @Summary      Get one order
// @Description  Returns a single order owned by the authenticated account. If the order is still awaiting payment its status is reconciled against Cashfree before being returned, so this is the endpoint to poll after checkout closes.
// @Tags         orders
// @Produce      json
// @Security     BearerAuth
// @Param        orderId  path      string  true  "Order id returned from order creation"
// @Success      200      {object}  models.Order
// @Failure      400      {object}  apperr.ErrorBody  "orderId is not valid"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404      {object}  apperr.ErrorBody  "Order not found"
// @Router       /orders/{orderId} [get]
func (h *OrderHandler) Get(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	orderUID := c.Params("orderId")
	if !orderUIDRE.MatchString(orderUID) {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"orderId": "orderId is not valid"})
	}
	order, err := h.svc.GetOrder(c.Context(), accountID, orderUID)
	if err != nil {
		return err
	}
	return c.JSON(order)
}

// ---- POST /api/payments/cashfree/webhook ------------------------------------

// Webhook receives Cashfree's server-to-server payment notifications. It is
// unauthenticated: trust comes from the HMAC signature over the raw body.
//
// @Summary      Cashfree payment webhook
// @Description  Server-to-server endpoint called by Cashfree on payment success/failure. Not for client use: there is no bearer auth, trust comes from the `x-webhook-signature` HMAC over the raw body, and `x-idempotency-key` de-duplicates redeliveries.
// @Tags         payments
// @Accept       json
// @Produce      plain
// @Param        x-webhook-timestamp  header    string  true   "Cashfree signature timestamp"
// @Param        x-webhook-signature  header    string  true   "Base64 HMAC-SHA256 of timestamp+body"
// @Param        x-idempotency-key    header    string  false  "Cashfree delivery id, used to drop duplicates"
// @Success      200                  {string}  string            "OK"
// @Failure      400                  {object}  apperr.ErrorBody  "Invalid webhook payload"
// @Failure      401                  {object}  apperr.ErrorBody  "Invalid webhook signature"
// @Router       /payments/cashfree/webhook [post]
func (h *OrderHandler) Webhook(c *fiber.Ctx) error {
	// Fiber reuses the body buffer between requests; copy before use.
	body := append([]byte(nil), c.Body()...)
	err := h.svc.ProcessWebhook(
		c.Context(),
		c.Get("x-webhook-timestamp"),
		c.Get("x-webhook-signature"),
		c.Get("x-idempotency-key"),
		body,
	)
	if err != nil {
		return err
	}
	return c.SendString("OK")
}
