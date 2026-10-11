package handler

import (
	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// AdminAudienceHandler carries the console's retargeting lists: the catalogue
// with each list's size, one list's accounts, and the per-channel CSV export.
type AdminAudienceHandler struct {
	svc *service.AudienceService
}

func NewAdminAudienceHandler(svc *service.AudienceService) *AdminAudienceHandler {
	return &AdminAudienceHandler{svc: svc}
}

// Lists godoc
//
// @Summary      The retargeting lists
// @Description  Every list — id, group, name, description, columns, filters — with how many accounts are in it now.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.AudienceList
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'audience:export' permission"
// @Router       /admin/audiences [get]
func (h *AdminAudienceHandler) Lists(c *fiber.Ctx) error {
	lists, err := h.svc.Lists(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(lists)
}

// Rows godoc
//
// @Summary      One retargeting list's accounts
// @Description  Every account in the list with its contact details, platform, push reachability and the list's own columns. At most 5000 rows; truncated says when the list held more.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        id   path  string  true  "List id, A to K"
// @Success      200  {object}  models.AudienceListRows
// @Failure      404  {object}  apperr.ErrorBody  "No such list"
// @Router       /admin/audiences/{id} [get]
func (h *AdminAudienceHandler) Rows(c *fiber.Ctx) error {
	rows, err := h.svc.Rows(c.Context(), c.Params("id"))
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(rows)
}

type audienceExportReq struct {
	Channel    string  `json:"channel"`
	AccountIDs []int64 `json:"accountIds"`
}

// Export godoc
//
// @Summary      Build a channel CSV from selected accounts
// @Description  The selected accounts of one list as a CSV shaped for whatsapp, sms, email, push or all. Accounts the channel cannot reach are left out and counted. Ids not in the list are ignored. Every file generated is recorded against the caller.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id    path  string              true  "List id, A to K"
// @Param        body  body  audienceExportReq   true  "Channel and selected account ids"
// @Success      200  {object}  models.AudienceExport
// @Failure      400  {object}  apperr.ErrorBody
// @Failure      404  {object}  apperr.ErrorBody  "No such list"
// @Router       /admin/audiences/{id}/export [post]
func (h *AdminAudienceHandler) Export(c *fiber.Ctx) error {
	adminID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req audienceExportReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("Invalid request body")
	}
	out, err := h.svc.Export(c.Context(), adminID, c.Params("id"), req.Channel, req.AccountIDs)
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(out)
}
