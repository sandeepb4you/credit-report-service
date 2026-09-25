// Package service — tax invoices.
//
// An invoice is issued at the moment an order first turns PAID (fulfilOrder),
// numbered from a gapless per-financial-year series, and snapshotted whole into
// its row: what was sold, for how much, the tax split, the supplier and the
// recipient as they were at issue. The PDF is a rendering of that row by the
// same headless-Chromium sidecar as the advanced report, and can be rebuilt
// from it at any time — which is why downloads and emails re-render rather than
// trusting a stored file to still match.
//
// Delivery follows the two design flows (mypurchases-without-with-email.html):
// an account with an email gets the invoice mailed automatically, by the
// InvoiceDeliverer, within seconds of paying; one without gets nothing sent
// and downloads it, or asks for it by email, from My Purchases.
package service

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
	"credit-report-service/internal/payments"
	"credit-report-service/internal/repository"
)

//go:embed templates/invoice.html
var invoiceHTML string

//go:embed templates/fonts/*.woff2
var invoiceFonts embed.FS

var invoiceTmpl = template.Must(template.New("invoice").Parse(invoiceHTML))

// invoiceFontCSS is the @font-face block, built once. The fonts travel inside
// the document as data URIs so the renderer needs no fonts and no network; see
// templates/fonts/README.md for where each file comes from.
var invoiceFontCSS = func() template.CSS {
	faces := []struct {
		family string
		weight int
		file   string
	}{
		{"Inter", 400, "inter-400.woff2"},
		{"Inter", 600, "inter-600.woff2"},
		{"Inter", 700, "inter-700.woff2"},
		{"Bricolage Grotesque", 700, "bricolage-700.woff2"},
		{"Bricolage Grotesque", 800, "bricolage-800.woff2"},
		{"IBM Plex Mono", 400, "plexmono-400.woff2"},
		{"IBM Plex Mono", 600, "plexmono-600.woff2"},
	}
	var b strings.Builder
	for _, f := range faces {
		data, err := invoiceFonts.ReadFile("templates/fonts/" + f.file)
		if err != nil {
			panic(fmt.Sprintf("invoice font %s: %v", f.file, err))
		}
		fmt.Fprintf(&b, "@font-face{font-family:'%s';font-weight:%d;src:url(data:font/woff2;base64,%s) format('woff2')}\n",
			f.family, f.weight, base64.StdEncoding.EncodeToString(data))
	}
	return template.CSS(b.String())
}()

// specimenSeries numbers sandbox orders' invoices, apart from the tax series.
// Three letters, like the tax series, to stay within sixteen characters.
const specimenSeries = "TST"

// Rate limits on asking for an invoice by email. The cooldown matches the
// design's "you can resend in 00:30"; the one-time cap is its build note ④,
// because an address the account does not own is otherwise a way to mail a
// stranger a document with this user's name on it, on demand.
const (
	invoiceResendCooldown = 30 * time.Second
	invoiceOneTimePerDay  = 3
	// paymentFetchAttempts bounds how often an invoice asks Cashfree for its
	// payment record before settling for the payment group alone.
	paymentFetchAttempts = 3
	paymentFetchTimeout  = 5 * time.Second
)

// ErrInvoiceEmailMissing is the invoice counterpart of ErrReportEmailMissing:
// no address on the account and none in the request. The app reads the 409 as
// "ask where to send it", which is the design's B3 sheet.
var ErrInvoiceEmailMissing = errors.New("account has no email address")

// InvoiceSendLimited is a refused send, with how long until one would be
// accepted. The handler turns it into a 429 with Retry-After.
type InvoiceSendLimited struct {
	RetryAfter time.Duration
	Msg        string
}

func (e *InvoiceSendLimited) Error() string { return e.Msg }

type invoiceMailer interface {
	SendInvoice(m InvoiceMail) error
}

// InvoiceMail is one invoice email.
type InvoiceMail struct {
	From, To, Number, PlanName, Amount, Filename string
	Specimen                                     bool
	PDF                                          []byte
}

