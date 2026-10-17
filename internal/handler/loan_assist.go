package handler

import (
	"io"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// LoanAssistHandler serves Loan Assist: the customer's applications and
// documents, and the partners' review.
type LoanAssistHandler struct {
	svc         *service.LoanAssistService
	maxDocBytes int
}

func NewLoanAssistHandler(svc *service.LoanAssistService, maxDocBytes int) *LoanAssistHandler {
	return &LoanAssistHandler{svc: svc, maxDocBytes: maxDocBytes}
}

// ---- customer -------------------------------------------------------------------

// State godoc
//
// @Summary      Loan Assist: price, loan types, open draft, applications, saved documents
// @Tags         loan-assist
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  service.LoanAssistState
// @Router       /loan-assist [get]
func (h *LoanAssistHandler) State(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	st, err := h.svc.State(c.Context(), accountID)
	if err != nil {
		return err
	}
	return c.JSON(st)
}

type loanAssistDraftReq struct {
	LoanType     *string `json:"loanType"`
	AmountNeeded *int64  `json:"amountNeeded"`
	Rented       *bool   `json:"rented"`
}

// UpdateDraft godoc
//
// @Summary      Start or change the open application
// @Description  Creates the open application on first use. loanType must be an active loan type; amountNeeded is whole rupees. Documents the new loan type or premises no longer need are removed.
// @Tags         loan-assist
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body  loanAssistDraftReq  true  "Fields to change"
// @Success      200  {object}  service.LoanAssistDraft
// @Router       /loan-assist/draft [put]
func (h *LoanAssistHandler) UpdateDraft(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req loanAssistDraftReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	if req.LoanType == nil && req.AmountNeeded == nil && req.Rented == nil {
		return apperr.NewValidation("nothing to change")
	}
	draft, err := h.svc.UpdateDraft(c.Context(), accountID, req.LoanType, req.AmountNeeded, req.Rented)
	if err != nil {
		return err
	}
	return c.JSON(draft)
}

type loanAssistReuseReq struct {
	Slots []string `json:"slots"`
}

// ReuseSaved godoc
//
// @Summary      Use saved, partner-verified documents in the open application
// @Tags         loan-assist
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body  loanAssistReuseReq  true  "Slots to fill from saved documents"
// @Success      200  {object}  service.LoanAssistDraft
// @Router       /loan-assist/draft/reuse [post]
func (h *LoanAssistHandler) ReuseSaved(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req loanAssistReuseReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	draft, err := h.svc.ReuseSaved(c.Context(), accountID, req.Slots)
	if err != nil {
		return err
	}
	return c.JSON(draft)
}

// UploadDocument godoc
//
// @Summary      Add or replace one document of the open application
// @Description  Multipart: "slot" (one the loan type asks for) and "file" (JPG, PNG or PDF, judged by its bytes).
// @Tags         loan-assist
// @Accept       multipart/form-data
// @Produce      json
// @Security     BearerAuth
// @Success      201  {object}  service.LoanAssistDraft
// @Router       /loan-assist/documents [post]
func (h *LoanAssistHandler) UploadDocument(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	header, data, err := h.readUpload(c)
	if err != nil {
		return err
	}
	draft, err := h.svc.UploadDocument(c.Context(), accountID, c.FormValue("slot"),
		header.Filename, header.Header.Get("Content-Type"), data)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(draft)
}

// readUpload reads the multipart "file" field, bounded by the per-document cap.
func (h *LoanAssistHandler) readUpload(c *fiber.Ctx) (*multipart.FileHeader, []byte, error) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return nil, nil, apperr.NewValidation("no file uploaded under field 'file'")
	}
	if fileHeader.Size == 0 {
		return nil, nil, apperr.NewValidation("uploaded file is empty")
	}
	if h.maxDocBytes > 0 && fileHeader.Size > int64(h.maxDocBytes) {
		return nil, nil, apperr.NewPayloadTooLarge("Files can be up to 10 MB")
	}
	file, err := fileHeader.Open()
	if err != nil {
		return nil, nil, apperr.NewValidation("could not open the uploaded file")
	}
	defer file.Close()
	data := make([]byte, fileHeader.Size)
	if _, err := io.ReadFull(file, data); err != nil {
		return nil, nil, apperr.NewValidation("could not read the uploaded file")
	}
	return fileHeader, data, nil
}

// RemoveDocument godoc
//
// @Summary      Empty one slot of the open application
// @Tags         loan-assist
// @Produce      json
// @Security     BearerAuth
// @Param        slot  path  string  true  "Document slot"
// @Success      200  {object}  service.LoanAssistDraft
// @Router       /loan-assist/documents/{slot} [delete]
func (h *LoanAssistHandler) RemoveDocument(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	draft, err := h.svc.RemoveDocument(c.Context(), accountID, c.Params("slot"))
	if err != nil {
		return err
	}
	return c.JSON(draft)
}

