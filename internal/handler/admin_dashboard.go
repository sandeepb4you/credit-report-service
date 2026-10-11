package handler

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// AdminDashboardHandler carries the console's custom dashboards: the metric
// catalog and series their widgets draw, CSV export of a series, and saving,
// importing and exporting the dashboards themselves.
type AdminDashboardHandler struct {
	svc *service.DashboardService
}

func NewAdminDashboardHandler(svc *service.DashboardService) *AdminDashboardHandler {
	return &AdminDashboardHandler{svc: svc}
}

type dashboardReq struct {
	Name    string                   `json:"name"`
	Widgets []models.DashboardWidget `json:"widgets"`
}

// Metrics godoc
//
// @Summary      The dashboard metric catalog
// @Description  Every metric a widget can show, with whether the caller holds the permission that owns its data. A metric the caller cannot read is still listed (allowed=false) so an imported dashboard can draw it as a locked tile.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.DashboardMetric
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'dashboard:manage' permission"
// @Router       /admin/dashboards/metrics [get]
func (h *AdminDashboardHandler) Metrics(c *fiber.Ctx) error {
	role, _ := middleware.AccountRole(c)
	return c.JSON(h.svc.Metrics(role))
}

// Series godoc
//
// @Summary      One metric per day
// @Description  The metric over whole UTC days, from and to inclusive and both required, at most 366 days. Gap-filled: every day has a point. Money is in paise. 403 when the caller lacks the permission that owns the metric's data.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        key   path   string  true  "Metric key from the catalog"
// @Param        from  query  string  true  "First day, inclusive (YYYY-MM-DD)"
// @Param        to    query  string  true  "Last day, inclusive (YYYY-MM-DD)"
// @Success      200  {object}  models.DashboardSeries
// @Failure      400  {object}  apperr.ErrorBody
// @Failure      403  {object}  apperr.ErrorBody
// @Failure      404  {object}  apperr.ErrorBody  "No such metric"
// @Router       /admin/dashboards/metrics/{key} [get]
func (h *AdminDashboardHandler) Series(c *fiber.Ctx) error {
	from, to, err := seriesWindow(c)
	if err != nil {
		return err
	}
	role, _ := middleware.AccountRole(c)
	series, err := h.svc.Series(c.Context(), role, c.Params("key"), from, to)
	if err != nil {
		return err
	}
	return c.JSON(series)
}

// ExportSeries godoc
//
// @Summary      Download one metric per day as CSV
// @Description  The same series as the JSON route, as a CSV attachment (date, count — or date, amount_inr for money). Every export is recorded against the caller.
// @Tags         admin
// @Produce      text/csv
// @Security     BearerAuth
// @Param        key   path   string  true  "Metric key from the catalog"
// @Param        from  query  string  true  "First day, inclusive (YYYY-MM-DD)"
// @Param        to    query  string  true  "Last day, inclusive (YYYY-MM-DD)"
// @Success      200  {string}  string
// @Router       /admin/dashboards/metrics/{key}/export [get]
func (h *AdminDashboardHandler) ExportSeries(c *fiber.Ctx) error {
	from, to, err := seriesWindow(c)
	if err != nil {
		return err
	}
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	role, _ := middleware.AccountRole(c)
	name, body, err := h.svc.ExportCSV(c.Context(), accountID, role, c.Params("key"), from, to)
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+name+`"`)
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Send(body)
}

// List godoc
//
// @Summary      The caller's dashboards
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}  models.AdminDashboard
// @Router       /admin/dashboards [get]
func (h *AdminDashboardHandler) List(c *fiber.Ctx) error {
	ownerID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	list, err := h.svc.List(c.Context(), ownerID)
	if err != nil {
		return err
	}
	return c.JSON(list)
}

// Get godoc
//
// @Summary      One of the caller's dashboards
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        id  path  int  true  "Dashboard id"
// @Success      200  {object}  models.AdminDashboard
// @Failure      404  {object}  apperr.ErrorBody  "No such dashboard, or not yours"
// @Router       /admin/dashboards/{id} [get]
func (h *AdminDashboardHandler) Get(c *fiber.Ctx) error {
	id, ownerID, err := dashboardIDs(c)
	if err != nil {
		return err
	}
	d, err := h.svc.Get(c.Context(), id, ownerID)
	if err != nil {
		return err
	}
	return c.JSON(d)
}

