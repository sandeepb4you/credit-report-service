package digitap

// mock.go synthesizes a realistic, randomized Experian-style INProfileResponse
// credit report. It is used only by the offline stub client (see client.go) so
// the service returns rich data while the Digitap UAT endpoint is unavailable,
// or when no client credentials are configured.
//
// The shape mirrors the documented Digitap result.result_json payload (spec
// V2.7, section 1.4.2). Values are randomized per call but kept internally
// consistent — e.g. an account's payment history and CAIS history agree on the
// open date, the summary counts match the generated accounts, and the masked
// identity fields are derived from the request payload.

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

// reportInput is the subset of the credit-analytics request payload used to
// personalize the synthesized report.
type reportInput struct {
	ClientRefNum string `json:"client_ref_num"`
	MobileNo     string `json:"mobile_no"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	PAN          string `json:"pan"`
}

// generateReport builds a randomized INProfileResponse wrapped in the
// result_json envelope, personalized from the supplied request payload. The
// payload is the same value passed to Request (a digitapPayload in production,
// but treated opaquely here).
func generateReport(payload any) json.RawMessage {
	in := extractReportInput(payload)

	now := time.Now().UTC()
	accounts := generateAccounts(now)

	summary := buildSummary(accounts)
	score := 680 + rand.IntN(150) // 680–829

	report := map[string]any{
		"INProfileResponse": map[string]any{
			"Header": map[string]any{
				"SystemCode":  "0",
				"MessageText": nil,
				"ReportDate":  now.Format("20060102"),
				"ReportTime":  now.Format("150405"),
			},
			"UserMessage": map[string]any{
				"UserMessageText": "Normal Response",
			},
			"CreditProfileHeader": map[string]any{
				"Enquiry_Username": "customized_match_v3__decimusfin_~DS",
				"ReportDate":       now.Format("20060102"),
				"ReportTime":       now.Format("150405"),
				"Version":          "V2.4",
				"ReportNumber":     fmt.Sprintf("%d", now.UnixMilli()),
				"Subscriber":       nil,
				"Subscriber_Name":  "Bureau Disclosure Report with Customized Match V3",
			},
			"Current_Application": map[string]any{
				"Current_Application_Details": map[string]any{
					"Enquiry_Reason":            fmt.Sprintf("%d", 1+rand.IntN(9)),
					"Finance_Purpose":           nil,
					"Amount_Financed":           "0",
					"Duration_Of_Agreement":     "0",
					"Current_Applicant_Details": buildApplicantDetails(in),
					"Current_Other_Details": map[string]any{
						"Income":                           "0",
						"Marital_Status":                   nil,
						"Employment_Status":                nil,
						"Time_with_Employer":               nil,
						"Number_of_Major_Credit_Card_Held": nil,
					},
					"Current_Applicant_Address_Details": []any{
						map[string]any{
							"FlatNoPlotNoHouseNo":    nil,
							"BldgNoSocietyName":      nil,
							"RoadNoNameAreaLocality": nil,
							"City":                   nil,
							"Landmark":               nil,
							"State":                  nil,
							"PINCode":                nil,
							"Country_Code":           "IB",
						},
					},
					"Current_Applicant_Additional_AddressDetails": nil,
				},
			},
			"CAIS_Account": map[string]any{
				"CAIS_Summary":         summary,
				"CAIS_Account_DETAILS": accounts,
			},
			"Match_result": map[string]any{
				"Exact_match": "Y",
			},
			"TotalCAPS_Summary": capsSummary("TotalCAPS", 0),
			"CAPS": map[string]any{
				"CAPS_Summary": capsSummary("CAPS", rand.IntN(3)),
			},
			"NonCreditCAPS": map[string]any{
				"NonCreditCAPS_Summary": capsSummary("NonCreditCAPS", rand.IntN(2)),
			},
			"SCORE": map[string]any{
				"BureauScore":            fmt.Sprintf("%d", score),
				"BureauScoreConfidLevel": nil,
			},
		},
	}

	b, _ := json.Marshal(map[string]any{"result_json": report})
	return b
}

// extractReportInput pulls the personalizing fields out of the opaque request
// payload via a JSON round-trip, tolerating any payload shape.
func extractReportInput(payload any) reportInput {
	var in reportInput
	if b, err := json.Marshal(payload); err == nil {
		_ = json.Unmarshal(b, &in)
	}
	return in
}

func buildApplicantDetails(in reportInput) map[string]any {
	return map[string]any{
		"Last_Name":                      in.LastName,
		"First_Name":                     in.FirstName,
		"Middle_Name1":                   nil,
		"Middle_Name2":                   nil,
		"Middle_Name3":                   nil,
		"Gender_Code":                    nil,
		"IncomeTaxPan":                   nil,
		"PAN_Issue_Date":                 nil,
		"PAN_Expiration_Date":            nil,
		"Passport_number":                nil,
		"Passport_Issue_Date":            nil,
		"Passport_Expiration_Date":       nil,
		"Voter_s_Identity_Card":          nil,
		"Voter_ID_Issue_Date":            nil,
		"Voter_ID_Expiration_Date":       nil,
		"Driver_License_Number":          nil,
		"Driver_License_Issue_Date":      nil,
		"Driver_License_Expiration_Date": nil,
		"Ration_Card_Number":             nil,
		"Ration_Card_Issue_Date":         nil,
		"Ration_Card_Expiration_Date":    nil,
		"Universal_ID_Number":            nil,
		"Universal_ID_Issue_Date":        nil,
		"Universal_ID_Expiration_Date":   nil,
		"Date_Of_Birth_Applicant":        nil,
		"Telephone_Number_Applicant_1st": nil,
		"Telephone_Extension":            nil,
		"Telephone_Type":                 nil,
		"MobilePhoneNumber":              in.MobileNo,
		"EMailId":                        nil,
	}
}

// ---- CAIS account generation -------------------------------------------------

// A small catalogue of plausible Indian subscribers / portfolio types so the
// synthesized tradelines read like a real bureau file.
var subscribers = []struct {
	name   string
	prefix string
}{
	{"HDFC Bank Ltd", "PVTHDFC"},
	{"State Bank of India", "PVTSBI0"},
	{"ICICI Bank Limited", "PVTICIC"},
	{"Axis Bank Limited", "PVTAXIS"},
	{"Kotak Mahindra Bank", "PVTKOTK"},
	{"Citibank N.A.", "PVTCITI"},
	{"Bajaj Finance Ltd", "PVTBAJA"},
	{"American Express", "PVTAMEX"},
}

// portfolioType -> set of account-type codes used in the sample schema.
// Portfolio codes: R = Revolving (credit card), I = Installment (loan),
// O = Open, C = Consumer (auto/personal). Account_Type "10" = credit card,
// "06" = personal loan, "01" = auto loan, "07" = home loan in the schema.
var portfolioTypes = []struct {
	portfolio string
	accounts  []string
}{
	{"R", []string{"10", "09"}},       // revolving / cards
	{"I", []string{"06", "01", "07"}}, // installment / loans
	{"C", []string{"32", "33"}},       // consumer durable / auto
}

// generateAccounts returns between 1 and 5 randomized tradelines. At least one
// is active; the rest are a mix of active/closed, with a small chance of one
// written-off/defaulted account so the report exercises the delinquency fields.
func generateAccounts(now time.Time) []any {
	n := 1 + rand.IntN(5)
	out := make([]any, 0, n)
	hasDefault := n > 1 && rand.IntN(4) == 0 // ~25% chance when n>1

	for i := 0; i < n; i++ {
		forcedDefault := hasDefault && i == 0
		out = append(out, generateAccount(now, forcedDefault))
	}
	return out
}

func generateAccount(now time.Time, forceDefault bool) map[string]any {
	sub := subscribers[rand.IntN(len(subscribers))]
	pt := portfolioTypes[rand.IntN(len(portfolioTypes))]
	acctType := pt.accounts[rand.IntN(len(pt.accounts))]

	// Open date: 1–96 months ago.
	openAgeMonths := 1 + rand.IntN(96)
	openDate := now.AddDate(0, -openAgeMonths, -rand.IntN(28))

	// Decide lifecycle. Defaults are rare and force a written-off status.
	status, isClosed, isDefault := pickStatus(forceDefault)

	creditLimit := (1 + rand.IntN(50)) * 10000 // 10k–500k
	highCredit := creditLimit
	if pt.portfolio == "I" || pt.portfolio == "C" {
		// Loans: "highest credit" is the disbursed principal, not a limit.
		highCredit = (1 + rand.IntN(60)) * 25000
	}
	// Current balance: 0 for closed; otherwise a fraction of high credit.
	currentBalance := 0
	if !isClosed {
		currentBalance = rand.IntN(highCredit + 1)
	}

	// Monthly EMI for active installment/consumer loans. Revolving cards have
	// no fixed EMI (nil). Generated as ~1/24th of the disbursed amount.
	var scheduledMonthly any
	if !isClosed && (pt.portfolio == "I" || pt.portfolio == "C") {
		scheduledMonthly = fmt.Sprintf("%d", highCredit/24)
	}

	// Interest rate: loans carry 8–18% p.a.; revolving cards 24–42% p.a.
	var rateOfInterest any
	if !isClosed {
		if pt.portfolio == "R" {
			rateOfInterest = fmt.Sprintf("%d", 24+rand.IntN(18))
		} else {
			rateOfInterest = fmt.Sprintf("%d", 8+rand.IntN(10))
		}
	}

	// Repayment tenure (months): loans have fixed tenures (12–240); revolving
	// cards have no tenure (0).
	repaymentTenure := "0"
	if pt.portfolio == "I" || pt.portfolio == "C" {
		repaymentTenure = fmt.Sprintf("%d", 12+rand.IntN(229)) // 12–240 months
	}

	phpLen := 36
	monthsOpen := monthsBetween(openDate, now)
	if monthsOpen > phpLen {
		monthsOpen = phpLen
	}
	php := buildPaymentHistory(phpLen, monthsOpen, isDefault)
	paymentRating := string(php[0]) // most-recent month's rating

	history := buildCAISHistory(openDate, now, isDefault)

	acc := map[string]any{
		"Identification_Number":                   fmt.Sprintf("%s%03d", sub.prefix, 1+rand.IntN(999)),
		"Subscriber_Name":                         sub.name,
		"Account_Number":                          maskedAccountNumber(),
		"Portfolio_Type":                          pt.portfolio,
		"Account_Type":                            acctType,
		"Open_Date":                               openDate.Format("20060102"),
		"Credit_Limit_Amount":                     fmt.Sprintf("%d", creditLimit),
		"Highest_Credit_or_Original_Loan_Amount":  fmt.Sprintf("%d", highCredit),
		"Terms_Duration":                          nil,
		"Terms_Frequency":                         nil,
		"Scheduled_Monthly_Payment_Amount":        scheduledMonthly,
		"Account_Status":                          status,
		"Payment_Rating":                          paymentRating,
		"Payment_History_Profile":                 php,
		"Special_Comment":                         nil,
		"Current_Balance":                         fmt.Sprintf("%d", currentBalance),
		"Amount_Past_Due":                         amountPastDue(isDefault, currentBalance),
		"Original_Charge_Off_Amount":              chargeOffAmount(isDefault, currentBalance),
		"Date_Reported":                           now.AddDate(0, 0, -rand.IntN(20)).Format("20060102"),
		"Date_of_First_Delinquency":               dateOfFirstDelinquency(openDate, now, isDefault),
		"Date_Closed":                             closedDate(isClosed, now),
		"Date_of_Last_Payment":                    now.AddDate(0, 0, -rand.IntN(45)).Format("20060102"),
		"SuitFiledWillfulDefaultWrittenOffStatus": suitStatus(isDefault),
		"SuitFiled_WilfulDefault":                 nil,
		"Written_off_Settled_Status":              writtenOffStatus(isDefault),
		"Value_of_Credits_Last_Month":             nil,
		"Occupation_Code":                         pick([]string{"S", "P", "O"}),
		"Settlement_Amount":                       nil,
		"Value_of_Collateral":                     nil,
		"Type_of_Collateral":                      nil,
		"Written_Off_Amt_Total":                   nil,
		"Written_Off_Amt_Principal":               nil,
		"Rate_of_Interest":                        rateOfInterest,
		"Repayment_Tenure":                        repaymentTenure,
		"Promotional_Rate_Flag":                   nil,
		"Income":                                  nil,
		"Income_Indicator":                        nil,
		"Income_Frequency_Indicator":              nil,
		"DefaultStatusDate":                       defaultStatusDate(now, isDefault),
		"LitigationStatusDate":                    nil,
		"WriteOffStatusDate":                      writeOffStatusDate(now, isDefault),
		"DateOfAddition":                          openDate.AddDate(0, 0, 20).Format("20060102"),
		"CurrencyCode":                            "INR",
		"Subscriber_comments":                     nil,
		"Consumer_comments":                       nil,
		"AccountHoldertypeCode":                   "1",
		"CAIS_Account_History":                    history,
		"CAIS_Holder_Details":                     []any{holderDetails()},
		"CAIS_Holder_Address_Details":             []any{holderAddress()},
		"CAIS_Holder_Phone_Details":               []any{holderPhone()},
		"CAIS_Holder_ID_Details":                  []any{holderID()},
	}
	return acc
}

// pickStatus returns (Account_Status code, isClosed, isDefault).
//
// Account_Status codes (subset of the Experian schema):
//
//	"00" closed, "01" current/0 bal, "11" active >0 bal, "61-65" delinquent
//	buckets, "97" written-off.
func pickStatus(forceDefault bool) (string, bool, bool) {
	if forceDefault {
		return "97", true, true
	}
	switch r := rand.IntN(100); {
	case r < 15:
		return "00", true, false // closed, never late
	case r < 20:
		return "64", false, false // 61-90 days past due
	case r < 22:
		return "65", false, false // 91-120 days past due
	default:
		// Active: ~even split between paid-current (01) and carrying a balance (11).
		if rand.IntN(2) == 0 {
			return "01", false, false
		}
		return "11", false, false
	}
}

// buildPaymentHistory renders the 36-month payment-string. Each position is the
// payment rating for that month (0 = current, 1-6 = days-past-due buckets, '?'
// = before the account was opened / not reported). The most recent month is the
// first character, matching the bureau convention.
func buildPaymentHistory(length, monthsOpen int, isDefault bool) string {
	buf := make([]byte, length)
	// Months before the account existed are unknown.
	for i := monthsOpen; i < length; i++ {
		buf[i] = '?'
	}
	for i := 0; i < monthsOpen; i++ {
		buf[i] = paymentRatingChar(i, isDefault)
	}
	return string(buf)
}

// paymentRatingChar picks a rating for the i-th most-recent month. Defaults
// accumulate late ratings toward the older end of the history.
func paymentRatingChar(monthsBack int, isDefault bool) byte {
	if isDefault {
		// Deteriorating history: recent months worse, oldest occasionally clean.
		switch {
		case monthsBack < 6:
			return '5'
		case monthsBack < 12:
			return '4'
		default:
			if rand.IntN(4) == 0 {
				return '0'
			}
			return byte('1' + rand.IntN(3))
		}
	}
	// Healthy account: mostly current, with the occasional slip.
	switch rand.IntN(20) {
	case 0:
		return '1'
	case 1:
		return '2'
	default:
		return '0'
	}
}

// buildCAISHistory returns monthly {Year, Month, Days_Past_Due,
// Asset_Classification} entries from the open date up to the report date
// (capped at 36 months). "Asset_Classification" follows the RBI schema:
// "STD" standard, "SMA" special-mention, "SUB" sub-standard, "DBT" doubtful,
// "LSS" loss, "?" not reported (e.g. before the account reported).
func buildCAISHistory(openDate, now time.Time, isDefault bool) []any {
	months := monthsBetween(openDate, now)
	if months > 36 {
		months = 36
	}
	if months < 1 {
		months = 1
	}
	out := make([]any, 0, months)
	t := now
	for i := 0; i < months; i++ {
		dpd := "000"
		asset := "STD"
		if isDefault {
			switch {
			case i < 3:
				dpd, asset = "180", "DBT"
			case i < 6:
				dpd, asset = "120", "SUB"
			default:
				dpd, asset = "030", "SMA"
			}
		} else if rand.IntN(18) == 0 {
			dpd, asset = "030", "SMA"
		}
		out = append(out, map[string]any{
			"Year":                 t.Format("2006"),
			"Month":                t.Format("01"),
			"Days_Past_Due":        dpd,
			"Asset_Classification": asset,
		})
		t = t.AddDate(0, -1, 0)
	}
	return out
}

// ---- Summary -----------------------------------------------------------------

// buildSummary aggregates counts and balances from the generated accounts so
// the summary block agrees with the tradelines it summarizes.
func buildSummary(accounts []any) map[string]any {
	var total, active, closed, defaultCount int
	var secured, unsecured int

	for _, a := range accounts {
		m, _ := a.(map[string]any)
		total++
		status, _ := m["Account_Status"].(string)
		balStr, _ := m["Current_Balance"].(string)
		bal := atoiSafe(balStr)
		portfolio, _ := m["Portfolio_Type"].(string)

		switch {
		case status == "00" || status == "97":
			closed++
		default:
			active++
		}
		if status == "97" {
			defaultCount++
		}
		switch portfolio {
		case "I", "C":
			secured += bal
		default:
			unsecured += bal
		}
	}

	totalBal := secured + unsecured
	secPct := pct(secured, totalBal)
	unsecPct := 100 - secPct

	return map[string]any{
		"Credit_Account": map[string]any{
			"CreditAccountTotal":         fmt.Sprintf("%d", total),
			"CreditAccountActive":        fmt.Sprintf("%d", active),
			"CreditAccountDefault":       fmt.Sprintf("%d", defaultCount),
			"CreditAccountClosed":        fmt.Sprintf("%d", closed),
			"CADSuitFiledCurrentBalance": "0",
		},
		"Total_Outstanding_Balance": map[string]any{
			"Outstanding_Balance_Secured":              fmt.Sprintf("%d", secured),
			"Outstanding_Balance_Secured_Percentage":   fmt.Sprintf("%d", secPct),
			"Outstanding_Balance_UnSecured":            fmt.Sprintf("%d", unsecured),
			"Outstanding_Balance_UnSecured_Percentage": fmt.Sprintf("%d", unsecPct),
			"Outstanding_Balance_All":                  fmt.Sprintf("%d", totalBal),
		},
	}
}

// ---- Holder / ID / address / phone blocks ------------------------------------

func holderDetails() map[string]any {
	return map[string]any{
		"Surname_Non_Normalized":       upperName(),
		"First_Name_Non_Normalized":    nil,
		"Middle_Name_1_Non_Normalized": nil,
		"Middle_Name_2_Non_Normalized": nil,
		"Middle_Name_3_Non_Normalized": nil,
		"Alias":                        nil,
		"Gender_Code":                  fmt.Sprintf("%d", 1+rand.IntN(2)),
		"Income_TAX_PAN":               nil,
		"Passport_Number":              nil,
		"Voter_ID_Number":              nil,
		"Date_of_birth":                dob(),
	}
}

func holderAddress() map[string]any {
	city := pick([]string{"MUMBAI", "DELHI", "BENGALURU", "KOLKATA", "PUNE", "HYDERABAD", "CHENNAI"})
	return map[string]any{
		"First_Line_Of_Address_non_normalized":  fmt.Sprintf("%d %s", 1+rand.IntN(200), pickStreet()),
		"Second_Line_Of_Address_non_normalized": pickArea() + " COLONY " + city,
		"Third_Line_Of_Address_non_normalized":  "CORP " + pick([]string{"MUMBAI", "DELHI", "BENGALURU"}),
		"City_non_normalized":                   nil,
		"Fifth_Line_Of_Address_non_normalized":  nil,
		"State_non_normalized":                  fmt.Sprintf("%02d", 1+rand.IntN(35)),
		"ZIP_Postal_Code_non_normalized":        pinCode(),
		"CountryCode_non_normalized":            "IB",
		"Address_indicator_non_normalized":      "02",
		"Residence_code_non_normalized":         nil,
	}
}

func holderPhone() map[string]any {
	return map[string]any{
		"Telephone_Number":        nil,
		"Telephone_Type":          "01",
		"Telephone_Extension":     nil,
		"Mobile_Telephone_Number": "XXXXX" + fmt.Sprintf("%05d", rand.IntN(100000)),
		"FaxNumber":               nil,
		"EMailId":                 lowerName() + "@example.com",
	}
}

func holderID() map[string]any {
	return map[string]any{
		"Income_TAX_PAN":                 maskedPAN(),
		"PAN_Issue_Date":                 nil,
		"PAN_Expiration_Date":            nil,
		"Passport_Number":                nil,
		"Passport_Issue_Date":            nil,
		"Passport_Expiration_Date":       nil,
		"Voter_ID_Number":                nil,
		"Voter_ID_Issue_Date":            nil,
		"Voter_ID_Expiration_Date":       nil,
		"Driver_License_Number":          nil,
		"Driver_License_Issue_Date":      nil,
		"Driver_License_Expiration_Date": nil,
		"Ration_Card_Number":             nil,
		"Ration_Card_Issue_Date":         nil,
		"Ration_Card_Expiration_Date":    nil,
		"Universal_ID_Number":            nil,
		"Universal_ID_Issue_Date":        nil,
		"Universal_ID_Expiration_Date":   nil,
		"EMailId":                        lowerName() + "@example.com",
	}
}

// capsSummary builds one of the CAPS_*_Summary blocks. prefix selects the key
// family: "TotalCAPS", "CAPS", or "NonCreditCAPS". base is the 7-day count;
// longer windows count up from there to look like cumulative enquiry volumes.
func capsSummary(prefix string, base int) map[string]any {
	return map[string]any{
		prefix + "Last7Days":   fmt.Sprintf("%d", base),
		prefix + "Last30Days":  fmt.Sprintf("%d", base+rand.IntN(2)),
		prefix + "Last90Days":  fmt.Sprintf("%d", base+rand.IntN(3)),
		prefix + "Last180Days": fmt.Sprintf("%d", base+rand.IntN(5)),
	}
}

// ---- Small helpers -----------------------------------------------------------

func pick(s []string) string { return s[rand.IntN(len(s))] }

func maskedAccountNumber() string {
	// 15 X's followed by 4 random digits, like the sample "XXX...4328".
	return strings.Repeat("X", 15) + fmt.Sprintf("%04d", rand.IntN(10000))
}

func maskedPAN() string {
	// PAN shape ABCDE1234X masked as ABCDExxxxX.
	return fmt.Sprintf("%sXXXX%c", randomUpper(5), upperAlpha())
}

func amountPastDue(isDefault bool, balance int) any {
	if !isDefault || balance == 0 {
		return nil
	}
	return fmt.Sprintf("%d", balance/4)
}

func chargeOffAmount(isDefault bool, balance int) any {
	if !isDefault {
		return nil
	}
	return fmt.Sprintf("%d", balance)
}

func dateOfFirstDelinquency(openDate, now time.Time, isDefault bool) any {
	if !isDefault {
		return nil
	}
	d := openDate.Add((time.Duration(rand.IntN(180)) + 60) * 24 * time.Hour)
	if d.After(now) {
		d = now.AddDate(0, -3, 0)
	}
	return d.Format("20060102")
}

func closedDate(isClosed bool, now time.Time) any {
	if !isClosed {
		return nil
	}
	return now.AddDate(0, -rand.IntN(12), -rand.IntN(28)).Format("20060102")
}

func suitStatus(isDefault bool) any {
	if !isDefault {
		return nil
	}
	return "04" // written-off
}

func writtenOffStatus(isDefault bool) any {
	if !isDefault {
		return nil
	}
	return "W"
}

func defaultStatusDate(now time.Time, isDefault bool) any {
	if !isDefault {
		return nil
	}
	return now.AddDate(0, -rand.IntN(6), 0).Format("20060102")
}

func writeOffStatusDate(now time.Time, isDefault bool) any {
	if !isDefault {
		return nil
	}
	return now.AddDate(0, -rand.IntN(4), 0).Format("20060102")
}

func upperName() string {
	return pick([]string{"SHARMA", "VERMA", "NAIR", "REDDY", "DAS", "GUPTA", "KHANNA", "BANERJEE"})
}

func lowerName() string {
	return strings.ToLower(pick([]string{"sharma", "verma", "nair", "reddy", "das", "gupta", "khanna", "banerjee"}))
}

func upperAlpha() byte { return byte('A' + rand.IntN(26)) }

func randomUpper(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'A' + byte(rand.IntN(26))
	}
	return string(b)
}

func pinCode() string { return fmt.Sprintf("%06d", 100000+rand.IntN(899999)) }

func pickStreet() string {
	return pick([]string{"MG ROAD", "PARK STREET", "MARINE DRIVE", "RESIDENCY ROAD", "LINKING ROAD", "ANNA SALAI"})
}

func pickArea() string {
	return pick([]string{"SANGHATI NAGAR", "JUBILEE HILLS", "BANDRA WEST", "KORAMANGALA", "SALT LAKE"})
}

func dob() string {
	// Age 25-60.
	year := time.Now().Year() - (25 + rand.IntN(36))
	t := time.Date(year, time.Month(1+rand.IntN(12)), 1+rand.IntN(28), 0, 0, 0, 0, time.UTC)
	return t.Format("20060102")
}

func monthsBetween(a, b time.Time) int {
	if b.Before(a) {
		return 0
	}
	y := b.Year() - a.Year()
	m := int(b.Month()) - int(a.Month())
	res := y*12 + m
	if res < 0 {
		res = 0
	}
	return res
}

func pct(n, total int) int {
	if total == 0 {
		return 0
	}
	return n * 100 / total
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
