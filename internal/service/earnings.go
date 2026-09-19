package service

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// EarningsService is the referral-money counterpart to ReferralService: where
// that one reads who-referred-whom for the admin report, this one credits
// ₹125 per converted referral, serves the user's own dashboard, and runs the
// manual-payout queue (user requests PENDING, admin pays off-app and marks
// PAID, amount leaves the pool on request as locked and on pay for good).
type EarningsService struct {
	earnings *repository.EarningsRepo
	accounts *repository.AccountRepo
	orders   *repository.OrderRepo
	coupons  *CouponService
}

func NewEarningsService(
	earnings *repository.EarningsRepo,
	accounts *repository.AccountRepo,
	orders *repository.OrderRepo,
	coupons *CouponService,
) *EarningsService {
	return &EarningsService{earnings: earnings, accounts: accounts, orders: orders, coupons: coupons}
}

// CreditForFirstPurchase credits the referrer when a referred account's first
// order turns PAID. Called from OrderService fulfilment (webhook + reconcile
// paths), after the first-transition guard — but idempotent anyway: the
// UNIQUE on referral_earnings.referred_account_id collapses concurrent first
// purchases into one credit, and a non-first purchase is a no-op by the paid
// count. Returns true when this call created the credit.
func (s *EarningsService) CreditForFirstPurchase(
	ctx context.Context, buyerAccountID int64, orderUID string,
) (bool, error) {
	referrer, err := s.accounts.ReferredBy(ctx, buyerAccountID)
	if err != nil {
		return false, err
	}
	if referrer == nil {
		return false, nil
	}
	n, err := s.orders.CountPaidOrders(ctx, buyerAccountID)
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, nil
	}
	// The current reward, not a constant: an admin may have repriced the
	// programme since the referral signed up, and the credit pays what the
	// programme promises at conversion time.
	settings, err := s.earnings.GetSettings(ctx)
	if err != nil {
		return false, err
	}
	return s.earnings.CreditForReferral(
		ctx, *referrer, buyerAccountID, orderUID, settings.RewardPaise)
}

// Summary serves the balance card: earned / withdrawn / available plus the
// successful-vs-pending counts. The code is minted on read (asking is what
// gives the account one), so the dashboard and the account tab always agree.
func (s *EarningsService) Summary(ctx context.Context, accountID int64) (*models.EarningsSummary, error) {
	code, err := s.coupons.ReferralCode(ctx, accountID)
	if err != nil {
		return nil, err
	}
	t, err := s.earnings.Totals(ctx, accountID)
	if err != nil {
		return nil, err
	}
	settings, err := s.earnings.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	available := t.EarnedPaise - t.PaidPaise - t.PendingPaise
	if available < 0 {
		available = 0
	}
	return &models.EarningsSummary{
		ReferralCode:           code.Code,
		RewardPerReferralPaise: settings.RewardPaise,
		MinWithdrawalPaise:     settings.MinWithdrawalPaise,
		TotalEarnedPaise:       t.EarnedPaise,
		TotalPaidPaise:         t.PaidPaise,
		TotalPendingPaise:      t.PendingPaise,
		AvailablePaise:         available,
		SuccessfulCount:        t.SuccessfulCount,
		PendingCount:           t.PendingCount,
	}, nil
}

// MyReferrals lists every signup through the caller's code with masked
// contacts. Paid = credited (first purchase done); anything else is pending —
// including the narrow window where the purchase exists but the credit row is
// still being written, which reads as paid with the standard reward.
func (s *EarningsService) MyReferrals(ctx context.Context, accountID int64) ([]models.MyReferralItem, error) {
	rows, err := s.earnings.ListReferredBy(ctx, accountID)
	if err != nil {
		return nil, err
	}
	settings, err := s.earnings.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.MyReferralItem, 0, len(rows))
	for _, r := range rows {
		item := models.MyReferralItem{
			Name:        r.Name,
			MaskedPhone: maskPhone(ptrVal(r.Phone)),
			MaskedEmail: maskEmail(ptrVal(r.Email)),
			SignupDate:  r.ReferredAt,
		}
		if r.CreditedPaise != nil {
			item.Status = "paid"
			item.PurchaseDate = r.FirstPaidAt
			item.RewardPaise = *r.CreditedPaise
		} else if r.FirstPaidAt != nil {
			item.Status = "paid"
			item.PurchaseDate = r.FirstPaidAt
			item.RewardPaise = settings.RewardPaise
		} else {
			item.Status = "pending"
		}
		out = append(out, item)
	}
	return out, nil
}

