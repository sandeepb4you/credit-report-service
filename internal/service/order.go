package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
	"credit-report-service/internal/payments"
	"credit-report-service/internal/repository"
)

// OrderService implements the purchase flow: create a local order, open the
// matching Razorpay order, and settle the outcome from webhooks (with
// on-demand reconciliation against the gateway as backup).
type OrderService struct {
	orders   *repository.OrderRepo
	accounts *repository.AccountRepo
	coupons  *CouponService
	gateway  payments.Gateway
	cfg      config.RazorpayConfig
	// testGateway is the sandbox gateway internal builds pay through, and
	// testModeKey the secret they present to reach it (SetTestPayments). Nil /
	// empty means test payments are off. In a deployment whose default mode is
	// already sandbox, testGateway is the same gateway as the default one.
	testGateway payments.Gateway
	testModeKey string
	// scheduled is where a plan purchase's batch of prepaid report runs is
	// minted at fulfilment. A constructor argument like the analytics service's
	// order repo, and for the same reason: forgetting to wire it would mean
	// plans that take money and schedule nothing.
	scheduled *repository.ScheduledCheckRepo
	// scheduleLoc is the business day boundary for due dates (see
	// config.ScheduledChecksConfig.Timezone).
	scheduleLoc *time.Location
	// earnings credits the referrer when a referred account's first order is
	// paid. Interface (not *EarningsService) so tests that never buy can pass
	// nil; fulfilment guards it.
	earnings earningsCrediter
	// invoices issues the tax invoice at fulfilment and describes it on the
	// orders API. Optional like earnings: nil means no invoicing (tests that
	// never look at one), and every use guards it.
	invoices invoiceIssuer
}

// invoiceIssuer is what OrderService needs from InvoiceService.
type invoiceIssuer interface {
	Issue(ctx context.Context, order *models.Order, product *models.Product,
		pd *payments.PaymentDetails) (*models.Invoice, error)
	Summaries(ctx context.Context, accountID int64) (map[int64]*models.InvoiceSummary, error)
	SummaryForOrder(ctx context.Context, orderID int64) *models.InvoiceSummary
}

// earningsCrediter is the one OrderService method the referral-money feature
// needs: credit whoever referred this buyer, once, on their first purchase.
type earningsCrediter interface {
	CreditForFirstPurchase(ctx context.Context, buyerAccountID int64, orderUID string) (bool, error)
}

func NewOrderService(
	orders *repository.OrderRepo,
	accounts *repository.AccountRepo,
	coupons *CouponService,
	gateway payments.Gateway,
	cfg config.RazorpayConfig,
	scheduled *repository.ScheduledCheckRepo,
	scheduleLoc *time.Location,
	earnings earningsCrediter,
) *OrderService {
	return &OrderService{
		orders: orders, accounts: accounts, coupons: coupons, gateway: gateway, cfg: cfg,
		scheduled: scheduled, scheduleLoc: scheduleLoc, earnings: earnings,
	}
}

// SetInvoices wires invoicing into fulfilment and the orders API.
func (s *OrderService) SetInvoices(inv invoiceIssuer) { s.invoices = inv }

// PaymentDetails reads the successful payment on an order from the Razorpay
// environment that took it: the invoice's payment lines. Implements the
// invoice service's paymentLookup.
func (s *OrderService) PaymentDetails(ctx context.Context, order *models.Order) (*payments.PaymentDetails, error) {
	gateway := s.gatewayFor(order.PaymentMode)
	if gateway == nil {
		return nil, fmt.Errorf("no %q gateway configured for order %s", order.PaymentMode, order.OrderUID)
	}
	if order.GatewayOrderID == nil || *order.GatewayOrderID == "" {
		return nil, fmt.Errorf("order %s was never registered with the gateway", order.OrderUID)
	}
	return gateway.GetPayment(ctx, *order.GatewayOrderID)
}

