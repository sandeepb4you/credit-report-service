package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// AudienceRepo reads the retargeting lists (Admin -> Retargeting) and records
// their exports.
type AudienceRepo struct {
	pool *pgxpool.Pool
}

func NewAudienceRepo(pool *pgxpool.Pool) *AudienceRepo {
	return &AudienceRepo{pool: pool}
}

// AudienceRowLimit caps one list read. The lists are small today; the cap is
// there so a list that grows does not turn one console request into a full
// customer dump, and the response says when it bit.
const AudienceRowLimit = 5000

// audienceBaseCTE is the single definition of who may appear in any list, and
// the per-account facts several lists share. Every list query starts from it,
// so the eligibility rule cannot differ between lists:
//
//   - customers only (role 'user'): staff accounts are not an audience;
//   - ACTIVE and not deleted;
//   - **no pending deletion request.** Someone counting down to erasure has
//     asked us to forget them; putting them in a campaign file in the
//     meantime would be the worst possible message to send.
//
// last_seen is the latest of a session's token rotation and its push-token
// registration. The second is what makes it usable: the app re-registers its
// FCM token on every cold start (AppRoot's push collector), so on Android it
// moves on each app open, where last_used_at alone moves only about weekly.
// It is still approximate — a resume from background records nothing, and the
// web build has no push token — and the list descriptions say so.
const audienceBaseCTE = `
	WITH people AS (
	  SELECT a.id                            AS account_id,
	         COALESCE(a.first_name, '')      AS first_name,
	         COALESCE(a.last_name, '')       AS last_name,
	         COALESCE(a.primary_phone, '')   AS phone,
	         COALESCE(a.primary_email, '')   AS email,
	         a.created_at                    AS signed_up_at,
	         COALESCE((SELECT s.device_platform FROM sessions s
	                    WHERE s.account_id = a.id AND s.device_platform IS NOT NULL
	                    ORDER BY s.last_used_at DESC LIMIT 1), '') AS platform,
	         EXISTS (SELECT 1 FROM sessions s
	                  WHERE s.account_id = a.id AND s.revoked_at IS NULL
	                    AND s.fcm_token IS NOT NULL)            AS can_push,
	         (SELECT MAX(GREATEST(s.last_used_at,
	                              COALESCE(s.fcm_token_updated_at, s.last_used_at)))
	            FROM sessions s WHERE s.account_id = a.id)      AS last_seen
	    FROM accounts a
	   WHERE a.role = 'user'
	     AND a.status = 'ACTIVE'
	     AND a.deleted_at IS NULL
	     AND NOT EXISTS (SELECT 1 FROM account_deletion_requests d
	                      WHERE d.account_id = a.id AND d.status = 'PENDING')
	),
	latest_report AS (
	  SELECT DISTINCT ON (cr.account_id)
	         cr.account_id, cr.id AS report_id, cr.credit_score, cr.created_at,
	         COALESCE(cr.idempotency_key, '') AS idempotency_key
	    FROM credit_analytics_requests cr
	   WHERE cr.credit_score IS NOT NULL
	     AND ` + succeededPredicate + `
	   ORDER BY cr.account_id, cr.id DESC
	),
	latest_paid AS (
	  SELECT DISTINCT ON (o.account_id)
	         o.account_id, COALESCE(pr.name, o.product_code) AS plan
	    FROM orders o
	    LEFT JOIN products pr ON pr.code = o.product_code
	   WHERE o.status = 'PAID'
	   ORDER BY o.account_id, COALESCE(o.paid_at, o.created_at) DESC
	)`

// audienceSelect is the common head of every list query: the person, then the
// list's own fields as a JSON object of strings.
const audienceSelect = `
	SELECT p.account_id, p.first_name, p.last_name, p.phone, p.email,
	       p.signed_up_at, p.platform, p.can_push, %s::text AS fields
	  FROM people p`

// liveOrder keeps sandbox purchases — the internal APK's test payments — out
// of every list that reasons about orders. A tester's free checkout is not a
// customer abandoning one.
const liveOrder = `o.payment_mode IS DISTINCT FROM 'sandbox'`

