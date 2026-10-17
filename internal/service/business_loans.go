package service

import (
	"context"
	"encoding/json"
	"errors"

	"credit-report-service/internal/repository"
)

// Business-loan match statuses, as the app keys its colours and copy on them.
const (
	BusinessLoanExcellent = "excellent"
	BusinessLoanGood      = "good"
	BusinessLoanNotMatch  = "not_match"
	// BusinessLoanNoScore is an account with no scored report yet. The list is
	// shown to it anyway (design rule 1) with a nudge to check the score.
	BusinessLoanNoScore = "no_score"
	// BusinessLoanOpen is a lender that screens nobody: it has no eligibility
	// rules of its own, so every account can apply whatever its score, and an
	// account with no report at all still gets a usable card. Kept distinct
	// from excellent/good rather than folded into them, because those two say
	// "based on your score" and this one was never computed from a score.
	BusinessLoanOpen = "open"
)

// The match rules (design/screen-business-loans-list-final.html, "Internal
// match rules"). Never sent to the app and never named to the user: the screen
// shows the status and what to do next, not which rule failed, so the rules
// can be tuned without anyone learning to game them.
const (
	blMinScore          = 650   // R1
	blMaxActiveLoans    = 3     // R2: personal + business loans, gold excluded
	blMaxOverdueRupees  = 10000 // R3: loans only, credit cards excluded
	blExcellentMinScore = 750   // Good vs Excellent — the design's proposal, marked for confirmation
)

// blCountedLoanTypes are the Account_Type codes R2 counts: personal and
// business loans, from the bureau's account-type master (accountTypeMap).
// Gold loans (7, 70) are left out by the rule; non-funded facilities (55–58)
// are guarantees rather than loans and are left out too.
var blCountedLoanTypes = map[string]bool{
	// Personal
	"5": true, "41": true, "45": true, "69": true,
	// Business
	"40": true, "50": true, "51": true, "52": true, "53": true, "54": true, "59": true, "61": true,
}

// blNonLoanTypes are left out of R3's overdue total: cards (the rule says
// "credit cards excluded, loans only") and telecom lines, which are not loans.
var blNonLoanTypes = map[string]bool{
	"10": true, "16": true, "31": true, "35": true, // credit, fleet, secured, corporate cards
	"18": true, "19": true, "20": true, // telco
}

// businessLoanLenderName is the screened lender — the one R1-R4 above decide
// for. Named once so the retargeting list that counts business-loan matches
// (list I) cannot quote a different provider from the screen the customer
// sees. businessLoanOpenLenderName is screened by nothing and so is not in
// that list: there is no match to count.
const (
	businessLoanLenderName     = "Muthoot FinCorp"
	businessLoanOpenLenderName = "mPokket"
)

// businessLoanFeeDisclosure is on every card, per the RBI digital-lending
// rules the design cites: the user is told we are paid for the handoff before
// they take it.
const businessLoanFeeDisclosure = "myScorr may earn a referral fee from lenders. This never changes the rate you pay."

// BusinessLoanLender is one lender on the list. Copy and figures live here
// rather than in the app so they change with a deploy, not a release.
type BusinessLoanLender struct {
	Name         string `json:"name"`
	Regulator    string `json:"regulator"`
	MaxAmount    int64  `json:"maxAmount"` // rupees
	TenureMonths int    `json:"tenureMonths"`
	// TenureMinMonths is the short end of a tenure range; 0 means the lender
	// quotes one figure and TenureMonths is it. TenureMonths stays the long
	// end, so an app that predates the range still prints a true maximum.
	TenureMinMonths int      `json:"tenureMinMonths,omitempty"`
	Collateral      string   `json:"collateral"`
	Features        []string `json:"features"`
	OfferURL        string   `json:"offerUrl,omitempty"`
	FeeDisclosure   string   `json:"feeDisclosure"`
	// MatchStatus is this lender's own verdict, which is not always the
	// profile's: an unscreened lender is "open" to an account the screened one
	// turns away. The app draws the card from this and the strip from the
	// list's, so the two can disagree without either being wrong.
	MatchStatus string `json:"matchStatus"`
}

// BusinessLoanList is the response for GET /api/loans/business.
type BusinessLoanList struct {
	// MatchStatus is the profile's standing — excellent, good, not_match or
	// no_score — and is what the strip above the cards reads. It is the
	// screened lender's verdict; it is never "open", which is a property of a
	// lender rather than of a borrower.
	MatchStatus string `json:"matchStatus"`
	// Score is the score the status was computed from; null with no_score.
	Score   *int64               `json:"score"`
	Lenders []BusinessLoanLender `json:"lenders"`
}

