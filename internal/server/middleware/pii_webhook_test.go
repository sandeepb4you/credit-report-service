package middleware

import (
	"strings"
	"testing"
)

// The shape Cashfree actually posts to /api/payments/cashfree/webhook. Its PII
// fields are namespaced (customer_name, not name), which an exact-name key
// allowlist does not catch — the customer's name and mobile number reached the
// logs in the clear. This pins the redaction.
const cashfreeSuccessBody = `{
  "data": {
    "customer_details": {
      "customer_email": "customer@example.com",
      "customer_id": "acct_1",
      "customer_name": "Test Customer",
      "customer_phone": "9999999999"
    },
    "order": {"order_amount": 299, "order_currency": "INR", "order_id": "737f6050-b1ff-4892-ae0e-287c06407361"},
    "payment": {
      "bank_reference": "206823902413856",
      "cf_payment_id": "206823902413856",
      "payment_group": "wallet",
      "payment_status": "SUCCESS"
    }
  },
  "type": "PAYMENT_SUCCESS_WEBHOOK"
}`

func TestMaskJSON_CashfreeWebhookHidesCustomerPII(t *testing.T) {
	got := string(maskJSON([]byte(cashfreeSuccessBody)))

	for _, secret := range []string{"Test Customer", "9999999999", "customer@example.com"} {
		if strings.Contains(got, secret) {
			t.Errorf("customer PII %q survived redaction:\n%s", secret, got)
		}
	}

	// The fields needed to debug a payment must stay readable — redaction that
	// blanks the whole payload would make the log useless.
	for _, keep := range []string{
		"737f6050-b1ff-4892-ae0e-287c06407361", // order_uid: how you find the order
		"206823902413856",                      // cf_payment_id / bank_reference
		"PAYMENT_SUCCESS_WEBHOOK",              // event type
		"SUCCESS",                              // payment_status
		"299",                                  // amount
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("over-redacted: %q should remain for debugging:\n%s", keep, got)
		}
	}
}

func TestIsSensitiveKey_NamespacedAndCamelCase(t *testing.T) {
	sensitive := []string{
		"customer_name", "customer_phone", "customer_email",
		"customerName", "customerPhone", "CustomerEmail",
		"name", "phone", "email", "pan", "dateOfBirth", "date_of_birth",
		"applicant_first_name", "user.mobile", "PANName",
		"aadhaar_last4", "primary_email",
	}
	for _, k := range sensitive {
		t.Run("sensitive/"+k, func(t *testing.T) {
			if !isSensitiveKey(k) {
				t.Errorf("isSensitiveKey(%q) = false, want true", k)
			}
		})
	}

	// Suffix matching must be over segments, not raw characters — and the
	// fields a payment engineer reads must stay visible.
	notSensitive := []string{
		"japan_code", // ends with "pan" as characters, not as a segment
		"order_id", "order_amount", "order_currency",
		"cf_payment_id", "bank_reference", "payment_status", "payment_group",
		"customer_id", "type", "gateway_order_id",
	}
	for _, k := range notSensitive {
		t.Run("visible/"+k, func(t *testing.T) {
			if isSensitiveKey(k) {
				t.Errorf("isSensitiveKey(%q) = true, want false", k)
			}
		})
	}
}

func TestMaskShapes_PhoneNumbers(t *testing.T) {
	masked := []string{"9999999999", "+91 9876543210", "+919876543210", "08123456789"}
	for _, s := range masked {
		t.Run("masked/"+s, func(t *testing.T) {
			if got := maskShapes(s); strings.Contains(got, "9876543210") || got == s {
				t.Errorf("maskShapes(%q) = %q, want redacted", s, got)
			}
		})
	}

	// 15-digit gateway references must survive: they are how a payment is
	// traced with Cashfree support.
	for _, s := range []string{"206823902413856", "12345", "299"} {
		t.Run("kept/"+s, func(t *testing.T) {
			if got := maskShapes(s); got != s {
				t.Errorf("maskShapes(%q) = %q, want unchanged", s, got)
			}
		})
	}
}
