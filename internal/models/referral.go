package models

import "time"

// ReferralReport is the admin-facing referral view for one date window.
//
// Referral attribution is written once, at signup, onto accounts.referred_at —
// so the window filters on when the referral happened, not on when either
// account was last active. Days are whole UTC days; ops in IST will see a
// signup just after 05:30 IST land on the previous report day, which is the
// price of not carrying a per-request timezone through the query.
type ReferralReport struct {
	// From and To are the inclusive first and last day of the window.
	From string `json:"from" example:"2026-07-31"`
	To   string `json:"to"   example:"2026-08-30"`

	// TotalReferred counts every attributed signup in the window and ignores
	// ReferrerID — it is the headline number for the period, so drilling into
	// one referrer must not make it move. The filtered count is Referred.Total.
	TotalReferred int `json:"totalReferred" example:"42"`

	// Referrers is the leaderboard for the window, busiest first. Always the
	// whole window, so the drill-down never hides who else was recruiting.
	Referrers []ReferrerSummary `json:"referrers"`

	// Referred is the page of individual signups, narrowed to one referrer
	// when the caller asked for that.
	Referred ReferredPage `json:"referred"`
}

// ReferrerSummary is one account and how many signups it brought in.
type ReferrerSummary struct {
	AccountID int64   `json:"accountId" example:"12" db:"account_id"`
	Name      string  `json:"name"      example:"Asha Menon" db:"name"`
	Phone     *string `json:"phone,omitempty" db:"phone"`
	Email     *string `json:"email,omitempty" db:"email"`

	// ReferralCode is the referrer's current live code. Null when the account
	// has never opened the app's referral screen — codes are minted on first
	// read, so an account can have referrals attributed to a code that was
	// later revoked and not yet replaced.
	ReferralCode *string `json:"referralCode,omitempty" example:"K7QM4XZ" db:"referral_code"`

	ReferredCount int `json:"referredCount" example:"12" db:"referred_count"`
}

// ReferredPage is a page of referred accounts plus the size of the whole
// filtered set, so the caller can page without a second call.
type ReferredPage struct {
	Items []ReferredAccount `json:"items"`
	Total int               `json:"total" example:"42"`
}

// ReferredAccount is one signup that arrived through a referral code.
//
// It carries the new user's phone and email because an operator's whole job
// here is telling one signup from another; the endpoint's permission gate is
// what keeps that from being a general contact-list export.
type ReferredAccount struct {
	AccountID int64   `json:"accountId" example:"87" db:"account_id"`
	Name      string  `json:"name"      example:"Ravi Kumar" db:"name"`
	Phone     *string `json:"phone,omitempty" db:"phone"`
	Email     *string `json:"email,omitempty" db:"email"`
	Status    string  `json:"status"    example:"ACTIVE" db:"status"`

	// ProfileCompleted is the cheapest signal of whether a referral turned
	// into a real user or stopped at the OTP screen.
	ProfileCompleted bool `json:"profileCompleted" db:"profile_completed"`

	ReferredByAccountID int64  `json:"referredByAccountId" example:"12" db:"referred_by_account_id"`
	ReferredByName      string `json:"referredByName"      example:"Asha Menon" db:"referred_by_name"`

	// ReferredByCode is the code as typed at signup, which for an older
	// account may be a REF-prefixed one the referrer no longer holds.
	ReferredByCode string    `json:"referredByCode" example:"K7QM4XZ" db:"referred_by_code"`
	ReferredAt     time.Time `json:"referredAt" db:"referred_at"`
}