// SetTestPayments wires the sandbox gateway that internal builds pay through,
// and the key they must present to use it. See config.RazorpayConfig.TestModeKey
// for why this is a key rather than a flag the app could simply send.
func (s *OrderService) SetTestPayments(gateway payments.Gateway, key string) {
	s.testGateway = gateway
	s.testModeKey = key
}

// gatewayFor returns the gateway for an environment, or nil if none is
// configured for it. Every operation on an existing order goes through this
// with the order's own PaymentMode, never with the server's current default:
// an order is settled by the environment that created it.
func (s *OrderService) gatewayFor(mode string) payments.Gateway {
	if s.gateway != nil && s.gateway.Mode() == mode {
		return s.gateway
	}
	if s.testGateway != nil && s.testGateway.Mode() == mode {
		return s.testGateway
	}
	return nil
}

// gatewayForNewOrder picks the environment a new order is created in.
//
// No key: the deployment's default -- live, for the store app and the web.
// The right key: the sandbox gateway. A WRONG key is refused rather than
// quietly treated as live: a key is only ever sent by an internal build, and
// a misbuilt one that fell through to live would charge a tester real money
// for a purchase they believe is a test.
func (s *OrderService) gatewayForNewOrder(testKey string) (payments.Gateway, error) {
	if testKey == "" {
		return s.gateway, nil
	}
	if s.testModeKey == "" ||
		subtle.ConstantTimeCompare([]byte(testKey), []byte(s.testModeKey)) != 1 {
		slog.Warn("order refused: test-payments key not recognised")
		return nil, apperr.NewForbidden(
			"This build's test-payments key is not recognised. Install the latest internal build.")
	}
	if s.testGateway == nil {
		return nil, apperr.NewServiceUnavailable("Test payments are not configured on this server")
	}
	return s.testGateway, nil
}

// PurchaseResult is returned from CreateOrder: everything the app needs to
// open Razorpay's checkout for this order.
type PurchaseResult struct {
	OrderID string `json:"orderId"`
	// Gateway names the checkout to open. Always "razorpay"; sent so a client
	// can refuse an order it does not know how to pay rather than guess.
	Gateway string `json:"gateway"`
	// GatewayOrderID is Razorpay's order id, the checkout's order_id.
	GatewayOrderID string `json:"gatewayOrderId"`
	// KeyID is the public key the checkout is opened with. It is what selects
	// the Razorpay environment on the client, so it comes from the gateway
	// that created THIS order — an internal build gets the test key.
	KeyID string `json:"keyId"`
	// AmountMinor is Amount in paise, the unit the checkout takes, computed
	// here so the client never rounds a float.
	AmountMinor int64 `json:"amountMinor"`
	// CheckoutName and Description are the checkout header's two lines.
	CheckoutName string `json:"checkoutName"`
	Description  string `json:"description"`
	// Prefill is the caller's own contact details, so the checkout does not
	// ask for what the account already knows. Fields are omitted rather than
	// faked: a placeholder phone here would be offered as the payer's number.
	Prefill CheckoutPrefill `json:"prefill"`
	// Amount is the charged total, already net of any coupon. OriginalAmount
	// and DiscountAmount let the payment screen show the saving without
	// recomputing it — and without being trusted to.
	Amount         float64 `json:"amount"`
	OriginalAmount float64 `json:"originalAmount"`
	DiscountAmount float64 `json:"discountAmount"`
	CouponCode     *string `json:"couponCode,omitempty"`
	Currency       string  `json:"currency"`
	Status         string  `json:"status"`
	Mode           string  `json:"mode"`
}

// CheckoutPrefill is the checkout form's prefill. Only the caller's own data.
type CheckoutPrefill struct {
	Name    string `json:"name,omitempty"`
	Email   string `json:"email,omitempty"`
	Contact string `json:"contact,omitempty"`
}

// ListProducts returns the purchasable catalog.
func (s *OrderService) ListProducts(ctx context.Context) ([]models.Product, error) {
	return s.orders.ListActiveProducts(ctx)
}