// paymentLookup reads the successful payment behind an order from the
// environment that took it. OrderService implements it, because only it knows
// which gateway an order belongs to.
type paymentLookup interface {
	PaymentDetails(ctx context.Context, order *models.Order) (*payments.PaymentDetails, error)
}

// InvoiceService issues, renders and delivers invoices.
type InvoiceService struct {
	repo     *repository.InvoiceRepo
	orders   *repository.OrderRepo
	accounts *repository.AccountRepo
	cfg      config.InvoiceConfig
	loc      *time.Location
	// mailFrom is the address invoice mail is sent from, as the app shows it
	// on the "Invoice sent" confirmation so the user can find the message.
	mailFrom string

	renderer htmlRenderer
	store    advancedReportStore
	mailer   invoiceMailer
	payments paymentLookup

	kick chan struct{}
	now  func() time.Time
}

func NewInvoiceService(
	repo *repository.InvoiceRepo,
	orders *repository.OrderRepo,
	accounts *repository.AccountRepo,
	cfg config.InvoiceConfig,
	loc *time.Location,
	mailFrom string,
) *InvoiceService {
	return &InvoiceService{
		repo: repo, orders: orders, accounts: accounts, cfg: cfg, loc: loc, mailFrom: mailFrom,
		kick: make(chan struct{}, 1), now: time.Now,
	}
}

func (s *InvoiceService) SetRenderer(r htmlRenderer)       { s.renderer = r }
func (s *InvoiceService) SetStore(st advancedReportStore)  { s.store = st }
func (s *InvoiceService) SetMailer(m invoiceMailer)        { s.mailer = m }
func (s *InvoiceService) SetPaymentLookup(p paymentLookup) { s.payments = p }

// Kick wakes the delivery worker, so an invoice issued now is mailed now rather
// than on the next tick. Never blocks: one pending wake-up is as good as ten.
func (s *InvoiceService) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// ---- issue ----------------------------------------------------------------

// Issue creates the invoice for a paid order, or returns the existing one.
// pd is the payment record when the caller has it to hand (the webhook carries
// it, reconcile reads it); nil leaves it to be read at first render.
//
// A live order with no GSTIN configured is not invoiced: (nil, nil), logged
// loudly. See config.InvoiceConfig for why that is the fail-closed choice.
func (s *InvoiceService) Issue(
	ctx context.Context, order *models.Order, product *models.Product, pd *payments.PaymentDetails,
) (*models.Invoice, error) {
	specimen := order.PaymentMode != "production"
	if !specimen && !s.cfg.Issuable() {
		slog.Error("invoice NOT issued: a live order was paid but invoice.gstin / invoice.sac are not configured",
			"order_uid", order.OrderUID, "account_id", order.AccountID)
		return nil, nil
	}
	acc, err := s.accounts.FindByID(ctx, order.AccountID)
	if err != nil {
		return nil, fmt.Errorf("invoice: account: %w", err)
	}

	now := s.now().UTC()
	today := businessToday(s.loc)
	total := toPaise(order.Amount)
	discount := toPaise(order.DiscountAmount)
	// Place of supply for a service to an unregistered recipient is where the
	// recipient's address on record is, or else where the supplier is (IGST Act
	// s.12(2)(b)). myScorr records no postal address, so every supply is
	// intra-state and taxed CGST + SGST. The inter-state branch of splitGST and
	// the template's IGST line are there for the day an address is collected.
	split := splitGST(total, s.cfg.GSTRatePercent, true)

	var validUntil *time.Time
	if product.IsPlan() && product.ValidityDays != nil && *product.ValidityDays > 0 {
		v := today.AddDate(0, 0, *product.ValidityDays) // the same date fulfilment mints as expires_on
		validUntil = &v
	}
	details := s.planDetails(ctx, product, total, total+discount, today, validUntil)
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return nil, err
	}

	orderID := order.ID
	inv := &models.Invoice{
		Specimen:          specimen,
		OrderID:           &orderID,
		OrderUID:          order.OrderUID,
		AccountID:         order.AccountID,
		IssuedAt:          now,
		ProductCode:       product.Code,
		ValidUntil:        validUntil,
		Currency:          order.Currency,
		TotalPaise:        total,
		TaxablePaise:      split.Taxable,
		CGSTPaise:         split.CGST,
		SGSTPaise:         split.SGST,
		IGSTPaise:         split.IGST,
		GSTRatePercent:    s.cfg.GSTRatePercent,
		ListPricePaise:    total + discount,
		DiscountPaise:     discount,
		CouponCode:        order.CouponCode,
		PlaceOfSupply:     s.cfg.StateName,
		PlaceOfSupplyCode: s.cfg.StateCode,
		SupplierName:      s.cfg.LegalName,
		SupplierAddress:   s.cfg.Address,
		SupplierGSTIN:     nilIfEmpty(s.cfg.GSTIN),
		SAC:               nilIfEmpty(s.cfg.SAC),
		BilledToName:      nilIfEmpty(customerName(acc)),
		BilledToEmail:     nilIfEmpty(strDeref(acc.PrimaryEmail)),
		BilledToPhone:     nilIfEmpty(strDeref(acc.PrimaryPhone)),
		Details:           detailsJSON,
		AutoEmail:         models.InvoiceEmailNoAddress,
	}
	if inv.BilledToEmail != nil {
		inv.AutoEmail = models.InvoiceEmailPending
	}
	applyPayment(inv, pd, order)

	series := s.cfg.Series
	if specimen {
		series = specimenSeries
	}
	out, created, err := s.repo.Issue(ctx, inv, series, financialYear(now, s.loc))
	if err != nil {
		return nil, err
	}
	if created {
		slog.Info("invoice issued", "invoice", out.Number, "order_uid", order.OrderUID,
			"account_id", order.AccountID, "specimen", specimen, "auto_email", out.AutoEmail)
		if out.AutoEmail == models.InvoiceEmailPending {
			s.Kick()
		}
	}
	return out, nil
}

