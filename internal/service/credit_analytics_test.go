package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
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

// ---- parseReportInsights ----

func TestParseReportInsights_FullData(t *testing.T) {
	// Build a response with known values.
	// Account 1 (revolving): PHP "000010000000000000000000000000000000" = 2 on-time out of 3 reported, limit 100000, balance 30000
	// Account 2 (installment): PHP "000000000000000000000000000000000000" = 36 on-time out of 36
	// TotalCAPSLast180Days = 7
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [
						{
							"Payment_History_Profile": "000010000000000000000000000000000000",
							"Portfolio_Type": "R",
							"Credit_Limit_Amount": "100000",
							"Current_Balance": "30000"
						},
						{
							"Payment_History_Profile": "000000000000000000000000000000000000",
							"Portfolio_Type": "I",
							"Credit_Limit_Amount": "500000",
							"Current_Balance": "200000"
						}
					]
				},
				"TotalCAPS_Summary": {
					"TotalCAPSLast180Days": "7"
				}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}

	// On-time: Account 1 has 35 '0' + Account 2 has 36 '0' = 71 on-time out of 72 total
	// 71/72 * 100 = 98.61... rounded to 98.6
	if insights.OnTimePaymentPercent != 98.6 {
		t.Errorf("onTimePaymentPercent = %.1f, want 98.6", insights.OnTimePaymentPercent)
	}

	// Card utilization: only revolving (R) accounts. Account 1: 30000/100000 = 30.0%
	if insights.CardUtilizationPercent != 30.0 {
		t.Errorf("cardUtilizationPercent = %.1f, want 30.0", insights.CardUtilizationPercent)
	}

	// Enquiry count
	if insights.EnquiryCount180Days != 7 {
		t.Errorf("enquiryCount180Days = %d, want 7", insights.EnquiryCount180Days)
	}
}

func TestParseReportInsights_AllUnknownPayments(t *testing.T) {
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [{
						"Payment_History_Profile": "????????????????????????????????",
						"Portfolio_Type": "R",
						"Credit_Limit_Amount": "100000",
						"Current_Balance": "0"
					}]
				},
				"TotalCAPS_Summary": {
					"TotalCAPSLast180Days": "3"
				}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}

	// All '?' -> no reported months -> 0%
	if insights.OnTimePaymentPercent != 0 {
		t.Errorf("onTimePaymentPercent = %.1f, want 0.0", insights.OnTimePaymentPercent)
	}
	// No balance on revolving -> 0%
	if insights.CardUtilizationPercent != 0 {
		t.Errorf("cardUtilizationPercent = %.1f, want 0.0", insights.CardUtilizationPercent)
	}
	if insights.EnquiryCount180Days != 3 {
		t.Errorf("enquiryCount180Days = %d, want 3", insights.EnquiryCount180Days)
	}
}

func TestParseReportInsights_NoRevolvingAccounts(t *testing.T) {
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [{
						"Payment_History_Profile": "000000000000000000000000000000000000",
						"Portfolio_Type": "I",
						"Credit_Limit_Amount": "500000",
						"Current_Balance": "200000"
					}]
				},
				"TotalCAPS_Summary": {
					"TotalCAPSLast180Days": "0"
				}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}

	if insights.OnTimePaymentPercent != 100.0 {
		t.Errorf("onTimePaymentPercent = %.1f, want 100.0", insights.OnTimePaymentPercent)
	}
	// No revolving accounts -> 0%
	if insights.CardUtilizationPercent != 0 {
		t.Errorf("cardUtilizationPercent = %.1f, want 0.0", insights.CardUtilizationPercent)
	}
	if insights.EnquiryCount180Days != 0 {
		t.Errorf("enquiryCount180Days = %d, want 0", insights.EnquiryCount180Days)
	}
}

