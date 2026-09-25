package config

import "testing"

// A GSTIN that is malformed, or registered in another state than the one the
// invoice prints as the supplier's, is cleared rather than printed: live orders
// then go un-invoiced with a warning, which is recoverable, where a wrong
// number on every invoice is not.
func TestInvoiceConfigRejectsABadGSTIN(t *testing.T) {
	cases := []struct {
		name, gstin, state string
		keep               bool
	}{
		{"valid", "29abcde1234f1zw", "29", true}, // upper-cased on the way in
		{"the design's placeholder", "29XXXXX0000X1ZX", "29", false},
		{"one character off", "29ABCDE1234F1ZX", "29", false},
		{"the GST portal's worked example", "27AAPFU0939F1ZV", "27", true},
		{"other state", "36ABCDE1234F1Z1", "29", false},
		{"empty", "", "29", false},
	}
	for _, c := range cases {
		cfg := InvoiceConfig{GSTIN: c.gstin, StateCode: c.state, SAC: "998399", Series: "MSC", GSTRatePercent: 18}
		warns := cfg.validate()
		if got := cfg.Issuable(); got != c.keep {
			t.Errorf("%s: Issuable = %v, want %v (warnings %v)", c.name, got, c.keep, warns)
		}
		if c.gstin != "" && !c.keep && len(warns) == 0 {
			t.Errorf("%s: a rejected GSTIN must say so", c.name)
		}
	}
}

func TestInvoiceConfigNeedsASAC(t *testing.T) {
	cfg := InvoiceConfig{GSTIN: "29ABCDE1234F1ZW", StateCode: "29", Series: "MSC", GSTRatePercent: 18}
	cfg.validate()
	if cfg.Issuable() {
		t.Fatalf("issuable with no SAC")
	}
}
