package payments

import (
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
// What is NOT here matters as much as what is. Razorpay's payment entity
// carries the payer's email, phone and UPI id (a person's handle, which the
// account-deletion scrub exists to remove from the webhook log). None of them
// is copied out: the invoice says "UPI" and the UTR, which is what the
// customer's bank statement shows and what a dispute is traced by.
type PaymentDetails struct {
	// PaymentID is the gateway's payment id (pay_...).
	PaymentID string
	// Group is the payment group in our vocabulary: upi, credit_card,
	// debit_card, net_banking, wallet, emi, pay_later, ...
	Group string
	// BankReference is the UTR (RRN) for UPI, the bank's reference otherwise.
	BankReference  string
	CardNetwork    string
	CardLast4      string
	BankName       string
	WalletProvider string
}

// Label is the "Payment mode" line: "UPI", "Card · Visa ending 4417",
// "Net banking · HDFC", "Wallet · Phonepe".
//
// No UPI app name, although the design shows "UPI · PhonePe": the gateway does
// not report which app paid, only the payer's handle, and naming an app from
// the handle's suffix is a guess that would sometimes print the wrong company
// on a tax document.
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
	case d.Group == "emi":
		return "EMI"
	case d.Group == "":
		return ""
	default:
		return titleWord(strings.ReplaceAll(d.Group, "_", " "))
	}
}

// Reference is the label and value of the one payment reference an invoice
// prints: the UTR for UPI when there is one, the gateway's payment id otherwise.
func (d PaymentDetails) Reference() (label, value string) {
	if d.Group == "upi" && d.BankReference != "" {
		return "UTR", d.BankReference
	}
	if d.PaymentID != "" {
		return "Payment ID", d.PaymentID
	}
	return "", ""
}

// LabelForGroup is the label when only the payment group is known — an order
// settled before its payment record could be read.
func LabelForGroup(group string) string {
	return PaymentDetails{Group: strings.ToLower(group)}.Label()
}

// rzpPayment is one Razorpay payment entity, as GET /orders/{id}/payments lists
// it and as a webhook carries it under payload.payment.entity. Only the fields
// read here are declared; the entity's email, contact and vpa are deliberately
// not, so they cannot be copied anywhere by accident.
type rzpPayment struct {
	ID          string `json:"id"`
	OrderID     string `json:"order_id"`
	Status      string `json:"status"` // created | authorized | captured | refunded | failed
	Method      string `json:"method"` // upi | card | netbanking | wallet | emi | paylater | cardless_emi
	Bank        string `json:"bank"`
	Wallet      string `json:"wallet"`
	CreatedAt   int64  `json:"created_at"`
	ErrorReason string `json:"error_reason"`
	ErrorDesc   string `json:"error_description"`
	Card        *struct {
		Network string `json:"network"`
		Last4   string `json:"last4"`
		Type    string `json:"type"` // credit | debit | prepaid
	} `json:"card"`
	AcquirerData struct {
		RRN               string `json:"rrn"`
		BankTransactionID string `json:"bank_transaction_id"`
	} `json:"acquirer_data"`
}

// group maps Razorpay's method onto the payment-group vocabulary the orders
// table and the invoice labels already use.
func (p rzpPayment) group() string {
	switch strings.ToLower(p.Method) {
	case "card":
		if p.Card != nil && p.Card.Type != "" {
			return strings.ToLower(p.Card.Type) + "_card"
		}
		return "card"
	case "netbanking":
		return "net_banking"
	case "paylater":
		return "pay_later"
	default:
		return strings.ToLower(p.Method)
	}
}

func (p rzpPayment) details() *PaymentDetails {
	d := &PaymentDetails{
		PaymentID:      p.ID,
		Group:          p.group(),
		BankName:       strings.TrimSpace(p.Bank),
		WalletProvider: strings.TrimSpace(p.Wallet),
	}
	// The UPI RRN is the 12-digit reference the payer's bank statement shows —
	// the UTR. Net banking has the bank's own transaction id instead.
	if ref := strings.TrimSpace(p.AcquirerData.RRN); ref != "" {
		d.BankReference = ref
	} else {
		d.BankReference = strings.TrimSpace(p.AcquirerData.BankTransactionID)
	}
	if c := p.Card; c != nil {
		d.CardNetwork = c.Network
		if n := strings.TrimSpace(c.Last4); len(n) == 4 && isDigits(n) {
			d.CardLast4 = n
		}
	}
	return d
}

// capturedPayment picks the captured payment out of an order's attempts. An
// order can carry several — a declined card, then a UPI payment that went
// through — and only the one that succeeded belongs on the invoice.
func capturedPayment(list []rzpPayment) (*rzpPayment, error) {
	for i := range list {
		if strings.EqualFold(list[i].Status, "captured") {
			return &list[i], nil
		}
	}
	return nil, ErrNoSuccessfulPayment
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
// "Phonepe". The gateway's values are identifiers, not display names, and a
// wrong capital inside a brand is less bad than a table of brands here.
func titleWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}
