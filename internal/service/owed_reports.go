package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// OwedReportService delivers the credit report a customer has paid for but not
// received — and lists who those customers are.
//
// The case it exists for: a customer pays, their PAN fails the automated check
// and goes to manual review, and an admin approves it. The pull is gated on a
// VERIFIED PAN, so it could not run when they paid; and approval used to change
// only the KYC record, so the report then waited for the customer to come back
// and tap "Continue score check". Some never did, and had paid for nothing.
//
// Approval now runs the owed report straight away (RunAfterApproval, wired as
// KycService's onVerified hook), and the admin console lists anyone still owed
// one with a "Run now" (List, Run).
//
// "Owed" is decided by the pull's own funding rules, never restated here: an
// unspent one-time order, or a plan refresh that is due. Run goes through
// CreditAnalyticsService.Request, so the paywall, the PAN and profile gates,
// reuse, persistence and the PDF relay are exactly the customer's own path.
type OwedReportService struct {
	analytics *CreditAnalyticsService
	orders    *repository.OrderRepo
	scheduled *repository.ScheduledCheckRepo
	kyc       *KycService
	loc       *time.Location
}

func NewOwedReportService(
	analytics *CreditAnalyticsService,
	orders *repository.OrderRepo,
	scheduled *repository.ScheduledCheckRepo,
	kyc *KycService,
	loc *time.Location,
) *OwedReportService {
	return &OwedReportService{analytics: analytics, orders: orders, scheduled: scheduled, kyc: kyc, loc: loc}
}

// approvalPullTimeout bounds the background pull after an approval. The admin's
// request has already returned by then, so this only stops a hung vendor call
// from holding a goroutine forever; the owed list still shows the customer if
// it gives up.
const approvalPullTimeout = 2 * time.Minute

// List returns everyone currently owed a report, newest payment first.
func (s *OwedReportService) List(ctx context.Context) ([]models.OwedReportRow, error) {
	return s.orders.ListOwedReports(ctx, businessToday(s.loc))
}

// RunInput is an admin's "Run now". FirstName/LastName are only for an
// account that has no name on file — read off the uploaded PAN card — and are
// ignored for one that has.
type RunInput struct {
	FirstName string
	LastName  string
}

// Run pulls the report an account is owed, now. It answers 409 when nothing is
// owed, and passes the pull's own refusal through otherwise (a PAN still in
// review, a profile without a name), so the admin sees exactly why.
func (s *OwedReportService) Run(ctx context.Context, accountID int64, in RunInput) (*models.OwedRunResult, error) {
	if in.FirstName != "" || in.LastName != "" {
		if err := s.kyc.FillNameIfMissing(ctx, accountID, in.FirstName, in.LastName); err != nil {
			return nil, err
		}
	}

	key, source, err := s.owedFunding(ctx, accountID)
	if err != nil {
		return nil, err
	}

	report, reused, err := s.analytics.Request(ctx, accountID, CreditAnalyticsInput{
		// No device made this request; the loopback address is the honest value
		// for a server-initiated pull, as RunScheduled uses.
		DeviceIP:       "127.0.0.1",
		IdempotencyKey: key,
	})
	if err != nil {
		return nil, err
	}
	slog.Info("owed report delivered",
		"account_id", accountID, "source", source, "report_id", report.ID, "reused", reused)
	return &models.OwedRunResult{
		AccountID:   accountID,
		Source:      source,
		ReportID:    report.ID,
		CreditScore: report.CreditScore,
		Reused:      reused,
	}, nil
}

// owedFunding names what pays for the owed pull and the idempotency key that
// ties the pull to it.
//
// The key is the purchase, not the attempt: an approval and an admin's "Run
// now" moments later — or two clicks — are the same delivery, and the second
// replays the first's report instead of paying the bureau twice. A failed pull
// leaves no stored row, so the key is free again for the retry.
func (s *OwedReportService) owedFunding(ctx context.Context, accountID int64) (key, source string, err error) {
	uid, err := s.orders.OldestUnspentOrderUID(ctx, accountID, models.ProductCreditAnalysis)
	switch {
	case err == nil:
		return "owed-order-" + uid, models.OwedFromOrder, nil
	case !errors.Is(err, repository.ErrNotFound):
		return "", "", err
	}

	next, err := s.scheduled.NextPending(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return "", "", apperr.NewConflict("Nothing is owed: no unused purchase and no plan refresh due")
	}
	if err != nil {
		return "", "", err
	}
	// Only a refresh that is due. Spending a future one early re-anchors the
	// customer's whole schedule, which is their decision to make in the app,
	// not an operator's.
	if next.DueOn.After(businessToday(s.loc)) {
		return "", "", apperr.NewConflict(fmt.Sprintf(
			"Nothing is owed yet: the next plan refresh is due on %s", next.DueOn.Format(dateOnly)))
	}
	return fmt.Sprintf("owed-sched-%d", next.ID), models.OwedFromPlan, nil
}

// RunAfterApproval runs the owed report in the background after an admin
// approves a PAN. In the background because the approval must not wait on, or
// fail with, a bureau call: the decision was made and is recorded either way.
// An account owed nothing is the common case and is not an error.
func (s *OwedReportService) RunAfterApproval(accountID int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), approvalPullTimeout)
		defer cancel()
		res, err := s.Run(ctx, accountID, RunInput{})
		var nothingOwed *apperr.Conflict
		switch {
		case err == nil:
			slog.Info("owed report run after pan approval",
				"account_id", accountID, "report_id", res.ReportID, "source", res.Source)
		case errors.As(err, &nothingOwed):
			// Nothing owed — they had not paid, or the report already ran.
		default:
			// Loud: a customer paid and the report did not arrive. They are still
			// on the console's owed list, where an admin can run it again.
			slog.Error("owed report after pan approval failed; still owed, see the admin owed-reports list",
				"account_id", accountID, "error", err)
		}
	}()
}
