package models

import "time"

// Referral reward: flat credit per referred account whose first order turns
// PAID. One per referred account, not one per order — enforced by the UNIQUE
// on referral_earnings.referred_account_id.
//
// The live values live in referral_settings (migration 0029) and are editable
// by an admin; the constants below are the seeded defaults, used only as a
// fallback when the row is unreadable rather than failing the request.
const ReferralRewardPaise = 12500

// DefaultReferralMinWithdrawalPaise is the seeded smallest withdrawable
// amount (Rs 500). Same fallback role as above.
const DefaultReferralMinWithdrawalPaise = 50000

// Withdrawal lifecycle: a request locks its amount out of the available
// balance while PENDING; PAID keeps it out permanently; REJECTED frees it.
// See the money-math note on EarningsSummary.
const (
	WithdrawalPending  = "PENDING"
	WithdrawalPaid     = "PAID"
	WithdrawalRejected = "REJECTED"
)

// EarningsSummary is what the app's referral-earnings balance card renders.
//
// available = earned − paid − pending: a pending request locks its amount so
// two requests cannot spend the same rupees before an admin looks at either.
// Deducting only on pay would allow exactly that double-spend.
type EarningsSummary struct {
	// The caller's own code, so the dashboard and the account tab agree.
	ReferralCode string `json:"referralCode" example:"7WF9VNX"`
	// Flat credit per successful referral, in paise (12500 = ₹125).
	RewardPerReferralPaise int `json:"rewardPerReferralPaise" example:"12500"`
	// Smallest withdrawable amount in paise (50000 = Rs 500).
	MinWithdrawalPaise     int `json:"minWithdrawalPaise" example:"50000"`
	TotalEarnedPaise       int `json:"totalEarnedPaise" example:"37500"`
	TotalPaidPaise         int `json:"totalPaidPaise" example:"25000"`
	TotalPendingPaise      int `json:"totalPendingPaise" example:"0"`
	AvailablePaise         int `json:"availablePaise" example:"12500"`
	// Successful = referred account made its first paid purchase (credited).
	// Pending = signed up through the code but hasn't purchased yet.
	SuccessfulCount int `json:"successfulCount" example:"3"`
	PendingCount    int `json:"pendingCount" example:"1"`
}

// MyReferralItem is one signup through the caller's code, as the caller may
// see it. Contact details are masked server-side — the caller gets enough to
// recognise the person, never the full number or address. Full details stay
// behind the admin report's permission gate.
type MyReferralItem struct {
	// Name as the referred account holds it ("Rahul S.").
	Name string `json:"name" example:"Rahul S."`
	// Masked: "+91 98xxx xx041", "r****l@gmail.com". Empty when unknown.
	MaskedPhone string `json:"maskedPhone" example:"+91 98xxx xx041"`
	MaskedEmail string `json:"maskedEmail" example:"r****l@gmail.com"`
	// Paid = first purchase done and ₹125 credited; Pending = signed up only.
	Status string `json:"status" example:"paid"`
	// Dates as YYYY-MM-DD. PurchaseDate is nil until the purchase happens.
	SignupDate   string  `json:"signupDate" example:"2026-09-08"`
	PurchaseDate *string `json:"purchaseDate,omitempty" example:"2026-09-12"`
	// Credited reward in paise when paid, zero while pending.
	RewardPaise int `json:"rewardPaise" example:"12500"`
}

// PayoutBankAccount is the caller's own withdrawal destination, masked.
// The full account number is write-only: it is stored for the manual payout
// and never returned by any endpoint.
type PayoutBankAccount struct {
	HolderName  string `json:"holderName" example:"Aradhyula Tandava"`
	AccountLast4 string `json:"accountLast4" example:"4821"`
	IFSC        string `json:"ifsc" example:"HDFC0001234"`
}

// ReferralSettings is the editable programme economics: the flat credit per
// converted referral and the smallest withdrawable amount, both in paise.
// A change applies going forward — credited rows snapshot the amount, so
// history is never rewritten.
type ReferralSettings struct {
	RewardPaise        int   `json:"rewardPaise" example:"12500" db:"reward_paise"`
	MinWithdrawalPaise int   `json:"minWithdrawalPaise" example:"50000" db:"min_withdrawal_paise"`
}

// Withdrawal is one withdraw request by the caller.
type Withdrawal struct {
	ID            int64      `json:"id" example:"7" db:"id"`
	AmountPaise   int        `json:"amountPaise" example:"12500" db:"amount_paise"`
	Status        string     `json:"status" example:"PENDING" db:"status"`
	HolderName    string     `json:"holderName" db:"holder_name"`
	AccountLast4  string     `json:"accountLast4" db:"account_last4"`
	IFSC          string     `json:"ifsc" db:"ifsc"`
	RequestedAt   time.Time  `json:"requestedAt" db:"requested_at"`
	DecidedAt     *time.Time `json:"decidedAt,omitempty" db:"decided_at"`
	RejectReason  *string    `json:"rejectReason,omitempty" db:"reject_reason"`
}

// AdminWithdrawalItem is one request with the requester's identity attached,
// for the manual-payout queue. Full contact + full bank details: this is why
// it needs its own permission rather than riding on referral:view.
type AdminWithdrawalItem struct {
	Withdrawal
	AccountID      int64   `json:"accountId" example:"12" db:"account_id"`
	UserName       string  `json:"userName" db:"user_name"`
	UserPhone      *string `json:"userPhone,omitempty" db:"user_phone"`
	UserEmail      *string `json:"userEmail,omitempty" db:"user_email"`
	// Full destination the admin pays to. Never exposed on user routes.
	AccountNumber string `json:"accountNumber" db:"account_number"`
	BankIFSC      string `json:"bankIfsc" db:"bank_ifsc"`
	BankHolder    string `json:"bankHolder" db:"bank_holder"`
}
