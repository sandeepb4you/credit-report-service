package payments

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrNoSuccessfulPayment is GetPayment's answer for an order with no captured
// payment (yet): nothing to describe.
var ErrNoSuccessfulPayment = errors.New("no successful payment on this order")

// PaymentDetails is the successful payment behind a PAID order — as much of it
// as a tax invoice prints, and deliberately no more.
//
// What is NOT here matters as much as what is. Cashfree's payment_method
// carries the payer's UPI id (a person's handle, which the account-deletion
// scrub exists to remove from the webhook log) and, for a wallet, their phone.
// Neither is copied out: the invoice says "UPI" and the UTR, which is what the
// customer's bank statement shows and what a dispute is traced by.
type PaymentDetails struct {
	CFPaymentID string
	// Group is Cashfree's payment_group: upi, credit_card, debit_card,
	// net_banking, wallet, pay_later, ...
	Group string
	// BankReference is the UTR for UPI, the bank's reference otherwise.
	BankReference  string
	CardNetwork    string
	CardLast4      string
	BankName       string
	WalletProvider string
}

// Label is the "Payment mode" line: "UPI", "Card · Visa ending 4417",
// "Net banking · HDFC Bank", "Wallet · Phonepe".
//
// No UPI app name, although the design shows "UPI · PhonePe": Cashfree does not
// report which app paid, only the payer's handle, and naming an app from the
// handle's suffix is a guess that would sometimes print the wrong company on a
// tax document.
func (d PaymentDetails) Label() string {
	switch {
	case d.Group == "upi":
		return "UPI"
	case strings.Contains(d.Group, "card") && d.CardLast4 != "":
		network := titleWord(d.CardNetwork)
		if network == "" {
			network = "Card"
		}
		return fmt.Sprintf("Card · %s ending %s", network, d.CardLast4)
	case strings.Contains(d.Group, "card"):
		return "Card"
	case d.Group == "net_banking" && d.BankName != "":
		return "Net banking · " + d.BankName
	case d.Group == "net_banking":
		return "Net banking"
	case d.Group == "wallet" && d.WalletProvider != "":
		return "Wallet · " + titleWord(d.WalletProvider)
	case d.Group == "":
		return ""
	default:
		return titleWord(strings.ReplaceAll(d.Group, "_", " "))
	}
}

// Reference is the label and value of the one payment reference an invoice
// prints: the UTR for UPI when there is one, Cashfree's payment id otherwise.
func (d PaymentDetails) Reference() (label, value string) {
	if d.Group == "upi" && d.BankReference != "" {
		return "UTR", d.BankReference
	}
	if d.CFPaymentID != "" {
		return "Payment ID", d.CFPaymentID
	}
	return "", ""
}

// LabelForGroup is the label when only the payment_group is known — an order
// settled before its payment record could be read.
func LabelForGroup(group string) string {
	return PaymentDetails{Group: strings.ToLower(group)}.Label()
}

// cfPayment is one Cashfree payment entity, as GET /orders/{id}/payments lists
// it and as a PAYMENT_SUCCESS webhook carries it under data.payment.
type cfPayment struct {
	CFPaymentID   json.RawMessage `json:"cf_payment_id"`
	PaymentStatus string          `json:"payment_status"`
	PaymentGroup  string          `json:"payment_group"`
	BankReference string          `json:"bank_reference"`
	PaymentMethod struct {
		Card *struct {
			CardNetwork string `json:"card_network"`
			CardNumber  string `json:"card_number"`
		} `json:"card"`
		Netbanking *struct {
			BankName string `json:"netbanking_bank_name"`
		} `json:"netbanking"`
		App *struct {
			Provider string `json:"provider"`
		} `json:"app"`
	} `json:"payment_method"`
}

func (p cfPayment) details() *PaymentDetails {
	d := &PaymentDetails{
		CFPaymentID:   rawString(p.CFPaymentID),
		Group:         strings.ToLower(p.PaymentGroup),
		BankReference: strings.TrimSpace(p.BankReference),
	}
	if c := p.PaymentMethod.Card; c != nil {
		d.CardNetwork = c.CardNetwork
		// Cashfree masks the number (XXXXXXXXXXXX4417); only the last four are
		// kept, and only when they are digits rather than more mask.
		if n := strings.TrimSpace(c.CardNumber); len(n) >= 4 && isDigits(n[len(n)-4:]) {
			d.CardLast4 = n[len(n)-4:]
		}
	}
	if nb := p.PaymentMethod.Netbanking; nb != nil {
		d.BankName = strings.TrimSpace(nb.BankName)
	}
	if app := p.PaymentMethod.App; app != nil {
		d.WalletProvider = strings.TrimSpace(app.Provider)
	}
	return d
}

// DetailsFromWebhook reads the payment a PAYMENT_SUCCESS webhook describes.
// raw is the webhook's data.payment object. Nil when it carries nothing usable.
func DetailsFromWebhook(raw json.RawMessage) *PaymentDetails {
	if len(raw) == 0 {
		return nil
	}
	var p cfPayment
	if err := json.Unmarshal(raw, &p); err != nil || p.PaymentGroup == "" {
		return nil
	}
	return p.details()
}

// successfulPayment picks the captured payment out of an order's attempts. An
// order can carry several — a declined card, then a UPI payment that went
// through — and only the one that succeeded belongs on the invoice.
func successfulPayment(list []cfPayment) (*PaymentDetails, error) {
	for _, p := range list {
		if strings.EqualFold(p.PaymentStatus, "SUCCESS") {
			return p.details(), nil
		}
	}
	return nil, ErrNoSuccessfulPayment
}

// rawString reads a JSON value that Cashfree has sent as both a string and a
// number across API versions (cf_payment_id).
func rawString(b json.RawMessage) string {
	if len(b) == 0 || string(b) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(b))
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// titleWord upper-cases the first letter: "visa" -> "Visa", "phonepe" ->
// "Phonepe". Cashfree's values are lower-case identifiers, not display names,
// and a wrong capital inside a brand is less bad than a table of brands here.
func titleWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}