// CreateOrder starts a purchase: snapshots the product price into a local
// order row, opens the Razorpay order, and returns what the frontend needs to
// launch checkout.
// An optional couponCode discounts the price. The discount is computed here
// from the catalog price and the coupon's stored percentage — the client sends
// only the code, never an amount — and the redemption is committed in the same
// transaction as the order, so a coupon can never be consumed without an order
// to show for it.
// ListAllPlans returns the whole catalog, retired plans included, for admin.
func (s *OrderService) ListAllPlans(ctx context.Context) ([]models.Product, error) {
	return s.orders.ListAllProducts(ctx)
}

// UpdatePlan changes a plan's price or availability. Both fields are optional;
// nil leaves the stored value alone.
//
// Deactivating is the supported way to retire a plan, and it is a real removal
// rather than a hide: ListActiveProducts stops advertising it AND CreateOrder
// refuses it, so a client holding the code cannot buy it either.
//
// A price change is deliberately NOT retroactive. Existing orders keep the
// amount they were created with — orders snapshot their own price — so this can
// never alter what someone has already been charged or is midway through paying.
func (s *OrderService) UpdatePlan(
	ctx context.Context, code string, edit repository.ProductEdit,
) (*models.Product, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"code": "code is required"})
	}
	amount, active := edit.Amount, edit.Active
	if amount == nil && active == nil && edit.Description == nil &&
		edit.Badge == nil && edit.Tagline == nil && edit.SortOrder == nil {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"amount": "provide at least one field to change"})
	}
	// Presentation copy is short by design — it is a pill and a single line on a
	// phone card. Bounds here, not in the column, so the message names the field.
	if edit.Badge != nil {
		b := strings.TrimSpace(*edit.Badge)
		if len(b) > 32 {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"badge": "at most 32 characters"})
		}
		edit.Badge = &b
	}
	if edit.Tagline != nil {
		t := strings.TrimSpace(*edit.Tagline)
		if len(t) > 80 {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"tagline": "at most 80 characters"})
		}
		edit.Tagline = &t
	}
	if edit.SortOrder != nil && (*edit.SortOrder < 0 || *edit.SortOrder > 10000) {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"sortOrder": "must be between 0 and 10000"})
	}
	if amount != nil {
		// A negative price would be a refund the gateway cannot express, and NaN
		// or Inf would reach the database as a value no currency column can hold.
		if math.IsNaN(*amount) || math.IsInf(*amount, 0) || *amount < 0 {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"amount": "must be a non-negative number"})
		}
	}
	p, err := s.orders.UpdateProduct(ctx, code, edit)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Plan not found")
	}
	if err != nil {
		return nil, err
	}
	slog.Info("plan updated", "code", p.Code, "amount", p.Amount, "active", p.Active)
	return p, nil
}

