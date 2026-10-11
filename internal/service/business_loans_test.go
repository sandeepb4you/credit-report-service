package service

import (
	"os"
	"testing"
)

// loan builds an open tradeline of the given Account_Type with one clean month.
func loan(accountType string) caisAccountDetail {
	return caisAccountDetail{
		AccountType:   accountType,
		AccountStatus: "11", // open
		CAISAccountHistory: caisAccountHistoryList{
			{Year: "2026", Month: "08", DaysPastDue: "000"},
		},
	}
}

func withOverdue(a caisAccountDetail, rupees string) caisAccountDetail {
	a.AmountPastDue = rupees
	return a
}

func TestBusinessLoanMatchRules(t *testing.T) {
	late := loan("05")
	late.CAISAccountHistory = caisAccountHistoryList{{Year: "2025", Month: "02", DaysPastDue: "030"}}
	settled := loan("05")
	settled.WrittenOffSettledStatus = "03"
	closedLoan := loan("05")
	closedLoan.AccountStatus = "13"

	tests := []struct {
		name     string
		score    int64
		accounts []caisAccountDetail
		want     string
	}{
		{"clean file, 812", 812, []caisAccountDetail{loan("05")}, BusinessLoanExcellent},
		{"clean file, 702", 702, nil, BusinessLoanGood},
		{"exactly 750 is excellent", 750, nil, BusinessLoanExcellent},
		{"exactly 650 passes R1", 650, nil, BusinessLoanGood},
		{"R1: 649", 649, nil, BusinessLoanNotMatch},

		{"R2: three loans pass", 800, []caisAccountDetail{loan("05"), loan("51"), loan("61")}, BusinessLoanExcellent},
		{"R2: four loans fail", 800, []caisAccountDetail{loan("05"), loan("51"), loan("61"), loan("69")}, BusinessLoanNotMatch},
		{"R2: gold loans are not counted", 800, []caisAccountDetail{loan("05"), loan("51"), loan("61"), loan("07"), loan("70")}, BusinessLoanExcellent},
		{"R2: closed loans are not counted", 800, []caisAccountDetail{loan("05"), loan("51"), loan("61"), closedLoan}, BusinessLoanExcellent},
		{"R2: cards and home loans are not counted", 800, []caisAccountDetail{loan("05"), loan("51"), loan("61"), loan("10"), loan("02")}, BusinessLoanExcellent},

		{"R3: small overdue is good, not excellent", 800, []caisAccountDetail{withOverdue(loan("05"), "4000")}, BusinessLoanGood},
		{"R3: overdue summed across loans", 800, []caisAccountDetail{withOverdue(loan("05"), "6000"), withOverdue(loan("51"), "5000")}, BusinessLoanNotMatch},
		{"R3: exactly 10,000 passes", 800, []caisAccountDetail{withOverdue(loan("05"), "10000")}, BusinessLoanGood},
		{"R3: card overdue is not counted", 800, []caisAccountDetail{withOverdue(loan("10"), "50000")}, BusinessLoanExcellent},

		{"R4: any past DPD fails", 800, []caisAccountDetail{late}, BusinessLoanNotMatch},
		{"R4: settled or written off fails", 800, []caisAccountDetail{settled}, BusinessLoanNotMatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := businessLoanMatch(tt.score, tt.accounts); got != tt.want {
				t.Errorf("businessLoanMatch = %s, want %s", got, tt.want)
			}
		})
	}
}

// The demo bureau files (internal/digitap/testdata) through the same rules. Pinned
// so that a rule change shows up here as a deliberate edit: the 700 file is a
// Not-a-match because it carries late months, and R4 refuses any past DPD.
func TestBusinessLoanMatchOnTheDemoReports(t *testing.T) {
	want := map[string]string{
		"score_500_poor.json":      BusinessLoanNotMatch,
		"score_600_fair.json":      BusinessLoanNotMatch,
		"score_700_good.json":      BusinessLoanNotMatch,
		"score_800_excellent.json": BusinessLoanExcellent,
	}
	for file, status := range want {
		raw, err := os.ReadFile("../digitap/testdata/" + file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		accounts, err := matchAccounts(raw)
		score := extractBureauScore(raw)
		if err != nil || score == nil {
			t.Fatalf("%s: accounts err %v, score %v", file, err, score)
		}
		if got := businessLoanMatch(*score, accounts); got != status {
			t.Errorf("%s: %s, want %s", file, got, status)
		}
	}
}
