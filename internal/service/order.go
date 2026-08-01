package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
	"credit-report-service/internal/payments"
	"credit-report-service/internal/repository"
)

// Cashfree webhook event types (x-api-version 2025-01-01).
const (
	webhookPaymentSuccess = "PAYMENT_SUCCESS_WEBHOOK"
	webhookPaymentFailed  = "PAYMENT_FAILED_WEBHOOK"
	webhookUserDropped    = "PAYMENT_USER_DROPPED_WEBHOOK"
	webhookTestPing       = "WEBHOOK" // dashboard "test webhook" event
)

// OrderService implements the purchase flow: create a local order, open the
// matching Cashfree order, and settle the outcome from webhooks (with
// on-demand reconciliation against the gateway as backup).
type OrderService struct {
	orders   *repository.OrderRepo
	accounts *repository.AccountRepo
	gateway  payments.Gateway
	cfg      config.CashfreeConfig
}

func NewOrderService(
	orders *repository.OrderRepo,
	accounts *repository.AccountRepo,
	gateway payments.Gateway,
	cfg config.CashfreeConfig,
) *OrderService {
	return &OrderService{orders: orders, accounts: accounts, gateway: gateway, cfg: cfg}
}

// PurchaseResult is returned from CreateOrder. The frontend initialises the
// Cashfree JS SDK with Mode and opens checkout with PaymentSessionID.
type PurchaseResult struct {
	OrderID          string  `json:"orderId"`
	CFOrderID        string  `json:"cfOrderId"`
	PaymentSessionID string  `json:"paymentSessionId"`
	Amount           float64 `json:"amount"`
	Currency         string  `json:"currency"`
	Status           string  `json:"status"`
	Mode             string  `json:"mode"`
}

// ListProducts returns the purchasable catalog.
func (s *OrderService) ListProducts(ctx context.Context) ([]models.Product, error) {
	return s.orders.ListActiveProducts(ctx)
}

// CreateOrder starts a purchase: snapshots the product price into a local
// order row, opens the Cashfree order, and returns the payment session the
// frontend needs to launch checkout.
func (s *OrderService) CreateOrder(ctx context.Context, accountID int64, productCode string) (*PurchaseResult, error) {
	product, err := s.orders.FindProduct(ctx, productCode)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"productCode": "unknown product"})
	}
	if err != nil {
		return nil, err
	}
	if !product.Active {
		return nil, apperr.NewConflict("Product is not available for purchase")
	}

	account, err := s.accounts.FindByID(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewUnauthorized("Account not found")
	}
	if err != nil {
		return nil, err
	}

	order := &models.Order{
		OrderUID:    uuid.NewString(),
		AccountID:   accountID,
		ProductCode: product.Code,
		Amount:      product.Amount,
		Currency:    product.Currency,
		Status:      models.OrderCreationRequested,
	}
	if err := s.orders.CreateOrder(ctx, order); err != nil {
		return nil, err
	}

	res, err := s.gateway.CreateOrder(ctx, payments.CreateOrderParams{
		OrderID:       order.OrderUID,
		Amount:        order.Amount,
		Currency:      order.Currency,
		CustomerID:    fmt.Sprintf("acct_%d", accountID),
		CustomerEmail: strDeref(account.PrimaryEmail),
		CustomerPhone: customerPhone(account),
		CustomerName:  customerName(account),
		ReturnURL:     buildReturnURL(s.cfg.ReturnURL, order.OrderUID),
		NotifyURL:     s.cfg.NotifyURL,
		OrderNote:     product.Name,
	})
	if err != nil {
		log.Printf("[order] cashfree create failed for %s: %v", order.OrderUID, err)
		if ferr := s.orders.MarkOrderCreationFailed(ctx, order.OrderUID, err.Error()); ferr != nil {
			log.Printf("[order] mark creation-failed %s: %v", order.OrderUID, ferr)
		}
		return nil, apperr.NewBadGateway("Payment gateway could not create the order; please try again")
	}

	order.Status = models.OrderActive
	if res.Status != "" {
		order.Status = res.Status
	}
	order.CFOrderID = &res.CFOrderID
	order.PaymentSessionID = &res.PaymentSessionID
	order.OrderExpiryTime = res.ExpiryTime
	if err := s.orders.MarkOrderCreated(ctx, order); err != nil {
		return nil, err
	}

	return &PurchaseResult{
		OrderID:          order.OrderUID,
		CFOrderID:        res.CFOrderID,
		PaymentSessionID: res.PaymentSessionID,
		Amount:           order.Amount,
		Currency:         order.Currency,
		Status:           order.Status,
		Mode:             s.gateway.Mode(),
	}, nil
}