// testKey is the internal build's test-payments key, or "" -- see
// gatewayForNewOrder. It is resolved before anything is written, so a refused
// key leaves no order and claims no coupon.
func (s *OrderService) CreateOrder(
	ctx context.Context, accountID int64, productCode, couponCode, testKey string,
) (*PurchaseResult, error) {
	gateway, err := s.gatewayForNewOrder(testKey)
	if err != nil {
		return nil, err
	}

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

	// One plan batch at a time. Two overlapping cadences would race each other's
	// re-anchoring and read as double-billing; a user who wants more checks
	// sooner can still buy one-time checks (which never touch the schedule).
	// Checked at purchase rather than fulfilment because refusing money is
	// kinder than refunding it.
	if product.IsPlan() {
		hasPending, perr := s.scheduled.HasPending(ctx, accountID)
		if perr != nil {
			return nil, perr
		}
		if hasPending {
			return nil, apperr.NewConflict(
				"You already have a plan with scheduled checks remaining. You can buy a new plan once they finish.")
		}
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
		PaymentMode: gateway.Mode(),
	}
	if couponCode = strings.TrimSpace(couponCode); couponCode == "" {
		if err := s.orders.CreateOrder(ctx, order); err != nil {
			return nil, err
		}
	} else if err := s.createDiscountedOrder(ctx, order, product, couponCode); err != nil {
		return nil, err
	}

	res, err := gateway.CreateOrder(ctx, payments.CreateOrderParams{
		OrderID:   order.OrderUID,
		Amount:    order.Amount,
		Currency:  order.Currency,
		OrderNote: product.Name,
	})
	if err != nil {
		log.Printf("[order] razorpay create failed for %s: %v", order.OrderUID, err)
		if ferr := s.orders.MarkOrderCreationFailed(ctx, order.OrderUID, err.Error()); ferr != nil {
			log.Printf("[order] mark creation-failed %s: %v", order.OrderUID, ferr)
		}
		// The order will never be paid, so the coupon slot goes back — otherwise
		// a flaky gateway would burn a limited-use code for nothing.
		s.coupons.Release(ctx, order.OrderUID)
		return nil, apperr.NewBadGateway("Payment gateway could not create the order; please try again")
	}

	order.Status = models.OrderActive
	if res.Status != "" {
		order.Status = res.Status
	}
	order.GatewayOrderID = &res.GatewayOrderID
	order.OrderExpiryTime = res.ExpiryTime
	if err := s.orders.MarkOrderCreated(ctx, order); err != nil {
		return nil, err
	}

	return &PurchaseResult{
		OrderID:        order.OrderUID,
		Gateway:        "razorpay",
		GatewayOrderID: res.GatewayOrderID,
		KeyID:          gateway.KeyID(),
		AmountMinor:    payments.ToMinorUnits(order.Amount),
		CheckoutName:   s.cfg.CheckoutName,
		Description:    product.Name,
		Prefill: CheckoutPrefill{
			Name:    customerName(account),
			Email:   strDeref(account.PrimaryEmail),
			Contact: strDeref(account.PrimaryPhone),
		},
		Amount:         order.Amount,
		OriginalAmount: order.Amount + order.DiscountAmount,
		DiscountAmount: order.DiscountAmount,
		CouponCode:     order.CouponCode,
		Currency:       order.Currency,
		Status:         order.Status,
		// The ORDER's environment, the same one KeyID belongs to -- which is what
		// makes an internal build open the test checkout without being built
		// any differently.
		Mode: gateway.Mode(),
	}, nil
}

// createDiscountedOrder claims the coupon, prices the order from it, and
// inserts both in one transaction.
//
// Everything that can reject the coupon happens before the commit, so a
// rejected code leaves no order and no reservation behind. The reverse — order
// committed, redemption lost — is what the shared transaction rules out.
func (s *OrderService) createDiscountedOrder(
	ctx context.Context, order *models.Order, product *models.Product, couponCode string,
) error {
	tx, err := s.orders.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	coupon, discount, payable, err := s.coupons.ClaimForOrder(
		ctx, tx, couponCode, order.AccountID, product)
	if err != nil {
		return err
	}

	order.Amount = payable
	order.DiscountAmount = discount
	order.CouponCode = &coupon.Code

	if err := s.orders.CreateOrderTx(ctx, tx, order); err != nil {
		return err
	}
	if err := s.coupons.RecordRedemption(
		ctx, tx, coupon.ID, order.AccountID, order.OrderUID, discount); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	log.Printf("[order] %s priced with coupon %s: %.2f -> %.2f",
		order.OrderUID, coupon.Code, product.Amount, payable)
	return nil
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
		order.GatewayOrderID != nil {
		if err := s.reconcile(ctx, order); err != nil {
			// Reconciliation is best-effort; return the local state.
			log.Printf("[order] reconcile %s: %v", order.OrderUID, err)
			return order, nil
		}
		order, err = s.findOwnedOrder(ctx, accountID, orderUID)
		if err != nil {
			return nil, err
		}
	} else if order.Status == models.OrderPaid {
		s.ensurePlanFulfilled(ctx, order)
	}
	// The payment-success screen reads the invoice number off this response,
	// so it is attached here too, not only on the list.
	if order.Status == models.OrderPaid && s.invoices != nil {
		order.Invoice = s.invoices.SummaryForOrder(ctx, order.ID)
	}
	return order, nil
}

