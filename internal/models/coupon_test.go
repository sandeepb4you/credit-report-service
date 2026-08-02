package models

import (
	"testing"
	"time"
)

// The discount is money, so rounding is checked explicitly rather than
// assumed. Every case is stated in rupees and paise.
func TestCoupon_ApplyTo(t *testing.T) {
	tests := []struct {
		name         string
		amount       float64
		percent      float64
		wantDiscount float64
		wantPayable  float64
	}{
		{"clean 20% of 299", 299.00, 20, 59.80, 239.20},
		{"10% of 299", 299.00, 10, 29.90, 269.10},
		{"33% of 299 rounds to paise", 299.00, 33, 98.67, 200.33},
		{"1% of 299", 299.00, 1, 2.99, 296.01},
		{"full 100% leaves nothing payable", 299.00, 100, 299.00, 0},
		{"fractional percent", 100.00, 12.5, 12.50, 87.50},
		{"half-paise rounds up", 1.00, 33, 0.33, 0.67},
		{"zero amount is a no-op", 0, 50, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Coupon{DiscountPercent: tt.percent}
			discount, payable := c.ApplyTo(tt.amount)
			if !nearlyEqual(discount, tt.wantDiscount) {
				t.Errorf("discount = %.4f, want %.2f", discount, tt.wantDiscount)
			}
			if !nearlyEqual(payable, tt.wantPayable) {
				t.Errorf("payable = %.4f, want %.2f", payable, tt.wantPayable)
			}
		})
	}
}

// Discount plus payable must always reconstruct the original price exactly, or
// the order row and the redemption row disagree about what happened.
func TestCoupon_ApplyTo_SumsBackToOriginal(t *testing.T) {
	amounts := []float64{1, 9.99, 99.95, 100, 199.99, 299, 1234.56, 99999.99}
	percents := []float64{1, 3, 7.5, 12.5, 33, 50, 66.67, 99, 100}
	for _, amt := range amounts {
		for _, pct := range percents {
			c := &Coupon{DiscountPercent: pct}
			discount, payable := c.ApplyTo(amt)
			if !nearlyEqual(discount+payable, amt) {
				t.Errorf("%.2f at %.2f%%: discount %.2f + payable %.2f != %.2f",
					amt, pct, discount, payable, amt)
			}
			if payable < 0 {
				t.Errorf("%.2f at %.2f%% produced a negative payable %.2f", amt, pct, payable)
			}
			if discount > amt {
				t.Errorf("%.2f at %.2f%% discounted more than the price: %.2f", amt, pct, discount)
			}
		}
	}
}

func TestCoupon_AppliesTo(t *testing.T) {
	credit := "CREDIT_ANALYSIS"
	scoped := &Coupon{ProductCode: &credit}
	if !scoped.AppliesTo(credit) {
		t.Error("scoped coupon should apply to its own product")
	}
	if scoped.AppliesTo("BANK_STATEMENT_ANALYSIS") {
		t.Error("scoped coupon must not apply to another product")
	}
	unscoped := &Coupon{}
	if !unscoped.AppliesTo("ANYTHING") {
		t.Error("unscoped coupon should apply to every product")
	}
}

func TestCoupon_UsableAt(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	hourAgo := now.Add(-time.Hour)
	hourAhead := now.Add(time.Hour)

	tests := []struct {
		name string
		c    Coupon
		want bool
	}{
		{"open window", Coupon{ValidFrom: hourAgo}, true},
		{"bounded and inside", Coupon{ValidFrom: hourAgo, ValidUntil: &hourAhead}, true},
		{"not started yet", Coupon{ValidFrom: hourAhead}, false},
		{"expired", Coupon{ValidFrom: hourAgo.Add(-time.Hour), ValidUntil: &hourAgo}, false},
		// Revocation wins over an otherwise-open window.
		{"revoked", Coupon{ValidFrom: hourAgo, RevokedAt: &hourAgo}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.UsableAt(now); got != tt.want {
				t.Errorf("UsableAt = %v, want %v", got, tt.want)
			}
		})
	}
}

// valid_until is exclusive: a coupon is dead the instant it is reached.
func TestCoupon_UsableAt_ExpiryIsExclusive(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	c := Coupon{ValidFrom: now.Add(-time.Hour), ValidUntil: &now}
	if c.UsableAt(now) {
		t.Error("a coupon must not be usable at its exact expiry instant")
	}
	if !c.UsableAt(now.Add(-time.Nanosecond)) {
		t.Error("a coupon should still be usable a moment before expiry")
	}
}

func TestCoupon_Exhausted(t *testing.T) {
	two := 2
	tests := []struct {
		name string
		c    Coupon
		want bool
	}{
		{"unlimited never exhausts", Coupon{RedemptionCount: 9999}, false},
		{"under the cap", Coupon{MaxRedemptions: &two, RedemptionCount: 1}, false},
		{"at the cap", Coupon{MaxRedemptions: &two, RedemptionCount: 2}, true},
		{"over the cap", Coupon{MaxRedemptions: &two, RedemptionCount: 3}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.Exhausted(); got != tt.want {
				t.Errorf("Exhausted = %v, want %v", got, tt.want)
			}
		})
	}
}

// nearlyEqual compares to well under a paisa, so a genuine rounding error is
// caught while float representation noise is not.
func nearlyEqual(a, b float64) bool {
	d := a - b
	return d < 0.0001 && d > -0.0001
}
