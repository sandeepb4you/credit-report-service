package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// AgentService handles agent CRUD and the business rules around agent codes.
type AgentService struct {
	agents   *repository.AgentRepo
	accounts *repository.AccountRepo
}

func NewAgentService(agents *repository.AgentRepo, accounts *repository.AccountRepo) *AgentService {
	return &AgentService{agents: agents, accounts: accounts}
}

// CreateAgent creates a new agent. The code must be unique (enforced by DB).
func (s *AgentService) CreateAgent(ctx context.Context, code, name string, email, phone *string) (*models.Agent, error) {
	code = strings.TrimSpace(code)
	name = strings.TrimSpace(name)
	if code == "" {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"code": "code is required"})
	}
	if name == "" {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"name": "name is required"})
	}
	// Trim email/phone if provided.
	email = trimPtr(email)
	phone = trimPtr(phone)

	a := &models.Agent{Code: code, Name: name, Email: email, Phone: phone}
	tx, err := s.agents.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.agents.Create(ctx, tx, a); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, apperr.NewConflict("Agent code already exists")
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	slog.Info("agent created", "agent_id", a.ID, "code", a.Code)
	return a, nil
}

// UpdateAgent updates an agent's mutable fields (name, email, phone).
func (s *AgentService) UpdateAgent(ctx context.Context, id int64, name string, email, phone *string) (*models.Agent, error) {
	a, err := s.agents.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Agent not found")
	}
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"name": "name is required"})
	}
	a.Name = name
	a.Email = trimPtr(email)
	a.Phone = trimPtr(phone)

	tx, err := s.agents.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.agents.Update(ctx, tx, a); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	slog.Info("agent updated", "agent_id", a.ID)
	return a, nil
}

// SetAgentStatus activates or deactivates an agent.
func (s *AgentService) SetAgentStatus(ctx context.Context, id int64, status string) (*models.Agent, error) {
	if status != models.AgentActive && status != models.AgentInactive {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"status": "status must be ACTIVE or INACTIVE"})
	}
	a, err := s.agents.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Agent not found")
	}
	if err != nil {
		return nil, err
	}
	if err := s.agents.SetStatus(ctx, id, status); err != nil {
		return nil, err
	}
	a.Status = status
	slog.Info("agent status changed", "agent_id", a.ID, "status", status)
	return a, nil
}

// ListActiveAgents returns all active agents.
func (s *AgentService) ListActiveAgents(ctx context.Context) ([]models.Agent, error) {
	agents, err := s.agents.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	if agents == nil {
		agents = []models.Agent{}
	}
	return agents, nil
}

// GetAgentWithSignupCount returns an agent and the count of accounts signed up
// under that agent within the given date range. Both from and to are optional.
func (s *AgentService) GetAgentWithSignupCount(ctx context.Context, id int64, from, to *string) (*models.AgentWithSignupCount, error) {
	a, err := s.agents.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Agent not found")
	}
	if err != nil {
		return nil, err
	}
	count, err := s.agents.CountSignups(ctx, id, from, to)
	if err != nil {
		return nil, err
	}
	return &models.AgentWithSignupCount{Agent: a, SignupCount: count}, nil
}

// ValidateAgentCode looks up an active agent by code. Returns the agent ID
// if valid and active, or an error if not found / inactive. Used by AuthService
// during signup and agent-code updates.
func (s *AgentService) ValidateAgentCode(ctx context.Context, code string) (int64, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return 0, apperr.NewValidationWith("Validation failed",
			map[string]string{"agentCode": "agentCode is required"})
	}
	a, err := s.agents.FindByCode(ctx, code)
	if errors.Is(err, repository.ErrNotFound) {
		return 0, apperr.NewValidationWith("Validation failed",
			map[string]string{"agentCode": "invalid agent code"})
	}
	if err != nil {
		return 0, err
	}
	if a.Status != models.AgentActive {
		return 0, apperr.NewValidationWith("Validation failed",
			map[string]string{"agentCode": "agent code is not active"})
	}
	return a.ID, nil
}

// trimPtr trims whitespace around a *string value, returning nil if empty.
func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}