// audienceQueries are the lists' own halves: the fields object, and the joins
// and conditions after the common head. Keyed by list id; the catalogue in the
// service is what names and describes them.
var audienceQueries = map[string]struct{ fields, tail string }{
	// Verified a contact and never placed an order at all. Anyone who tried to
	// pay is in B instead — a different conversation.
	"A": {
		fields: `jsonb_build_object(
		    'signup',     to_char(p.signed_up_at, 'YYYY-MM-DD'),
		    'signedWith', CASE WHEN p.phone = '' THEN 'Email' ELSE 'Mobile' END,
		    'daysSince',  (current_date - p.signed_up_at::date)::text)`,
		tail: `
		 WHERE NOT EXISTS (SELECT 1 FROM orders o WHERE o.account_id = p.account_id)
		 ORDER BY p.signed_up_at DESC`,
	},
	// Their newest live order did not reach PAID: it failed, expired, was
	// cancelled, or has sat unpaid for over half an hour. Being the newest is
	// what makes "has not paid since" true by construction.
	"B": {
		fields: `jsonb_build_object(
		    'orderOn', to_char(lo.created_at, 'YYYY-MM-DD'),
		    'plan',    COALESCE(pr.name, lo.product_code),
		    'amount',  ROUND(lo.amount * 100)::bigint::text,
		    'reason',  COALESCE(NULLIF(lo.failure_reason, ''),
		                 CASE lo.status WHEN 'FAILED' THEN 'Payment failed'
		                                WHEN 'TERMINATED' THEN 'Cancelled'
		                                ELSE 'Left checkout' END))`,
		tail: `
		  JOIN LATERAL (SELECT o.* FROM orders o
		                 WHERE o.account_id = p.account_id AND ` + liveOrder + `
		                 ORDER BY o.created_at DESC LIMIT 1) lo ON true
		  LEFT JOIN products pr ON pr.code = lo.product_code
		 WHERE lo.status <> 'PAID'
		   AND (lo.status IN ('FAILED', 'EXPIRED', 'TERMINATED')
		        OR lo.created_at < now() - interval '30 minutes')
		 ORDER BY lo.created_at DESC`,
	},
	// Paid and never submitted a PAN, so no report can be pulled. Money taken,
	// nothing delivered: the most urgent list.
	"C": {
		fields: `jsonb_build_object(
		    'paidOn', to_char(COALESCE(po.paid_at, po.created_at), 'YYYY-MM-DD'),
		    'plan',   COALESCE(pr.name, po.product_code),
		    'hours',  FLOOR(EXTRACT(EPOCH FROM now() - COALESCE(po.paid_at, po.created_at)) / 3600)::bigint::text)`,
		tail: `
		  JOIN LATERAL (SELECT o.* FROM orders o
		                 WHERE o.account_id = p.account_id AND o.status = 'PAID' AND ` + liveOrder + `
		                 ORDER BY COALESCE(o.paid_at, o.created_at) DESC LIMIT 1) po ON true
		  LEFT JOIN products pr ON pr.code = po.product_code
		 WHERE NOT EXISTS (SELECT 1 FROM kyc_records k WHERE k.account_id = p.account_id)
		 ORDER BY COALESCE(po.paid_at, po.created_at)`,
	},
	// Their PAN is waiting on a human, or was rejected and needs resubmitting.
	// The date is the last submission: a re-upload re-queues the record.
	"D": {
		fields: `jsonb_build_object(
		    'submitted', to_char(COALESCE(k.updated_at, k.created_at), 'YYYY-MM-DD'),
		    'panStatus', CASE k.status WHEN 'PENDING' THEN 'In review' ELSE 'Rejected' END,
		    'rejReason', COALESCE(k.rejection_reason, ''))`,
		tail: `
		  JOIN LATERAL (SELECT k.* FROM kyc_records k
		                 WHERE k.account_id = p.account_id
		                 ORDER BY k.id DESC LIMIT 1) k ON true
		 WHERE k.status IN ('PENDING', 'REJECTED')
		 ORDER BY COALESCE(k.updated_at, k.created_at) DESC`,
	},
	// A report the user did not wait for — a scheduled plan refresh, or an owed
	// report run after a manual PAN approval — and no sign of them since. A
	// report they pulled themselves is excluded: they watched it arrive.
	"E": {
		fields: `jsonb_build_object(
		    'readyOn', to_char(r.created_at, 'YYYY-MM-DD'),
		    'plan',    COALESCE(lp.plan, ''),
		    'score',   r.credit_score::text)`,
		tail: `
		  JOIN latest_report r ON r.account_id = p.account_id
		  LEFT JOIN latest_paid lp ON lp.account_id = p.account_id
		 WHERE (r.idempotency_key LIKE 'owed-%'
		        OR EXISTS (SELECT 1 FROM scheduled_score_checks sc
		                    WHERE sc.report_id = r.report_id AND sc.completed_by = 'runner'))
		   AND (p.last_seen IS NULL OR p.last_seen < r.created_at)
		 ORDER BY r.created_at DESC`,
	},
	// No plan running, and the newest score is 30 to 120 days old.
	"F": {
		fields: `jsonb_build_object(
		    'scoreOn', to_char(r.created_at, 'YYYY-MM-DD'),
		    'score',   r.credit_score::text,
		    'age',     (current_date - r.created_at::date)::text)`,
		tail: `
		  JOIN latest_report r ON r.account_id = p.account_id
		 WHERE NOT EXISTS (SELECT 1 FROM scheduled_score_checks sc
		                    WHERE sc.account_id = p.account_id AND sc.status = 'PENDING')
		   AND r.created_at <= now() - interval '30 days'
		   AND r.created_at >  now() - interval '120 days'
		 ORDER BY r.created_at`,
	},
	// A prepaid plan with a run due in the next 7 days or already overdue, or
	// whose year ends within 14 days.
	"G": {
		fields: `jsonb_build_object(
		    'plan',      COALESCE(pr.name, g.product_code),
		    'nextCheck', to_char(g.next_due, 'YYYY-MM-DD'),
		    'missed',    g.missed::text,
		    'planEnds',  to_char(g.plan_ends, 'YYYY-MM-DD'))`,
		tail: `
		  JOIN LATERAL (SELECT MIN(sc.due_on)                                  AS next_due,
		                       MAX(sc.expires_on)                              AS plan_ends,
		                       COUNT(*) FILTER (WHERE sc.due_on < current_date) AS missed,
		                       (ARRAY_AGG(sc.product_code ORDER BY sc.id DESC))[1] AS product_code
		                  FROM scheduled_score_checks sc
		                 WHERE sc.account_id = p.account_id AND sc.status = 'PENDING') g
		    ON g.next_due IS NOT NULL
		  LEFT JOIN products pr ON pr.code = g.product_code
		 WHERE g.next_due <= current_date + 7
		    OR g.plan_ends <= current_date + 14
		 ORDER BY g.next_due`,
	},
	// Has a score and no sign of the app for 30 days or more.
	"H": {
		fields: `jsonb_build_object(
		    'lastSeen', to_char(p.last_seen, 'YYYY-MM-DD'),
		    'inactive', (current_date - p.last_seen::date)::text,
		    'plan',     COALESCE(lp.plan, ''),
		    'score',    r.credit_score::text)`,
		tail: `
		  JOIN latest_report r ON r.account_id = p.account_id
		  LEFT JOIN latest_paid lp ON lp.account_id = p.account_id
		 WHERE p.last_seen IS NOT NULL
		   AND p.last_seen <= now() - interval '30 days'
		 ORDER BY p.last_seen`,
	},
	// Signed up by phone and never linked an email address.
	"J": {
		fields: `jsonb_build_object(
		    'signup',    to_char(p.signed_up_at, 'YYYY-MM-DD'),
		    'paid',      CASE WHEN EXISTS (SELECT 1 FROM orders o
		                                    WHERE o.account_id = p.account_id AND o.status = 'PAID')
		                      THEN 'Yes' ELSE 'No' END,
		    'daysSince', (current_date - p.signed_up_at::date)::text)`,
		tail: `
		 WHERE p.email = '' AND p.phone <> ''
		 ORDER BY p.signed_up_at DESC`,
	},
	// A good score (700+), their own referral code, and fewer than three
	// friends brought in so far.
	"K": {
		fields: `jsonb_build_object(
		    'signup',  to_char(p.signed_up_at, 'YYYY-MM-DD'),
		    'code',    rc.code,
		    'friends', rf.friends::text,
		    'earned',  COALESCE((SELECT SUM(e.amount_paise) FROM referral_earnings e
		                          WHERE e.referrer_account_id = p.account_id), 0)::text,
		    'score',   r.credit_score::text)`,
		tail: `
		  JOIN latest_report r ON r.account_id = p.account_id AND r.credit_score >= 700
		  JOIN LATERAL (SELECT c.code FROM coupons c
		                 WHERE c.kind = 'referral' AND c.created_by = p.account_id
		                   AND c.revoked_at IS NULL
		                 ORDER BY c.id DESC LIMIT 1) rc ON true
		  JOIN LATERAL (SELECT COUNT(*) AS friends FROM accounts ref
		                 WHERE ref.referred_by_account_id = p.account_id) rf ON true
		 WHERE rf.friends < 3
		 ORDER BY r.credit_score DESC`,
	},
}