// ensurePlanFulfilled re-runs the schedule mint for a PAID plan order whose
// batch is missing. The gap it closes: fulfilOrder runs exactly once (on the
// first PAID transition) and its mint is best-effort, so a DB error there
// would leave money taken and nothing scheduled with no retry path — webhook
// redeliveries never reach fulfilment again because MarkOrderPaid's status
// guard reports them as not-first. The app polls this endpoint right after
// checkout, which makes it the natural self-heal hook. One EXISTS per poll of
// a healthy paid order; Mint's ON CONFLICT makes a racing double-heal inert.
func (s *OrderService) ensurePlanFulfilled(ctx context.Context, order *models.Order) {
	minted, err := s.scheduled.HasForOrder(ctx, order.ID)
	if err != nil || minted {
		return
	}
	product, err := s.orders.FindProduct(ctx, order.ProductCode)
	if err != nil || !product.IsPlan() {
		return
	}
	slog.Warn("plan fulfilment self-heal: PAID plan order had no scheduled checks; minting now",
		"order_uid", order.OrderUID, "account_id", order.AccountID)
	s.fulfillOrder(ctx, order, nil)
}

// ListOrders returns the caller's order history, newest first, each paid order
// carrying its invoice summary and what it still entitles the account to.
//
// Both enrichments are best-effort: My Purchases without chips or invoice
// numbers is still a correct list of purchases, and failing the whole history
// because a side query did not answer would not be.
func (s *OrderService) ListOrders(ctx context.Context, accountID int64) ([]models.Order, error) {
	list, err := s.orders.ListOrdersByAccount(ctx, accountID)
	if err != nil || len(list) == 0 {
		return list, err
	}
	var invoices map[int64]*models.InvoiceSummary
	if s.invoices != nil {
		if invoices, err = s.invoices.Summaries(ctx, accountID); err != nil {
			slog.Warn("orders: invoice summaries unavailable", "account_id", accountID, "error", err)
		}
	}
	products := map[string]*models.Product{}
	if all, err := s.orders.ListAllProducts(ctx); err == nil {
		for i := range all {
			products[all[i].Code] = &all[i]
		}
	}
	remaining, err := s.scheduled.RemainingByOrder(ctx, accountID)
	if err != nil {
		slog.Warn("orders: plan runs unavailable", "account_id", accountID, "error", err)
	}
	for i := range list {
		o := &list[i]
		if o.Status != models.OrderPaid {
			continue
		}
		o.Invoice = invoices[o.ID]
		o.Entitlement = entitlementOf(o, products[o.ProductCode], remaining)
	}
	return list, nil
}

