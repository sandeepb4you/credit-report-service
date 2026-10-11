package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// DashboardRepo stores console dashboards and answers the metric queries their
// widgets draw.
type DashboardRepo struct{ pool *pgxpool.Pool }

func NewDashboardRepo(pool *pgxpool.Pool) *DashboardRepo { return &DashboardRepo{pool: pool} }

// metricSQL is the query behind each catalog key: one row per UTC day that had
// anything, (day, value), over the half-open window [$1, $2). Gap-filling is
// the service's job, so a day with nothing is simply absent here.
//
// The set is closed on purpose. A widget names a key and nothing else — there
// is no path from a stored definition (or an imported file) to SQL text.
//
// Money and orders count live payments only: a sandbox purchase is a test, and
// a revenue line that included the internal APK's test buys would be wrong in
// exactly the direction nobody checks.
var metricSQL = map[string]string{
	"signups": `
		SELECT (created_at AT TIME ZONE 'UTC')::date, count(*)
		  FROM accounts
		 WHERE created_at >= $1 AND created_at < $2
		 GROUP BY 1`,
	"reports_pulled": `
		SELECT (created_at AT TIME ZONE 'UTC')::date, count(*)
		  FROM credit_analytics_requests
		 WHERE result_code = 101 AND created_at >= $1 AND created_at < $2
		 GROUP BY 1`,
	"pan_verified": `
		SELECT (verified_at AT TIME ZONE 'UTC')::date, count(*)
		  FROM kyc_records
		 WHERE status = 'VERIFIED' AND verified_at >= $1 AND verified_at < $2
		 GROUP BY 1`,
	"paid_orders": `
		SELECT (paid_at AT TIME ZONE 'UTC')::date, count(*)
		  FROM orders
		 WHERE status = 'PAID' AND payment_mode = 'production'
		   AND paid_at >= $1 AND paid_at < $2
		 GROUP BY 1`,
	"revenue": `
		SELECT (paid_at AT TIME ZONE 'UTC')::date, COALESCE(SUM(ROUND(amount * 100)), 0)::bigint
		  FROM orders
		 WHERE status = 'PAID' AND payment_mode = 'production'
		   AND paid_at >= $1 AND paid_at < $2
		 GROUP BY 1`,
	"referral_conversions": `
		SELECT (credited_at AT TIME ZONE 'UTC')::date, count(*)
		  FROM referral_earnings
		 WHERE credited_at >= $1 AND credited_at < $2
		 GROUP BY 1`,
}

// HasMetricQuery reports whether the repository can answer a catalog key. The
// service checks its catalog against this at construction, so a key added to
// one list and not the other fails at boot rather than on a tile.
func HasMetricQuery(key string) bool {
	_, ok := metricSQL[key]
	return ok
}

// MetricByDay returns the metric's value per UTC day ("YYYY-MM-DD") inside
// [from, to). Days with nothing are absent.
func (r *DashboardRepo) MetricByDay(ctx context.Context, key string, from, to time.Time) (map[string]int64, error) {
	q, ok := metricSQL[key]
	if !ok {
		return nil, fmt.Errorf("no query for metric %q", key)
	}
	rows, err := r.pool.Query(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var day time.Time
		var v int64
		if err := rows.Scan(&day, &v); err != nil {
			return nil, err
		}
		out[day.Format("2006-01-02")] = v
	}
	return out, rows.Err()
}

// ListDashboards returns the owner's dashboards, most recently changed first.
func (r *DashboardRepo) ListDashboards(ctx context.Context, ownerID int64) ([]models.AdminDashboard, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, name, widgets, created_at, updated_at
		   FROM admin_dashboards
		  WHERE owner_account_id = $1
		  ORDER BY updated_at DESC, id DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.AdminDashboard{}
	for rows.Next() {
		d, err := scanDashboard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// CountDashboards is how many dashboards the owner holds, for the per-account cap.
func (r *DashboardRepo) CountDashboards(ctx context.Context, ownerID int64) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM admin_dashboards WHERE owner_account_id = $1`, ownerID).Scan(&n)
	return n, err
}

// GetDashboard returns one of the owner's dashboards. Someone else's and a
// missing one are both ErrNotFound — another operator's dashboard is not
// something to confirm the existence of.
func (r *DashboardRepo) GetDashboard(ctx context.Context, id, ownerID int64) (*models.AdminDashboard, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, name, widgets, created_at, updated_at
		   FROM admin_dashboards
		  WHERE id = $1 AND owner_account_id = $2`, id, ownerID)
	d, err := scanDashboard(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// CreateDashboard inserts a dashboard for the owner.
func (r *DashboardRepo) CreateDashboard(ctx context.Context, ownerID int64, name string, widgets []models.DashboardWidget) (*models.AdminDashboard, error) {
	raw, err := json.Marshal(widgets)
	if err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx,
		`INSERT INTO admin_dashboards (owner_account_id, name, widgets)
		 VALUES ($1, $2, $3::jsonb)
		 RETURNING id, name, widgets, created_at, updated_at`, ownerID, name, string(raw))
	return scanDashboard(row)
}

// UpdateDashboard replaces a dashboard's name and widgets whole.
func (r *DashboardRepo) UpdateDashboard(ctx context.Context, id, ownerID int64, name string, widgets []models.DashboardWidget) (*models.AdminDashboard, error) {
	raw, err := json.Marshal(widgets)
	if err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx,
		`UPDATE admin_dashboards
		    SET name = $3, widgets = $4::jsonb, updated_at = now()
		  WHERE id = $1 AND owner_account_id = $2
		 RETURNING id, name, widgets, created_at, updated_at`, id, ownerID, name, string(raw))
	d, err := scanDashboard(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// DeleteDashboard removes one of the owner's dashboards.
func (r *DashboardRepo) DeleteDashboard(ctx context.Context, id, ownerID int64) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM admin_dashboards WHERE id = $1 AND owner_account_id = $2`, id, ownerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordExport writes the audit row for a CSV download.
func (r *DashboardRepo) RecordExport(ctx context.Context, accountID int64, metric string, from, to time.Time, rowCount int) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO admin_data_exports (account_id, metric, range_from, range_to, row_count)
		 VALUES ($1, $2, $3, $4, $5)`, accountID, metric, from, to, rowCount)
	return err
}

func scanDashboard(row pgx.Row) (*models.AdminDashboard, error) {
	var d models.AdminDashboard
	var raw []byte
	if err := row.Scan(&d.ID, &d.Name, &raw, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &d.Widgets); err != nil {
		return nil, fmt.Errorf("dashboard %d widgets: %w", d.ID, err)
	}
	if d.Widgets == nil {
		d.Widgets = []models.DashboardWidget{}
	}
	return &d, nil
}