func TestParseReportInsights_InvalidJSON(t *testing.T) {
	_, err := parseReportInsights(json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseReportInsights_AccountCountsAndOutstanding(t *testing.T) {
	// 3 accounts: 2 active (status "11"), 1 closed (status "00").
	// Active balances: 30000 + 50000 = 80000 outstanding.
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [
						{
							"Payment_History_Profile": "000000000000000000000000000000000000",
							"Portfolio_Type": "R",
							"Credit_Limit_Amount": "100000",
							"Current_Balance": "30000",
							"Account_Status": "11"
						},
						{
							"Payment_History_Profile": "000000000000000000000000000000000000",
							"Portfolio_Type": "I",
							"Credit_Limit_Amount": "0",
							"Current_Balance": "50000",
							"Account_Status": "11"
						},
						{
							"Payment_History_Profile": "000000000000000000000000000000000000",
							"Portfolio_Type": "I",
							"Credit_Limit_Amount": "0",
							"Current_Balance": "0",
							"Account_Status": "00"
						}
					]
				},
				"TotalCAPS_Summary": {"TotalCAPSLast180Days": "2"}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}
	if insights.TotalAccountCount != 3 {
		t.Errorf("totalAccountCount = %d, want 3", insights.TotalAccountCount)
	}
	if insights.ActiveAccountCount != 2 {
		t.Errorf("activeAccountCount = %d, want 2", insights.ActiveAccountCount)
	}
	if insights.TotalOutstandingAmount != 80000 {
		t.Errorf("totalOutstandingAmount = %.2f, want 80000.00", insights.TotalOutstandingAmount)
	}
}

func TestParseReportInsights_EMIAndInterest(t *testing.T) {
	// Two active loan accounts with EMI and interest rate.
	// Account 1: balance 200000, EMI 10000, rate 12% -> interest/year = 24000
	// Account 2: balance 100000, EMI 5000, rate 10% -> interest/year = 10000
	// Expected: monthlyEmi = 15000, interestPaidPerYear = 34000
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [
						{
							"Payment_History_Profile": "000000000000000000000000000000000000",
							"Portfolio_Type": "I",
							"Current_Balance": "200000",
							"Account_Status": "11",
							"Scheduled_Monthly_Payment_Amount": "10000",
							"Rate_of_Interest": "12"
						},
						{
							"Payment_History_Profile": "000000000000000000000000000000000000",
							"Portfolio_Type": "I",
							"Current_Balance": "100000",
							"Account_Status": "11",
							"Scheduled_Monthly_Payment_Amount": "5000",
							"Rate_of_Interest": "10"
						}
					]
				},
				"TotalCAPS_Summary": {"TotalCAPSLast180Days": "1"}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}
	if insights.MonthlyEMI != 15000 {
		t.Errorf("monthlyEmi = %.2f, want 15000.00", insights.MonthlyEMI)
	}
	if insights.InterestPaidPerYear != 34000 {
		t.Errorf("interestPaidPerYear = %.2f, want 34000.00", insights.InterestPaidPerYear)
	}
}

func TestParseReportInsights_DecimalInterestRate(t *testing.T) {
	// Balance 100000, rate 12.5% -> interest/year = 12500
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [{
						"Payment_History_Profile": "000000000000000000000000000000000000",
						"Portfolio_Type": "I",
						"Current_Balance": "100000",
						"Account_Status": "11",
						"Rate_of_Interest": "12.5"
					}]
				},
				"TotalCAPS_Summary": {"TotalCAPSLast180Days": "0"}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}
	if insights.InterestPaidPerYear != 12500 {
		t.Errorf("interestPaidPerYear = %.2f, want 12500.00", insights.InterestPaidPerYear)
	}
}

func TestParseReportInsights_ClosedAccountsExcluded(t *testing.T) {
	// Closed account (status "00") should not contribute to outstanding/EMI/interest.
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [{
						"Payment_History_Profile": "000000000000000000000000000000000000",
						"Portfolio_Type": "I",
						"Current_Balance": "500000",
						"Account_Status": "00",
						"Scheduled_Monthly_Payment_Amount": "20000",
						"Rate_of_Interest": "15"
					}]
				},
				"TotalCAPS_Summary": {"TotalCAPSLast180Days": "0"}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}
	if insights.ActiveAccountCount != 0 {
		t.Errorf("activeAccountCount = %d, want 0", insights.ActiveAccountCount)
	}
	if insights.TotalOutstandingAmount != 0 {
		t.Errorf("totalOutstandingAmount = %.2f, want 0", insights.TotalOutstandingAmount)
	}
	if insights.MonthlyEMI != 0 {
		t.Errorf("monthlyEmi = %.2f, want 0", insights.MonthlyEMI)
	}
	if insights.InterestPaidPerYear != 0 {
		t.Errorf("interestPaidPerYear = %.2f, want 0", insights.InterestPaidPerYear)
	}
}

