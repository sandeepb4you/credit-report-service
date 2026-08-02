package handler

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	_ "credit-report-service/internal/models" // referenced by swag annotations
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// AgentHandler serves the admin agent management and user agent-code endpoints.
type AgentHandler struct {
	agentSvc *service.AgentService
	authSvc  *service.AuthService
}

func NewAgentHandler(agentSvc *service.AgentService, authSvc *service.AuthService) *AgentHandler {
	return &AgentHandler{agentSvc: agentSvc, authSvc: authSvc}
}

// ---- POST /api/admin/agents ----------------------------------------------

type createAgentReq struct {
	Code  string `json:"code"  example:"AGENT001"`
	Name  string `json:"name"  example:"John Doe"`
	Email string `json:"email" example:"john@example.com"`
	Phone string `json:"phone" example:"+919876543210"`
}

// CreateAgent godoc
//
// @Summary      Create an agent (admin only)
// @Description  Creates a new agent with a unique referral code.
// @Tags         admin-agents
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      createAgentReq  true  "Agent details"
// @Success      201      {object}  models.Agent
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Forbidden"
// @Failure      409      {object}  apperr.ErrorBody  "Agent code already exists"
// @Router       /admin/agents [post]
func (h *AgentHandler) CreateAgent(c *fiber.Ctx) error {
	var req createAgentReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.Code = strings.TrimSpace(req.Code)
	req.Name = strings.TrimSpace(req.Name)

	var details map[string]string
	if req.Code == "" {
		details = setDetail(details, "code", "code is required")
	}
	if req.Name == "" {
		details = setDetail(details, "name", "name is required")
	}
	if len(details) > 0 {
		return apperr.NewValidationWith("Validation failed", details)
	}

	var email, phone *string
	if e := strings.TrimSpace(req.Email); e != "" {
		email = &e
	}
	if p := strings.TrimSpace(req.Phone); p != "" {
		phone = &p
	}

	agent, err := h.agentSvc.CreateAgent(c.Context(), req.Code, req.Name, email, phone)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(agent)
}

// ---- PUT /api/admin/agents/:id -------------------------------------------

type updateAgentReq struct {
	Name  string `json:"name"  example:"Jane Doe"`
	Email string `json:"email" example:"jane@example.com"`
	Phone string `json:"phone" example:"+919876543210"`
}

// UpdateAgent godoc
//
// @Summary      Update an agent (admin only)
// @Description  Updates an agent's name, email, and/or phone.
// @Tags         admin-agents
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      int              true  "Agent ID"
// @Param        request  body      updateAgentReq   true  "Agent fields to update"
// @Success      200      {object}  models.Agent
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Forbidden"
// @Failure      404      {object}  apperr.ErrorBody  "Agent not found"
// @Router       /admin/agents/{id} [put]
func (h *AgentHandler) UpdateAgent(c *fiber.Ctx) error {
	id, err := c.ParamsInt("id")
	if err != nil {
		return apperr.NewValidation("invalid agent id")
	}
	var req updateAgentReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"name": "name is required"})
	}

	var email, phone *string
	if e := strings.TrimSpace(req.Email); e != "" {
		email = &e
	}
	if p := strings.TrimSpace(req.Phone); p != "" {
		phone = &p
	}

	agent, err := h.agentSvc.UpdateAgent(c.Context(), int64(id), req.Name, email, phone)
	if err != nil {
		return err
	}
	return c.JSON(agent)
}

// ---- PATCH /api/admin/agents/:id/status ---------------------------------

type setAgentStatusReq struct {
	Status string `json:"status" example:"ACTIVE"`
}

// SetAgentStatus godoc
//
// @Summary      Activate or deactivate an agent (admin only)
// @Description  Sets an agent's status to ACTIVE or INACTIVE.
// @Tags         admin-agents
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      int                 true  "Agent ID"
// @Param        request  body      setAgentStatusReq   true  "Status"
// @Success      200      {object}  models.Agent
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Forbidden"
// @Failure      404      {object}  apperr.ErrorBody  "Agent not found"
// @Router       /admin/agents/{id}/status [patch]
func (h *AgentHandler) SetAgentStatus(c *fiber.Ctx) error {
	id, err := c.ParamsInt("id")
	if err != nil {
		return apperr.NewValidation("invalid agent id")
	}
	var req setAgentStatusReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.Status = strings.TrimSpace(strings.ToUpper(req.Status))
	if req.Status != "ACTIVE" && req.Status != "INACTIVE" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"status": "status must be ACTIVE or INACTIVE"})
	}

	agent, err := h.agentSvc.SetAgentStatus(c.Context(), int64(id), req.Status)
	if err != nil {
		return err
	}
	return c.JSON(agent)
}

