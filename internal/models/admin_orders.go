package models

import "time"

// AdminOrderRow is one line of the admin purchases list: what was bought, by
// whom, for how much, and where the payment and the entitlement stand.
//
// Like AdminAccountRow, the buyer's contact details are NOT masked: the
// console is web-only and permission-gated, and a purchase is looked up here
// precisely when a customer is on the phone about it.
type AdminOrderRow struct {
	// OrderID is the public order uid — the receipt id the customer sees and
	// the one sent to Razorpay as the receipt.
	OrderID   string `json:"orderId"   db:"order_uid"`
	AccountID int64  `json:"accountId" db:"account_id"`
	// BuyerName is first+last trimmed, empty when the profile has none yet —
	// which is common, because PAN verification (which fills the name) runs
	// after payment.
	BuyerName string  `json:"buyerName" db:"buyer_name"`
	Phone     *string `json:"phone"     db:"phone"`
	Email     *string `json:"email"     db:"email"`

	ProductCode string `json:"productCode" db:"product_code"`
	// ProductName is the catalog's current name, nil for a code the catalog no
	// longer lists.
	ProductName *string `json:"productName" db:"product_name"`

	// Amount is what was charged, already net of any coupon; DiscountAmount
	// and CouponCode are how it got there (snapshots on the order).
	Amount         float64 `json:"amount"         db:"amount"`
	DiscountAmount float64 `json:"discountAmount" db:"discount_amount"`
	CouponCode     *string `json:"couponCode"     db:"coupon_code"`
	Currency       string  `json:"currency"       db:"currency"`

	Status string `json:"status" db:"status"`
	// PaymentMode is "production" or "sandbox": a sandbox order is a test
	// purchase from an internal build and took no money.
	PaymentMode   string  `json:"paymentMode"   db:"payment_mode"`
	PaymentMethod *string `json:"paymentMethod" db:"payment_method"`
	// The gateway's ids. Razorpay's live in columns still named for Cashfree;
	// see Order.
	GatewayOrderID   *string `json:"gatewayOrderId"   db:"cf_order_id"`
	GatewayPaymentID *string `json:"gatewayPaymentId" db:"cf_payment_id"`
	FailureReason    *string `json:"failureReason"    db:"failure_reason"`

	CreatedAt time.Time  `json:"createdAt" db:"created_at"`
	PaidAt    *time.Time `json:"paidAt"    db:"paid_at"`

	// InvoiceNumber is nil until an invoice is issued (and stays nil for a
	// live order placed before a GSTIN was configured).
	InvoiceNumber *string `json:"invoiceNumber" db:"invoice_number"`
	// Entitlement is what a PAID order still gives — the same four states as
	// the customer's own orders API (Order.Entitlement) — and nil for any other
	// status or a product the catalog no longer lists.
	Entitlement *string `json:"entitlement" db:"entitlement"`
}

// AdminOrderSummary totals the WHOLE filtered set, not the page: the figures
// an operator reads the screen for ("how much came in last week") must not
// change when they turn to page 2.
//
// The money is PAID orders only, whatever the status filter says: an unpaid
// order has a price, not revenue. Currency is assumed single (INR); every
// product in the catalog is priced in it.
type AdminOrderSummary struct {
	// Orders is every row the filter matched; Paid the PAID ones among them.
	Orders int `json:"orders" db:"orders"`
	Paid   int `json:"paid"   db:"paid"`
	// Collected is what PAID orders were charged, net of coupons; Discounts is
	// what coupons took off those same orders.
	Collected float64 `json:"collected" db:"collected"`
	Discounts float64 `json:"discounts" db:"discounts"`
	// Buyers counts distinct accounts with a PAID order in the set.
	Buyers int `json:"buyers" db:"buyers"`
}

// AdminOrderPage is a window of the purchases list, the total it was cut from,
// and the summary of that total.
type AdminOrderPage struct {
	Items   []AdminOrderRow   `json:"items"`
	Total   int               `json:"total"`
	Limit   int               `json:"limit"`
	Offset  int               `json:"offset"`
	Summary AdminOrderSummary `json:"summary"`
}

// The two values orders.payment_mode holds (migration 0031). Named here because
// the purchases list filters on them; the gateway code predates the names and
// still spells them out.
const (
	PaymentModeProduction = "production"
	PaymentModeSandbox    = "sandbox"
)

// Status groups the purchases list filters by. The console offers three tabs
// rather than the seven raw statuses, because the operator's question is
// "did it pay, is it still open, or did it die" — not which of four ways it
// died. Each group maps to raw statuses in OrderStatusGroups.
const (
	OrderGroupPaid    = "paid"
	OrderGroupPending = "pending"
	OrderGroupFailed  = "failed"
)

// OrderStatusGroups maps each group to the stored statuses it covers. Every
// status in the order lifecycle is in exactly one group, so the three tabs add
// up to "All".
var OrderStatusGroups = map[string][]string{
	OrderGroupPaid:    {OrderPaid},
	OrderGroupPending: {OrderActive, OrderCreationRequested},
	OrderGroupFailed:  {OrderFailed, OrderCreationFailed, OrderExpired, OrderTerminated},
}

// OrderSortColumn is a column the purchases list can be ordered by. A closed
// set for AccountSortColumn's reason: the value reaches an ORDER BY.
type OrderSortColumn string

const (
	OrderSortCreated OrderSortColumn = "created"
	OrderSortPaid    OrderSortColumn = "paid"
	OrderSortAmount  OrderSortColumn = "amount"
	OrderSortProduct OrderSortColumn = "product"
	OrderSortBuyer   OrderSortColumn = "buyer"
	OrderSortStatus  OrderSortColumn = "status"

	// OrderSortDefault is newest order first.
	OrderSortDefault = OrderSortCreated
)

var orderSortSQL = map[OrderSortColumn]string{
	OrderSortCreated: "created_at",
	OrderSortPaid:    "paid_at",
	OrderSortAmount:  "amount",
	OrderSortProduct: "product_code",
	OrderSortBuyer:   "buyer_name",
	OrderSortStatus:  "status",
}

// ParseOrderSort resolves a client's sort key, defaulting when it is absent.
func ParseOrderSort(raw string) (OrderSortColumn, bool) {
	if raw == "" {
		return OrderSortDefault, true
	}
	c := OrderSortColumn(raw)
	if _, ok := orderSortSQL[c]; !ok {
		return "", false
	}
	return c, true
}

// SQL renders the ORDER BY fragment. NULLS LAST both ways, for the reason
// AccountSortColumn.SQL gives: "most recently paid" must open on payments, not
// on every order that never paid.
func (c OrderSortColumn) SQL(desc bool) string {
	col, ok := orderSortSQL[c]
	if !ok {
		col = orderSortSQL[OrderSortDefault]
	}
	if desc {
		return col + " DESC NULLS LAST"
	}
	return col + " ASC NULLS LAST"
}

// SortableOrderColumns lists every accepted sort key, for the 400 message.
func SortableOrderColumns() []string {
	return []string{
		string(OrderSortCreated), string(OrderSortPaid), string(OrderSortAmount),
		string(OrderSortProduct), string(OrderSortBuyer), string(OrderSortStatus),
	}
}