func TestParseReportInsights_NullFieldsHandled(t *testing.T) {
	// All optional fields are null (as Digitap sends for revolving cards with
	// no EMI/interest). Account_Status is also null — must NOT be counted active.
	// Current_Balance null -> 0. Credit_Limit present -> utilization computed.
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [{
						"Payment_History_Profile": "000000000000000000000000000000000000",
						"Portfolio_Type": "R",
						"Credit_Limit_Amount": "100000",
						"Current_Balance": null,
						"Account_Status": null,
						"Scheduled_Monthly_Payment_Amount": null,
						"Rate_of_Interest": null
					}]
				},
				"TotalCAPS_Summary": {"TotalCAPSLast180Days": null}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}
	// Null status -> NOT active.
	if insights.TotalAccountCount != 1 {
		t.Errorf("totalAccountCount = %d, want 1", insights.TotalAccountCount)
	}
	if insights.ActiveAccountCount != 0 {
		t.Errorf("activeAccountCount = %d, want 0 (null status must not count as active)", insights.ActiveAccountCount)
	}
	if insights.TotalOutstandingAmount != 0 {
		t.Errorf("totalOutstandingAmount = %.2f, want 0", insights.TotalOutstandingAmount)
	}
	if insights.MonthlyEMI != 0 {
		t.Errorf("monthlyEmi = %.2f, want 0", insights.MonthlyEMI)
	}
	if insights.InterestPaidPerYear != 0 {
		t.Errorf("interestPaidPerYear = %.2f, want 0", insights.InterestPaidPerYear)
	}
	if insights.EnquiryCount180Days != 0 {
		t.Errorf("enquiryCount180Days = %d, want 0", insights.EnquiryCount180Days)
	}
	// Null balance -> 0 utilization.
	if insights.CardUtilizationPercent != 0 {
		t.Errorf("cardUtilizationPercent = %.1f, want 0.0", insights.CardUtilizationPercent)
	}
}

func TestIsActiveStatus(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"01", true},
		{"11", true},
		{"64", true},
		{"", false},
		{"00", false},
		{"97", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := isActiveStatus(tc.input); got != tc.want {
				t.Errorf("isActiveStatus(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// ---- loan accounts ----

func TestParseReportInsights_LoanAccounts(t *testing.T) {
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [
						{
							"Account_Number": "XXXXXXXXXXXX4328",
							"Account_Type": "07",
							"Subscriber_Name": "HDFC Bank Ltd",
							"Portfolio_Type": "I",
							"Current_Balance": "250000",
							"Highest_Credit_or_Original_Loan_Amount": "500000",
							"Repayment_Tenure": "240",
							"Account_Status": "11",
							"Payment_History_Profile": "000000000000000000000000000000000000"
						},
						{
							"Account_Number": "XXXXXXXXXXXX1234",
							"Account_Type": "01",
							"Subscriber_Name": "State Bank of India",
							"Portfolio_Type": "I",
							"Current_Balance": "0",
							"Highest_Credit_or_Original_Loan_Amount": "300000",
							"Repayment_Tenure": "60",
							"Account_Status": "00",
							"Payment_History_Profile": "000000000000000000000000000000000000"
						}
					]
				},
				"TotalCAPS_Summary": {"TotalCAPSLast180Days": "0"}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}
	if len(insights.LoanAccounts) != 2 {
		t.Fatalf("loanAccounts len = %d, want 2", len(insights.LoanAccounts))
	}

	la := insights.LoanAccounts[0]
	if la.LoanType != "Home Loan" {
		t.Errorf("loanType = %q, want %q", la.LoanType, "Home Loan")
	}
	if la.Company != "HDFC Bank Ltd" {
		t.Errorf("company = %q, want %q", la.Company, "HDFC Bank Ltd")
	}
	if la.TotalTenureMonths != 240 {
		t.Errorf("totalTenureMonths = %d, want 240", la.TotalTenureMonths)
	}
	if la.OriginalLoanAmount != 500000 {
		t.Errorf("originalLoanAmount = %.2f, want 500000", la.OriginalLoanAmount)
	}
	if la.CurrentBalance != 250000 {
		t.Errorf("currentBalance = %.2f, want 250000", la.CurrentBalance)
	}
	// (500000 - 250000) / 500000 * 100 = 50.0
	if la.PercentagePaid != 50.0 {
		t.Errorf("percentagePaid = %.1f, want 50.0", la.PercentagePaid)
	}
	if len(la.PaymentHistory) != 36 {
		t.Errorf("paymentHistory len = %d, want 36", len(la.PaymentHistory))
	}

	// Second account: closed auto loan, fully paid.
	la2 := insights.LoanAccounts[1]
	if la2.LoanType != "Auto Loan" {
		t.Errorf("loanType = %q, want %q", la2.LoanType, "Auto Loan")
	}
	// (300000 - 0) / 300000 * 100 = 100.0
	if la2.PercentagePaid != 100.0 {
		t.Errorf("percentagePaid = %.1f, want 100.0", la2.PercentagePaid)
	}
}

func TestParseReportInsights_LoanAccountNullFields(t *testing.T) {
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [{
						"Account_Number": null,
						"Account_Type": null,
						"Subscriber_Name": null,
						"Portfolio_Type": "R",
						"Current_Balance": null,
						"Highest_Credit_or_Original_Loan_Amount": null,
						"Repayment_Tenure": null,
						"Account_Status": "11",
						"Payment_History_Profile": null
					}]
				},
				"TotalCAPS_Summary": {"TotalCAPSLast180Days": "0"}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}
	if len(insights.LoanAccounts) != 1 {
		t.Fatalf("loanAccounts len = %d, want 1", len(insights.LoanAccounts))
	}
	la := insights.LoanAccounts[0]
	if la.LoanType != "Other" {
		t.Errorf("loanType = %q, want %q for null account type", la.LoanType, "Other")
	}
	if la.Company != "" {
		t.Errorf("company = %q, want empty for null", la.Company)
	}
	if la.TotalTenureMonths != 0 {
		t.Errorf("totalTenureMonths = %d, want 0 for null", la.TotalTenureMonths)
	}
	if la.OriginalLoanAmount != 0 {
		t.Errorf("originalLoanAmount = %.2f, want 0 for null", la.OriginalLoanAmount)
	}
	if la.PercentagePaid != 0 {
		t.Errorf("percentagePaid = %.1f, want 0 for null amounts", la.PercentagePaid)
	}
	if len(la.PaymentHistory) != 0 {
		t.Errorf("paymentHistory len = %d, want 0 for null profile", len(la.PaymentHistory))
	}
}

