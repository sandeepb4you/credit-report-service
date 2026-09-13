package handler

import (
	"errors"
	"strconv"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
	"github.com/gofiber/fiber/v2"
)

// ReportPDFLinkResponse carries a short-lived download URL for a report PDF.
type ReportPDFLinkResponse struct {
	// URL is presigned and expires. Not stored anywhere client-side: ask again
	// rather than keeping it, or a stale link becomes a confusing failure.
	URL string `json:"url" example:"https://myscorr-credit-reports.s3.ap-south-1.amazonaws.com/..."`
	// ExpiresInSeconds is how long the URL stays valid.
	ExpiresInSeconds int `json:"expiresInSeconds" example:"600"`
	// PasswordHint states how to open the file. Sent with the link because the
	// PDF is encrypted and a download nobody can open is not a download.
	PasswordHint string `json:"passwordHint"`
}

// ReportEmailResponse acknowledges a report emailed to the account's address.
type ReportEmailResponse struct {
	Message string `json:"message" example:"Your report is on its way"`
}

// GetReportPDFLink godoc
//
// @Summary      Get a download link for a report's PDF
// @Description  Returns a short-lived presigned URL for the caller's own report PDF, plus the rule for the password that opens it. The PDF is encrypted with the account holder's PAN and date of birth, so the link alone does not expose the report. A 404 means the report has no PDF yet — the relay that fetches it from the bureau is asynchronous and best-effort.
// @Tags         credit-analytics
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Report id"
// @Success      200  {object}  ReportPDFLinkResponse
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Report not found, or no PDF stored for it"
// @Failure      502  {object}  apperr.ErrorBody  "Could not prepare the download"
// @Failure      503  {object}  apperr.ErrorBody  "Report storage is not configured"
// @Router       /credit-analytics/reports/{id}/pdf [get]
func (h *CreditAnalyticsHandler) GetReportPDFLink(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}

	url, ttl, err := h.svc.ReportPDFLink(c.Context(), accountID, id)
	if err != nil {
		return err
	}
	return c.JSON(ReportPDFLinkResponse{
		URL:              url,
		ExpiresInSeconds: int(ttl.Seconds()),
		PasswordHint:     service.ReportPDFPasswordHint,
	})
}

// AdvancedReportLinkResponse carries a download URL for the myScorr Advanced
// Report. No password hint, unlike its bureau sibling: this document is written
// by myScorr and ships unencrypted by design.
type AdvancedReportLinkResponse struct {
	URL              string `json:"url"`
	ExpiresInSeconds int    `json:"expiresInSeconds" example:"600"`
}

// GetAdvancedReportLink godoc
//
// @Summary      Get a download link for the myScorr Advanced Report
// @Description  Renders the caller's own report as the myScorr Advanced Report — an eight-page PDF written by myScorr from the bureau data: score and band, the factor grades, open and closed accounts, the merged payment record and the improvement plan. Rendered on first request and kept, so a second call is just a new link. Unlike the bureau PDF it carries no password. 503 means no renderer or no storage is configured.
// @Tags         credit-analytics
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Report id"
// @Success      200  {object}  AdvancedReportLinkResponse
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Report not found"
// @Failure      502  {object}  apperr.ErrorBody  "Could not prepare the report"
// @Failure      503  {object}  apperr.ErrorBody  "The advanced report is not available"
// @Router       /credit-analytics/reports/{id}/advanced-pdf [get]
func (h *CreditAnalyticsHandler) GetAdvancedReportLink(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}

	url, ttl, err := h.svc.AdvancedReportLink(c.Context(), accountID, id)
	if err != nil {
		return err
	}
	return c.JSON(AdvancedReportLinkResponse{
		URL:              url,
		ExpiresInSeconds: int(ttl.Seconds()),
	})
}

// EmailAdvancedReport godoc
//
// @Summary      Email the myScorr Advanced Report
// @Description  Renders the caller's own report as the myScorr Advanced Report and sends it to the address on their account. Returns 409 when the account has no email, like the bureau report's own email route, so the client can offer to link one. The attachment carries no password.
// @Tags         credit-analytics
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Report id"
// @Success      200  {object}  ReportEmailResponse
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Report not found"
// @Failure      409  {object}  apperr.ErrorBody  "No email address on the account — link one first"
// @Failure      502  {object}  apperr.ErrorBody  "Could not send the email"
// @Failure      503  {object}  apperr.ErrorBody  "The advanced report or email delivery is not available"
// @Router       /credit-analytics/reports/{id}/advanced-pdf/email [post]
func (h *CreditAnalyticsHandler) EmailAdvancedReport(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}

	switch err := h.svc.EmailAdvancedReport(c.Context(), accountID, id); {
	case err == nil:
		return c.JSON(ReportEmailResponse{Message: "Your advanced report is on its way"})
	case errors.Is(err, service.ErrReportEmailMissing):
		return apperr.NewConflict("Add an email address to your account to have the report sent to you.")
	default:
		return err
	}
}

// EmailReportPDF godoc
//
// @Summary      Email a report's PDF to the account's address
// @Description  Sends the caller's own report PDF, encrypted, to the email address on their account. Returns 409 when the account has no email — a phone signup that never linked one — so the client can offer to link an address rather than reporting a dead end.
// @Tags         credit-analytics
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      int  true  "Report id"
// @Success      200  {object}  ReportEmailResponse
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Report not found, or no PDF stored for it"
// @Failure      409  {object}  apperr.ErrorBody  "No email address on the account — link one first"
// @Failure      502  {object}  apperr.ErrorBody  "Could not send the email"
// @Failure      503  {object}  apperr.ErrorBody  "Report storage or email delivery is not configured"
// @Router       /credit-analytics/reports/{id}/email [post]
func (h *CreditAnalyticsHandler) EmailReportPDF(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}

	switch err := h.svc.EmailReportPDF(c.Context(), accountID, id); {
	case err == nil:
		return c.JSON(ReportEmailResponse{Message: "Your report is on its way"})
	case errors.Is(err, service.ErrReportEmailMissing):
		// 409, not 400: nothing about the request is malformed, and there is
		// nothing on this screen for the user to correct. The client reads this
		// status as "send them to link an email", which is the only useful
		// next step.
		return apperr.NewConflict("Add an email address to your account to have the report sent to you.")
	default:
		return err
	}
}
