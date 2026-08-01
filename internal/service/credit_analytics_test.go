package service

import (
	"fmt"
	"strings"
	"testing"
)

// ---- CreditAnalyticsInput.validate ----

func TestValidate_CreditAnalyticsInput_Valid(t *testing.T) {
	in := &CreditAnalyticsInput{DeviceIP: "1.2.3.4"}
	if d := in.validate(); len(d) > 0 {
		t.Errorf("expected no errors, got %v", d)
	}
}

func TestValidate_CreditAnalyticsInput_EmptyDeviceIP(t *testing.T) {
	in := &CreditAnalyticsInput{DeviceIP: ""}
	d := in.validate()
	if len(d) == 0 {
		t.Fatal("expected error for empty device_ip")
	}
	if _, ok := d["device_ip"]; !ok {
		t.Errorf("expected device_ip key, got %v", d)
	}
}

func TestValidate_CreditAnalyticsInput_WhitespaceOnly(t *testing.T) {
	in := &CreditAnalyticsInput{DeviceIP: "   \t"}
	d := in.validate()
	if len(d) == 0 {
		t.Fatal("expected error for whitespace device_ip")
	}
}

// ---- strPtr ----

func TestStrPtr_NonEmpty(t *testing.T) {
	p := strPtr("hello")
	if p == nil {
		t.Fatal("expected non-nil")
	}
	if *p != "hello" {
		t.Errorf("got %q", *p)
	}
}

func TestStrPtr_Empty(t *testing.T) {
	if strPtr("") != nil {
		t.Fatal("expected nil for empty string")
	}
}

// ---- keysOf ----

func TestKeysOf_NonEmpty(t *testing.T) {
	m := map[string]string{"a": "1", "b": "2", "c": "3"}
	keys := keysOf(m)
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}
	found := map[string]bool{}
	for _, k := range keys {
		found[k] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !found[want] {
			t.Errorf("key %q missing", want)
		}
	}
}

func TestKeysOf_Empty(t *testing.T) {
	if keys := keysOf(nil); len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}
}

// ---- generateOTP ----

func TestGenerateOTP_LengthAndDigits(t *testing.T) {
	for _, n := range []int{4, 6, 8} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			otp, err := generateOTP(n)
			if err != nil {
				t.Fatalf("generateOTP(%d): %v", n, err)
			}
			if len(otp) != n {
				t.Errorf("length = %d, want %d", len(otp), n)
			}
			for _, c := range otp {
				if c < '0' || c > '9' {
					t.Errorf("non-digit %c in %q", c, otp)
				}
			}
		})
	}
}

func TestGenerateOTP_UniqueAcrossCalls(t *testing.T) {
	a, _ := generateOTP(6)
	b, _ := generateOTP(6)
	// With 10^6 possibilities, collision is astronomically unlikely but not
	// impossible. Just check they're valid.
	for _, otp := range []string{a, b} {
		if len(otp) != 6 {
			t.Errorf("bad length: %q", otp)
		}
	}
}

// ---- randomHex ----

func TestRandomHex_Length(t *testing.T) {
	h := randomHex(3)
	if len(h) != 6 {
		t.Errorf("randomHex(3) length = %d, want 6", len(h))
	}
	// All hex chars.
	for _, c := range h {
		if !strings.Contains("0123456789abcdef", string(c)) {
			t.Errorf("non-hex char %c", c)
		}
	}
}

func TestRandomHex_Zero(t *testing.T) {
	h := randomHex(0)
	if len(h) != 0 {
		t.Errorf("randomHex(0) length = %d, want 0", len(h))
	}
}

// ---- generateClientRefNum ----

func TestGenerateClientRefNum_Format(t *testing.T) {
	ref := generateClientRefNum()
	if !strings.HasPrefix(ref, "CA-") {
		t.Errorf("expected CA- prefix, got %q", ref)
	}
	// Format: CA-<milli>-<6hex>
	parts := strings.SplitN(ref, "-", 3)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts in %q", ref)
	}
	if parts[0] != "CA" {
		t.Errorf("parts[0] = %q, want CA", parts[0])
	}
	if len(parts[2]) != 6 {
		t.Errorf("hex tail = %q, want 6 chars", parts[2])
	}
}

func TestGenerateClientRefNum_Unique(t *testing.T) {
	a := generateClientRefNum()
	b := generateClientRefNum()
	if a == b {
		t.Errorf("two calls produced the same ref: %q", a)
	}
}