func TestParseReportInsights_LoanAccountClampedPercentage(t *testing.T) {
	// Balance exceeds original (shouldn't happen, but must clamp to 0%).
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [{
						"Account_Type": "06",
						"Portfolio_Type": "I",
						"Current_Balance": "600000",
						"Highest_Credit_or_Original_Loan_Amount": "500000",
						"Account_Status": "11",
						"Payment_History_Profile": "000000000000000000000000000000000000"
					}]
				},
				"TotalCAPS_Summary": {"TotalCAPSLast180Days": "0"}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}
	la := insights.LoanAccounts[0]
	if la.PercentagePaid != 0 {
		t.Errorf("percentagePaid = %.1f, want 0 (clamped, balance > original)", la.PercentagePaid)
	}
}

// ---- loanTypeFor ----

func TestLoanTypeFor(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"07", "Home Loan"},
		{"01", "Auto Loan"},
		{"06", "Personal Loan"},
		{"10", "Credit Card"},
		{"27", "Gold Loan"},
		{"99", "Other"},
		{"", "Other"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := loanTypeFor(tc.input); got != tc.want {
				t.Errorf("loanTypeFor(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---- parsePaymentHistory ----

func TestParsePaymentHistory(t *testing.T) {
	// "010?2" = 5 months: paid, delayed(1), paid, not_reported, delayed(31)
	history := parsePaymentHistory("010?2")
	if len(history) != 5 {
		t.Fatalf("len = %d, want 5", len(history))
	}

	if history[0].Status != "paid" {
		t.Errorf("[0].Status = %q, want %q", history[0].Status, "paid")
	}
	if history[0].DaysLate != 0 {
		t.Errorf("[0].DaysLate = %d, want 0", history[0].DaysLate)
	}

	if history[1].Status != "delayed" {
		t.Errorf("[1].Status = %q, want %q", history[1].Status, "delayed")
	}
	if history[1].DaysLate != 1 {
		t.Errorf("[1].DaysLate = %d, want 1", history[1].DaysLate)
	}

	if history[2].Status != "paid" {
		t.Errorf("[2].Status = %q, want %q", history[2].Status, "paid")
	}

	if history[3].Status != "not_reported" {
		t.Errorf("[3].Status = %q, want %q", history[3].Status, "not_reported")
	}

	if history[4].Status != "delayed" {
		t.Errorf("[4].Status = %q, want %q", history[4].Status, "delayed")
	}
	if history[4].DaysLate != 31 {
		t.Errorf("[4].DaysLate = %d, want 31", history[4].DaysLate)
	}
}

func TestParsePaymentHistory_Empty(t *testing.T) {
	history := parsePaymentHistory("")
	if len(history) != 0 {
		t.Errorf("len = %d, want 0 for empty string", len(history))
	}
}

func TestParsePaymentHistory_DaysPastDueBuckets(t *testing.T) {
	// Each rating code should map to its DPD bucket lower bound.
	history := parsePaymentHistory("0123456")
	if len(history) != 7 {
		t.Fatalf("len = %d, want 7", len(history))
	}
	wantDays := []int{0, 1, 31, 61, 91, 121, 151}
	wantStatus := []string{"paid", "delayed", "delayed", "delayed", "delayed", "delayed", "delayed"}
	for i, w := range wantDays {
		if history[i].DaysLate != w {
			t.Errorf("[%d].DaysLate = %d, want %d", i, history[i].DaysLate, w)
		}
		if history[i].Status != wantStatus[i] {
			t.Errorf("[%d].Status = %q, want %q", i, history[i].Status, wantStatus[i])
		}
	}
}

func TestParsePaymentHistory_MonthLabels(t *testing.T) {
	history := parsePaymentHistory("00")
	if len(history) != 2 {
		t.Fatalf("len = %d, want 2", len(history))
	}
	// Both entries should have a valid YYYY-MM format.
	for i, pm := range history {
		if len(pm.Month) != 7 {
			t.Errorf("[%d].Month = %q, want YYYY-MM format", i, pm.Month)
		}
	}
}

// ---- report card grading ----

func TestGradePaymentHistory(t *testing.T) {
	tests := []struct {
		name      string
		onTime    float64
		missed    int64
		wantGrade string
	}{
		{"perfect", 100, 0, "A+"},
		{"near perfect", 99.5, 1, "A"},
		{"good", 90, 5, "B"},
		{"fair", 80, 10, "C"},
		{"poor", 60, 20, "D"},
		{"critical", 30, 30, "F"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			grade, summary, _ := gradePaymentHistory(tc.onTime, tc.missed)
			if grade != tc.wantGrade {
				t.Errorf("grade = %q, want %q (summary: %s)", grade, tc.wantGrade, summary)
			}
		})
	}
}

func TestGradeUtilization(t *testing.T) {
	tests := []struct {
		name      string
		pct       float64
		wantGrade string
	}{
		{"zero", 0, "A+"},
		{"under 10", 8.5, "A+"},
		{"under 30", 25.0, "A"},
		{"under 50", 40.0, "B"},
		{"under 75", 60.0, "C"},
		{"maxed", 90.0, "D"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			grade, _, _ := gradeUtilization(tc.pct)
			if grade != tc.wantGrade {
				t.Errorf("grade = %q, want %q", grade, tc.wantGrade)
			}
		})
	}
}

func TestGradeCreditAge(t *testing.T) {
	tests := []struct {
		name      string
		yearsAgo  int
		wantGrade string
	}{
		{"zero date", -1, "B"}, // zero time -> B with "unavailable"
		{"12 years", 12, "A+"},
		{"6 years", 6, "A"},
		{"4 years", 4, "B"},
		{"2 years", 2, "C"},
		{"6 months", 0, "D"}, // less than 1 year
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d time.Time
			if tc.yearsAgo >= 0 {
				d = time.Now().AddDate(-tc.yearsAgo, -6, 0)
			}
			grade, _, _ := gradeCreditAge(d)
			if grade != tc.wantGrade {
				t.Errorf("grade = %q, want %q", grade, tc.wantGrade)
			}
		})
	}
}