// applyPayment fills the payment lines from the best source available: the
// payment record, else the order's own payment group and Cashfree id.
func applyPayment(inv *models.Invoice, pd *payments.PaymentDetails, order *models.Order) {
	if pd != nil {
		if l := pd.Label(); l != "" {
			inv.PaymentLabel = &l
		}
		if label, value := pd.Reference(); value != "" {
			inv.PaymentRefLabel, inv.PaymentRef = &label, &value
		}
		return
	}
	if order == nil {
		return
	}
	if order.PaymentMethod != nil {
		if l := payments.LabelForGroup(*order.PaymentMethod); l != "" {
			inv.PaymentLabel = &l
		}
	}
	if order.CFPaymentID != nil && *order.CFPaymentID != "" {
		label := "Payment ID"
		inv.PaymentRefLabel, inv.PaymentRef = &label, order.CFPaymentID
	}
}

// planDetails is the plan card, from the catalog as it reads now.
//
// The checklist is the catalog's own description — the lines the plans screen
// shows — rather than the design's typed lists, which promised score-change
// alerts and loan matches that nothing in the app provides. An invoice is the
// last place to describe a purchase as more than it was.
func (s *InvoiceService) planDetails(
	ctx context.Context, product *models.Product, totalPaise, listPaise int64,
	today time.Time, validUntil *time.Time,
) models.InvoiceDetails {
	d := models.InvoiceDetails{
		PlanName: product.Name,
		Badge:    strings.TrimSpace(strDeref(product.Badge)),
		Tagline:  strings.TrimSpace(strDeref(product.Tagline)),
	}
	for _, line := range strings.Split(product.Description, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			d.Features = append(d.Features, line)
		}
	}

	if !product.IsPlan() {
		d.PriceLabel = inr(listPaise) + " one-time"
		checks := product.ChecksIncluded
		if checks < 1 {
			checks = 1
		}
		d.Chips = append(d.Chips, models.InvoiceChip{Text: plural(checks, "score check", "score checks")})
		return d
	}

	d.PriceLabel = inr(listPaise) + " / year"
	if validUntil == nil || !isAboutAYear(today, *validUntil) {
		d.PriceLabel = inr(listPaise) + " plan"
	}
	d.Chips = append(d.Chips,
		models.InvoiceChip{Text: plural(product.ChecksIncluded, "score refresh", "score refreshes")})
	if validUntil != nil {
		d.Chips = append(d.Chips, models.InvoiceChip{
			Text: "Valid " + invoiceDate(today) + " to " + invoiceDate(*validUntil)})
	}
	if product.ChecksIncluded > 1 && product.IntervalMonths != nil {
		// Run 1 is due today (fulfilment mints it so); run 2 one interval on.
		d.Chips = append(d.Chips, models.InvoiceChip{
			Text: "Next refresh " + invoiceDate(today.AddDate(0, *product.IntervalMonths, 0))})
	}
	// The saving is against buying the same number of one-time checks, priced
	// from the cheapest one-time product — the plans screen's own rule — and
	// measured on what was actually paid, coupon included.
	if unit := s.cheapestOneTime(ctx); unit > 0 {
		if saved := unit*int64(product.ChecksIncluded) - totalPaise; saved > 0 {
			d.Chips = append(d.Chips, models.InvoiceChip{
				Text: "You saved " + inr(saved) + " vs one-time checks", Saving: true})
		}
	}
	return d
}