// GetOrder returns one of the caller's orders. While the order is still
// ACTIVE it re-checks the gateway first, so a missed webhook can't leave a
// paid order looking unpaid (the return_url redirect is never trusted as
// proof of payment — this is).
func (s *OrderService) GetOrder(ctx context.Context, accountID int64, orderUID string) (*models.Order, error) {
	order, err := s.findOwnedOrder(ctx, accountID, orderUID)
	if err != nil {
		return nil, err
	}

	if (order.Status == models.OrderActive || order.Status == models.OrderCreationRequested) &&
		order.CFOrderID != nil {
		if err := s.reconcile(ctx, order); err != nil {
			// Reconciliation is best-effort; return the local state.
			log.Printf("[order] reconcile %s: %v", order.OrderUID, err)
			return order, nil
		}
		return s.findOwnedOrder(ctx, accountID, orderUID)
	}
	return order, nil
}

// ListOrders returns the caller's order history, newest first.
func (s *OrderService) ListOrders(ctx context.Context, accountID int64) ([]models.Order, error) {
	return s.orders.ListOrdersByAccount(ctx, accountID)
}

func (s *OrderService) findOwnedOrder(ctx context.Context, accountID int64, orderUID string) (*models.Order, error) {
	order, err := s.orders.FindOrderByUID(ctx, orderUID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Order not found")
	}
	if err != nil {
		return nil, err
	}
	// Another account's order is a 404, not a 403, to avoid leaking existence.
	if order.AccountID != accountID {
		return nil, apperr.NewNotFound("Order not found")
	}
	return order, nil
}

// reconcile pulls the order state from the gateway and applies it locally.
func (s *OrderService) reconcile(ctx context.Context, order *models.Order) error {
	res, err := s.gateway.GetOrder(ctx, order.OrderUID)
	if err != nil {
		return err
	}
	switch res.Status {
	case models.OrderPaid:
		first, err := s.orders.MarkOrderPaid(ctx, order.OrderUID, nil, nil, time.Now().UTC())
		if err != nil {
			return err
		}
		if first {
			s.fulfillOrder(order)
		}
	case models.OrderExpired, models.OrderTerminated:
		return s.orders.UpdateOrderStatus(ctx, order.OrderUID, res.Status, nil)
	}
	return nil
}

// ---- webhook processing ----------------------------------------------------

// webhookEnvelope is the subset of the Cashfree webhook payload we act on.
type webhookEnvelope struct {
	Type string `json:"type"`
	Data struct {
		Order struct {
			OrderID string `json:"order_id"`
		} `json:"order"`
		Payment struct {
			CFPaymentID    flexString `json:"cf_payment_id"`
			PaymentStatus  string     `json:"payment_status"`
			PaymentMessage string     `json:"payment_message"`
			PaymentGroup   string     `json:"payment_group"`
			PaymentTime    string     `json:"payment_time"`
		} `json:"payment"`
	} `json:"data"`
}

