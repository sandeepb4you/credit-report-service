package handler

import (
	"strconv"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
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
	SubjectID string `json:"subjectId"`
	Score     *int32 `json:"score"`
	Status    *string `json:"status"`
}

func (h *CreditReportHandler) List(c *fiber.Ctx) error {
	rs, err := h.svc.List(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(rs)
}

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

func (h *CreditReportHandler) GetBySubject(c *fiber.Ctx) error {
	subjectID := c.Params("subjectId")
	cr, err := h.svc.GetBySubject(c.Context(), subjectID)
	if err != nil {
		return err
	}
	return c.JSON(cr)
}

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
