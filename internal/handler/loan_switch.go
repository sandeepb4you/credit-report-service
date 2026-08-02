package handler

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	_ "credit-report-service/internal/models" // referenced by swag annotations (models.LoanProvider)
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// LoanSwitchHandler serves the admin loan-provider CRUD, the switch settings,
// and the user-facing switch-opportunities endpoint.
type LoanSwitchHandler struct {
	svc *service.LoanSwitchService
}

func NewLoanSwitchHandler(svc *service.LoanSwitchService) *LoanSwitchHandler {
	return &LoanSwitchHandler{svc: svc}
}

// providerReq is the create/update body for a loan provider.
type providerReq struct {
	Name                 string  `json:"name"                 example:"HDFC Bank"`
	LoanType             string  `json:"loanType"             example:"HOME"`
	InterestRatePercent  float64 `json:"interestRatePercent"  example:"7.2"`
	ProcessingFeePercent float64 `json:"processingFeePercent" example:"0.5"`
	ProcessingFeeFlat    float64 `json:"processingFeeFlat"    example:"3000"`
	MinCreditScore       int     `json:"minCreditScore"       example:"750"`
	MaxTenureMonths      *int    `json:"maxTenureMonths"      example:"360"`
	Active               *bool   `json:"active"               example:"true"`
}

func (r providerReq) toInput() service.ProviderInput {
	return service.ProviderInput{
		Name:                 r.Name,
		LoanType:             strings.ToUpper(strings.TrimSpace(r.LoanType)),
		InterestRatePercent:  r.InterestRatePercent,
		ProcessingFeePercent: r.ProcessingFeePercent,
		ProcessingFeeFlat:    r.ProcessingFeeFlat,
		MinCreditScore:       r.MinCreditScore,
		MaxTenureMonths:      r.MaxTenureMonths,
		Active:               r.Active,
	}
}

// CreateProvider godoc
//
// @Summary      Create a loan provider offering (admin)
// @Description  Adds what a lender is offering for a loan type (HOME, PERSONAL, CAR): interest rate, processing fees, minimum credit score, and optional tenure cap. Used by the switch optimizer. Requires the 'loan-provider:manage' permission.
// @Tags         loan-providers
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      providerReq  true  "Provider offering"
// @Success      201      {object}  models.LoanProvider
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Missing the 'loan-provider:manage' permission"
// @Router       /admin/loan-providers [post]
func (h *LoanSwitchHandler) CreateProvider(c *fiber.Ctx) error {
	var req providerReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	p, err := h.svc.CreateProvider(c.Context(), req.toInput())
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(p)
}

// ListProviders godoc
//
// @Summary      List loan providers (admin)
// @Description  Returns configured providers, optionally filtered by loanType (HOME|PERSONAL|CAR) and active. Requires 'loan-provider:manage'.
// @Tags         loan-providers
// @Produce      json
// @Security     BearerAuth
// @Param        loanType  query     string  false  "Filter by loan type (HOME, PERSONAL, CAR)"
// @Param        active    query     bool    false  "Filter by active flag"
// @Success      200       {array}   models.LoanProvider
// @Failure      400       {object}  apperr.ErrorBody  "Invalid loanType"
// @Failure      401       {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403       {object}  apperr.ErrorBody  "Missing the 'loan-provider:manage' permission"
// @Router       /admin/loan-providers [get]
func (h *LoanSwitchHandler) ListProviders(c *fiber.Ctx) error {
	var loanType *string
	if v := strings.TrimSpace(c.Query("loanType")); v != "" {
		loanType = &v
	}
	var active *bool
	if v := strings.TrimSpace(c.Query("active")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return apperr.NewValidationWith("Validation failed",
				map[string]string{"active": "must be true or false"})
		}
		active = &b
	}
	out, err := h.svc.ListProviders(c.Context(), loanType, active)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// GetProvider godoc