// MyBank returns the caller's payout destination, masked.
func (s *EarningsService) MyBank(ctx context.Context, accountID int64) (*models.PayoutBankAccount, error) {
	row, err := s.earnings.GetBank(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return &models.PayoutBankAccount{
		HolderName:   row.HolderName,
		AccountLast4: last4(row.AccountNumber),
		IFSC:         row.IFSC,
	}, nil
}

// SaveBankInput is the "Change bank account" sheet: the number twice (so a
// typo cannot send withdrawals somewhere unrecoverable) plus holder and IFSC.
type SaveBankInput struct {
	HolderName      string
	AccountNumber   string
	ConfirmNumber   string
	IFSC            string
}

// SaveBank validates and stores the caller's payout destination.
func (s *EarningsService) SaveBank(
	ctx context.Context, accountID int64, in SaveBankInput,
) (*models.PayoutBankAccount, error) {
	holder := strings.TrimSpace(in.HolderName)
	number := strings.TrimSpace(in.AccountNumber)
	confirm := strings.TrimSpace(in.ConfirmNumber)
	ifsc := strings.ToUpper(strings.TrimSpace(in.IFSC))

	var details map[string]string
	if holder == "" {
		details = setDetail(details, "holderName", "account holder name is required")
	}
	if !validAccountNumber(number) {
		details = setDetail(details, "accountNumber", "enter a valid account number (9–18 digits)")
	} else if number != confirm {
		details = setDetail(details, "confirmNumber", "account numbers don't match")
	}
	if !validIFSC(ifsc) {
		details = setDetail(details, "ifsc", "enter a valid IFSC (e.g. HDFC0001234)")
	}
	if len(details) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", details)
	}
	if err := s.earnings.UpsertBank(ctx, accountID, holder, number, ifsc); err != nil {
		return nil, err
	}
	return &models.PayoutBankAccount{
		HolderName:   holder,
		AccountLast4: last4(number),
		IFSC:         ifsc,
	}, nil
}

// RequestWithdrawal creates a PENDING request, locking its amount out of the
// available balance. Requires a saved bank account and an amount within
// min..available — all re-read here (not trusted from the client), so a
// stale screen can neither overdraw nor slip under the minimum. The bank is
// checked first: "save a destination" is more actionable than any amount
// complaint when there is nowhere to send the money.
func (s *EarningsService) RequestWithdrawal(
	ctx context.Context, accountID int64, amountPaise int,
) (*models.Withdrawal, error) {
	bank, err := s.earnings.GetBank(ctx, accountID)
	if err != nil {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"bank": "save a bank account first"})
	}
	settings, err := s.earnings.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	if amountPaise < settings.MinWithdrawalPaise {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"amount": "minimum withdrawal is " + formatRupees(settings.MinWithdrawalPaise)})
	}
	t, err := s.earnings.Totals(ctx, accountID)
	if err != nil {
		return nil, err
	}
	available := t.EarnedPaise - t.PaidPaise - t.PendingPaise
	if amountPaise > available {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"amount": "amount exceeds the available balance"})
	}
	return s.earnings.CreateWithdrawal(ctx, accountID, amountPaise,
		bank.HolderName, last4(bank.AccountNumber), bank.IFSC)
}

// MyWithdrawals lists the caller's own requests, newest first.
func (s *EarningsService) MyWithdrawals(
	ctx context.Context, accountID int64,
) ([]models.Withdrawal, error) {
	return s.earnings.ListWithdrawalsMine(ctx, accountID)
}

// GetSettings returns the editable programme economics.
func (s *EarningsService) GetSettings(ctx context.Context) (*models.ReferralSettings, error) {
	return s.earnings.GetSettings(ctx)
}

// UpdateSettingsInput reprices the programme going forward. Either pointer
// may be nil to leave that value alone; both nil is a no-op error rather
// than a silent success.
type UpdateSettingsInput struct {
	RewardPaise        *int
	MinWithdrawalPaise *int
}

// UpdateSettings validates and applies new economics. History keeps its
// snapshots — credited rows and decided withdrawals never change — so this
// only affects future credits and requests.
func (s *EarningsService) UpdateSettings(
	ctx context.Context, adminID int64, in UpdateSettingsInput,
) (*models.ReferralSettings, error) {
	var details map[string]string
	if in.RewardPaise == nil && in.MinWithdrawalPaise == nil {
		return nil, apperr.NewValidation("nothing to update")
	}
	if in.RewardPaise != nil && *in.RewardPaise <= 0 {
		details = setDetail(details, "rewardPaise", "reward must be greater than zero")
	}
	if in.MinWithdrawalPaise != nil && *in.MinWithdrawalPaise <= 0 {
		details = setDetail(details, "minWithdrawalPaise", "minimum must be greater than zero")
	}
	if len(details) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", details)
	}
	return s.earnings.UpdateSettings(ctx, adminID, in.RewardPaise, in.MinWithdrawalPaise)
}

