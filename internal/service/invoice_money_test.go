package service

import (
	"testing"
	"time"
)

// The three figures the invoice design was drawn with. If these move, the
// rounding rule has changed, and the design's own arithmetic says it should not.
func TestSplitGSTMatchesTheDesign(t *testing.T) {
	cases := []struct {
		total               int64
		intra               bool
		taxable, cgst, sgst int64
		igst                int64
	}{
		{29900, true, 25339, 2280, 2281, 0}, // Starter: 253.39 + 22.80 + 22.81
		{69900, false, 59237, 0, 0, 10663},  // Quarterly, inter-state: 592.37 + 106.63
		{99900, true, 84661, 7619, 7620, 0}, // Monthly: 846.61 + 76.19 + 76.20
	}
	for _, c := range cases {
		got := splitGST(c.total, 18, c.intra)
		want := gstSplit{Taxable: c.taxable, CGST: c.cgst, SGST: c.sgst, IGST: c.igst}
		if got != want {
			t.Errorf("splitGST(%d) = %+v, want %+v", c.total, got, want)
		}
	}
}

// Whatever the amount, the lines add back to exactly what was charged. That is
// the one property the database CHECK also enforces; this finds a rounding bug
// before an insert does.
func TestSplitGSTAlwaysSumsToTheTotal(t *testing.T) {
	for total := int64(0); total <= 200000; total += 7 {
		for _, intra := range []bool{true, false} {
			s := splitGST(total, 18, intra)
			if sum := s.Taxable + s.CGST + s.SGST + s.IGST; sum != total {
				t.Fatalf("splitGST(%d, intra=%v) sums to %d", total, intra, sum)
			}
			if s.CGST > s.SGST {
				t.Fatalf("splitGST(%d): the odd paisa belongs to SGST, got %+v", total, s)
			}
		}
	}
}

func TestRupeesInWords(t *testing.T) {
	cases := map[int64]string{
		29900:     "Rupees Two Hundred Ninety Nine Only",
		69900:     "Rupees Six Hundred Ninety Nine Only",
		99900:     "Rupees Nine Hundred Ninety Nine Only",
		100:       "Rupees One Only",
		0:         "Rupees Zero Only",
		26950:     "Rupees Two Hundred Sixty Nine and Fifty Paise Only",
		12000005:  "Rupees One Lakh Twenty Thousand and Five Paise Only",
		358800:    "Rupees Three Thousand Five Hundred Eighty Eight Only",
		123456789: "Rupees Twelve Lakh Thirty Four Thousand Five Hundred Sixty Seven and Eighty Nine Paise Only",
	}
	for paise, want := range cases {
		if got := rupeesInWords(paise); got != want {
			t.Errorf("rupeesInWords(%d) = %q, want %q", paise, got, want)
		}
	}
}

func TestFormatRupeesGroupsTheIndianWay(t *testing.T) {
	cases := map[int64]string{
		29900:     "₹299.00",
		258900:    "₹2,589.00",
		12345678:  "₹1,23,456.78",
		100000000: "₹10,00,000.00",
		5:         "₹0.05",
	}
	for paise, want := range cases {
		if got := inrExact(paise); got != want {
			t.Errorf("inrExact(%d) = %q, want %q", paise, got, want)
		}
	}
	if got := inr(258900); got != "₹2,589" {
		t.Errorf("inr = %q", got)
	}
}

// The year turns on 1 April in India, not in UTC.
func TestFinancialYearTurnsOnFirstAprilIST(t *testing.T) {
	ist, _ := time.LoadLocation("Asia/Kolkata")
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC), "26-27"},
		{time.Date(2027, 3, 31, 18, 0, 0, 0, time.UTC), "26-27"}, // 23:30 IST, 31 March
		{time.Date(2027, 3, 31, 19, 0, 0, 0, time.UTC), "27-28"}, // 00:30 IST, 1 April
		{time.Date(2099, 12, 1, 0, 0, 0, 0, time.UTC), "99-00"},
	}
	for _, c := range cases {
		if got := financialYear(c.at, ist); got != c.want {
			t.Errorf("financialYear(%s) = %q, want %q", c.at, got, c.want)
		}
	}
}
