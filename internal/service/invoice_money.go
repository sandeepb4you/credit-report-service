package service

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Money on an invoice is integer paise from the moment it is read off an order.
// The order stores rupees as a float (NUMERIC in the database), and a tax split
// done in floats is how an invoice ends up with a total that is not the sum of
// its lines.

// toPaise converts an order's rupee amount, rounding to the nearest paisa.
func toPaise(rupees float64) int64 {
	return int64(math.Round(rupees * 100))
}

// gstSplit is how one GST-inclusive total divides into taxable value and tax.
type gstSplit struct {
	Taxable int64
	CGST    int64
	SGST    int64
	IGST    int64
}

// splitGST divides a GST-inclusive total. Prices are sold inclusive (₹299 is
// what the customer pays, not ₹299 + tax), so the taxable value is derived from
// the total and the tax is whatever remains — never the other way round, which
// would let rounding make the invoice total disagree with the amount charged.
//
// Intra-state supplies split the tax into CGST and SGST halves, the odd paisa
// going to SGST: ₹299 at 18% is 253.39 taxable, 22.80 CGST, 22.81 SGST, which is
// the design's own arithmetic. Inter-state supplies carry it all as IGST.
func splitGST(totalPaise int64, ratePercent float64, intraState bool) gstSplit {
	// taxable = total / (1 + rate), rounded half-up, in integer arithmetic on
	// basis points so 18% and 12.5% are both exact.
	rateBP := int64(math.Round(ratePercent * 100))
	den := 10000 + rateBP
	taxable := (totalPaise*10000 + den/2) / den
	tax := totalPaise - taxable
	if !intraState {
		return gstSplit{Taxable: taxable, IGST: tax}
	}
	cgst := tax / 2
	return gstSplit{Taxable: taxable, CGST: cgst, SGST: tax - cgst}
}

// inrExact prints paise as ₹2,589.00 — Indian digit grouping (lakh, crore),
// always two decimals, as an invoice line should.
func inrExact(paise int64) string {
	neg := paise < 0
	if neg {
		paise = -paise
	}
	s := fmt.Sprintf("₹%s.%02d", indianGroup(strconv.FormatInt(paise/100, 10)), paise%100)
	if neg {
		return "−" + s
	}
	return s
}

// inr drops ".00" for whole amounts: "₹699", "₹2,589". For
// captions and chips, never for a line of the tax table.
func inr(paise int64) string {
	if paise%100 == 0 {
		return "₹" + indianGroup(strconv.FormatInt(paise/100, 10))
	}
	return inrExact(paise)
}

// rupeesInWords is the "amount in words" line: "Rupees Two Hundred Ninety Nine
// Only", "Rupees One Lakh Twenty Thousand and Fifty Paise Only". Indian scale
// words, because that is how the amount is read aloud and checked.
func rupeesInWords(paise int64) string {
	rupees, p := paise/100, paise%100
	words := "Rupees " + numberWords(rupees)
	if p > 0 {
		words += " and " + numberWords(p) + " Paise"
	}
	return words + " Only"
}

var (
	onesWords = []string{"Zero", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight",
		"Nine", "Ten", "Eleven", "Twelve", "Thirteen", "Fourteen", "Fifteen", "Sixteen",
		"Seventeen", "Eighteen", "Nineteen"}
	tensWords = []string{"", "", "Twenty", "Thirty", "Forty", "Fifty", "Sixty", "Seventy",
		"Eighty", "Ninety"}
)

func numberWords(n int64) string {
	if n < 20 {
		return onesWords[n]
	}
	var parts []string
	for _, scale := range []struct {
		div  int64
		name string
	}{{10000000, "Crore"}, {100000, "Lakh"}, {1000, "Thousand"}, {100, "Hundred"}} {
		if n >= scale.div {
			parts = append(parts, numberWords(n/scale.div)+" "+scale.name)
			n %= scale.div
		}
	}
	if n > 0 {
		switch {
		case n < 20:
			parts = append(parts, onesWords[n])
		case n%10 == 0:
			parts = append(parts, tensWords[n/10])
		default:
			parts = append(parts, tensWords[n/10]+" "+onesWords[n%10])
		}
	}
	return strings.Join(parts, " ")
}

// financialYear is the Indian financial year t falls in, as the invoice number
// writes it: 1 April 2026 to 31 March 2027 is "26-27". Read in the business
// timezone — a payment at 00:30 IST on 1 April belongs to the new year even
// though it is still 31 March in UTC.
func financialYear(t time.Time, loc *time.Location) string {
	lt := t.In(loc)
	start := lt.Year()
	if lt.Month() < time.April {
		start--
	}
	return fmt.Sprintf("%02d-%02d", start%100, (start+1)%100)
}
