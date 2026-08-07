package handler

import (
	"log/slog"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// BankStatementHandler exposes the bank-statement analysis endpoints: a
// multipart upload that queues async analysis, the Digitap redirect-flow
// initiation, the Digitap callback webhook, and reads for polling/results.
type BankStatementHandler struct {
	svc *service.BankStatementService
	// maxBytes is the per-upload size cap (statement.max-file-size), enforced
	// here so an oversized upload fails with 413 before reaching the service.
	maxBytes int
	// callbackSecret, when non-empty, gates the public Digitap webhook: the
	// caller must echo it back as ?secret=. v1.20 of the API defines no HMAC,
	// so this shared-secret is our guard against the endpoint being fully open.
	callbackSecret string
}

func NewBankStatementHandler(svc *service.BankStatementService, maxBytes int, callbackSecret string) *BankStatementHandler {
	return &BankStatementHandler{svc: svc, maxBytes: maxBytes, callbackSecret: callbackSecret}
}

// Analyze godoc
//
// @Summary      Upload a bank-statement PDF for analysis
// @Description  Accepts a single PDF under the multipart field "file", persists it in 'processing' status, and queues asynchronous analysis. Returns the created row immediately with status 202; poll /bank-statements/{id} until status is 'completed' (analysis populated) or 'failed' (errorMessage set).
// @Tags         bank-statements
// @Accept       multipart/form-data
// @Produce      json
// @Security     BearerAuth
// @Param        file  formData  file  true  "Bank statement PDF (text layer required; scanned PDFs are not supported yet)"
// @Success      202   {object}  models.BankStatement
// @Failure      400   {object}  apperr.ErrorBody  "No file uploaded / not a PDF / empty file"
// @Failure      401   {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      413   {object}  apperr.ErrorBody  "File exceeds the maximum allowed size"
// @Failure      503   {object}  apperr.ErrorBody  "Analysis queue is full; retry shortly"
// @Router       /bank-statements/analyze [post]
func (h *BankStatementHandler) Analyze(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return apperr.NewValidation("no file uploaded under field 'file'")
	}
	// Reject obviously non-PDF uploads by extension and content type. The real
	// work happens on the PDF bytes regardless, but an early, clear 400 beats a
	// generic 'unparseable' after the row is created.
	if !looksLikePDF(fileHeader.Filename, fileHeader.Header.Get("Content-Type")) {
		return apperr.NewValidation("only PDF statements are supported")
	}
	if fileHeader.Size == 0 {
		return apperr.NewValidation("uploaded file is empty")
	}
	if h.maxBytes > 0 && fileHeader.Size > int64(h.maxBytes) {
		return apperr.NewPayloadTooLarge(
			"statement exceeds the maximum allowed size")
	}

	file, err := fileHeader.Open()
	if err != nil {
		return apperr.NewValidation("could not open the uploaded file")
	}
	defer file.Close()

	// Read fully into memory: statements are bounded by maxBytes and the worker
	// needs the bytes off the request goroutine anyway.
	pdfBytes := make([]byte, fileHeader.Size)
	if _, err := file.Read(pdfBytes); err != nil {
		return apperr.NewValidation("could not read the uploaded file")
	}

	mimeType := fileHeader.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/pdf"
	}

	row, err := h.svc.Submit(c.Context(), accountID, fileHeader.Filename, mimeType, pdfBytes, h.maxBytes)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(row)
}

// looksLikePDF accepts .pdf filenames and the standard/casual PDF mime types.
func looksLikePDF(filename, contentType string) bool {
	if strings.HasSuffix(strings.ToLower(filename), ".pdf") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "application/pdf", "application/octet-stream":
		return true
	}
	return false
}

// List godoc
//
// @Summary      List the authenticated account's bank-statement analyses
// @Description  Returns a paginated list of the caller's uploads, newest first. Each item carries the id, filename, status, transaction count, and period — not the full analysis (fetch by id for that).
// @Tags         bank-statements
// @Produce      json
// @Security     BearerAuth
// @Param        page  query     int  false  "1-indexed page number (default 1)"  default(1)
// @Param        size  query     int  false  "page size (default 20, max 100)"   default(20)
// @Success      200   {object}  service.StatementPage
// @Failure      401   {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /bank-statements [get]
func (h *BankStatementHandler) List(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	page := c.QueryInt("page", 0)
	size := c.QueryInt("size", 0)
	res, err := h.svc.List(c.Context(), accountID, page, size)
	if err != nil {
		return err
	}
	return c.JSON(res)
}

// Get godoc
//
// @Summary      Fetch a bank-statement analysis by id
// @Description  Returns the analysis for one of the caller's own uploads. When status is 'processing', analysis is null; when 'completed', analysis carries the derived metrics (summary, salary, emis, subscriptions, categories, topMerchants, monthlyTotals, transactions); when 'failed', errorMessage explains why.
// @Tags         bank-statements
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Statement id"
// @Success      200  {object}  models.BankStatement
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Statement not found (or belongs to another account)"
// @Router       /bank-statements/{id} [get]
func (h *BankStatementHandler) Get(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	row, err := h.svc.Get(c.Context(), accountID, id)
	if err != nil {
		return err
	}
	return c.JSON(row)
}