func (s *InvoiceService) cheapestOneTime(ctx context.Context) int64 {
	products, err := s.orders.ListActiveProducts(ctx)
	if err != nil {
		return 0
	}
	var best int64
	for _, p := range products {
		if p.IsPlan() {
			continue
		}
		if v := toPaise(p.Amount); v > 0 && (best == 0 || v < best) {
			best = v
		}
	}
	return best
}

func isAboutAYear(from, to time.Time) bool {
	days := to.Sub(from).Hours() / 24
	return days >= 360 && days <= 370
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func invoiceDate(t time.Time) string { return t.Format("2 Jan 2006") }

// ---- reading --------------------------------------------------------------

// Summaries maps order id to the invoice summary the orders API attaches.
func (s *InvoiceService) Summaries(ctx context.Context, accountID int64) (map[int64]*models.InvoiceSummary, error) {
	list, err := s.repo.ListByAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*models.InvoiceSummary, len(list))
	for i := range list {
		inv := &list[i]
		if inv.OrderID != nil {
			out[*inv.OrderID] = summaryOf(inv)
		}
	}
	return out, nil
}

// SummaryForOrder is Summaries for one order; nil when it has no invoice.
func (s *InvoiceService) SummaryForOrder(ctx context.Context, orderID int64) *models.InvoiceSummary {
	inv, err := s.repo.FindByOrderID(ctx, orderID)
	if err != nil {
		return nil
	}
	return summaryOf(inv)
}

func summaryOf(inv *models.Invoice) *models.InvoiceSummary {
	sum := &models.InvoiceSummary{
		Number: inv.Number, IssuedAt: inv.IssuedAt, Specimen: inv.Specimen, PaidVia: inv.PaymentLabel,
	}
	if inv.ValidUntil != nil {
		v := inv.ValidUntil.Format("2006-01-02")
		sum.ValidUntil = &v
	}
	return sum
}