// formatRupees renders paise as whole rupees for error strings ("Rs 500").
// Callers only pass validated whole-rupee values here.
func formatRupees(paise int) string {
	return "Rs " + indianGroup(strconv.Itoa(paise/100))
}

// indianGroup groups digits Indian-style: 1250000 -> 12,50,000.
func indianGroup(src string) string {
	neg := strings.HasPrefix(src, "-")
	src = strings.TrimPrefix(src, "-")
	if len(src) <= 3 {
		if neg {
			return "-" + src
		}
		return src
	}
	tail := src[len(src)-3:]
	head := src[:len(src)-3]
	parts := []string{}
	for len(head) > 2 {
		parts = append([]string{head[len(head)-2:]}, parts...)
		head = head[:len(head)-2]
	}
	if head != "" {
		parts = append([]string{head}, parts...)
	}
	out := strings.Join(parts, ",") + "," + tail
	if neg {
		return "-" + out
	}
	return out
}

// ReviewQueue pages the manual-payout queue for admins, oldest first.
func (s *EarningsService) ReviewQueue(
	ctx context.Context, status *string, limit, offset int,
) ([]models.AdminWithdrawalItem, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	if status != nil {
		up := strings.ToUpper(strings.TrimSpace(*status))
		switch up {
		case models.WithdrawalPending, models.WithdrawalPaid, models.WithdrawalRejected:
			status = &up
		default:
			return nil, 0, apperr.NewValidationWith("Validation failed",
				map[string]string{"status": "expected PENDING, PAID or REJECTED"})
		}
	}
	return s.earnings.ListWithdrawalsForReview(ctx, status, limit, offset)
}

// MarkPaid records that the admin paid the request off-app. The money was
// already locked at request time, so paying just moves it pending → paid.
func (s *EarningsService) MarkPaid(ctx context.Context, adminID, id int64) error {
	ok, err := s.earnings.DecideWithdrawal(ctx, id, adminID, true, nil)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NewNotFound("No pending withdrawal with that id")
	}
	return nil
}

// Reject frees a request's locked amount back into the available balance.
func (s *EarningsService) Reject(
	ctx context.Context, adminID, id int64, reason string,
) error {
	if strings.TrimSpace(reason) == "" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"reason": "a reason is required so the user knows what to fix"})
	}
	ok, err := s.earnings.DecideWithdrawal(ctx, id, adminID, false, &reason)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NewNotFound("No pending withdrawal with that id")
	}
	return nil
}

// ---- validation + masking --------------------------------------------------

// validAccountNumber accepts the 9–18 digit range Indian bank accounts span.
// Spaces are stripped so a pasted "1234 5678" still validates; anything else
// non-digit fails.
func validAccountNumber(v string) bool {
	digits := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		if unicode.IsSpace(r) {
			return -1
		}
		return 'x'
	}, v)
	if len(digits) < 9 || len(digits) > 18 {
		return false
	}
	for _, r := range digits {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// ifscRe is the standard shape: 4-letter bank code, zero, 6-char branch.
var ifscRe = regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`)

func validIFSC(v string) bool { return ifscRe.MatchString(v) }

func last4(number string) string {
	digits := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		return -1
	}, number)
	if len(digits) < 4 {
		return digits
	}
	return digits[len(digits)-4:]
}

// maskPhone renders "+91 98xxx xx041": prefix + first 2 + last 3 visible.
// Anything unparseable is returned blank rather than guessed at — a wrong
// mask is worse than none, and the admin report carries the real value.
func maskPhone(raw string) string {
	digits := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		return -1
	}, strings.TrimSpace(raw))
	var local, prefix string
	switch {
	case len(digits) == 12 && strings.HasPrefix(digits, "91"):
		prefix, local = "+91", digits[2:]
	case len(digits) == 10:
		prefix, local = "+91", digits
	default:
		return ""
	}
	return prefix + " " + local[:2] + "xxx xx" + local[7:]
}

// maskEmail renders "r****l@gmail.com": first + last initial of the local
// part visible, domain whole (the domain is what tells the user it is the
// wrong address). Short local parts collapse to "r****@…".
func maskEmail(raw string) string {
	v := strings.TrimSpace(raw)
	at := strings.LastIndex(v, "@")
	if at <= 0 || at == len(v)-1 {
		return ""
	}
	local, domain := v[:at], v[at+1:]
	if len(local) <= 2 {
		return string(local[0]) + "****@" + domain
	}
	return string(local[0]) + "****" + string(local[len(local)-1]) + "@" + domain
}

func ptrVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// setDetail accumulates field errors. Coupon handler has its own unexported
// copy; this one serves the earnings handlers.
func setDetail(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[k] = v
	return m
}
