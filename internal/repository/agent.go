package repository

import (
	"context"
	"errors"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// AgentRepo is the data access layer for the agents table.
type AgentRepo struct{ pool *pgxpool.Pool }

func NewAgentRepo(pool *pgxpool.Pool) *AgentRepo { return &AgentRepo{pool: pool} }

// BeginTx starts a transaction so the service layer doesn't import pgxpool.
func (r *AgentRepo) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

const agentCols = `id, code, name, email, phone, status, created_at, updated_at`

// FindByID returns an agent by its primary key, or ErrNotFound.
func (r *AgentRepo) FindByID(ctx context.Context, id int64) (*models.Agent, error) {
	var a models.Agent
	err := pgxscan.Get(ctx, r.pool, &a,
		`SELECT `+agentCols+` FROM agents WHERE id = $1`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// FindByCode returns an agent by its unique referral code, or ErrNotFound.
func (r *AgentRepo) FindByCode(ctx context.Context, code string) (*models.Agent, error) {
	var a models.Agent
	err := pgxscan.Get(ctx, r.pool, &a,
		`SELECT `+agentCols+` FROM agents WHERE code = $1`, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// ListActive returns all agents with status = 'ACTIVE'.
func (r *AgentRepo) ListActive(ctx context.Context) ([]models.Agent, error) {
	var agents []models.Agent
	err := pgxscan.Select(ctx, r.pool, &agents,
		`SELECT `+agentCols+` FROM agents WHERE status = 'ACTIVE' ORDER BY created_at DESC`)
	return agents, err
}

// Create inserts a new agent within a transaction.
func (r *AgentRepo) Create(ctx context.Context, tx pgx.Tx, a *models.Agent) error {
	row := tx.QueryRow(ctx,
		`INSERT INTO agents (code, name, email, phone, status)
		 VALUES ($1, $2, $3, $4, COALESCE($5, 'ACTIVE'))
		 RETURNING id, created_at, updated_at`,
		a.Code, a.Name, a.Email, a.Phone, nilString(a.Status),
	)
	return row.Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
}

// Update saves the mutable agent columns (name, email, phone) within a transaction.
func (r *AgentRepo) Update(ctx context.Context, tx pgx.Tx, a *models.Agent) error {
	_, err := tx.Exec(ctx,
		`UPDATE agents SET
		     name = $2, email = $3, phone = $4, updated_at = now()
		 WHERE id = $1`,
		a.ID, a.Name, a.Email, a.Phone,
	)
	return classifyPgErr(err)
}

// SetStatus activates or deactivates an agent.
func (r *AgentRepo) SetStatus(ctx context.Context, id int64, status string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE agents SET status = $2, updated_at = now() WHERE id = $1`,
		id, status,
	)
	return err
}

// CountSignups returns the number of accounts linked to an agent whose
// created_at falls within [from, to] (inclusive). Either bound may be nil
// to leave that side open-ended.
func (r *AgentRepo) CountSignups(ctx context.Context, agentID int64, from, to *string) (int64, error) {
	var count int64
	var err error
	switch {
	case from != nil && to != nil:
		err = r.pool.QueryRow(ctx,
			`SELECT count(*) FROM accounts WHERE agent_id = $1 AND created_at >= $2 AND created_at <= $3`,
			agentID, *from, *to,
		).Scan(&count)
	case from != nil:
		err = r.pool.QueryRow(ctx,
			`SELECT count(*) FROM accounts WHERE agent_id = $1 AND created_at >= $2`,
			agentID, *from,
		).Scan(&count)
	case to != nil:
		err = r.pool.QueryRow(ctx,
			`SELECT count(*) FROM accounts WHERE agent_id = $1 AND created_at <= $2`,
			agentID, *to,
		).Scan(&count)
	default:
		err = r.pool.QueryRow(ctx,
			`SELECT count(*) FROM accounts WHERE agent_id = $1`,
			agentID,
		).Scan(&count)
	}
	return count, err
}
