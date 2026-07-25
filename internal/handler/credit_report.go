package handler

import (
	"strconv"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	_ "credit-report-service/internal/models" // referenced by swag annotations (models.CreditReport)
	"credit-report-service/internal/service"
)

type CreditReportHandler struct {
	svc *service.CreditReportService
}

func NewCreditReportHandler(svc *service.CreditReportService) *CreditReportHandler {
	return &CreditReportHandler{svc: svc}
}

// request body for POST /api/credit-reports
type creditReportCreateReq struct {
	SubjectID string  `json:"subjectId" example:"PAN-ABCDE1234F"`
	Score     *int32  `json:"score"     example:"780"`
	Status    *string `json:"status"    example:"HEALTHY"`
}

// List godoc
//
// @Summary      List all credit reports
// @Description  Returns every credit report row.
// @Tags         credit-reports
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.CreditReport
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /credit-reports [get]
func (h *CreditReportHandler) List(c *fiber.Ctx) error {
	rs, err := h.svc.List(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(rs)
}

// Get godoc
//
// @Summary      Get a credit report by id
// @Tags         credit-reports
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Report id"
// @Success      200  {object}  models.CreditReport
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Not found"
// @Router       /credit-reports/{id} [get]
func (h *CreditReportHandler) Get(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	cr, err := h.svc.Get(c.Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(cr)
}

// GetBySubject godoc
//
// @Summary      Get a credit report by subject id
// @Tags         credit-reports
// @Produce      json
// @Security     BearerAuth
// @Param        subjectId  path      string  true  "Subject id"
// @Success      200  {object}  models.CreditReport
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Not found"
// @Router       /credit-reports/by-subject/{subjectId} [get]
func (h *CreditReportHandler) GetBySubject(c *fiber.Ctx) error {
	subjectID := c.Params("subjectId")
	cr, err := h.svc.GetBySubject(c.Context(), subjectID)
	if err != nil {
		return err
	}
	return c.JSON(cr)
}

// Create godoc
//
// @Summary      Create a credit report
// @Tags         credit-reports
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      creditReportCreateReq  true  "Report fields"
// @Success      201      {object}  models.CreditReport
// @Failure      400      {object}  apperr.ErrorBody  "Invalid JSON body"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /credit-reports [post]
func (h *CreditReportHandler) Create(c *fiber.Ctx) error {
	var req creditReportCreateReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	cr, err := h.svc.Create(c.Context(), req.SubjectID, req.Score, req.Status)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(cr)
}

// Delete godoc
//
// @Summary      Delete a credit report by id
// @Tags         credit-reports
// @Security     BearerAuth
// @Param        id  path  int  true  "Report id"
// @Success      204
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Not found"
// @Router       /credit-reports/{id} [delete]
func (h *CreditReportHandler) Delete(c *fiber.Ctx) error {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	if err := h.svc.Delete(c.Context(), id); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}