// InvoiceLink is a short-lived download of one invoice.
type InvoiceLink struct {
	URL              string `json:"url"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
	Filename         string `json:"filename"`
	Number           string `json:"number"`
}

// Link renders the invoice if it has not been stored yet and returns a
// presigned URL for it.
func (s *InvoiceService) Link(ctx context.Context, accountID int64, orderUID string) (*InvoiceLink, error) {
	inv, err := s.ownedInvoice(ctx, accountID, orderUID)
	if err != nil {
		return nil, err
	}
	if s.store == nil || s.store.IsStub() {
		return nil, apperr.NewServiceUnavailable("Invoice downloads are not available right now. Please try again later.")
	}
	if inv.PDFURI == nil {
		pdf, err := s.render(ctx, inv)
		if err != nil {
			return nil, err
		}
		uri, err := s.upload(ctx, inv, pdf)
		if err != nil {
			return nil, apperr.NewBadGateway("Could not prepare the download. Please try again.")
		}
		inv.PDFURI = &uri
	}
	url, ttl, err := s.store.PresignGet(ctx, *inv.PDFURI)
	if err != nil {
		slog.Error("invoice: presign failed", "invoice", inv.Number, "error", err)
		return nil, apperr.NewBadGateway("Could not prepare the download. Please try again.")
	}
	return &InvoiceLink{
		URL: url, ExpiresInSeconds: int(ttl.Seconds()), Filename: invoiceFilename(inv), Number: inv.Number,
	}, nil
}

func (s *InvoiceService) ownedInvoice(ctx context.Context, accountID int64, orderUID string) (*models.Invoice, error) {
	order, err := s.orders.FindOrderByUID(ctx, orderUID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && order.AccountID != accountID) {
		return nil, apperr.NewNotFound("Order not found")
	}
	if err != nil {
		return nil, err
	}
	inv, err := s.repo.FindByOrderID(ctx, order.ID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("There is no invoice for this purchase yet.")
	}
	return inv, err
}

// invoiceFilename is the design's myScorr_Invoice_MSC-26-27-000186.pdf.
func invoiceFilename(inv *models.Invoice) string {
	return "myScorr_Invoice_" + strings.ReplaceAll(inv.Number, "/", "-") + ".pdf"
}

func invoiceKey(inv *models.Invoice) string {
	return fmt.Sprintf("invoices/%d/%d.pdf", inv.AccountID, inv.ID)
}

func (s *InvoiceService) upload(ctx context.Context, inv *models.Invoice, pdf []byte) (string, error) {
	uri, err := s.store.UploadAs(ctx, invoiceKey(inv), invoiceFilename(inv), "application/pdf", pdf)
	if err != nil {
		slog.Error("invoice: upload failed", "invoice", inv.Number, "error", err)
		return "", err
	}
	if err := s.repo.SetPDF(ctx, inv.ID, uri); err != nil {
		slog.Error("invoice: stored but not recorded", "invoice", inv.Number, "uri", uri, "error", err)
	}
	return uri, nil
}

// ---- email on request -----------------------------------------------------

// InvoiceEmailResult is what the "Invoice sent" confirmation shows.
type InvoiceEmailResult struct {
	Number string    `json:"number"`
	SentTo string    `json:"sentTo"`
	SentAt time.Time `json:"sentAt"`
	From   string    `json:"from"`
	// Saved reports whether SentTo is the account's own address — false for
	// the design's "send once, don't save".
	Saved             bool `json:"saved"`
	ResendAfterSecond int  `json:"resendAfterSeconds"`
}

// Email sends the invoice to the account's address, or — when to is given and
// is some other address — once to that one without saving it.
func (s *InvoiceService) Email(
	ctx context.Context, accountID int64, orderUID, to string,
) (*InvoiceEmailResult, error) {
	inv, err := s.ownedInvoice(ctx, accountID, orderUID)
	if err != nil {
		return nil, err
	}
	acc, err := s.accounts.FindByID(ctx, accountID)
	if err != nil {
		return nil, apperr.NewNotFound("Account not found")
	}
	own := normalizeEmail(strDeref(acc.PrimaryEmail))
	to = normalizeEmail(to)
	kind := models.InvoiceSendAccount
	switch {
	case to == "" && own == "":
		return nil, ErrInvoiceEmailMissing
	case to == "":
		to = own
	case to != own:
		kind = models.InvoiceSendOneTime
	}

	now := s.now()
	last, oneTime, err := s.repo.SendStats(ctx, inv.ID, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	if last != nil {
		if wait := invoiceResendCooldown - now.Sub(*last); wait > 0 {
			secs := int(wait.Seconds()) + 1
			return nil, &InvoiceSendLimited{RetryAfter: wait,
				Msg: fmt.Sprintf("This invoice was just sent. You can send it again in %d seconds.", secs)}
		}
	}
	if kind == models.InvoiceSendOneTime && oneTime >= invoiceOneTimePerDay {
		return nil, &InvoiceSendLimited{RetryAfter: time.Hour,
			Msg: "This invoice has been sent to other addresses too many times today. " +
				"Please try again tomorrow, or add your email to your account."}
	}
	if s.mailer == nil {
		return nil, apperr.NewServiceUnavailable("Email delivery is not configured.")
	}

	pdf, err := s.render(ctx, inv)
	if err != nil {
		return nil, err
	}
	if inv.PDFURI == nil && s.store != nil && !s.store.IsStub() {
		_, _ = s.upload(ctx, inv, pdf) // best-effort: the next download is a presign
	}
	if err := s.send(inv, to, pdf); err != nil {
		return nil, apperr.NewBadGateway("Could not send the email. Please try again.")
	}
	if err := s.repo.RecordSend(ctx, inv.ID, accountID, kind, hashEmail(to)); err != nil {
		slog.Error("invoice: sent but not logged; the rate limit will not see it",
			"invoice", inv.Number, "error", err)
	}
	return &InvoiceEmailResult{
		Number: inv.Number, SentTo: to, SentAt: now.UTC(), From: s.mailFrom,
		Saved: kind == models.InvoiceSendAccount, ResendAfterSecond: int(invoiceResendCooldown.Seconds()),
	}, nil
}

func (s *InvoiceService) send(inv *models.Invoice, to string, pdf []byte) error {
	var d models.InvoiceDetails
	_ = json.Unmarshal(inv.Details, &d)
	return s.mailer.SendInvoice(InvoiceMail{
		To: to, Number: inv.Number, PlanName: d.PlanName, Amount: inrExact(inv.TotalPaise),
		Filename: invoiceFilename(inv), Specimen: inv.Specimen, PDF: pdf,
	})
}

// ---- rendering ------------------------------------------------------------

func (s *InvoiceService) render(ctx context.Context, inv *models.Invoice) ([]byte, error) {
	if s.renderer == nil || !s.renderer.Available() {
		return nil, apperr.NewServiceUnavailable("Invoices are not available right now. Please try again later.")
	}
	s.ensurePayment(ctx, inv)
	html, err := renderInvoiceHTML(inv, s.cfg, s.loc)
	if err != nil {
		slog.Error("invoice: template failed", "invoice", inv.Number, "error", err)
		return nil, apperr.NewBadGateway("Could not prepare your invoice. Please try again.")
	}
	pdf, err := s.renderer.PDF(ctx, html)
	if err != nil {
		slog.Error("invoice: render failed", "invoice", inv.Number, "error", err)
		return nil, apperr.NewBadGateway("Could not prepare your invoice. Please try again.")
	}
	return pdf, nil
}

// ensurePayment reads the payment record for an invoice issued without one,
// a bounded number of times. The payment lines are information, not a GST
// requirement, so a Cashfree that cannot answer costs the invoice its UTR, not
// its existence.
func (s *InvoiceService) ensurePayment(ctx context.Context, inv *models.Invoice) {
	if inv.PaymentLabel != nil || s.payments == nil || inv.PaymentFetchAttempts >= paymentFetchAttempts {
		return
	}
	order, err := s.orders.FindOrderByUID(ctx, inv.OrderUID)
	if err != nil {
		return
	}
	fetchCtx, cancel := context.WithTimeout(ctx, paymentFetchTimeout)
	defer cancel()
	pd, err := s.payments.PaymentDetails(fetchCtx, order)
	if err != nil {
		slog.Warn("invoice: payment record unavailable", "invoice", inv.Number, "error", err)
		_ = s.repo.CountPaymentFetch(ctx, inv.ID)
		inv.PaymentFetchAttempts++
		if inv.PaymentFetchAttempts < paymentFetchAttempts {
			return
		}
		pd = nil // out of attempts: settle for what the order row knows
	}
	applyPayment(inv, pd, order)
	if err := s.repo.SetPayment(ctx, inv.ID, inv.PaymentLabel, inv.PaymentRefLabel, inv.PaymentRef); err != nil {
		slog.Warn("invoice: payment lines not saved", "invoice", inv.Number, "error", err)
	}
}

type invoiceTaxLine struct{ Label, Note, Amount string }

type invoiceCell struct {
	Label, Value string
	Mono         bool
}

type invoiceView struct {
	Fonts           template.CSS
	Specimen        bool
	Number          string
	IssuedOn        string
	PlaceOfSupply   string
	Amount          string
	PaidVia         string
	Plan            models.InvoiceDetails
	BilledToName    string
	BilledToLines   []string
	SupplierName    string
	SupplierAddress string
	SupplierGSTIN   string
	LineTitle       string
	SAC             string
	DiscountNote    string
	Taxable         string
	Taxes           []invoiceTaxLine
	Words           string
	PayCells        []invoiceCell
	SupportEmail    string
	Website         string
}

// renderInvoiceHTML builds the document from the row alone. cfg supplies only
// the contact lines in the footer; everything with legal weight comes from the
// snapshot, so a config change never alters an invoice already issued.
func renderInvoiceHTML(inv *models.Invoice, cfg config.InvoiceConfig, loc *time.Location) (string, error) {
	var details models.InvoiceDetails
	if err := json.Unmarshal(inv.Details, &details); err != nil {
		return "", fmt.Errorf("invoice details: %w", err)
	}
	v := invoiceView{
		Fonts:           invoiceFontCSS,
		Specimen:        inv.Specimen,
		Number:          inv.Number,
		IssuedOn:        inv.IssuedAt.In(loc).Format("2 Jan 2006 · 3:04 PM"),
		PlaceOfSupply:   fmt.Sprintf("%s (%s)", inv.PlaceOfSupply, inv.PlaceOfSupplyCode),
		Amount:          inrExact(inv.TotalPaise),
		PaidVia:         strDeref(inv.PaymentLabel),
		Plan:            details,
		BilledToName:    strDeref(inv.BilledToName),
		SupplierName:    inv.SupplierName,
		SupplierAddress: inv.SupplierAddress,
		SupplierGSTIN:   strDeref(inv.SupplierGSTIN),
		LineTitle:       details.PlanName,
		SAC:             strDeref(inv.SAC),
		Taxable:         inrExact(inv.TaxablePaise),
		Words:           rupeesInWords(inv.TotalPaise),
		SupportEmail:    cfg.SupportEmail,
		Website:         cfg.Website,
	}
	// Billed to: the email if there was one, else the number, masked — the
	// document can be sent on to an address the account does not own, and a
	// full mobile number has no business travelling with it.
	switch {
	case inv.BilledToEmail != nil:
		v.BilledToLines = append(v.BilledToLines, *inv.BilledToEmail)
	case inv.BilledToPhone != nil:
		if m := maskPhone(*inv.BilledToPhone); m != "" {
			v.BilledToLines = append(v.BilledToLines, m)
		}
	}
	if v.BilledToName == "" && len(v.BilledToLines) == 0 {
		v.BilledToLines = []string{"myScorr customer"}
	}
	if inv.DiscountPaise > 0 {
		code := strDeref(inv.CouponCode)
		if code != "" {
			code = " " + code
		}
		v.DiscountNote = fmt.Sprintf("List price %s, coupon%s −%s",
			inrExact(inv.ListPricePaise), code, inrExact(inv.DiscountPaise))
	}
	half := trimRate(inv.GSTRatePercent / 2)
	// IGST exactly when the invoice carries any: Issue never mixes the two.
	if inv.IGSTPaise > 0 {
		v.Taxes = []invoiceTaxLine{{Label: "IGST @ " + trimRate(inv.GSTRatePercent) + "%",
			Note: "Interstate supply", Amount: inrExact(inv.IGSTPaise)}}
	} else {
		v.Taxes = []invoiceTaxLine{
			{Label: "CGST @ " + half + "%", Amount: inrExact(inv.CGSTPaise)},
			{Label: "SGST @ " + half + "%", Amount: inrExact(inv.SGSTPaise)},
		}
	}
	if inv.PaymentLabel != nil {
		v.PayCells = append(v.PayCells, invoiceCell{Label: "Payment mode", Value: *inv.PaymentLabel})
	}
	if inv.PaymentRef != nil && inv.PaymentRefLabel != nil {
		v.PayCells = append(v.PayCells, invoiceCell{Label: *inv.PaymentRefLabel, Value: *inv.PaymentRef, Mono: true})
	}
	v.PayCells = append(v.PayCells, invoiceCell{Label: "Order ID", Value: inv.OrderUID, Mono: true})

	var buf bytes.Buffer
	if err := invoiceTmpl.Execute(&buf, v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// trimRate prints 9 as "9" and 2.5 as "2.5".
func trimRate(r float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", r), "0"), ".")
}