// ProcessWebhook verifies, records, and applies a Cashfree webhook delivery.
// body must be the raw request bytes — the signature is computed over them.
func (s *OrderService) ProcessWebhook(ctx context.Context, timestamp, signature, idempotencyKey string, body []byte) error {
	if !s.gateway.VerifyWebhookSignature(timestamp, body, signature) {
		return apperr.NewUnauthorized("Invalid webhook signature")
	}

	var env webhookEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return apperr.NewValidation("invalid webhook payload")
	}
	if env.Type == "" || strings.EqualFold(env.Type, webhookTestPing) {
		// Dashboard test ping — acknowledge without recording.
		return nil
	}

	event := &models.PaymentWebhookEvent{
		IdempotencyKey: nilIfEmpty(idempotencyKey),
		EventType:      env.Type,
		OrderUID:       nilIfEmpty(env.Data.Order.OrderID),
		Payload:        string(body),
	}
	if err := s.orders.CreateWebhookEvent(ctx, event); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			// Duplicate delivery of an already-recorded webhook.
			return nil
		}
		return err
	}

	if err := s.applyWebhook(ctx, &env); err != nil {
		// Leave processed=false; Cashfree's retry (new idempotency key) or the
		// GetOrder reconciliation path will settle the order.
		return err
	}
	if err := s.orders.MarkWebhookProcessed(ctx, event.ID); err != nil {
		log.Printf("[order] mark webhook %d processed: %v", event.ID, err)
	}
	return nil
}

func (s *OrderService) applyWebhook(ctx context.Context, env *webhookEnvelope) error {
	orderUID := env.Data.Order.OrderID
	if orderUID == "" {
		log.Printf("[order] webhook %s without order_id; ignoring", env.Type)
		return nil
	}
	order, err := s.orders.FindOrderByUID(ctx, orderUID)
	if errors.Is(err, repository.ErrNotFound) {
		log.Printf("[order] webhook %s for unknown order %s; ignoring", env.Type, orderUID)
		return nil
	}
	if err != nil {
		return err
	}

	switch strings.ToUpper(env.Type) {
	case webhookPaymentSuccess:
		paidAt := time.Now().UTC()
		if t, perr := time.Parse(time.RFC3339, env.Data.Payment.PaymentTime); perr == nil {
			paidAt = t
		}
		first, err := s.orders.MarkOrderPaid(ctx, orderUID,
			nilIfEmpty(string(env.Data.Payment.CFPaymentID)),
			nilIfEmpty(env.Data.Payment.PaymentGroup), paidAt)
		if err != nil {
			return err
		}
		if first {
			s.fulfillOrder(order)
		}
	case webhookPaymentFailed:
		return s.orders.UpdateOrderStatus(ctx, orderUID, models.OrderFailed,
			nilIfEmpty(env.Data.Payment.PaymentMessage))
	case webhookUserDropped:
		// The customer abandoned checkout; the order stays ACTIVE and can be
		// retried until it expires.
	default:
		log.Printf("[order] unhandled webhook type %s for order %s", env.Type, orderUID)
	}
	return nil
}

// fulfillOrder grants what was bought. Entitlement creation for the analysis
// products is not built yet, so for now the paid transition stamps
// fulfilled_at and this hook just logs; wire the real grant here.
func (s *OrderService) fulfillOrder(order *models.Order) {
	log.Printf("[order] fulfilled %s: account %d purchased %s",
		order.OrderUID, order.AccountID, order.ProductCode)
}

// ---- helpers ----------------------------------------------------------------

// buildReturnURL substitutes the {order_id} placeholder, or appends an
// order_id query param when no placeholder is present.
func buildReturnURL(base, orderUID string) string {
	if base == "" {
		return ""
	}
	if strings.Contains(base, "{order_id}") {
		return strings.ReplaceAll(base, "{order_id}", orderUID)
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "order_id=" + orderUID
}

// customerPhone returns the account's phone; Cashfree requires one, so a
// placeholder stands in for accounts that haven't added a phone yet.
func customerPhone(a *models.Account) string {
	if a.PrimaryPhone != nil && *a.PrimaryPhone != "" {
		return *a.PrimaryPhone
	}
	return "9999999999"
}

func customerName(a *models.Account) string {
	name := strings.TrimSpace(strDeref(a.FirstName) + " " + strDeref(a.LastName))
	return name
}

func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// flexString tolerates a JSON field arriving as either a string or a number
// (Cashfree's cf_payment_id has changed types across API versions).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	*f = flexString(string(b))
	return nil
}