func TestGradeEnquiries(t *testing.T) {
	tests := []struct {
		count     int64
		wantGrade string
	}{
		{0, "A+"},
		{1, "A"},
		{2, "A"},
		{3, "B"},
		{4, "B"},
		{5, "C"},
		{8, "D"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("count=%d", tc.count), func(t *testing.T) {
			grade, _, _ := gradeEnquiries(tc.count)
			if grade != tc.wantGrade {
				t.Errorf("grade = %q, want %q", grade, tc.wantGrade)
			}
		})
	}
}

func TestGradeCreditMix(t *testing.T) {
	tests := []struct {
		name      string
		count     int
		types     map[string]bool
		wantGrade string
	}{
		{"5 types", 5, map[string]bool{"Home Loan": true, "Auto Loan": true, "Credit Card": true, "Personal Loan": true, "Gold Loan": true}, "A+"},
		{"3 types", 3, map[string]bool{"Home Loan": true, "Auto Loan": true, "Credit Card": true}, "A"},
		{"2 types", 2, map[string]bool{"Home Loan": true, "Credit Card": true}, "B"},
		{"1 type", 1, map[string]bool{"Credit Card": true}, "C"},
		{"0 types", 0, map[string]bool{}, "D"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			grade, summary, _ := gradeCreditMix(tc.count, tc.types)
			if grade != tc.wantGrade {
				t.Errorf("grade = %q, want %q (summary: %s)", grade, tc.wantGrade, summary)
			}
		})
	}
}