type loanAssistSubmitReq struct {
	Consent    bool   `json:"consent"`
	CouponCode string `json:"couponCode"`
}

// Submit godoc
//
// @Summary      Agree and open the order for the open application
// @Description  Requires an active loan type, the amount, every required document and consent=true. Records the consent with a timestamp; the response is the same checkout payload as POST /orders.
// @Tags         loan-assist
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body  loanAssistSubmitReq  true  "Consent and optional coupon"
// @Success      201  {object}  service.PurchaseResult
// @Router       /loan-assist/submit [post]
func (h *LoanAssistHandler) Submit(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req loanAssistSubmitReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	testKey := strings.TrimSpace(c.Get(testPaymentsKeyHeader))
	res, err := h.svc.Submit(c.Context(), accountID, req.Consent, req.CouponCode, testKey)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(res)
}

// Application godoc
//
// @Summary      One of the caller's applications: progress, documents, expert
// @Tags         loan-assist
// @Produce      json
// @Security     BearerAuth
// @Param        code  path  string  true  "Request code, e.g. LA-20481"
// @Success      200  {object}  service.LoanAssistDetail
// @Router       /loan-assist/applications/{code} [get]
func (h *LoanAssistHandler) Application(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	d, err := h.svc.Application(c.Context(), accountID, c.Params("code"))
	if err != nil {
		return err
	}
	return c.JSON(d)
}

// Resubmit godoc
//
// @Summary      Upload a document again that a partner asked for
// @Description  Multipart: "slot" and "file". Only a slot whose review is RETAKE; it goes back into review.
// @Tags         loan-assist
// @Accept       multipart/form-data
// @Produce      json
// @Security     BearerAuth
// @Param        code  path  string  true  "Request code"
// @Success      200  {object}  service.LoanAssistDetail
// @Router       /loan-assist/applications/{code}/documents [post]
func (h *LoanAssistHandler) Resubmit(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	header, data, err := h.readUpload(c)
	if err != nil {
		return err
	}
	d, err := h.svc.Resubmit(c.Context(), accountID, c.Params("code"), c.FormValue("slot"),
		header.Filename, header.Header.Get("Content-Type"), data)
	if err != nil {
		return err
	}
	return c.JSON(d)
}

// ---- partner review -------------------------------------------------------------

// reviewer is the caller as the review routes see them. The route group is
// gated on loan-assist:review; manage (admins) widens what they may do.
func reviewer(c *fiber.Ctx) (service.Reviewer, error) {
	id, ok := middleware.AccountID(c)
	if !ok {
		return service.Reviewer{}, apperr.NewUnauthorized("Not authenticated")
	}
	role, _ := middleware.AccountRole(c)
	return service.Reviewer{AccountID: id, Admin: models.HasPermission(role, models.PermLoanAssistManage)}, nil
}

func pathID(c *fiber.Ctx) (int64, error) {
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return 0, apperr.NewValidation("invalid application id")
	}
	return id, nil
}

// ReviewList godoc
//
// @Summary      Applications to review (partners and admins)
// @Description  scope: mine (assigned to the caller, the default for a partner), unassigned, or all (admins). Contact details are masked on applications the caller may not open.
// @Tags         loan-assist-review
// @Produce      json
// @Security     BearerAuth
// @Param        scope   query  string  false  "mine | unassigned | all"
// @Param        status  query  string  false  "SUBMITTED, DOCS_VERIFIED, EXPERT_CONTACTED, GUIDANCE or CLOSED"
// @Router       /loan-assist/review [get]
func (h *LoanAssistHandler) ReviewList(c *fiber.Ctx) error {
	rv, err := reviewer(c)
	if err != nil {
		return err
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	rows, total, err := h.svc.ReviewList(c.Context(), rv, strings.ToLower(strings.TrimSpace(c.Query("scope"))),
		c.Query("status"), limit, offset)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"applications": rows, "total": total, "admin": rv.Admin})
}

// ReviewDetail godoc
//
// @Summary      One application to review, with its documents
// @Tags         loan-assist-review
// @Produce      json
// @Security     BearerAuth
// @Param        id  path  int  true  "Application id"
// @Router       /loan-assist/review/{id} [get]
func (h *LoanAssistHandler) ReviewDetail(c *fiber.Ctx) error {
	rv, err := reviewer(c)
	if err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	app, err := h.svc.ReviewDetail(c.Context(), rv, id)
	if err != nil {
		return err
	}
	return c.JSON(app)
}