// Create godoc
//
// @Summary      Save a new dashboard
// @Description  A name and up to 24 widgets, each a catalog metric, a viz (kpi, line, bar) and a relative range (7d, 30d, 90d, 365d). Private to the caller.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      201  {object}  models.AdminDashboard
// @Failure      400  {object}  apperr.ErrorBody
// @Failure      409  {object}  apperr.ErrorBody  "At the per-account dashboard cap"
// @Router       /admin/dashboards [post]
func (h *AdminDashboardHandler) Create(c *fiber.Ctx) error {
	ownerID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req dashboardReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	d, err := h.svc.Create(c.Context(), ownerID, req.Name, req.Widgets)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(d)
}

// Update godoc
//
// @Summary      Replace a dashboard's name and widgets
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id  path  int  true  "Dashboard id"
// @Success      200  {object}  models.AdminDashboard
// @Router       /admin/dashboards/{id} [put]
func (h *AdminDashboardHandler) Update(c *fiber.Ctx) error {
	id, ownerID, err := dashboardIDs(c)
	if err != nil {
		return err
	}
	var req dashboardReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	d, err := h.svc.Update(c.Context(), id, ownerID, req.Name, req.Widgets)
	if err != nil {
		return err
	}
	return c.JSON(d)
}

// Delete godoc
//
// @Summary      Delete a dashboard
// @Tags         admin
// @Security     BearerAuth
// @Param        id  path  int  true  "Dashboard id"
// @Success      204
// @Router       /admin/dashboards/{id} [delete]
func (h *AdminDashboardHandler) Delete(c *fiber.Ctx) error {
	id, ownerID, err := dashboardIDs(c)
	if err != nil {
		return err
	}
	if err := h.svc.Delete(c.Context(), id, ownerID); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// Export godoc
//
// @Summary      Export a dashboard's definition
// @Description  The portable form ({format: "myscorr.dashboard", version: 1, name, widgets}) that the import route reads. Carries no data — only which metrics to draw and how.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        id  path  int  true  "Dashboard id"
// @Success      200  {object}  models.DashboardDefinition
// @Router       /admin/dashboards/{id}/export [get]
func (h *AdminDashboardHandler) Export(c *fiber.Ctx) error {
	id, ownerID, err := dashboardIDs(c)
	if err != nil {
		return err
	}
	def, err := h.svc.Export(c.Context(), id, ownerID)
	if err != nil {
		return err
	}
	return c.JSON(def)
}

// Import godoc
//
// @Summary      Import a dashboard definition
// @Description  Saves an exported definition as a new dashboard of the caller's, validated as strictly as a hand-built one. Widgets on metrics the caller cannot read are kept and draw locked.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body  models.DashboardDefinition  true  "An exported definition"
// @Success      201  {object}  models.AdminDashboard
// @Failure      400  {object}  apperr.ErrorBody  "Not a dashboard file, a newer version, or invalid widgets"
// @Router       /admin/dashboards/import [post]
func (h *AdminDashboardHandler) Import(c *fiber.Ctx) error {
	ownerID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var def models.DashboardDefinition
	if err := c.BodyParser(&def); err != nil {
		return apperr.NewValidation("This file isn't valid JSON")
	}
	d, err := h.svc.Import(c.Context(), ownerID, def)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(d)
}

func dashboardIDs(c *fiber.Ctx) (id, ownerID int64, err error) {
	id, err = strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, 0, apperr.NewValidationWith("Validation failed", map[string]string{
			"id": "expected a positive dashboard id",
		})
	}
	ownerID, ok := middleware.AccountID(c)
	if !ok {
		return 0, 0, apperr.NewUnauthorized("Not authenticated")
	}
	return id, ownerID, nil
}

func seriesWindow(c *fiber.Ctx) (from, to time.Time, err error) {
	if from, err = queryDate(c, "from"); err != nil {
		return
	}
	to, err = queryDate(c, "to")
	return
}