// entitlementOf is what a paid order still gives. Empty when it cannot be told
// (a product the catalog no longer lists, or the plan-run query failed): the
// app then draws no chip, rather than a wrong one.
func entitlementOf(o *models.Order, p *models.Product, remaining map[int64]int) string {
	if p == nil {
		return ""
	}
	if p.IsPlan() {
		if remaining == nil {
			return ""
		}
		if remaining[o.ID] > 0 {
			return models.EntitlementActive
		}
		return models.EntitlementEnded
	}
	if o.ConsumedAt != nil {
		return models.EntitlementUsed
	}
	return models.EntitlementUnused
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
//
// Only PAID moves anything. A Razorpay order has no expiry and no terminal
// failure — after a declined attempt the same order can still be paid — so
// "not paid yet" is the only other answer, and it leaves the order as it is.
func (s *OrderService) reconcile(ctx context.Context, order *models.Order) error {
	gateway := s.gatewayFor(order.PaymentMode)
	if gateway == nil {
		return fmt.Errorf("no %q gateway configured to reconcile order %s",
			order.PaymentMode, order.OrderUID)
	}
	res, err := gateway.GetOrder(ctx, *order.GatewayOrderID)
	if err != nil {
		return err
	}
	if res.Status != payments.StatusPaid {
		return nil
	}
	// The order endpoint carries no payment record, so the payment is read now
	// while the gateway is in hand: its id and group for the order row, and its
	// description for the invoice. Bounded and best-effort: the app is waiting
	// on this call, and an invoice issued without them reads them again at
	// first render.
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	pd, _ := gateway.GetPayment(pctx, *order.GatewayOrderID)
	cancel()
	var paymentID, group *string
	if pd != nil {
		paymentID, group = nilIfEmpty(pd.PaymentID), nilIfEmpty(pd.Group)
	}
	first, err := s.orders.MarkOrderPaid(ctx, order.OrderUID, paymentID, group, time.Now().UTC())
	if err != nil {
		return err
	}
	if first {
		s.fulfillOrder(ctx, order, pd)
	}
	return nil
}

// ---- webhook processing ----------------------------------------------------

// ProcessWebhook verifies, records, and applies a Razorpay webhook delivery.
// body must be the raw request bytes — the signature is computed over them.
// eventID is Razorpay's X-Razorpay-Event-Id, the same across retries of one
// event, which is what makes a redelivery a no-op.
//
// Razorpay's live and test environments each have their own webhooks, which
// may point at this same URL, each with its own secret. The signature
// therefore says which environment sent the delivery, and applyWebhook refuses
// one whose environment is not the order's.
func (s *OrderService) ProcessWebhook(ctx context.Context, signature, eventID string, body []byte) error {
	mode, ok := s.verifyWebhook(body, signature)
	if !ok {
		return apperr.NewUnauthorized("Invalid webhook signature")
	}

	ev, err := payments.ParseWebhook(body)
	if err != nil {
		return apperr.NewValidation("invalid webhook payload")
	}

	// Find the order before recording, so the stored event names it. The
	// account-deletion scrub finds webhook rows by order_uid, and a
	// payment.failed delivery (which names only Razorpay's order id) stored
	// without one would keep the payer's email, phone and UPI id forever.
	order, err := s.findWebhookOrder(ctx, ev)
	if err != nil {
		return err
	}
	var orderUID *string
	if order != nil {
		orderUID = &order.OrderUID
	}

	event := &models.PaymentWebhookEvent{
		IdempotencyKey: nilIfEmpty(eventID),
		EventType:      ev.Type,
		OrderUID:       orderUID,
		Payload:        string(body),
	}
	if err := s.orders.CreateWebhookEvent(ctx, event); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			// Duplicate delivery of an already-recorded webhook.
			return nil
		}
		return err
	}

	if err := s.applyWebhook(ctx, ev, order, mode); err != nil {
		// Leave processed=false; Razorpay's retry or the GetOrder
		// reconciliation path will settle the order.
		return err
	}
	if err := s.orders.MarkWebhookProcessed(ctx, event.ID); err != nil {
		log.Printf("[order] mark webhook %d processed: %v", event.ID, err)
	}
	return nil
}

// findWebhookOrder resolves a delivery to our order: by the receipt when the
// event carries the order entity, else by Razorpay's order id. Nil (no error)
// for an order this service never created: a delivery about someone else's
// order is not ours to fail on.
func (s *OrderService) findWebhookOrder(ctx context.Context, ev *payments.WebhookEvent) (*models.Order, error) {
	var (
		order *models.Order
		err   error
	)
	switch {
	case ev.OrderUID != "":
		order, err = s.orders.FindOrderByUID(ctx, ev.OrderUID)
	case ev.GatewayOrderID != "":
		order, err = s.orders.FindOrderByGatewayOrderID(ctx, ev.GatewayOrderID)
	default:
		return nil, nil
	}
	if errors.Is(err, repository.ErrNotFound) {
		log.Printf("[order] webhook %s for unknown order (uid %q, gateway %q); ignoring",
			ev.Type, ev.OrderUID, ev.GatewayOrderID)
		return nil, nil
	}
	return order, err
}