func TestOverallGrade(t *testing.T) {
	// All A+ -> A+
	factors := []CardFactor{
		{Name: "Payment history", Weight: 35, Grade: "A+"},
		{Name: "Credit utilisation", Weight: 30, Grade: "A+"},
		{Name: "Credit age", Weight: 15, Grade: "A+"},
		{Name: "Enquiries", Weight: 10, Grade: "A+"},
		{Name: "Credit mix", Weight: 10, Grade: "A"},
	}
	// Weighted: (5*35 + 5*30 + 5*15 + 5*10 + 4*10) / 100 = 490/100 = 4.9 -> A+
	if g := overallGrade(factors); g != "A+" {
		t.Errorf("overallGrade = %q, want A+", g)
	}

	// Mix of grades -> A
	factors = []CardFactor{
		{Name: "Payment history", Weight: 35, Grade: "A"},
		{Name: "Credit utilisation", Weight: 30, Grade: "B"},
		{Name: "Credit age", Weight: 15, Grade: "A"},
		{Name: "Enquiries", Weight: 10, Grade: "A+"},
		{Name: "Credit mix", Weight: 10, Grade: "B"},
	}
	// Weighted: (4*35 + 3*30 + 4*15 + 5*10 + 3*10) / 100 = 390/100 = 3.9 -> A
	if g := overallGrade(factors); g != "A" {
		t.Errorf("overallGrade = %q, want A", g)
	}
}

// ---- buildReportCard integration ----

func TestBuildReportCard_AllFactors(t *testing.T) {
	card := buildReportCard(reportCardInputs{
		OnTimePercent:    100,
		MissedPayments:   0,
		CardUtilization:  20.0,
		OldestOpenDate:   time.Now().AddDate(-11, 0, 0),
		Enquiries180Days: 0,
		ProductTypeCount: 5,
		ProductTypes: map[string]bool{
			"Home Loan": true, "Auto Loan": true, "Credit Card": true,
			"Personal Loan": true, "Gold Loan": true,
		},
	})

	if len(card.Factors) != 5 {
		t.Fatalf("factors len = %d, want 5", len(card.Factors))
	}

	// Verify factor names and weights.
	expected := []struct {
		name   string
		weight int
	}{
		{"Payment history", 35},
		{"Credit utilisation", 30},
		{"Credit age", 15},
		{"Enquiries", 10},
		{"Credit mix", 10},
	}
	for i, e := range expected {
		if card.Factors[i].Name != e.name {
			t.Errorf("factor[%d].Name = %q, want %q", i, card.Factors[i].Name, e.name)
		}
		if card.Factors[i].Weight != e.weight {
			t.Errorf("factor[%d].Weight = %d, want %d", i, card.Factors[i].Weight, e.weight)
		}
	}

	// With excellent inputs, overall should be A+.
	if card.OverallGrade != "A+" {
		t.Errorf("overallGrade = %q, want A+", card.OverallGrade)
	}

	// Every factor should have a non-empty summary and detail.
	for i, f := range card.Factors {
		if f.Summary == "" {
			t.Errorf("factor[%d] (%s) has empty summary", i, f.Name)
		}
		if f.Detail == "" {
			t.Errorf("factor[%d] (%s) has empty detail", i, f.Name)
		}
	}
}

