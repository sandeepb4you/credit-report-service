package handler

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	_ "credit-report-service/internal/models" // referenced by swag annotations (models.BankOffering)
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// ScoreBuilderHandler serves the admin bank-offering CRUD and the user-facing
// what-if simulator endpoint.
type ScoreBuilderHandler struct {
	svc *service.ScoreBuilderService
}

func NewScoreBuilderHandler(svc *service.ScoreBuilderService) *ScoreBuilderHandler {
	return &ScoreBuilderHandler{svc: svc}
}

// offeringReq is the create/update body for a bank offering.
type offeringReq struct {
	Name                string  `json:"name"                example:"HDFC Millennia FD-Backed Card"`
	ProductType         string  `json:"productType"         example:"FD_CARD"`
	MinFDAmount         float64 `json:"minFdAmount"         example:"15000"`
	InterestRatePercent float64 `json:"interestRatePercent" example:"7"`
	MinCreditScore      int     `json:"minCreditScore"      example:"0"`
	MaxCreditScore      int     `json:"maxCreditScore"      example:"650"`
	EstimatedPointsMin  int     `json:"estimatedPointsMin"  example:"40"`
	EstimatedPointsMax  int     `json:"estimatedPointsMax"  example:"80"`
	ApplyURL            string  `json:"applyUrl"            example:"https://apply.example.com/hdfc-fd-card"`
	RevenueNote         string  `json:"revenueNote"         example:"FD + secured-card referral"`
	Active              *bool   `json:"active"              example:"true"`
}

func (r offeringReq) toInput() service.OfferingInput {
	return service.OfferingInput{
		Name:                r.Name,
		ProductType:         strings.ToUpper(strings.TrimSpace(r.ProductType)),
		MinFDAmount:         r.MinFDAmount,
		InterestRatePercent: r.InterestRatePercent,
		MinCreditScore:      r.MinCreditScore,
		MaxCreditScore:      r.MaxCreditScore,
		EstimatedPointsMin:  r.EstimatedPointsMin,
		EstimatedPointsMax:  r.EstimatedPointsMax,
		ApplyURL:            r.ApplyURL,
		RevenueNote:         r.RevenueNote,
		Active:              r.Active,
	}
}

// CreateOffering godoc
//
// @Summary      Create a score-builder bank offering (admin)
// @Description  Adds a partner product that helps a user rebuild credit (e.g. an FD-secured credit card): the FD amount, the FD yield, the score band it targets, the estimated point impact, the apply link, and a revenue note. Surfaced by the score-builder toolkit (S28) when the user's score falls in the band. Requires the 'bank-offering:manage' permission.
// @Tags         bank-offerings
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      offeringReq  true  "Bank offering"
// @Success      201      {object}  models.BankOffering
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Missing the 'bank-offering:manage' permission"
// @Router       /admin/bank-offerings [post]
func (h *ScoreBuilderHandler) CreateOffering(c *fiber.Ctx) error {
	var req offeringReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	o, err := h.svc.CreateOffering(c.Context(), req.toInput())
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(o)
}