type BusinessLoanService struct {
	reports      *repository.CreditAnalyticsRepo
	offerURL     string
	openOfferURL string
}

func NewBusinessLoanService(reports *repository.CreditAnalyticsRepo, offerURL, openOfferURL string) *BusinessLoanService {
	return &BusinessLoanService{reports: reports, offerURL: offerURL, openOfferURL: openOfferURL}
}

// lenders builds the list, given the profile's standing against the screened
// lender. Order is deliberate: the screened lender leads, because it is the
// larger facility and the one the status strip is about.
func (s *BusinessLoanService) lenders(screened string) []BusinessLoanLender {
	return []BusinessLoanLender{
		{
			Name:         businessLoanLenderName,
			Regulator:    "RBI-registered NBFC",
			MaxAmount:    1000000,
			TenureMonths: 36,
			Collateral:   "Nil",
			Features: []string{
				"No collateral or guarantor needed",
				"Quick online application",
			},
			OfferURL:      s.offerURL,
			FeeDisclosure: businessLoanFeeDisclosure,
			MatchStatus:   screened,
		},
		{
			Name:            businessLoanOpenLenderName,
			Regulator:       "RBI-registered NBFC",
			MaxAmount:       200000,
			TenureMinMonths: 6,
			TenureMonths:    24,
			Collateral:      "Nil",
			Features: []string{
				"No minimum credit score",
				"Fully online, no paperwork",
			},
			OfferURL:      s.openOfferURL,
			FeeDisclosure: businessLoanFeeDisclosure,
			MatchStatus:   BusinessLoanOpen,
		},
	}
}

// List answers the business-loan screen. Shown to everyone, paid or not: an
// account with no report gets no_score rather than a 404. The status is worked
// out from the newest scored report on every call, so it moves with each
// refresh.
func (s *BusinessLoanService) List(ctx context.Context, accountID int64) (*BusinessLoanList, error) {
	out := &BusinessLoanList{MatchStatus: BusinessLoanNoScore}
	out.Lenders = s.lenders(out.MatchStatus)

	row, err := s.reports.FindLatestByAccount(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	score := row.CreditScore
	if score == nil {
		score = extractBureauScore(row.ResponseBody)
	}
	if score == nil {
		// A pull with no score (a thin file) cannot pass R1 or fail it; it is
		// treated as "check your score", not as a rejection.
		return out, nil
	}
	accounts, err := matchAccounts(row.ResponseBody)
	if err != nil {
		return nil, err
	}
	out.Score = score
	out.MatchStatus = businessLoanMatch(*score, accounts)
	out.Lenders = s.lenders(out.MatchStatus)
	return out, nil
}

// businessLoanMatch applies R1–R4, then splits a pass into Good or Excellent.
func businessLoanMatch(score int64, accounts []caisAccountDetail) string {
	if score < blMinScore { // R1
		return BusinessLoanNotMatch
	}
	activeLoans := 0
	var overdue float64
	for _, a := range accounts {
		code := normalizeCode(a.AccountType)
		active := isActiveStatus(a.AccountStatus)
		if active && blCountedLoanTypes[code] {
			activeLoans++
		}
		if active && !blNonLoanTypes[code] {
			overdue += atofSafe(a.AmountPastDue)
		}
		// R4: a settled or written-off account, or any month past due on any
		// account — cards included, since the rule reads "all accounts".
		if isDerogatoryStatus(a.WrittenOffSettledStatus) {
			return BusinessLoanNotMatch
		}
		for _, m := range paymentHistoryFor(a) {
			if m.DaysLate > 0 || m.Status == "delayed" {
				return BusinessLoanNotMatch
			}
		}
	}
	if activeLoans > blMaxActiveLoans { // R2
		return BusinessLoanNotMatch
	}
	if overdue > blMaxOverdueRupees { // R3
		return BusinessLoanNotMatch
	}
	if score >= blExcellentMinScore && overdue == 0 {
		return BusinessLoanExcellent
	}
	return BusinessLoanGood
}

// matchAccounts lifts the tradelines out of a stored report. An empty payload
// is no tradelines, not an error.
func matchAccounts(raw json.RawMessage) ([]caisAccountDetail, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var wrapper struct {
		ResultJSON struct {
			INProfileResponse struct {
				CAISAccount struct {
					CAISAccountDetails caisAccountDetailList `json:"CAIS_Account_DETAILS"`
				} `json:"CAIS_Account"`
			} `json:"INProfileResponse"`
		} `json:"result_json"`
	}
	if err := json.Unmarshal(unwrapResultObject(raw), &wrapper); err != nil {
		return nil, err
	}
	return wrapper.ResultJSON.INProfileResponse.CAISAccount.CAISAccountDetails, nil
}
