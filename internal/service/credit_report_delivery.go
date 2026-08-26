// Package service — handing a stored credit-report PDF to its owner.
//
// The relay (credit_report_pdf.go) has already downloaded the PDF from Digitap,
// encrypted it with the holder's PAN + date of birth, and put it in a private
// bucket. This file is the read side: a short-lived download link, or the file
// itself as an email attachment.
//
// Both paths go through findOwnedReport, so a report can only ever be handed to
// the account it belongs to. The object key is derivable from account and report
// id, which is exactly why the bucket is private and reads are presigned — the
// key is not a secret and must not be treated as one.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"credit-report-service/internal/apperr"
)

// reportPDFStore is the read side of the object store.
type reportPDFStore interface {
	PresignGet(ctx context.Context, keyOrURI string) (string, time.Duration, error)
	Download(ctx context.Context, keyOrURI string) ([]byte, error)
	IsStub() bool
}

// ErrReportEmailMissing signals that the account has no email address, so the
// report cannot be sent anywhere.
//
// A distinct error rather than a generic 400 because the client acts on it: the
// app routes the user into the email-linking flow instead of showing a dead end.
// The address is genuinely absent (a phone signup that never linked one), not
// wrong, so there is nothing for them to correct on the delivery screen.
var ErrReportEmailMissing = errors.New("account has no email address")

// SetReportPDFStore wires the read side. Optional: with no store configured the
// delivery endpoints report the PDF unavailable rather than failing at boot,
// matching how the rest of the service treats an unconfigured upstream.
func (s *CreditAnalyticsService) SetReportPDFStore(store reportPDFStore) {
	s.pdfStore = store
}

// ReportPDFLink returns a time-limited download URL for a report's PDF.
//
// A presigned link rather than proxying the bytes: the file is a few hundred KB
// to a few MB, and streaming every download through the API would spend request
// capacity on something S3 does better. The link expires, so it is also less
// dangerous than a permanent URL if it ends up in a browser history or a chat.
func (s *CreditAnalyticsService) ReportPDFLink(
	ctx context.Context, accountID, reportID int64,
) (string, time.Duration, error) {
	uri, err := s.reportPDFURI(ctx, accountID, reportID)
	if err != nil {
		return "", 0, err
	}
	url, ttl, err := s.pdfStore.PresignGet(ctx, uri)
	if err != nil {
		slog.Error("credit-report pdf presign failed",
			"account_id", accountID, "report_id", reportID, "error", err)
		return "", 0, apperr.NewBadGateway("Could not prepare the download. Please try again.")
	}
	return url, ttl, nil
}

// EmailReportPDF sends the report to the address on the account.
//
// Returns [ErrReportEmailMissing] when there is none, so the caller can send the
// user to link one rather than reporting a failure they cannot act on.
func (s *CreditAnalyticsService) EmailReportPDF(
	ctx context.Context, accountID, reportID int64,
) error {
	acc, err := s.accounts.FindByID(ctx, accountID)
	if err != nil {
		return apperr.NewNotFound("Account not found")
	}
	if acc.PrimaryEmail == nil || *acc.PrimaryEmail == "" {
		return ErrReportEmailMissing
	}

	uri, err := s.reportPDFURI(ctx, accountID, reportID)
	if err != nil {
		return err
	}
	pdf, err := s.pdfStore.Download(ctx, uri)
	if err != nil {
		slog.Error("credit-report pdf fetch for email failed",
			"account_id", accountID, "report_id", reportID, "error", err)
		return apperr.NewBadGateway("Could not read the report. Please try again.")
	}
	if s.mailer == nil {
		return apperr.NewServiceUnavailable("Email delivery is not configured.")
	}

	// Attached as-is: it was encrypted before it was ever stored, so the file
	// leaving here is already protected and no key of ours can open it.
	filename := fmt.Sprintf("myscorr-credit-report-%d.pdf", reportID)
	if err := s.mailer.SendCreditReport(*acc.PrimaryEmail, filename, pdf); err != nil {
		return apperr.NewBadGateway("Could not send the email. Please try again.")
	}
	return nil
}

// reportPDFURI resolves a report the caller owns to its stored object URI.
//
// The distinction between "no PDF for this report" and "no store configured"
// matters to the user: the first is a report whose relay failed or has not run,
// the second is our deployment. Both surface as unavailable, but only one is
// worth retrying.
func (s *CreditAnalyticsService) reportPDFURI(
	ctx context.Context, accountID, reportID int64,
) (string, error) {
	if s.pdfStore == nil || s.pdfStore.IsStub() {
		return "", apperr.NewServiceUnavailable("Report downloads are not available right now.")
	}
	row, err := s.findOwnedReport(ctx, accountID, reportID)
	if err != nil {
		return "", err
	}
	if row.ResultPDFURL == nil || *row.ResultPDFURL == "" {
		// The relay is best-effort and asynchronous, so a very recent report may
		// simply not have one yet — and one whose PAN or date of birth was
		// missing never will, because an unprotectable report is not stored.
		return "", apperr.NewNotFound("No PDF is available for this report yet.")
	}
	return *row.ResultPDFURL, nil
}