// verifyWebhook checks the signature against each configured environment and
// reports which one it belongs to. The default gateway is tried first; a
// deployment in sandbox mode has one gateway and so one answer.
func (s *OrderService) verifyWebhook(body []byte, signature string) (string, bool) {
	for _, gw := range []payments.Gateway{s.gateway, s.testGateway} {
		if gw != nil && gw.VerifyWebhookSignature(body, signature) {
			return gw.Mode(), true
		}
	}
	return "", false
}

func (s *OrderService) applyWebhook(
	ctx context.Context, ev *payments.WebhookEvent, order *models.Order, mode string,
) error {
	if order == nil {
		return nil
	}
	// A sandbox-signed delivery must never settle a live order. Test payments
	// cost nothing to make, so this is the line that keeps a test build from
	// being a way to get paid-for reports free.
	if order.PaymentMode != mode {
		slog.Warn("webhook refused: signed by a different Razorpay environment than the order's",
			"order_uid", order.OrderUID, "order_mode", order.PaymentMode, "webhook_mode", mode)
		return apperr.NewUnauthorized("Webhook environment does not match the order")
	}

	switch ev.Kind {
	case payments.WebhookPaid:
		first, err := s.orders.MarkOrderPaid(ctx, order.OrderUID,
			nilIfEmpty(ev.PaymentID), nilIfEmpty(ev.PaymentGroup), ev.PaidAt)
		if err != nil {
			return err
		}
		if first {
			s.fulfillOrder(ctx, order, ev.Payment)
		}
	case payments.WebhookPaymentFailed:
		// One attempt failed; the order is still payable and the checkout is
		// usually still open for another try. Nothing to change — marking the
		// order FAILED here would tell the app to give up on an order the user
		// may be about to pay.
		log.Printf("[order] payment attempt failed for %s: %s", order.OrderUID, ev.FailureReason)
	default:
		log.Printf("[order] unhandled webhook type %s for order %s", ev.Type, order.OrderUID)
	}
	return nil
}

