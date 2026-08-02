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

// CreditAnalyticsHandler exposes the Digitap credit-analytics proxy endpoint.
type CreditAnalyticsHandler struct {
	svc *service.CreditAnalyticsService
}

func NewCreditAnalyticsHandler(svc *service.CreditAnalyticsService) *CreditAnalyticsHandler {
	return &CreditAnalyticsHandler{svc: svc}
}

// Request godoc
//
// @Summary      Request a credit analysis from Digitap
// @Description  Builds the Digitap /credit_analytics/request payload from the authenticated account's profile and KYC record (mobile, name, PAN), plus server-generated values (client_ref_num, otp, timestamp), then persists the request and full upstream response and returns the stored row. Only device_ip is taken from the request body; if omitted it falls back to the detected remote IP.
// @Tags         credit-analytics
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      service.CreditAnalyticsInput  false  "Credit-analytics request (device_ip optional; defaults to the caller's IP)"
// @Success      201      {object}  models.CreditAnalyticsRequest
// @Failure      400      {object}  apperr.ErrorBody  "Invalid request body / missing profile or PAN / upstream 400"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated / upstream 401"
// @Failure      422      {object}  apperr.ErrorBody  "Upstream tradeline limit exceeded"
// @Router       /credit-analytics/request [post]
func (h *CreditAnalyticsHandler) Request(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}

	var in service.CreditAnalyticsInput
	if err := c.BodyParser(&in); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	// device_ip defaults to the caller's address when the body omits it, so a
	// client can POST {} and still produce a valid Digitap request.
	if strings.TrimSpace(in.DeviceIP) == "" {
		in.DeviceIP = c.IP()
	}

	row, err := h.svc.Request(c.Context(), accountID, in)
	if err != nil {
		// On upstream-mapped errors the row has already been persisted; surface
		// the error envelope as usual, and attach the row id via X-Request-Id so
		// the caller can reconcile.
		if row != nil {
			c.Set("X-Credit-Analytics-Id", strconv.FormatInt(row.ID, 10))
		}
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(row)
}

// ListReports godoc
//
// @Summary      List the authenticated account's credit-analytics reports
// @Description  Returns a paginated list of the caller's reports, newest first. Each item carries only the report id (unique identifier) and generation date.
// @Tags         credit-analytics
// @Produce      json
// @Security     BearerAuth
// @Param        page  query     int  false  "1-indexed page number (default 1)"  default(1)
// @Param        size  query     int  false  "page size (default 20, max 100)"   default(20)
// @Success      200   {object}  service.ReportPage
// @Failure      401   {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /credit-analytics/reports [get]
func (h *CreditAnalyticsHandler) ListReports(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	// QueryInt returns 0 when the param is absent or unparsable; the service
	// normalizes 0 to the defaults.
	page := c.QueryInt("page", 0)
	size := c.QueryInt("size", 0)
	res, err := h.svc.ListReports(c.Context(), accountID, page, size)
	if err != nil {
		return err
	}
	return c.JSON(res)
}

// GetReport godoc
//
// @Summary      Fetch a credit-analytics report by id
// @Description  Returns the full report row (including the persisted upstream response body) for one of the caller's own reports.
// @Tags         credit-analytics
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Report id"
// @Success      200  {object}  models.CreditAnalyticsRequest
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Report not found (or belongs to another account)"
// @Router       /credit-analytics/reports/{id} [get]
func (h *CreditAnalyticsHandler) GetReport(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	row, err := h.svc.GetReport(c.Context(), accountID, id)
	if err != nil {
		return err
	}
	return c.JSON(row)
}

// GetLatestInsights godoc
//
// @Summary      Get credit insights from the latest report
// @Description  Returns derived analytics from the most recent successful credit report: on-time payment percentage, card utilization percentage, and enquiry count for the past 180 days.
// @Tags         credit-analytics
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  service.ReportInsights
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "No credit report found"
// @Router       /credit-analytics/latest-insights [get]
func (h *CreditAnalyticsHandler) GetLatestInsights(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	insights, err := h.svc.GetLatestReportInsights(c.Context(), accountID)
	if err != nil {
		return err
	}
	return c.JSON(insights)
}