// ListOfferings godoc
//
// @Summary      List bank offerings (admin)
// @Description  Returns configured offerings, optionally filtered by productType (FD_CARD|SECURED_LOAN) and active. Requires 'bank-offering:manage'.
// @Tags         bank-offerings
// @Produce      json
// @Security     BearerAuth
// @Param        productType  query     string  false  "Filter by product type (FD_CARD, SECURED_LOAN)"
// @Param        active       query     bool    false  "Filter by active flag"
// @Success      200          {array}   models.BankOffering
// @Failure      400          {object}  apperr.ErrorBody  "Invalid productType"
// @Failure      401          {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403          {object}  apperr.ErrorBody  "Missing the 'bank-offering:manage' permission"
// @Router       /admin/bank-offerings [get]
func (h *ScoreBuilderHandler) ListOfferings(c *fiber.Ctx) error {
	var productType *string
	if v := strings.TrimSpace(c.Query("productType")); v != "" {
		productType = &v
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
	out, err := h.svc.ListOfferings(c.Context(), productType, active)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// GetOffering godoc
//
// @Summary      Fetch a bank offering by id (admin)
// @Tags         bank-offerings
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Offering id"
// @Success      200  {object}  models.BankOffering
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'bank-offering:manage' permission"
// @Failure      404  {object}  apperr.ErrorBody  "Bank offering not found"
// @Router       /admin/bank-offerings/{id} [get]
func (h *ScoreBuilderHandler) GetOffering(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	o, err := h.svc.GetOffering(c.Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(o)
}

// UpdateOffering godoc
//
// @Summary      Update a bank offering (admin)
// @Description  Replaces the mutable fields of an offering. Requires 'bank-offering:manage'.
// @Tags         bank-offerings
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      int          true  "Offering id"
// @Param        request  body      offeringReq  true  "Bank offering"
// @Success      200      {object}  models.BankOffering
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403      {object}  apperr.ErrorBody  "Missing the 'bank-offering:manage' permission"
// @Failure      404      {object}  apperr.ErrorBody  "Bank offering not found"
// @Router       /admin/bank-offerings/{id} [put]
func (h *ScoreBuilderHandler) UpdateOffering(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	var req offeringReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	o, err := h.svc.UpdateOffering(c.Context(), id, req.toInput())
	if err != nil {
		return err
	}
	return c.JSON(o)
}

// DeleteOffering godoc
//
// @Summary      Delete a bank offering (admin)
// @Tags         bank-offerings
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Offering id"
// @Success      200  {object}  map[string]string  "{\"message\": \"Bank offering deleted\"}"
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'bank-offering:manage' permission"
// @Failure      404  {object}  apperr.ErrorBody  "Bank offering not found"
// @Router       /admin/bank-offerings/{id} [delete]
func (h *ScoreBuilderHandler) DeleteOffering(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	if err := h.svc.DeleteOffering(c.Context(), id); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"message": "Bank offering deleted"})
}

// ---- User: what-if simulator -----------------------------------------------

// Simulate godoc
//
// @Summary      Score what-if simulator
// @Description  Projects the caller's credit score under a chosen set of actions. Builds a toggle-action set from the report's actual signals (positive levers the file has room to improve on, plus two universal negative actions), and sums the selected deltas onto the current score. By default every positive action is selected; pass `actions` (a comma-separated list of action keys) to override the selection. By default it uses the caller's most recent report; pass reportId to simulate against a specific one (must be your own). Deltas are estimates from your file, not guarantees.
// @Tags         score-builder
// @Produce      json
// @Security     BearerAuth
// @Param        reportId  query     int     false  "Simulate against this report instead of the latest (must be your own)"
// @Param        actions   query     string  false  "Comma-separated action keys to select (default: all positive actions)"
// @Success      200  {object}  service.Simulation
// @Failure      400  {object}  apperr.ErrorBody  "reportId must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "No credit report found / report not found"
// @Router       /credit-analytics/score-simulator [get]
func (h *ScoreBuilderHandler) Simulate(c *fiber.Ctx) error {
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
	// `actions` is optional: when absent the service selects all positive
	// actions by default; when present (even if empty) the caller has taken
	// control of the selection.
	//
	// Presence is tested on the raw query args, not on the parsed value: an empty
	// `?actions=` is a client that unselected everything, and treating it as
	// "absent" would hand back the default all-positive projection — the one
	// answer the user just said they didn't want.
	var selected map[string]bool
	if c.Context().QueryArgs().Has("actions") {
		selected = map[string]bool{}
		for _, k := range strings.Split(c.Query("actions"), ",") {
			if k = strings.TrimSpace(k); k != "" {
				selected[k] = true
			}
		}
	}
	out, err := h.svc.Simulate(c.Context(), accountID, reportID, selected)
	if err != nil {
		return err
	}
	return c.JSON(out)
}
