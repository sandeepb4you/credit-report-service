package service

import (
	"context"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// Window and page bounds for the admin referral report.
const (
	// ReferralDefaultWindowDays is the window used when the caller names no
	// dates. Thirty days is long enough that a slow week does not read as a
	// dead referral programme.
	ReferralDefaultWindowDays = 30

	// referralMaxWindowDays caps the range. The queries scan accounts by
	// referred_at, so an unbounded range is a table scan an operator can
	// trigger by typing a year into a date box.
	referralMaxWindowDays = 366

	referralMaxReferrers = 100
	referralDefaultPage  = 50
	referralMaxPage      = 200
	referralDateLayout   = "2006-01-02"
)

// ReferralService assembles the admin referral report.
type ReferralService struct {
	referrals *repository.ReferralRepo
}

func NewReferralService(referrals *repository.ReferralRepo) *ReferralService {
	return &ReferralService{referrals: referrals}
}

// ReferralQuery is a parsed, already-validated request for the report.
type ReferralQuery struct {
	// From and To are inclusive whole UTC days. Zero values mean "the default
	// window ending today".
	From time.Time
	To   time.Time

	// ReferrerID narrows the Referred page to one referrer. It deliberately
	// does not narrow TotalReferred or the leaderboard.
	ReferrerID *int64

	Limit  int
	Offset int
}

// Report runs the three reads behind the admin referral screen: the period
// total, the leaderboard, and one page of the individual signups.
func (s *ReferralService) Report(ctx context.Context, q ReferralQuery) (*models.ReferralReport, error) {
	from, to, err := resolveReferralWindow(q.From, q.To)
	if err != nil {
		return nil, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = referralDefaultPage
	}
	if limit > referralMaxPage {
		limit = referralMaxPage
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	// The window is half-open internally: [from, to+1d). Callers speak in
	// inclusive days, so a report "to 30 Aug" must include everything that
	// happened during the 30th.
	end := to.AddDate(0, 0, 1)

	total, err := s.referrals.CountReferred(ctx, from, end)
	if err != nil {
		return nil, err
	}
	referrers, err := s.referrals.TopReferrers(ctx, from, end, referralMaxReferrers)
	if err != nil {
		return nil, err
	}
	items, filtered, err := s.referrals.ListReferred(ctx, from, end, q.ReferrerID, limit, offset)
	if err != nil {
		return nil, err
	}

	return &models.ReferralReport{
		From:          from.Format(referralDateLayout),
		To:            to.Format(referralDateLayout),
		TotalReferred: total,
		Referrers:     referrers,
		Referred:      models.ReferredPage{Items: items, Total: filtered},
	}, nil
}

// resolveReferralWindow fills in whichever end of the range the caller left
// out and rejects the ones that cannot mean anything.
//
// Either bound alone is enough: "from 1 Aug" reads as 1 Aug until today, and
// "to 1 Aug" as the default window ending on the 1st. That is what makes the
// date inputs on the admin screen independently useful.
func resolveReferralWindow(from, to time.Time) (time.Time, time.Time, error) {
	today := time.Now().UTC().Truncate(24 * time.Hour)

	switch {
	case from.IsZero() && to.IsZero():
		to = today
		from = to.AddDate(0, 0, -(ReferralDefaultWindowDays - 1))
	case from.IsZero():
		from = to.AddDate(0, 0, -(ReferralDefaultWindowDays - 1))
	case to.IsZero():
		to = today
	}

	if to.Before(from) {
		return time.Time{}, time.Time{}, apperr.NewValidationWith("Validation failed",
			map[string]string{"to": "the end date cannot be before the start date"})
	}
	if to.Sub(from) > referralMaxWindowDays*24*time.Hour {
		return time.Time{}, time.Time{}, apperr.NewValidationWith("Validation failed",
			map[string]string{"from": "the range cannot be longer than a year"})
	}
	return from, to, nil
}