//
// @Summary      Fetch a loan provider by id (admin)
// @Tags         loan-providers
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Provider id"
// @Success      200  {object}  models.LoanProvider
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'loan-provider:manage' permission"
// @Failure      404  {object}  apperr.ErrorBody  "Loan provider not found"
// @Router       /admin/loan-providers/{id} [get]
func (h *LoanSwitchHandler) GetProvider(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	p, err := h.svc.GetProvider(c.Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(p)
}

// UpdateProvider godoc
//
// @Summary      Update a loan provider (admin)
// @Description  Replaces the mutable fields of a provider. Requires 'loan-provider:manage'.
// @Tags         loan-providers
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      int          true  "Provider id"
// @Param        request  body      providerReq  true  "Provider offering"
// @Success      200      {object}  models.LoanProvider
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Missing the 'loan-provider:manage' permission"
// @Failure      404      {object}  apperr.ErrorBody  "Loan provider not found"
// @Router       /admin/loan-providers/{id} [put]
func (h *LoanSwitchHandler) UpdateProvider(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	var req providerReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	p, err := h.svc.UpdateProvider(c.Context(), id, req.toInput())
	if err != nil {
		return err
	}
	return c.JSON(p)
}

// DeleteProvider godoc
//
// @Summary      Delete a loan provider (admin)
// @Tags         loan-providers
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Provider id"
// @Success      200  {object}  map[string]string  "{\"message\": \"Loan provider deleted\"}"
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'loan-provider:manage' permission"
// @Failure      404  {object}  apperr.ErrorBody  "Loan provider not found"
// @Router       /admin/loan-providers/{id} [delete]
func (h *LoanSwitchHandler) DeleteProvider(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	if err := h.svc.DeleteProvider(c.Context(), id); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"message": "Loan provider deleted"})
}

// ---- Switch settings ------------------------------------------------------

// GetSettings godoc
//
// @Summary      Get loan-switch settings (admin)
// @Description  Returns the configurable recovery window (how many months the switching cost must be recovered within to recommend a switch) and the default foreclosure fees per loan type. Requires 'loan-provider:manage'.
// @Tags         loan-providers
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  models.LoanSwitchSettings
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'loan-provider:manage' permission"
// @Router       /admin/loan-switch/settings [get]
func (h *LoanSwitchHandler) GetSettings(c *fiber.Ctx) error {
	cfg, err := h.svc.GetSettings(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(cfg)
}

// settingsReq is the update body for the switch settings.
type settingsReq struct {
	RecoveryWindowMonths          int     `json:"recoveryWindowMonths"          example:"12"`
	ForeclosureFeePercentHome     float64 `json:"foreclosureFeePercentHome"     example:"0"`
	ForeclosureFeePercentPersonal float64 `json:"foreclosureFeePercentPersonal" example:"4"`
	ForeclosureFeePercentCar      float64 `json:"foreclosureFeePercentCar"      example:"4"`
}

// UpdateSettings godoc
//
// @Summary      Update loan-switch settings (admin)
// @Description  Sets the recovery window (months) and default foreclosure fees per loan type. Requires 'loan-provider:manage'.
// @Tags         loan-providers
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      settingsReq  true  "Switch settings"
// @Success      200      {object}  models.LoanSwitchSettings
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Missing the 'loan-provider:manage' permission"
// @Router       /admin/loan-switch/settings [put]
func (h *LoanSwitchHandler) UpdateSettings(c *fiber.Ctx) error {
	var req settingsReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	cfg, err := h.svc.UpdateSettings(c.Context(), service.SettingsInput{
		RecoveryWindowMonths:   req.RecoveryWindowMonths,
		ForeclosureFeeHome:     req.ForeclosureFeePercentHome,
		ForeclosureFeePersonal: req.ForeclosureFeePercentPersonal,
		ForeclosureFeeCar:      req.ForeclosureFeePercentCar,
	})
	if err != nil {
		return err
	}
	return c.JSON(cfg)
}

// ---- User: switch opportunities -------------------------------------------

// GetOpportunities godoc
//
// @Summary      Loan-switch savings opportunities from a credit report
// @Description  Matches each active home/personal/car loan on a credit report to the cheapest configured provider for the caller's score band, and estimates the monthly and total savings of a balance transfer. By default it uses the caller's most recent report; pass reportId to evaluate a specific one (must be your own). A switch is 'recommended' only when its cost (current loan's foreclosure fee + new provider's processing fee) is recovered within the configured recovery window. Loans the report is too sparse to evaluate are returned with status 'insufficient_data'. Rates are indicative benchmarks, not offers.
// @Tags         loan-switch
// @Produce      json
// @Security     BearerAuth
// @Param        reportId  query     int  false  "Evaluate this report instead of the latest (must be your own)"
// @Success      200  {object}  service.SwitchOpportunities
// @Failure      400  {object}  apperr.ErrorBody  "reportId must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "No credit report found / report not found"
// @Router       /loan-switch/opportunities [get]
func (h *LoanSwitchHandler) GetOpportunities(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var reportID *int64
	if v := strings.TrimSpace(c.Query("reportId")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return apperr.NewValidation("reportId must be an integer")
		}
		reportID = &id
	}
	out, err := h.svc.GetOpportunities(c.Context(), accountID, reportID)
	if err != nil {
		return err
	}
	return c.JSON(out)
}
