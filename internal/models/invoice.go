package models

import "time"

// Auto-email states of an invoice (invoices.auto_email). See migration 0032.
const (
	InvoiceEmailPending   = "PENDING"
	InvoiceEmailSent      = "SENT"
	InvoiceEmailNoAddress = "NO_ADDRESS"
	InvoiceEmailFailed    = "FAILED"
	InvoiceEmailCancelled = "CANCELLED"
)

// Kinds of invoice email (invoice_email_sends.kind).
const (
	InvoiceSendAuto    = "AUTO"     // on payment, to the account's address
	InvoiceSendAccount = "ACCOUNT"  // asked for, to the account's address
	InvoiceSendOneTime = "ONE_TIME" // asked for, to an address the account does not hold
)

// Invoice is one issued invoice: the row is the record, the PDF a rendering of
// it. Every money field is paise. See migration 0032 for what each column
// promises and why the row is never edited after issue.
type Invoice struct {
	ID        int64     `db:"id"`
	Number    string    `db:"invoice_number"`
	Specimen  bool      `db:"specimen"`
	OrderID   *int64    `db:"order_id"`
	OrderUID  string    `db:"order_uid"`
	AccountID int64     `db:"account_id"`
	IssuedAt  time.Time `db:"issued_at"`

	ProductCode string     `db:"product_code"`
	ValidUntil  *time.Time `db:"valid_until"`

	Currency       string  `db:"currency"`
	TotalPaise     int64   `db:"total_paise"`
	TaxablePaise   int64   `db:"taxable_paise"`
	CGSTPaise      int64   `db:"cgst_paise"`
	SGSTPaise      int64   `db:"sgst_paise"`
	IGSTPaise      int64   `db:"igst_paise"`
	GSTRatePercent float64 `db:"gst_rate_percent"`
	ListPricePaise int64   `db:"list_price_paise"`
	DiscountPaise  int64   `db:"discount_paise"`
	CouponCode     *string `db:"coupon_code"`

	PlaceOfSupply     string `db:"place_of_supply"`
	PlaceOfSupplyCode string `db:"place_of_supply_code"`

	SupplierName    string  `db:"supplier_name"`
	SupplierAddress string  `db:"supplier_address"`
	SupplierGSTIN   *string `db:"supplier_gstin"`
	SAC             *string `db:"sac"`

	BilledToName  *string `db:"billed_to_name"`
	BilledToEmail *string `db:"billed_to_email"`
	BilledToPhone *string `db:"billed_to_phone"`

	// Details is the JSON-encoded InvoiceDetails. Kept as bytes so the
	// repository does not have to care what the design puts in it.
	Details []byte `db:"details"`

	PaymentLabel         *string `db:"payment_label"`
	PaymentRefLabel      *string `db:"payment_ref_label"`
	PaymentRef           *string `db:"payment_ref"`
	PaymentFetchAttempts int     `db:"payment_fetch_attempts"`

	PDFURI *string `db:"pdf_uri"`

	AutoEmail         string     `db:"auto_email"`
	AutoEmailAttempts int        `db:"auto_email_attempts"`
	NextAttemptAt     *time.Time `db:"next_attempt_at"`
	LastError         *string    `db:"last_error"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// InvoiceDetails is the presentation snapshot: what the plan card on the
// invoice said, as the catalog read at issue.
type InvoiceDetails struct {
	PlanName   string        `json:"planName"`
	Badge      string        `json:"badge,omitempty"`
	Tagline    string        `json:"tagline,omitempty"`
	PriceLabel string        `json:"priceLabel"`
	Features   []string      `json:"features,omitempty"`
	Chips      []InvoiceChip `json:"chips,omitempty"`
}

// InvoiceChip is one pill under the plan card. Saving selects the amber style.
type InvoiceChip struct {
	Text   string `json:"text"`
	Saving bool   `json:"saving,omitempty"`
}

// InvoiceSummary is what an order carries about its invoice in the orders API:
// enough for My Purchases and the payment-success screen to show without
// fetching the document.
type InvoiceSummary struct {
	Number   string    `json:"number"`
	IssuedAt time.Time `json:"issuedAt"`
	// Specimen is a sandbox order's invoice: not a tax invoice, and the app
	// says so rather than showing it as one.
	Specimen bool    `json:"specimen"`
	PaidVia  *string `json:"paidVia,omitempty"`
	// ValidUntil is a plan's last covered day, YYYY-MM-DD.
	ValidUntil *string `json:"validUntil,omitempty"`
}
