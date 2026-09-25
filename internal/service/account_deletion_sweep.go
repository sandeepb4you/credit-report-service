package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"credit-report-service/internal/repository"
)

// AccountDeletionSweeper carries out deletion requests whose grace period has
// run out.
//
// Durability rests on the rows, not on this timer: the predicate is
// "status = PENDING AND scheduled_for <= now", so a request confirmed while the
// process was down is picked up by the first sweep after boot (Start fires one
// immediately), and a process that dies mid-sweep leaves the remaining rows
// PENDING for the next pass. Nothing is lost by a restart; at worst a purge
// happens a poll interval late, which against a fourteen-day window is noise.
//
// WHY HOURLY rather than the minute-level cadence a queue would use: the
// deadline is a date, not a moment. A user told "14 September" does not care
// whether it happens at 00:04 or 01:00, and an hourly loop makes the heartbeat
// log readable rather than a wall of zeros.
//
// The sweep is single-writer by construction — one deployment, one loop — so
// unlike the scheduled-check runner there is no claim-and-lock dance. The
// status transition is the idempotency: CompleteDeletionRequest only moves a
// row OUT of PENDING, so a second pass over the same row finds nothing.
type AccountDeletionSweeper struct {
	repo     *repository.AccountRepo
	objects  reportPDFRemover
	interval time.Duration
	batch    int

	wg   sync.WaitGroup
	once sync.Once
}

// deletionSweepBatch bounds one pass. Deletions arrive at human pace, so this
// is never the binding constraint in practice — it exists so that a backlog
// (a migration, a long outage) cannot turn one tick into an unbounded
// transaction storm.
const deletionSweepBatch = 50

// NewAccountDeletionSweeper builds the worker. objects may be the S3 stub, in
// which case the rows still go and the orphaned files are logged — see Sweep.
func NewAccountDeletionSweeper(
	repo *repository.AccountRepo, objects reportPDFRemover, interval time.Duration,
) *AccountDeletionSweeper {
	if interval <= 0 {
		interval = time.Hour
	}
	return &AccountDeletionSweeper{
		repo: repo, objects: objects, interval: interval, batch: deletionSweepBatch,
	}
}

// Start spawns the sweep loop. Idempotent (guarded by once). The first sweep
// runs immediately: a deployment that restarts daily must not wait a poll
// interval to notice the requests that came due while it was down.
func (s *AccountDeletionSweeper) Start(ctx context.Context) {
	s.once.Do(func() {
		s.wg.Add(1)
		go s.run(ctx)
	})
}

// Stop waits for an in-flight sweep to finish. The loop itself ends when the
// ctx given to Start is cancelled, same as the other workers.
func (s *AccountDeletionSweeper) Stop() { s.wg.Wait() }

func (s *AccountDeletionSweeper) run(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sweep(ctx)
		}
	}
}

// Sweep is one pass: purge everything due. Exported so tests can drive a
// deterministic pass without the ticker.
//
// The heartbeat logs every sweep, zeros included. The failure this design
// cannot self-heal is the silent one — a wedged loop leaves accounts that
// asked to be deleted sitting undeleted, which is a DPDP breach that looks
// exactly like nothing happening. "No heartbeat for 2× the interval" is only
// an alarm somebody can set if the heartbeat exists.
func (s *AccountDeletionSweeper) Sweep(ctx context.Context) {
	due, err := s.repo.ListDueDeletions(ctx, time.Now().UTC(), s.batch)
	if err != nil {
		slog.Error("account-deletion: sweep failed to list due requests", "error", err)
		return
	}

	var purged, failed int
	for _, req := range due {
		// Re-check the context between rows: a shutdown mid-backlog should
		// stop cleanly rather than push another multi-table transaction into
		// a pool that is about to close.
		if ctx.Err() != nil {
			break
		}
		if s.purgeOne(ctx, req.ID, req.AccountID) {
			purged++
		} else {
			failed++
		}
	}

	slog.Info("account-deletion: sweep heartbeat",
		"due", len(due), "purged", purged, "failed", failed)
}

