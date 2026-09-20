package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"

	"credit-report-service/internal/models"
)

// AccountListFilter is everything the admin user list can be narrowed by. The
// nil-able fields are absent filters rather than zero ones: a minimum score of
// 0 and no minimum at all are different questions.
type AccountListFilter struct {
	From         time.Time
	To           time.Time
	Status       *string
	Search       *string // matches name, phone or email
	Paid         *bool
	MinScore     *int
	MaxScore     *int
	MinReferred  *int
	MinConverted *int
	// OrderBy is a SQL fragment chosen from accountSortColumns by the service.
	// It is never user text — see AccountSortColumn.
	OrderBy string
	Limit   int
	Offset  int
}

// accountRowsCTE computes one row per account inside the signup window. The
// derived figures are correlated subqueries rather than joins: each is an
// existence test or a count keyed by account_id, and a join would fan the row
// out and need a GROUP BY over every selected column to put it back.
//
// Two of them carry rules from elsewhere in the service and must not drift:
//   - the score is the newest SUCCESSFUL pull that carried one (see
//     succeededPredicate). A failed request is not a report, and a degraded
//     200 with no score must not blank out the number the user last saw.
//   - "paid" and "referred who paid" both mean an order that reached PAID,
//     which is the same transition that credits a referral reward.
//
// The cheap filters (window, status, text) live in here so the subqueries only
// run for rows that survive them. The filters on the derived columns cannot —
// an alias is not visible to the WHERE that computes it — so they are applied
// outside, against the CTE. That costs a full pass over the window when an
// operator filters by score or referral count, which is the right trade at
// this table's size and the thing to revisit if it ever isn't.
const accountRowsCTE = `
	WITH rows AS (
	  SELECT a.id                                                AS account_id,
	         TRIM(BOTH ' ' FROM COALESCE(a.first_name, '') || ' ' ||
	                            COALESCE(a.last_name, ''))       AS name,
	         a.primary_phone                                     AS phone,
	         a.primary_email                                     AS email,
	         a.status                                            AS status,
	         a.created_at                                        AS signed_up_at,
	         (SELECT cr.credit_score
	            FROM credit_analytics_requests cr
	           WHERE cr.account_id = a.id
	             AND cr.credit_score IS NOT NULL
	             AND ` + succeededPredicate + `
	           ORDER BY cr.id DESC
	           LIMIT 1)                                          AS credit_score,
	         EXISTS (SELECT 1 FROM orders o
	                  WHERE o.account_id = a.id AND o.status = $5)  AS paid,
	         (SELECT COUNT(*) FROM accounts ref
	           WHERE ref.referred_by_account_id = a.id)          AS referred_count,
	         (SELECT COUNT(*) FROM accounts ref
	           WHERE ref.referred_by_account_id = a.id
	             AND EXISTS (SELECT 1 FROM orders o2
	                          WHERE o2.account_id = ref.id
	                            AND o2.status = $5))             AS referred_paid_count
	    FROM accounts a
	   WHERE a.created_at >= $1 AND a.created_at < $2
	     AND ($3::text IS NULL
	          OR ($3 = 'INACTIVE' AND a.status <> 'ACTIVE')
	          OR a.status = $3)
	     AND ($4::text IS NULL
	          OR a.primary_phone ILIKE '%' || $4 || '%'
	          OR a.primary_email ILIKE '%' || $4 || '%'
	          OR TRIM(BOTH ' ' FROM COALESCE(a.first_name, '') || ' ' ||
	                                COALESCE(a.last_name, '')) ILIKE '%' || $4 || '%')
	)`

// accountDerivedPredicates filters on the CTE's computed columns. Kept beside
// the CTE so the page query and the count query cannot drift apart — a total
// counted under different filters from the rows is the one bug a paged table
// cannot show you.
const accountDerivedPredicates = `
	 WHERE ($6::boolean IS NULL OR paid = $6)
	   AND ($7::int IS NULL OR credit_score >= $7)
	   AND ($8::int IS NULL OR credit_score <= $8)
	   AND ($9::int IS NULL OR referred_count >= $9)
	   AND ($10::int IS NULL OR referred_paid_count >= $10)`

// ListAccounts pages the admin user list.
func (r *AccountRepo) ListAccounts(
	ctx context.Context, f AccountListFilter,
) ([]models.AdminAccountRow, int, error) {
	// pgx binds exactly the parameters a statement uses, so the count cannot
	// simply reuse the page's argument list: $11/$12 appear only in the LIMIT.
	// The shared prefix is what keeps the two queries filtering identically.
	filterArgs := []any{
		f.From, f.To, f.Status, f.Search, models.OrderPaid,
		f.Paid, f.MinScore, f.MaxScore, f.MinReferred, f.MinConverted,
	}
	pageArgs := append(append([]any{}, filterArgs...), f.Limit, f.Offset)

	var total int
	if err := pgxscan.Get(ctx, r.pool, &total,
		accountRowsCTE+` SELECT COUNT(*) FROM rows`+accountDerivedPredicates,
		filterArgs...); err != nil {
		return nil, 0, err
	}

	// OrderBy is a whitelisted fragment, never user text; the account_id tie-break
	// is appended here so every sort is deterministic and paging cannot repeat or
	// skip a row when two accounts share a value.
	orderBy := f.OrderBy
	if orderBy == "" {
		orderBy = models.AccountSortDefault.SQL(true)
	}

	var out []models.AdminAccountRow
	err := pgxscan.Select(ctx, r.pool, &out,
		accountRowsCTE+` SELECT * FROM rows`+accountDerivedPredicates+
			fmt.Sprintf(` ORDER BY %s, account_id DESC LIMIT $11 OFFSET $12`, orderBy),
		pageArgs...)
	if out == nil {
		out = []models.AdminAccountRow{}
	}
	return out, total, err
}

// ListRecentChecks returns an account's most recent successful score checks,
// newest first. Same predicate as the user's own report list, so the admin and
// the customer are counting the same things when they talk to each other.
func (r *AccountRepo) ListRecentChecks(
	ctx context.Context, accountID int64, limit int,
) ([]models.AdminAccountCheck, error) {
	var out []models.AdminAccountCheck
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT id, created_at, credit_score
		   FROM credit_analytics_requests
		  WHERE account_id = $1 AND `+succeededPredicate+`
		  ORDER BY id DESC
		  LIMIT $2`,
		accountID, limit)
	if out == nil {
		out = []models.AdminAccountCheck{}
	}
	return out, err
}