// HasAudienceQuery reports whether a list id is answered by a plain query here
// (list I is computed in the service, because its rule is Go code).
func HasAudienceQuery(id string) bool {
	_, ok := audienceQueries[id]
	return ok
}

// AudienceRows reads one list. It returns at most limit+1 rows, so the caller
// can tell a full list from a truncated one.
func (r *AudienceRepo) AudienceRows(ctx context.Context, id string, limit int) ([]models.AudienceRow, error) {
	q, ok := audienceQueries[id]
	if !ok {
		return nil, ErrNotFound
	}
	sql := audienceBaseCTE + fmt.Sprintf(audienceSelect, q.fields) + q.tail + fmt.Sprintf(" LIMIT %d", limit+1)
	return r.scan(ctx, sql)
}

// OfferCandidate is an account with a scored report, and the report itself —
// list I runs the business-loan rule over it.
type OfferCandidate struct {
	Row          models.AudienceRow
	Score        int64
	ReportAt     time.Time
	ResponseBody json.RawMessage
}

// OfferCandidates returns every eligible account with a scored report, newest
// report first.
func (r *AudienceRepo) OfferCandidates(ctx context.Context) ([]OfferCandidate, error) {
	rows, err := r.pool.Query(ctx, audienceBaseCTE+`
		SELECT p.account_id, p.first_name, p.last_name, p.phone, p.email,
		       p.signed_up_at, p.platform, p.can_push,
		       lr.credit_score, lr.created_at, cr.response_body
		  FROM people p
		  JOIN latest_report lr ON lr.account_id = p.account_id
		  JOIN credit_analytics_requests cr ON cr.id = lr.report_id
		 ORDER BY lr.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OfferCandidate
	for rows.Next() {
		var c OfferCandidate
		if err := rows.Scan(&c.Row.AccountID, &c.Row.FirstName, &c.Row.LastName, &c.Row.Phone,
			&c.Row.Email, &c.Row.SignedUpAt, &c.Row.Platform, &c.Row.CanPush,
			&c.Score, &c.ReportAt, &c.ResponseBody); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *AudienceRepo) scan(ctx context.Context, sql string) ([]models.AudienceRow, error) {
	rows, err := r.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.AudienceRow{}
	for rows.Next() {
		var row models.AudienceRow
		var fields string
		if err := rows.Scan(&row.AccountID, &row.FirstName, &row.LastName, &row.Phone, &row.Email,
			&row.SignedUpAt, &row.Platform, &row.CanPush, &fields); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(fields), &row.Fields); err != nil {
			return nil, fmt.Errorf("audience row %d fields: %w", row.AccountID, err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// RecordAudienceExport writes the audit row for one generated file.
func (r *AudienceRepo) RecordAudienceExport(ctx context.Context, accountID int64, listID, channel string, selected, rowCount int) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO admin_audience_exports (account_id, list_id, channel, selected_count, row_count)
		 VALUES ($1, $2, $3, $4, $5)`, accountID, listID, channel, selected, rowCount)
	return err
}