// purgeOne destroys one account and marks its request complete. Returns
// whether it succeeded; a failure leaves the row PENDING so the next sweep
// retries it, which is the right default — the alternative is a request that
// silently stops being honoured.
func (s *AccountDeletionSweeper) purgeOne(ctx context.Context, requestID, accountID int64) bool {
	res, err := s.repo.PurgeAccount(ctx, accountID)
	if err != nil {
		slog.Error("account-deletion: purge failed",
			"account_id", accountID, "request_id", requestID, "error", err)
		return false
	}

	// The row is marked complete before the objects are dealt with, and that
	// ordering is deliberate: the database is the record of what was erased,
	// the bucket cleanup is best-effort, and a failure to reach S3 must not
	// make the sweep retry a purge that has already committed.
	if err := s.repo.CompleteDeletionRequest(ctx, requestID); err != nil {
		slog.Error("account-deletion: purge committed but request not marked complete",
			"account_id", accountID, "request_id", requestID, "error", err)
	}

	// The advanced report's PDF is stored under a key derived from account and
	// report id, and no row records it — so it has to be named here or it
	// outlives the purge. It is also the one report PDF with no password on it.
	// Deleting a key that was never rendered is a no-op in S3, so every
	// report's key is included rather than guessing which ones were asked for.
	objects := res.ObjectURIs
	for _, reportID := range res.DeletedReportIDs {
		objects = append(objects, advancedReportKey(accountID, reportID))
	}
	s.deleteObjects(ctx, accountID, objects)

	// WARN, like the reset's audit line, and for a stronger reason: this is
	// the only action in the service that destroys a person's whole record,
	// and it happens with nobody watching. No contact details — the account id
	// is all anyone needs to follow it up, and the tombstone is what it points
	// at now.
	slog.Warn("account purged",
		"account_id", accountID,
		"request_id", requestID,
		"reports", res.Reports,
		"documents", res.Documents,
		"identities", res.Identities,
		"sessions", res.Sessions,
		"bank_statements", res.BankStatements,
		"payout_bank_details", res.PayoutBankDetails,
		"had_kyc_record", res.HadKYCRecord,
		"retained_orders", res.RetainedOrders,
		"webhook_events_scrubbed", res.WebhookEventsScrubbed,
		"invoices_scrubbed", res.InvoicesScrubbed,
		"withdrawals_redacted", res.WithdrawalsRedacted,
		"pending_withdrawals_cancelled", res.PendingWithdrawalsCancelled,
		"referral_codes_revoked", res.ReferralCodesRevoked,
		"objects", len(objects))
	return true
}

// deleteObjects removes the encrypted report PDFs and uploaded PAN cards whose
// rows have just gone.
//
// Best-effort, but LOUD when it fails — louder than the reset's equivalent,
// which logs and moves on. After a purge there is no row left pointing at
// these objects, so nothing will ever go looking for them again: an unlogged
// failure here is a bank full of somebody's credit report and PAN card that
// the service has forgotten it is holding, which is precisely the data this
// whole flow exists to destroy.
//
// Two ways this goes wrong quietly, both already known in this codebase:
// an empty s3.bucket selects the stub, and a stored s3:// URI naming a
// different bucket than the configured one is refused outright by
// s3store.keyFrom. Either leaves the objects behind, so both are reported by
// URI rather than just counted.
func (s *AccountDeletionSweeper) deleteObjects(ctx context.Context, accountID int64, uris []string) {
	if len(uris) == 0 {
		return
	}
	if s.objects == nil || s.objects.IsStub() {
		slog.Error("account-deletion: no object store configured; "+
			"purged account's stored files were NOT deleted",
			"account_id", accountID, "objects", uris)
		return
	}
	for _, uri := range uris {
		if err := s.objects.Delete(ctx, uri); err != nil {
			slog.Error("account-deletion: stored file left behind after purge",
				"account_id", accountID, "uri", uri, "error", err)
		}
	}
}
