package integration

import (
	"sort"
	"strings"
	"testing"
)

// resetClears is every table the reset deletes rows from, mirroring the
// statement list in repository.ResetToSignup. Keep the two in step: this list is
// what the test below checks the schema against.
var resetClears = map[string]bool{
	"credit_analytics_requests": true,
	"documents":                 true,
	"coupon_redemptions":        true,
	"scheduled_score_checks":    true,
	"payment_webhook_events":    true,
	"orders":                    true,
	"kyc_records":               true,
	"prefill_lookups":           true,
	"bank_statements":           true,
	"otp_challenges":            true,
	"password_reset_tokens":     true,
	"referral_earnings":         true,
	"referral_withdrawals":      true,
	"payout_bank_accounts":      true,
}

// TestAccountResetCoversEveryBlockingForeignKey is the test for the *class* of
// bug, not the instance.
//
// A reset deletes a dozen tables in one transaction. Any table that references
// one of them with NO ACTION or RESTRICT will block that delete and fail the
// whole reset, and the failure only appears for accounts that happen to have
// such a row — which is how `scheduled_score_checks` broke reset for every
// customer who had bought a plan, months after the migration that added it,
// with every test still green.
//
// So rather than waiting for the next one, this asks the live schema: for every
// table the reset empties, is each blocking reference to it also emptied? A new
// migration that adds one fails here, at the name of the table, with the list to
// add it to.
func TestAccountResetCoversEveryBlockingForeignKey(t *testing.T) {
	h := newHarness(t)

	rows, err := h.pool.Query(h.baseCtx, `
		SELECT src.relname, tgt.relname, c.conname
		  FROM pg_constraint c
		  JOIN pg_class src ON src.oid = c.conrelid
		  JOIN pg_class tgt ON tgt.oid = c.confrelid
		 WHERE c.contype = 'f'
		   AND c.confdeltype IN ('a', 'r')   -- NO ACTION, RESTRICT: the blocking ones
		   AND c.connamespace = current_schema()::regnamespace
		   AND src.relname <> tgt.relname`)
	if err != nil {
		t.Fatalf("read foreign keys: %v", err)
	}
	defer rows.Close()

	var problems []string
	for rows.Next() {
		var from, to, name string
		if err := rows.Scan(&from, &to, &name); err != nil {
			t.Fatalf("scan foreign key: %v", err)
		}
		if !resetClears[to] {
			continue // the target survives a reset, so nothing can block it
		}
		if !resetClears[from] {
			problems = append(problems, from+" -> "+to+" ("+name+")")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign keys: %v", err)
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf(
			"these tables hold a blocking reference to something the account reset deletes, "+
				"and the reset does not clear them — it will fail with a foreign key "+
				"violation for any account that has one:\n  %s\n\n"+
				"Add a DELETE to repository.ResetToSignup (before the table it points at) "+
				"and to resetClears in this file.",
			strings.Join(problems, "\n  "),
		)
	}
}
