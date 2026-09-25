package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
)

func sampleInvoiceConfig() config.InvoiceConfig {
	return config.InvoiceConfig{
		LegalName: "Reachout Tech Private Limited",
		Address:   "22, 4th Floor, 1st Main, Royal Placid, Haralur, HSR Layout, Bangalore South, Karnataka 560102",
		StateName: "Karnataka", StateCode: "29", GSTIN: "29ABCDE1234F1ZW", SAC: "998399",
		GSTRatePercent: 18, Series: "MSC", SupportEmail: "alerts@myscorr.com", Website: "myscorr.com",
	}
}

func ptr[T any](v T) *T { return &v }

// sampleInvoices are the design's three invoices, built the way Issue builds
// them, plus a coupon and a specimen.
func sampleInvoices(t *testing.T) map[string]*models.Invoice {
	t.Helper()
	cfg := sampleInvoiceConfig()
	issued := time.Date(2026, 9, 25, 11, 51, 0, 0, time.UTC)
	mk := func(number string, total, discount int64, d models.InvoiceDetails, validUntil *time.Time,
		pay, refLabel, ref string, name, email string) *models.Invoice {
		js, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		s := splitGST(total, cfg.GSTRatePercent, true)
		inv := &models.Invoice{
			Number: number, OrderUID: "05f058e6-94e0-409b-9b9b-2c6aab3c00c9", IssuedAt: issued,
			ValidUntil: validUntil, Currency: "INR", TotalPaise: total, TaxablePaise: s.Taxable,
			CGSTPaise: s.CGST, SGSTPaise: s.SGST, GSTRatePercent: 18, ListPricePaise: total + discount,
			DiscountPaise: discount, PlaceOfSupply: "Karnataka", PlaceOfSupplyCode: "29",
			SupplierName: cfg.LegalName, SupplierAddress: cfg.Address, SupplierGSTIN: ptr(cfg.GSTIN),
			SAC: ptr(cfg.SAC), BilledToName: nilIfEmpty(name), BilledToEmail: nilIfEmpty(email),
			BilledToPhone: ptr("+919812345041"), Details: js,
			PaymentLabel: nilIfEmpty(pay), PaymentRefLabel: nilIfEmpty(refLabel), PaymentRef: nilIfEmpty(ref),
		}
		if discount > 0 {
			inv.CouponCode = ptr("WELCOME50")
		}
		return inv
	}
	year := ptr(time.Date(2027, 9, 25, 0, 0, 0, 0, time.UTC))
	return map[string]*models.Invoice{
		"starter": mk("MSC/26-27/000184", 29900, 0, models.InvoiceDetails{
			PlanName: "myScorr Starter", Badge: "Trending", PriceLabel: "₹299 one-time",
			Tagline:  "One-time credit score check with the full report",
			Features: []string{"1 score check", "Full credit report", "Improvement plan", "PDF on email", "Soft check, no score impact"},
			Chips:    []models.InvoiceChip{{Text: "1 score check"}},
		}, nil, "UPI", "UTR", "626812345678", "Ananya Iyer", "ananya.iyer@example.com"),
		"quarterly": mk("MSC/26-27/000185", 69900, 0, models.InvoiceDetails{
			PlanName: "myScorr Quarterly", Badge: "Best value", PriceLabel: "₹699 / year",
			Tagline:  "12-month plan, a score refresh every quarter",
			Features: []string{"4 score refreshes a year", "Full report every refresh", "Improvement plan", "Score simulator"},
			Chips: []models.InvoiceChip{{Text: "4 score refreshes"}, {Text: "Valid 25 Sep 2026 to 25 Sep 2027"},
				{Text: "Next refresh 25 Dec 2026"}, {Text: "You saved ₹497 vs one-time checks", Saving: true}},
		}, year, "Card · Visa ending 4417", "Payment ID", "5114917421", "", ""),
		"monthly": mk("MSC/26-27/000186", 89900, 10000, models.InvoiceDetails{
			PlanName: "myScorr Monthly", Badge: "Most popular", PriceLabel: "₹999 / year",
			Features: []string{"12 score refreshes a year", "Full report every refresh", "Score simulator", "Improvement plan", "Payment history", "Soft check, no score impact"},
			Chips: []models.InvoiceChip{{Text: "12 score refreshes"}, {Text: "Valid 25 Sep 2026 to 25 Sep 2027"},
				{Text: "Next refresh 25 Oct 2026"}, {Text: "You saved ₹2,689 vs one-time checks", Saving: true}},
		}, year, "Net banking · HDFC Bank", "Payment ID", "5114917422", "Meera Nair", "meera.nair@example.com"),
	}
}