// ReviewDocumentLink godoc
//
// @Summary      Presigned link to one document (assigned partner or admin)
// @Tags         loan-assist-review
// @Produce      json
// @Security     BearerAuth
// @Param        id    path  int     true  "Application id"
// @Param        slot  path  string  true  "Document slot"
// @Router       /loan-assist/review/{id}/documents/{slot}/link [get]
func (h *LoanAssistHandler) ReviewDocumentLink(c *fiber.Ctx) error {
	rv, err := reviewer(c)
	if err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	url, ttl, doc, err := h.svc.DocumentLink(c.Context(), rv, id, c.Params("slot"))
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{
		"url": url, "expiresInSeconds": int(ttl.Seconds()),
		"fileName": doc.FileName, "mimeType": doc.MimeType,
	})
}

type reviewDocReq struct {
	Decision string `json:"decision"` // ACCEPT | RETAKE
	Note     string `json:"note"`
}

// ReviewDocument godoc
//
// @Summary      Accept a document, or ask the customer for it again
// @Description  RETAKE needs a note the customer will read, and tells their phone.
// @Tags         loan-assist-review
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path  int           true  "Application id"
// @Param        slot     path  string        true  "Document slot"
// @Param        request  body  reviewDocReq  true  "Decision"
// @Router       /loan-assist/review/{id}/documents/{slot} [post]
func (h *LoanAssistHandler) ReviewDocument(c *fiber.Ctx) error {
	rv, err := reviewer(c)
	if err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	var req reviewDocReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	app, err := h.svc.ReviewDocument(c.Context(), rv, id, c.Params("slot"), req.Decision, req.Note)
	if err != nil {
		return err
	}
	return c.JSON(app)
}

type reviewStageReq struct {
	Status string `json:"status"`
}

// ReviewStage godoc
//
// @Summary      Move an application to a status
// @Tags         loan-assist-review
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path  int             true  "Application id"
// @Param        request  body  reviewStageReq  true  "Status"
// @Router       /loan-assist/review/{id}/status [post]
func (h *LoanAssistHandler) ReviewStage(c *fiber.Ctx) error {
	rv, err := reviewer(c)
	if err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	var req reviewStageReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	app, err := h.svc.SetStage(c.Context(), rv, id, req.Status)
	if err != nil {
		return err
	}
	return c.JSON(app)
}

type reviewAssignReq struct {
	PartnerID *int64 `json:"partnerId"` // null = unassign
}

// ReviewAssign godoc
//
// @Summary      Assign an application to a partner
// @Description  An admin assigns anything to anyone; a partner takes an unassigned application or hands their own to another partner.
// @Tags         loan-assist-review
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path  int              true  "Application id"
// @Param        request  body  reviewAssignReq  true  "Partner"
// @Router       /loan-assist/review/{id}/assign [post]
func (h *LoanAssistHandler) ReviewAssign(c *fiber.Ctx) error {
	rv, err := reviewer(c)
	if err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	var req reviewAssignReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	app, err := h.svc.Assign(c.Context(), rv, id, req.PartnerID)
	if err != nil {
		return err
	}
	return c.JSON(app)
}

type reviewNotesReq struct {
	Notes string `json:"notes"`
}

// ReviewNotes godoc
//
// @Summary      Replace the partners' notes on an application
// @Tags         loan-assist-review
// @Accept       json
// @Security     BearerAuth
// @Param        id       path  int             true  "Application id"
// @Param        request  body  reviewNotesReq  true  "Notes"
// @Router       /loan-assist/review/{id}/notes [put]
func (h *LoanAssistHandler) ReviewNotes(c *fiber.Ctx) error {
	rv, err := reviewer(c)
	if err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	var req reviewNotesReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	if err := h.svc.SetNotes(c.Context(), rv, id, req.Notes); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// Partners godoc
//
// @Summary      Partners an application can be assigned to
// @Tags         loan-assist-review
// @Produce      json
// @Security     BearerAuth
// @Router       /loan-assist/partners [get]
func (h *LoanAssistHandler) Partners(c *fiber.Ctx) error {
	out, err := h.svc.Partners(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// MyProfile godoc
//
// @Summary      The caller's partner card, as customers see it
// @Tags         loan-assist-review
// @Produce      json
// @Security     BearerAuth
// @Router       /loan-assist/partner/profile [get]
func (h *LoanAssistHandler) MyProfile(c *fiber.Ctx) error {
	id, _ := middleware.AccountID(c)
	card, err := h.svc.MyProfile(c.Context(), id)
	if err != nil {
		return err
	}
	return c.JSON(card)
}

// SaveProfile godoc
//
// @Summary      Update the caller's partner card
// @Tags         loan-assist-review
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Router       /loan-assist/partner/profile [put]
func (h *LoanAssistHandler) SaveProfile(c *fiber.Ctx) error {
	id, _ := middleware.AccountID(c)
	var req models.PartnerCard
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	card, err := h.svc.SaveProfile(c.Context(), id, req)
	if err != nil {
		return err
	}
	return c.JSON(card)
}