// fulfillOrder grants what was bought.
//
// For a one-time product there is deliberately no grant to make: the PAID
// order IS the entitlement. A paid order with consumed_at NULL is one unspent
// purchase, and the credit-analytics pull claims it
// (OrderRepo.SpendEntitlement). Deriving it from the order rather than writing
// a separate credit row means the two can never disagree — and a refund or a
// reversal moves the entitlement with the order instead of leaving a granted
// credit stranded behind it.
//
// A PLAN purchase (product.IsPlan) is different: one payment buys a batch of
// N runs on a cadence, so fulfilment materializes the whole schedule as
// scheduled_score_checks rows — row 1 due TODAY (the "first check now"), row i
// due (i-1) intervals later. The runner picks row 1 up within a sweep, or the
// user, sitting in the app right after paying, runs it themselves (a due row
// spends without confirmation). The order itself is never consumable by
// SpendEntitlement — its product code isn't CREDIT_ANALYSIS — so the rows are
// the only entitlement a plan grants, and there is nothing to double-count.
//
// Minting is idempotent two ways: MarkOrderPaid's status guard means only the
// first PAID transition reaches here, and the (order_id, sequence_no) unique
// key means a replayed mint inserts nothing. A mint failure is logged loudly
// rather than failing the webhook: the payment HAS happened, and Razorpay's
// retry (or the reconcile path) re-enters here to try again.
//
// MarkOrderPaid stamps fulfilled_at on the same transition, recording when the
// entitlement was granted; consumed_at records when it was spent.
func (s *OrderService) fulfillOrder(ctx context.Context, order *models.Order, pd *payments.PaymentDetails) {
	log.Printf("[order] fulfilled %s: account %d purchased %s",
		order.OrderUID, order.AccountID, order.ProductCode)

	// Referral credit first, before the plan early-return below: it fires on
	// the FIRST paid order of ANY product, not just plans. Best-effort like
	// the schedule mint — the payment has happened, so a credit failure is
	// logged loudly and retried on the next webhook/reconcile pass (the
	// UNIQUE on referred_account_id makes repeats no-ops).
	if s.earnings != nil && s.creditsReferral(order) {
		if _, err := s.earnings.CreditForFirstPurchase(ctx, order.AccountID, order.OrderUID); err != nil {
			slog.Error("referral credit failed; will retry on next fulfilment pass",
				"order_uid", order.OrderUID, "account_id", order.AccountID, "error", err)
		}
	}

	product, err := s.orders.FindProduct(ctx, order.ProductCode)
	if err != nil {
		slog.Error("plan fulfilment: product lookup failed; scheduled checks NOT minted",
			"order_uid", order.OrderUID, "product_code", order.ProductCode, "error", err)
		return
	}

	// The tax invoice, for every product. Best-effort in the same way as the
	// two grants around it (the money has moved whatever happens here), and
	// Issue is idempotent on the order, so a re-entry returns the invoice
	// already issued. What issue cannot do quickly (render, mail) it hands to
	// the delivery worker; a failure here is picked up by that worker's heal.
	if s.invoices != nil {
		if _, err := s.invoices.Issue(ctx, order, product, pd); err != nil {
			slog.Error("invoice issue failed at fulfilment; the deliverer's heal will retry",
				"order_uid", order.OrderUID, "account_id", order.AccountID, "error", err)
		}
	}

	if !product.IsPlan() {
		return
	}

	n := product.ChecksIncluded
	interval := *product.IntervalMonths
	today := businessToday(s.scheduleLoc)
	dueDates := make([]time.Time, n)
	for i := range dueDates {
		dueDates[i] = today.AddDate(0, interval*i, 0)
	}
	var expiresOn *time.Time
	if product.ValidityDays != nil && *product.ValidityDays > 0 {
		e := today.AddDate(0, 0, *product.ValidityDays)
		expiresOn = &e
	}
	if err := s.scheduled.Mint(ctx, order.AccountID, order.ID, product.Code,
		interval, dueDates, expiresOn); err != nil {
		// The money is taken and the schedule is not there — the one state this
		// feature must never sit in quietly. The webhook retry / reconcile path
		// re-runs the mint (idempotent), but if this line is in the log more
		// than transiently, someone paid for checks that aren't scheduled.
		slog.Error("plan fulfilment: minting scheduled checks FAILED",
			"order_uid", order.OrderUID, "account_id", order.AccountID,
			"product_code", product.Code, "checks", n, "error", err)
		return
	}
	slog.Info("plan fulfilled: scheduled checks minted",
		"order_uid", order.OrderUID, "account_id", order.AccountID,
		"product_code", product.Code, "checks", n, "interval_months", interval,
		"first_due_on", today.Format("2006-01-02"))
}

// creditsReferral reports whether a paid order should earn its buyer's referrer
// the reward. The reward is real money paid out by bank transfer, so it follows
// real money in: a sandbox order in a LIVE deployment is a tester's purchase and
// earns nothing, or a test build becomes a way to farm referral payouts. While
// the whole deployment is still in sandbox there is no real money anywhere, and
// crediting keeps the referral flow testable end to end.
func (s *OrderService) creditsReferral(order *models.Order) bool {
	if order.PaymentMode == "production" {
		return true
	}
	return s.gateway != nil && s.gateway.Mode() == "sandbox"
}

// businessToday is the current date in the business timezone, carried as a
// midnight-UTC time.Time so a DATE column receives exactly that day whatever
// the driver does with zones.
func businessToday(loc *time.Location) time.Time {
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ---- helpers ----------------------------------------------------------------

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