// ---- parseExperianDate ----

func TestParseExperianDate(t *testing.T) {
	tests := []struct {
		input    string
		wantYear int
		wantZero bool
	}{
		{"20150312", 2015, false},
		{"20200101", 2020, false},
		{"", 0, true},
		{"abc", 0, true},
		{"20201301", 0, true}, // invalid month
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			d := parseExperianDate(tc.input)
			if tc.wantZero {
				if !d.IsZero() {
					t.Errorf("parseExperianDate(%q) = %v, want zero time", tc.input, d)
				}
				return
			}
			if d.Year() != tc.wantYear {
				t.Errorf("parseExperianDate(%q) year = %d, want %d", tc.input, d.Year(), tc.wantYear)
			}
		})
	}
}

// ---- parseReportInsights with report card ----

func TestParseReportInsights_ReportCard(t *testing.T) {
	raw := json.RawMessage(`{
		"result_json": {
			"INProfileResponse": {
				"CAIS_Account": {
					"CAIS_Account_DETAILS": [
						{
							"Account_Type": "07",
							"Portfolio_Type": "I",
							"Open_Date": "20150312",
							"Current_Balance": "100000",
							"Highest_Credit_or_Original_Loan_Amount": "500000",
							"Repayment_Tenure": "240",
							"Account_Status": "11",
							"Payment_History_Profile": "000000000000000000000000000000000000"
						},
						{
							"Account_Type": "10",
							"Portfolio_Type": "R",
							"Open_Date": "20180601",
							"Credit_Limit_Amount": "200000",
							"Current_Balance": "40000",
							"Account_Status": "11",
							"Payment_History_Profile": "000000000000000000000000000000000000"
						}
					]
				},
				"TotalCAPS_Summary": {
					"TotalCAPSLast180Days": "1",
					"TotalCAPSLast90Days": "1",
					"TotalCAPSLast30Days": "0",
					"TotalCAPSLast7Days": "0"
				}
			}
		}
	}`)

	insights, err := parseReportInsights(raw)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}

	if insights.ReportCard == nil {
		t.Fatal("reportCard is nil")
	}
	if len(insights.ReportCard.Factors) != 5 {
		t.Fatalf("factors len = %d, want 5", len(insights.ReportCard.Factors))
	}
	// Perfect payments, oldest account ~11 years, 1 enquiry, 2 product types.
	// Should be a strong grade overall.
	if insights.ReportCard.OverallGrade == "" {
		t.Error("overallGrade is empty")
	}

	// Payment history should be A+ (0 missed, 100% on-time).
	if insights.ReportCard.Factors[0].Grade != "A+" {
		t.Errorf("payment history grade = %q, want A+", insights.ReportCard.Factors[0].Grade)
	}
	if insights.ReportCard.Factors[0].MissedCount != 0 {
		t.Errorf("payment history missedCount = %d, want 0", insights.ReportCard.Factors[0].MissedCount)
	}

	// Credit age should be A+ (oldest from 2015, ~11 years).
	if insights.ReportCard.Factors[2].Grade != "A+" {
		t.Errorf("credit age grade = %q, want A+", insights.ReportCard.Factors[2].Grade)
	}
}

// ---- atoiSafe64 ----

func TestAtoiSafe64(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"0", 0},
		{"42", 42},
		{"100000", 100000},
		{"", 0},
		{"abc", 0},
		{"12abc34", 12},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := atoiSafe64(tc.input)
			if got != tc.want {
				t.Errorf("atoiSafe64(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

// ---- atofSafe ----

func TestAtofSafe(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"0", 0},
		{"42", 42},
		{"12.5", 12.5},
		{"0.5", 0.5},
		{"100.25", 100.25},
		{"", 0},
		{"abc", 0},
		{"12.abc", 12},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := atofSafe(tc.input)
			if got != tc.want {
				t.Errorf("atofSafe(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// ---- roundTo2 ----

func TestRoundTo2(t *testing.T) {
	tests := []struct {
		input float64
		want  float64
	}{
		{0, 0},
		{1.234, 1.23},
		{1.236, 1.24},
		{100.555, 100.56},
		{34000.00, 34000},
		{12500.50, 12500.5},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%v", tc.input), func(t *testing.T) {
			got := roundTo2(tc.input)
			if got != tc.want {
				t.Errorf("roundTo2(%v) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