// GetRaw godoc
//
// @Summary      Fetch the raw extracted text for a bank statement
// @Description  Returns one of the caller's own uploads including the extractedText (the PDF text layer exactly as parsed). Useful for debugging "why wasn't my salary detected" — the analysis is computed from this text. Never includes the raw PDF bytes.
// @Tags         bank-statements
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Statement id"
// @Success      200  {object}  models.BankStatement
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Statement not found (or belongs to another account)"
// @Router       /bank-statements/{id}/raw [get]
func (h *BankStatementHandler) GetRaw(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	row, err := h.svc.GetRaw(c.Context(), accountID, id)
	if err != nil {
		return err
	}
	return c.JSON(row)
}

// GetLatest godoc
//
// @Summary      Get the latest completed bank-statement analysis
// @Description  Returns the most recent 'completed' analysis for the caller, with the full derived metrics. Returns 404 if no analysis has completed yet.
// @Tags         bank-statements
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  models.BankStatement
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "No completed bank statement analysis found"
// @Router       /bank-statements/latest [get]
func (h *BankStatementHandler) GetLatest(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	row, err := h.svc.GetLatest(c.Context(), accountID)
	if err != nil {
		return err
	}
	return c.JSON(row)
}

// InitiateDigitap godoc
//
// @Summary      Start a Digitap bank-statement analysis (redirect flow)
// @Description  Calls Digitap's Generate URL API and returns a redirect URL the client should send the user to. The user uploads their statement PDF on Digitap's UI (it never touches this service). Digitap calls our callback on completion; the client polls GET /bank-statements/{id} until status is 'completed' or 'failed'. Available regardless of statement.provider — the client chooses between this and the local upload (POST /analyze).
// @Tags         bank-statements
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      object  false  "{ ""returnUrl"": ""https://app/..."" } — optional; defaults to the configured return URL"
// @Success      200  {object}  service.DigitapInitiateResponse
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      502  {object}  apperr.ErrorBody  "Digitap rejected the request"
// @Failure      503  {object}  apperr.ErrorBody  "Digitap flow not configured"
// @Router       /bank-statements/digitap/initiate [post]
func (h *BankStatementHandler) InitiateDigitap(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var in struct {
		ReturnURL string `json:"returnUrl"`
	}
	// Body is optional; an empty/absent body means "use the default return URL".
	_ = c.BodyParser(&in)
	res, err := h.svc.InitiateDigitap(c.Context(), accountID, strings.TrimSpace(in.ReturnURL))
	if err != nil {
		return err
	}
	return c.JSON(res)
}

// DigitapCallback godoc
//
// @Summary      Digitap transaction-complete webhook
// @Description  Public server-to-server endpoint called by Digitap once the user finishes (or cancels) the statement upload on its UI. Not for client use: there is no bearer auth — trust comes from the x-digitap-callback-type header and, when configured, a ?secret= shared-secret query param (the API defines no HMAC in v1.20). Always returns 200 quickly; the report fetch happens after the response.
// @Tags         bank-statements
// @Accept       json
// @Produce      json
// @Param        x-digitap-callback-type  header    string  true  "TRANSACTION_COMPLETE"
// @Param        secret                   query     string  false  "Shared secret, when callback-secret is configured"
// @Success      200  {object}  object  "{""ok"":true}"
// @Router       /bank-statements/digitap/callback [post]
func (h *BankStatementHandler) DigitapCallback(c *fiber.Ctx) error {
	// The doc warns this URL may carry other callback types in the future, so
	// check the header before processing.
	if c.Get("x-digitap-callback-type") != service.BankDataCallbackTransactionComplete {
		// Still 200 so Digitap doesn't retry an unsupported type.
		return c.JSON(fiber.Map{"ok": true, "ignored": true})
	}
	// Optional shared-secret guard. Off in dev (no secret configured); in prod
	// the URL registered with Digitap carries ?secret=<value> and we compare.
	if h.callbackSecret != "" && c.Query("secret") != h.callbackSecret {
		return apperr.NewUnauthorized("invalid callback secret")
	}

	var event service.BankDataCallbackEvent
	if err := c.BodyParser(&event); err != nil {
		// A malformed body is still acked with 200: we can't process it and a
		// 4xx would only make Digitap retry the same bad payload.
		return c.JSON(fiber.Map{"ok": false})
	}
	// Process inline for simplicity. The work is bounded (one status-check +
	// one retrieve-report); if it ever grows, hand off to the worker pool.
	if err := h.svc.HandleCallback(c.Context(), event); err != nil {
		// Log but still ack — a 5xx would trigger redelivery, which is fine
		// (HandleCallback is idempotent) but noisy. The poll fallback covers
		// the case where this never succeeds.
		slog.Warn("digitap callback handling failed",
			"request_id", event.RequestID, "txn_id", event.TxnID, "error", err)
	}
	return c.JSON(fiber.Map{"ok": true})
}
