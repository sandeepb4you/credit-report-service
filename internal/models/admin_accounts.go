package models

import "time"

// AdminAccountRow is one line of the admin user list: who they are, when they
// joined, and the four figures the console is read for — their latest score,
// whether they have ever paid, how many people they brought in, and how many
// of those went on to buy.
//
// The contact details are NOT masked here, unlike the referral report's user
// list. The console is web-only, admin-only and permission-gated, and the
// number exists on this screen to be copied into a call or a WhatsApp message
// — a masked one could not be. The referral report masks because its rows are
// about *other people's* recruits; this screen is the operator's own customer
// list.
type AdminAccountRow struct {
	AccountID int64  `json:"accountId" db:"account_id"`
	// Name is first+last trimmed, and empty for an account whose profile has
	// never been filled — most phone signups before PAN verification. The app
	// decides what to print in that gap; the server does not invent a name.
	Name      string     `json:"name"      db:"name"`
	Phone     *string    `json:"phone"     db:"phone"`
	Email     *string    `json:"email"     db:"email"`
	Status    string     `json:"status"    db:"status"`
	SignedUpAt time.Time `json:"signedUpAt" db:"signed_up_at"`
	// CreditScore is the score on the most recent *successful* pull that
	// carried one, and nil for an account that has never had a report or whose
	// only reports came back score-less. Deliberately not the newest row: a
	// degraded 200 with no score must not blank out the score they last saw.
	CreditScore *int64 `json:"creditScore" db:"credit_score"`
	// Paid is whether any order has ever reached PAID. Not a count — the
	// question the list answers is "customer or not".
	Paid bool `json:"paid" db:"paid"`
	// ReferredCount is how many accounts carry this one as their referrer;
	// ReferredPaidCount how many of those have a PAID order. The second is the
	// one the reward is owed on, so a gap between them is the conversion gap.
	ReferredCount     int `json:"referredCount"     db:"referred_count"`
	ReferredPaidCount int `json:"referredPaidCount" db:"referred_paid_count"`
}

// AdminAccountPage is a window of the user list plus the total it was cut
// from, so the console can say "50 of 1,284" rather than "50".
type AdminAccountPage struct {
	Items  []AdminAccountRow `json:"items"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

// AdminAccountCheck is one past score check on the detail view.
type AdminAccountCheck struct {
	ID          int64     `json:"id"          db:"id"`
	CreatedAt   time.Time `json:"createdAt"   db:"created_at"`
	CreditScore *int64    `json:"creditScore" db:"credit_score"`
}

// AdminAccountDetail is what opening a row shows: the account itself, its KYC
// record (nil when nothing was ever submitted) and its most recent checks.
//
// KYC is the full record rather than the client-facing KYCStatus projection —
// the reviewer needs the PAN and the name on it, which is exactly what that
// projection strips. Same data the PAN review queue already shows, and the
// same permission gate.
type AdminAccountDetail struct {
	Account *Account            `json:"account"`
	KYC     *KYCRecord          `json:"kyc"`
	Checks  []AdminAccountCheck `json:"checks"`
}

// AccountSortColumn is a column the admin user list can be ordered by.
//
// A closed set, not a string from the caller: the value reaches an ORDER BY,
// and the only safe way to put a client's choice there is to look it up in a
// table the server owns. An unknown value is a 400 rather than a silent
// fallback to the default — a console showing rows in an order nobody asked
// for is worse than one that says the sort was rejected.
type AccountSortColumn string

const (
	AccountSortSignedUp  AccountSortColumn = "signedUp"
	AccountSortName      AccountSortColumn = "name"
	AccountSortPhone     AccountSortColumn = "phone"
	AccountSortScore     AccountSortColumn = "score"
	AccountSortPaid      AccountSortColumn = "paid"
	AccountSortReferred  AccountSortColumn = "referred"
	AccountSortConverted AccountSortColumn = "converted"

	// AccountSortDefault is newest signup first, which is the order the list
	// is read in when nobody has chosen one.
	AccountSortDefault = AccountSortSignedUp
)

// accountSortSQL maps each sortable column to the CTE output column it orders
// by. Output-column names, not expressions: Postgres resolves them against the
// select list, so the derived figures sort without repeating their subqueries.
var accountSortSQL = map[AccountSortColumn]string{
	AccountSortSignedUp:  "signed_up_at",
	AccountSortName:      "name",
	AccountSortPhone:     "phone",
	AccountSortScore:     "credit_score",
	AccountSortPaid:      "paid",
	AccountSortReferred:  "referred_count",
	AccountSortConverted: "referred_paid_count",
}

// ParseAccountSort resolves a client's sort key, defaulting when it is absent.
// The bool reports whether the value was known.
func ParseAccountSort(raw string) (AccountSortColumn, bool) {
	if raw == "" {
		return AccountSortDefault, true
	}
	c := AccountSortColumn(raw)
	if _, ok := accountSortSQL[c]; !ok {
		return "", false
	}
	return c, true
}

// SQL renders the ORDER BY fragment for this column.
//
// NULLS LAST in both directions, deliberately: Postgres puts nulls first on a
// DESC sort, so "highest score" would open on every account that has never had
// one. A missing figure is not an extreme of that figure, and an operator
// sorting by score wants the scores.
func (c AccountSortColumn) SQL(desc bool) string {
	col, ok := accountSortSQL[c]
	if !ok {
		col = accountSortSQL[AccountSortDefault]
	}
	if desc {
		return col + " DESC NULLS LAST"
	}
	return col + " ASC NULLS LAST"
}

// SortableAccountColumns lists every accepted sort key, for the 400 message.
func SortableAccountColumns() []string {
	return []string{
		string(AccountSortSignedUp), string(AccountSortName), string(AccountSortPhone),
		string(AccountSortScore), string(AccountSortPaid),
		string(AccountSortReferred), string(AccountSortConverted),
	}
}