// ---- GET /api/admin/agents -----------------------------------------------

// ListActiveAgents godoc
//
// @Summary      List active agents (admin only)
// @Description  Returns all agents with status ACTIVE.
// @Tags         admin-agents
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.Agent
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Forbidden"
// @Router       /admin/agents [get]
func (h *AgentHandler) ListActiveAgents(c *fiber.Ctx) error {
	agents, err := h.agentSvc.ListActiveAgents(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(agents)
}

// ---- GET /api/admin/agents/:id -------------------------------------------

// GetAgent godoc
//
// @Summary      Get agent with signup count (admin only)
// @Description  Returns an agent's details along with the number of signups under that agent for a given date range. Query params: from, to (YYYY-MM-DD, optional).
// @Tags         admin-agents
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "Agent ID"
// @Param        from query     string  false "Start date (YYYY-MM-DD)"
// @Param        to   query     string  false "End date (YYYY-MM-DD)"
// @Success      200  {object}  models.AgentWithSignupCount
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Forbidden"
// @Failure      404  {object}  apperr.ErrorBody  "Agent not found"
// @Router       /admin/agents/{id} [get]
func (h *AgentHandler) GetAgent(c *fiber.Ctx) error {
	id, err := c.ParamsInt("id")
	if err != nil {
		return apperr.NewValidation("invalid agent id")
	}
	var from, to *string
	if f := strings.TrimSpace(c.Query("from")); f != "" {
		from = &f
	}
	if t := strings.TrimSpace(c.Query("to")); t != "" {
		to = &t
	}

	result, err := h.agentSvc.GetAgentWithSignupCount(c.Context(), int64(id), from, to)
	if err != nil {
		return err
	}
	return c.JSON(result)
}

// ---- PUT /api/profile/agent-code -----------------------------------------

type updateAgentCodeReq struct {
	AgentCode string `json:"agentCode" example:"AGENT001"`
}

// UpdateAgentCode godoc
//
// @Summary      Update agent code on profile
// @Description  Sets or updates the agent code for the authenticated account. Non-admin users can only update once. Admins can update anytime.
// @Tags         profile
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      updateAgentCodeReq  true  "Agent code"
// @Success      200      {object}  models.Account
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404      {object}  apperr.ErrorBody  "Account not found"
// @Failure      409      {object}  apperr.ErrorBody  "Agent code can only be updated once"
// @Router       /profile/agent-code [put]
func (h *AgentHandler) UpdateAgentCode(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	role, _ := middleware.AccountRole(c)
	isAdmin := role == "admin"

	var req updateAgentCodeReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.AgentCode = strings.TrimSpace(req.AgentCode)
	if req.AgentCode == "" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"agentCode": "agentCode is required"})
	}

	acc, err := h.authSvc.UpdateAgentCode(c.Context(), accountID, req.AgentCode, isAdmin)
	if err != nil {
		return err
	}
	return c.JSON(acc)
}

// ---- PUT /api/admin/agents/account/:accountId<int>/agent-code ----------------

// UpdateAccountAgentCode godoc
//
// @Summary      Update agent code for any account (admin only)
// @Description  Allows an admin to set or update the agent code for any account by ID. Admins bypass the one-time update limit.
// @Tags         admin-agents
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        accountId  path      int                  true  "Account ID"
// @Param        request    body      updateAgentCodeReq   true  "Agent code"
// @Success      200        {object}  models.Account
// @Failure      400        {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401        {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403        {object}  apperr.ErrorBody  "Forbidden"
// @Failure      404        {object}  apperr.ErrorBody  "Account not found"
// @Router       /admin/agents/account/{accountId}/agent-code [put]
func (h *AgentHandler) UpdateAccountAgentCode(c *fiber.Ctx) error {
	accountID, err := c.ParamsInt("accountId")
	if err != nil {
		return apperr.NewValidation("invalid accountId")
	}
	var req updateAgentCodeReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.AgentCode = strings.TrimSpace(req.AgentCode)
	if req.AgentCode == "" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"agentCode": "agentCode is required"})
	}

	// Admin always passes isAdmin=true to bypass the once-only limit.
	acc, err := h.authSvc.UpdateAgentCode(c.Context(), int64(accountID), req.AgentCode, true)
	if err != nil {
		return err
	}
	return c.JSON(acc)
}

// idFromParams is a shared helper to parse an integer path param.
func idFromParams(c *fiber.Ctx, key string) (int64, error) {
	v, err := strconv.ParseInt(c.Params(key), 10, 64)
	if err != nil {
		return 0, apperr.NewValidation("invalid " + key)
	}
	return v, nil
}