// The document says what the row says, in the design's words, and nothing a
// template author left behind.
func TestInvoiceHTMLCarriesTheRow(t *testing.T) {
	cfg := sampleInvoiceConfig()
	ist, _ := time.LoadLocation("Asia/Kolkata")
	inv := sampleInvoices(t)["starter"]
	html, err := renderInvoiceHTML(inv, cfg, ist)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"TAX INVOICE", "MSC/26-27/000184", "25 Sep 2026 · 5:21 PM", "Karnataka (29)",
		"₹299.00", "Paid via UPI", "₹253.39", "CGST @ 9%", "₹22.80", "SGST @ 9%", "₹22.81",
		"Rupees Two Hundred Ninety Nine Only", "GSTIN <span class=\"mono\">29ABCDE1234F1ZW</span>",
		"SAC <span class=\"mono\">998399</span>", "UTR", "626812345678", "Ananya Iyer",
		"ananya.iyer@example.com", "alerts@myscorr.com", "@font-face{font-family:'Inter'",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("invoice HTML lacks %q", want)
		}
	}
	for _, bad := range []string{"{{", "<no value>", "SPECIMEN", "IGST", "NOT A TAX INVOICE"} {
		if strings.Contains(html, bad) {
			t.Errorf("invoice HTML contains %q", bad)
		}
	}
}

// A specimen can never be mistaken for a tax invoice: no TAX INVOICE heading,
// and the no-money line in place of the signature line.
func TestSpecimenInvoiceSaysSo(t *testing.T) {
	ist, _ := time.LoadLocation("Asia/Kolkata")
	inv := sampleInvoices(t)["starter"]
	inv.Specimen, inv.Number, inv.SupplierGSTIN = true, "TST/26-27/000001", nil
	html, err := renderInvoiceHTML(inv, sampleInvoiceConfig(), ist)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, ">TAX INVOICE<") || !strings.Contains(html, "NOT A TAX INVOICE") ||
		!strings.Contains(html, "no money was charged") {
		t.Fatalf("specimen invoice does not say what it is")
	}
}

// With no email, the billed-to card shows the number masked: the PDF can be
// mailed on to an address the account does not own.
func TestInvoiceMasksAPhoneOnlyRecipient(t *testing.T) {
	ist, _ := time.LoadLocation("Asia/Kolkata")
	inv := sampleInvoices(t)["quarterly"]
	html, err := renderInvoiceHTML(inv, sampleInvoiceConfig(), ist)
	if err != nil {
		t.Fatal(err)
	}
	// html/template writes the "+" as &#43;, which is why only the digits are matched.
	if strings.Contains(html, "9812345041") || !strings.Contains(html, "91 98xxx xx041") {
		t.Fatalf("phone not masked on the invoice")
	}
	if !strings.Contains(html, "Card · Visa ending 4417") {
		t.Fatalf("card payment line missing")
	}
}

func TestInvoiceShowsTheCoupon(t *testing.T) {
	ist, _ := time.LoadLocation("Asia/Kolkata")
	html, err := renderInvoiceHTML(sampleInvoices(t)["monthly"], sampleInvoiceConfig(), ist)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "List price ₹999.00, coupon WELCOME50 −₹100.00") {
		t.Fatalf("coupon line missing")
	}
}

// TestInvoicePreview writes the sample invoices as HTML for a person to print
// and look at: INVOICE_PREVIEW_DIR=/tmp/inv go test -run InvoicePreview.
func TestInvoicePreview(t *testing.T) {
	dir := os.Getenv("INVOICE_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set INVOICE_PREVIEW_DIR to write preview HTML")
	}
	ist, _ := time.LoadLocation("Asia/Kolkata")
	all := sampleInvoices(t)
	spec := *all["starter"]
	spec.Specimen, spec.Number, spec.SupplierGSTIN = true, "TST/26-27/000001", nil
	all["specimen"] = &spec
	for name, inv := range all {
		html, err := renderInvoiceHTML(inv, sampleInvoiceConfig(), ist)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
