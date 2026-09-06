package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// ScheduledCheckRunner executes the prepaid report runs a myScorr Plus
// purchase minted (scheduled_score_checks), the repo's first interval worker
// — the PDF relay and the statement pool are both queue-driven.
//
// Durability rests on the rows, not on this timer. The sweep predicate is
// "PENDING and due_on <= today", so a run missed while the process was down is
// picked up by the first sweep after boot (Start fires one immediately); a
// missed tick delays work, it never loses it. Day granularity is deliberate:
// a run is owed on a DATE, and the sweep executes everything owed by today
// whatever the clock says.
//
// "Only once" is layered:
//   - one row per period exists at all (UNIQUE order_id+sequence_no), and a
//     DONE row is never eligible again;
//   - ClaimDue's FOR UPDATE SKIP LOCKED partitions concurrent sweeps (two
//     instances, or a tick overlapping a slow predecessor) instead of
//     duplicating them;
//   - a crash between claim and completion is healed by the stale reclaim,
//     and the retry cannot double-bill because the run's idempotency key
//     (sched-<row id>) replays the stored report if the pull had landed.
//
// A failed pull never burns the quota — only Complete spends a row — and a run
// that keeps failing is parked FAILED at the attempt cap, loudly: a paid run
// silently vanishing is the one unacceptable outcome.
type ScheduledCheckRunner struct {
	repo      *repository.ScheduledCheckRepo
	analytics *CreditAnalyticsService
	cfg       config.ScheduledChecksConfig
	loc       *time.Location

	wg   sync.WaitGroup
	once sync.Once
}

func NewScheduledCheckRunner(
	repo *repository.ScheduledCheckRepo,
	analytics *CreditAnalyticsService,
	cfg config.ScheduledChecksConfig,
) *ScheduledCheckRunner {
	return &ScheduledCheckRunner{
		repo: repo, analytics: analytics, cfg: cfg, loc: cfg.Location(),
	}
}

// Start spawns the sweep loop. Idempotent (guarded by once). The first sweep
// runs immediately — a restart must not wait a poll interval to notice a day's
// runs, or a daily-restarted deployment could starve them forever.
func (r *ScheduledCheckRunner) Start(ctx context.Context) {
	r.once.Do(func() {
		r.wg.Add(1)
		go r.run(ctx)
	})
}

// Stop waits for an in-flight sweep to finish. The loop itself ends when the
// ctx given to Start is cancelled (server shutdown), same as the other workers.
func (r *ScheduledCheckRunner) Stop() { r.wg.Wait() }

func (r *ScheduledCheckRunner) run(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	r.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Sweep(ctx)
		}
	}
}

// Sweep is one pass: heal (reclaim stale claims), expire what the plan year
// has voided, then claim and execute everything owed as of today. It loops
// while full batches keep coming so a backlog clears in one tick instead of
// one batch per hour. Exported so tests (and, if ever needed, an ops hook)
// can drive a deterministic single pass without the ticker.
//
// The heartbeat line logs every sweep, zeros included: the failure this design
// cannot self-heal is the silent one (a wedged loop), and "no heartbeat for
// 2× the poll interval" is only an alarm someone can set if the heartbeat
// exists.
func (r *ScheduledCheckRunner) Sweep(ctx context.Context) {
	today := businessToday(r.loc)

	reclaimed, err := r.repo.ReclaimStale(ctx, time.Now().Add(-r.cfg.StaleAfter))
	if err != nil {
		slog.Error("scheduled-checks: stale reclaim failed", "error", err)
	} else if reclaimed > 0 {
		slog.Warn("scheduled-checks: reclaimed runs orphaned by a crash", "count", reclaimed)
	}

	expired, err := r.repo.ExpireOverdue(ctx, today)
	if err != nil {
		slog.Error("scheduled-checks: expiry sweep failed", "error", err)
	} else if expired > 0 {
		slog.Info("scheduled-checks: expired unrun plan checks", "count", expired)
	}

	var claimed, done, failed int
	for {
		rows, err := r.repo.ClaimDue(ctx, today, r.cfg.BatchSize)
		if err != nil {
			slog.Error("scheduled-checks: claim failed", "error", err)
			break
		}
		claimed += len(rows)
		for i := range rows {
			if ctx.Err() != nil {
				// Shutting down mid-batch: the unexecuted claims go stale and the
				// next boot's reclaim returns them to PENDING. Nothing is lost.
				return
			}
			if r.execute(ctx, &rows[i]) {
				done++
			} else {
				failed++
			}
		}
		if len(rows) < r.cfg.BatchSize {
			break
		}
	}

	slog.Info("scheduled-checks: sweep heartbeat",
		"due_as_of", today.Format(dateOnly),
		"claimed", claimed, "done", done, "failed", failed,
		"reclaimed", reclaimed, "expired", expired)
}

// execute runs one claimed row through the full credit-analytics path and
// settles the row from the outcome. Returns true when the run completed.
func (r *ScheduledCheckRunner) execute(ctx context.Context, row *models.ScheduledScoreCheck) bool {
	report, err := r.analytics.RunScheduled(ctx, row)
	if err != nil {
		// The row goes back to PENDING for the next sweep — or FAILED at the
		// attempt cap (row.Attempts already counts this attempt; ClaimDue
		// incremented it). The quota is never spent by a failure.
		if rerr := r.repo.ReleaseFailed(ctx, row.ID, err.Error(), r.cfg.MaxAttempts); rerr != nil {
			slog.Error("scheduled-checks: release after failure failed; row stays RUNNING until the stale reclaim",
				"scheduled_check_id", row.ID, "error", rerr)
		}
		if row.Attempts >= r.cfg.MaxAttempts {
			slog.Error("scheduled-checks: run FAILED at the attempt cap — a paid check was not delivered; needs an operator",
				"scheduled_check_id", row.ID, "account_id", row.AccountID,
				"order_id", row.OrderID, "attempts", row.Attempts, "error", err)
		} else {
			slog.Warn("scheduled-checks: run failed; will retry next sweep",
				"scheduled_check_id", row.ID, "account_id", row.AccountID,
				"attempts", row.Attempts, "error", err)
		}
		return false
	}

	if err := r.repo.Complete(ctx, row.ID, report.ID, models.ScheduledCheckByRunner); err != nil {
		// The pull happened and the report is stored; only our bookkeeping is
		// behind. The stale reclaim will retry this row and the idempotency key
		// will replay the same report into a clean Complete — no second bill.
		slog.Error("scheduled-checks: run delivered but row not completed; stale reclaim will settle it",
			"scheduled_check_id", row.ID, "report_id", report.ID, "error", err)
		return false
	}
	slog.Info("scheduled-checks: run completed",
		"scheduled_check_id", row.ID, "account_id", row.AccountID,
		"report_id", report.ID, "sequence_no", row.SequenceNo)
	return true
}
