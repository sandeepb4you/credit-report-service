package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/push"
	"credit-report-service/internal/repository"
)

// loanAssistStore is the slice of the object store Loan Assist uses
// (s3store.Client in production, an in-memory bucket in tests).
type loanAssistStore interface {
	IsStub() bool
	UploadAs(ctx context.Context, key, filename, contentType string, body []byte) (string, error)
	PresignGet(ctx context.Context, keyOrURI string) (string, time.Duration, error)
	Delete(ctx context.Context, keyOrURI string) error
}

// loanAssistMailer tells the team about a paid application.
type loanAssistMailer interface {
	SendLoanAssistRequest(in LoanAssistTeamMail) error
}

// loanAssistPusher reaches the customer's devices. *push.Sender in production.
type loanAssistPusher interface {
	NotifyAccount(ctx context.Context, accountID int64, n push.Notification)
}

// LoanAssistConsentText is the sentence the customer ticks before paying,
// stored with the timestamp on every submit. The server's copy, not the app's:
// what is recorded as agreed must not depend on what a client chose to send.
const LoanAssistConsentText = "I agree to share these documents with myScorr and its loan experts " +
	"to evaluate my profile. I understand a loan is not guaranteed."

// Bounds on what the customer types as the amount they need, in rupees.
const (
	loanAssistMinAmount = 10_000
	loanAssistMaxAmount = 100_00_00_000 // ₹100 crore
)

// LoanAssistService runs Loan Assist (design/myscorr-loan-assist-customer-screens.html):
// applications, their documents, payment through the ordinary order flow, and
// the partners who review and call.
type LoanAssistService struct {
	repo     *repository.LoanAssistRepo
	accounts *repository.AccountRepo
	orders   *OrderService
	docs     loanAssistStore
	mailer   loanAssistMailer
	pusher   loanAssistPusher
	// notify is who hears about a paid application; consoleURL is where the
	// mail sends them. Empty notify = logged loudly, not mailed.
	notify     []string
	consoleURL string
	now        func() time.Time
}

func NewLoanAssistService(
	repo *repository.LoanAssistRepo, accounts *repository.AccountRepo, orders *OrderService,
	docs loanAssistStore, mailer loanAssistMailer, notify []string, consoleURL string,
) *LoanAssistService {
	return &LoanAssistService{
		repo: repo, accounts: accounts, orders: orders, docs: docs, mailer: mailer,
		notify: notify, consoleURL: consoleURL, now: time.Now,
	}
}

// SetPushSender wires the customer notifications (documents asked for again,
// status changes). Optional: nil sends nothing.
func (s *LoanAssistService) SetPushSender(p loanAssistPusher) { s.pusher = p }

// ---- views ------------------------------------------------------------------

// LoanAssistState is GET /loan-assist: everything the Assist tab draws.
type LoanAssistState struct {
	Product      LoanAssistProduct   `json:"product"`
	ConsentText  string              `json:"consentText"`
	LoanTypes    []models.LoanType   `json:"loanTypes"`
	Draft        *LoanAssistDraft    `json:"draft,omitempty"`
	Applications []LoanAssistSummary `json:"applications"`
	// AttentionCount is how many documents the customer must upload again,
	// across every application: the Assist tab's badge.
	AttentionCount int `json:"attentionCount"`
	// SavedDocuments are the documents partners accepted in earlier
	// applications, which a new one may reuse.
	SavedDocuments []models.SavedLoanDocument `json:"savedDocuments"`
}

type LoanAssistProduct struct {
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	// Available is false when the product is switched off in the catalog or
	// documents cannot be stored: the screens then say so instead of taking
	// documents.
	Available bool `json:"available"`
}

// LoanAssistDraft is the open application, as every draft-changing call returns it.
type LoanAssistDraft struct {
	LoanType     string                      `json:"loanType"`
	AmountNeeded int64                       `json:"amountNeeded"`
	Rented       bool                        `json:"rented"`
	Documents    []models.LoanAssistDocument `json:"documents"`
	// Missing is the slots still needed before it can be paid for.
	Missing []string `json:"missing"`
}

// LoanAssistSummary is one card of the Assist tab's list.
type LoanAssistSummary struct {
	RequestCode  string    `json:"requestCode"`
	LoanType     string    `json:"loanType"`
	LoanTypeName string    `json:"loanTypeName"`
	AmountNeeded int64     `json:"amountNeeded"`
	Status       string    `json:"status"`
	PaidAt       time.Time `json:"paidAt"`
	AmountPaid   float64   `json:"amountPaid"`
	// DocumentCount counts documents the way the screens do: the two sides of
	// the Aadhaar card are one.
	DocumentCount int `json:"documentCount"`
	// AttentionCount is how many documents must be uploaded again;
	// InReviewCount how many wait for the partner.
	AttentionCount int `json:"attentionCount"`
	InReviewCount  int `json:"inReviewCount"`
	// Expert is the partner who will call, once the documents are verified.
	Expert *models.PartnerCard `json:"expert,omitempty"`
}

// LoanAssistDetail is one application's page.
type LoanAssistDetail struct {
	LoanAssistSummary
	Rented            bool                        `json:"rented"`
	Documents         []models.LoanAssistDocument `json:"documents"`
	DocsVerifiedAt    *time.Time                  `json:"docsVerifiedAt,omitempty"`
	ExpertContactedAt *time.Time                  `json:"expertContactedAt,omitempty"`
	GuidanceAt        *time.Time                  `json:"guidanceAt,omitempty"`
	ClosedAt          *time.Time                  `json:"closedAt,omitempty"`
}

// ---- customer ---------------------------------------------------------------

// State answers GET /loan-assist.
func (s *LoanAssistService) State(ctx context.Context, accountID int64) (*LoanAssistState, error) {
	p, err := s.orders.FindService(ctx, models.ProductLoanAssist)
	if err != nil {
		return nil, err
	}
	st := &LoanAssistState{
		Product: LoanAssistProduct{
			Code: p.Code, Name: p.Name, Amount: p.Amount, Currency: p.Currency,
			Available: p.Active && s.storageReady(),
		},
		ConsentText:  LoanAssistConsentText,
		LoanTypes:    models.LoanTypes,
		Applications: []LoanAssistSummary{},
	}

	draft, err := s.repo.FindDraft(ctx, accountID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
	case err != nil:
		return nil, err
	default:
		if st.Draft, err = s.draftOf(ctx, draft); err != nil {
			return nil, err
		}
	}

	apps, err := s.repo.ListSubmitted(ctx, accountID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(apps))
	for i := range apps {
		ids[i] = apps[i].ID
	}
	docs, err := s.repo.DocumentsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	byReq := map[int64][]models.LoanAssistDocument{}
	for _, d := range docs {
		byReq[d.RequestID] = append(byReq[d.RequestID], d)
	}
	for i := range apps {
		sum, err := s.summaryOf(ctx, &apps[i], byReq[apps[i].ID])
		if err != nil {
			return nil, err
		}
		st.AttentionCount += sum.AttentionCount
		st.Applications = append(st.Applications, *sum)
	}

	if st.SavedDocuments, err = s.savedDocuments(ctx, accountID, nil); err != nil {
		return nil, err
	}
	return st, nil
}

// savedDocuments lists reusable documents, marking each stale where the loan
// type bounds its age. With lt nil the bound is read from the slot's rule in
// any loan type that has one.
func (s *LoanAssistService) savedDocuments(
	ctx context.Context, accountID int64, lt *models.LoanType,
) ([]models.SavedLoanDocument, error) {
	saved, err := s.repo.SavedDocuments(ctx, accountID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for i := range saved {
		maxAge := 0
		if lt != nil {
			if r := lt.Rule(saved[i].Slot); r != nil {
				maxAge = r.MaxAgeDays
			}
		} else {
			for j := range models.LoanTypes {
				if r := models.LoanTypes[j].Rule(saved[i].Slot); r != nil && r.MaxAgeDays > 0 {
					maxAge = r.MaxAgeDays
				}
			}
		}
		saved[i].Stale = maxAge > 0 && now.Sub(saved[i].UploadedAt) > time.Duration(maxAge)*24*time.Hour
	}
	return saved, nil
}

func (s *LoanAssistService) draftOf(ctx context.Context, r *models.LoanAssistRequest) (*LoanAssistDraft, error) {
	docs, err := s.repo.Documents(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, d := range docs {
		have[d.Slot] = true
	}
	missing := []string{}
	if lt := models.FindLoanType(r.LoanType); lt != nil {
		for _, slot := range lt.RequiredSlots(r.Rented) {
			if !have[slot] {
				missing = append(missing, slot)
			}
		}
	}
	return &LoanAssistDraft{
		LoanType: r.LoanType, AmountNeeded: r.AmountNeeded, Rented: r.Rented,
		Documents: docs, Missing: missing,
	}, nil
}

func (s *LoanAssistService) summaryOf(
	ctx context.Context, r *models.LoanAssistRequest, docs []models.LoanAssistDocument,
) (*LoanAssistSummary, error) {
	lt := models.FindLoanType(r.LoanType)
	sum := &LoanAssistSummary{
		RequestCode: r.Code(), LoanType: r.LoanType, AmountNeeded: r.AmountNeeded, Status: r.Status,
		DocumentCount: groupCount(lt, docs, func(models.LoanAssistDocument) bool { return true }),
		AttentionCount: groupCount(lt, docs, func(d models.LoanAssistDocument) bool {
			return d.ReviewState == models.LoanDocRetake
		}),
		InReviewCount: groupCount(lt, docs, func(d models.LoanAssistDocument) bool {
			return d.ReviewState == models.LoanDocPending
		}),
	}
	if lt != nil {
		sum.LoanTypeName = lt.Name
	}
	if r.PaidAt != nil {
		sum.PaidAt = *r.PaidAt
	}
	if r.OrderID != nil {
		amount, err := s.repo.OrderAmount(ctx, *r.OrderID)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
		sum.AmountPaid = amount
	}
	// The expert is introduced once the documents are verified — before that
	// the partner is checking files, and "your expert will call" would promise
	// a call that is not yet due.
	if r.AssignedPartnerID != nil && r.Status != models.LoanAssistSubmitted {
		if card, err := s.repo.PartnerCard(ctx, *r.AssignedPartnerID); err == nil {
			sum.Expert = card
		}
	}
	return sum, nil
}

// groupCount counts documents matching keep, with slots of one group (the
// Aadhaar card's two sides) counted once.
func groupCount(lt *models.LoanType, docs []models.LoanAssistDocument, keep func(models.LoanAssistDocument) bool) int {
	seen := map[string]bool{}
	for _, d := range docs {
		if !keep(d) {
			continue
		}
		group := d.Slot
		if lt != nil {
			if r := lt.Rule(d.Slot); r != nil && r.Group != "" {
				group = r.Group
			}
		}
		seen[group] = true
	}
	return len(seen)
}

func (s *LoanAssistService) storageReady() bool { return s.docs != nil && !s.docs.IsStub() }

// UpdateDraft starts or changes the open application: its loan type, the
// amount needed and whether the premises is rented. Documents the new loan
// type or premises no longer need are taken out (and deleted unless another
// application uses them) — a document nobody asked for is not one to keep.
func (s *LoanAssistService) UpdateDraft(
	ctx context.Context, accountID int64, loanType *string, amount *int64, rented *bool,
) (*LoanAssistDraft, error) {
	if loanType != nil {
		code := strings.ToUpper(strings.TrimSpace(*loanType))
		lt := models.FindLoanType(code)
		if lt == nil || !lt.Active {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"loanType": "this loan type is not available yet"})
		}
		loanType = &code
	}
	if amount != nil && (*amount < loanAssistMinAmount || *amount > loanAssistMaxAmount) {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"amountNeeded": "enter an amount between ₹10,000 and ₹100 crore"})
	}
	startType := models.LoanTypeBusiness
	if loanType != nil {
		startType = *loanType
	}
	draft, err := s.repo.EnsureDraft(ctx, accountID, startType)
	if err != nil {
		return nil, err
	}
	if err := s.repo.UpdateDraft(ctx, draft.ID, loanType, amount, rented); err != nil {
		return nil, err
	}
	if draft, err = s.repo.FindDraft(ctx, accountID); err != nil {
		return nil, err
	}
	if err := s.pruneDraft(ctx, draft); err != nil {
		return nil, err
	}
	return s.draftOf(ctx, draft)
}

// pruneDraft removes documents the draft's loan type and premises no longer need.
func (s *LoanAssistService) pruneDraft(ctx context.Context, draft *models.LoanAssistRequest) error {
	lt := models.FindLoanType(draft.LoanType)
	keep := map[string]bool{}
	if lt != nil {
		for _, slot := range lt.RequiredSlots(draft.Rented) {
			keep[slot] = true
		}
	}
	docs, err := s.repo.Documents(ctx, draft.ID)
	if err != nil {
		return err
	}
	var drop []string
	for _, d := range docs {
		if !keep[d.Slot] {
			drop = append(drop, d.Slot)
		}
	}
	uris, err := s.repo.RemoveSlots(ctx, draft.ID, drop)
	if err != nil {
		return err
	}
	for _, u := range uris {
		s.deleteObject(ctx, u, draft.AccountID)
	}
	return nil
}

// ReuseSaved fills the open application's slots with documents a partner
// accepted in an earlier application — linked, not copied. Only slots the
// loan type needs, and only copies young enough for its rules.
func (s *LoanAssistService) ReuseSaved(ctx context.Context, accountID int64, slots []string) (*LoanAssistDraft, error) {
	draft, err := s.repo.FindDraft(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Start a new application first")
	}
	if err != nil {
		return nil, err
	}
	lt := models.FindLoanType(draft.LoanType)
	if lt == nil {
		return nil, apperr.NewValidation("This application has no loan type")
	}
	saved, err := s.savedDocuments(ctx, accountID, lt)
	if err != nil {
		return nil, err
	}
	bySlot := map[string]models.SavedLoanDocument{}
	for _, d := range saved {
		bySlot[d.Slot] = d
	}
	required := map[string]bool{}
	for _, slot := range lt.RequiredSlots(draft.Rented) {
		required[slot] = true
	}
	for _, raw := range slots {
		slot := strings.ToUpper(strings.TrimSpace(raw))
		d, ok := bySlot[slot]
		if !ok || !required[slot] {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"slots": "no saved document can be used for " + slot})
		}
		if d.Stale {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"slots": "the saved " + strings.ToLower(lt.Rule(slot).Title) + " is too old; upload a new one"})
		}
		uri, err := s.repo.LinkDocument(ctx, draft.ID, slot, d)
		if err != nil {
			return nil, err
		}
		if uri != "" {
			s.deleteObject(ctx, uri, accountID)
		}
	}
	return s.draftOf(ctx, draft)
}

// UploadDocument fills (or replaces) one slot of the open application.
func (s *LoanAssistService) UploadDocument(
	ctx context.Context, accountID int64, slot, filename, mimeType string, data []byte,
) (*LoanAssistDraft, error) {
	draft, err := s.repo.FindDraft(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Start a new application first")
	}
	if err != nil {
		return nil, err
	}
	slot, err = s.storeIntoSlot(ctx, draft, slot, filename, mimeType, data, false)
	if err != nil {
		return nil, err
	}
	return s.draftOf(ctx, draft)
}

// storeIntoSlot checks and stores one upload into an application's slot.
func (s *LoanAssistService) storeIntoSlot(
	ctx context.Context, req *models.LoanAssistRequest, slot, filename, mimeType string,
	data []byte, resubmission bool,
) (string, error) {
	if !s.storageReady() {
		return "", apperr.NewServiceUnavailable("Document storage is not configured on this server")
	}
	slot = strings.ToUpper(strings.TrimSpace(slot))
	lt := models.FindLoanType(req.LoanType)
	if lt == nil || lt.Rule(slot) == nil {
		return "", apperr.NewValidationWith("Validation failed",
			map[string]string{"slot": "this loan type does not ask for that document"})
	}
	// The bytes decide, not the name or the header a client chose: these files
	// are opened by people, and a renamed executable is not a photo of a board.
	sniffed := sniffDocumentType(data)
	if sniffed == "" {
		return "", apperr.NewValidation("only JPG, PNG or PDF files are supported")
	}
	mimeType = sniffed
	name := cleanFileName(filename, slot, mimeType)
	// A random part, not only the time: a replacement uploaded in the same
	// millisecond would otherwise get the old object's key, overwrite it, and
	// then be deleted as "the file it replaced".
	key := fmt.Sprintf("documents/%d/loan-assist-%d-%s-%d-%s%s", req.AccountID, req.ID,
		strings.ToLower(slot), s.now().UnixMilli(), uuid.NewString()[:8], documentExt(filename, mimeType))
	uri, err := s.docs.UploadAs(ctx, key, name, mimeType, data)
	if err != nil {
		return "", fmt.Errorf("store loan assist document: %w", err)
	}
	size := int64(len(data))
	replaced, err := s.repo.PutDocument(ctx, req.ID, slot, &models.Document{
		AccountID: req.AccountID,
		DocType:   models.DocTypeLoanAssistPrefix + slot,
		S3URI:     uri, FileName: name, MimeType: mimeType, SizeBytes: &size,
	}, resubmission)
	if err != nil {
		s.deleteObject(ctx, uri, req.AccountID)
		return "", err
	}
	if replaced != "" {
		s.deleteObject(ctx, replaced, req.AccountID)
	}
	return slot, nil
}

// RemoveDocument empties a slot of the open application.
func (s *LoanAssistService) RemoveDocument(ctx context.Context, accountID int64, slot string) (*LoanAssistDraft, error) {
	draft, err := s.repo.FindDraft(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("No Loan Assist application in progress")
	}
	if err != nil {
		return nil, err
	}
	uris, err := s.repo.RemoveSlots(ctx, draft.ID, []string{strings.ToUpper(strings.TrimSpace(slot))})
	if err != nil {
		return nil, err
	}
	for _, u := range uris {
		s.deleteObject(ctx, u, accountID)
	}
	return s.draftOf(ctx, draft)
}

// Submit checks the open application is complete and agreed to, records the
// consent, and opens the order. Its fulfilment (OnPaid) turns the draft into
// a submitted application.
func (s *LoanAssistService) Submit(
	ctx context.Context, accountID int64, consent bool, couponCode, testKey string,
) (*PurchaseResult, error) {
	if !consent {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"consent": "agree to share your documents to continue"})
	}
	draft, err := s.repo.FindDraft(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewValidation("Start a new application first")
	}
	if err != nil {
		return nil, err
	}
	if lt := models.FindLoanType(draft.LoanType); lt == nil || !lt.Active {
		return nil, apperr.NewValidation("This loan type is not available yet")
	}
	if draft.AmountNeeded < loanAssistMinAmount {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"amountNeeded": "tell us how much you need"})
	}
	d, err := s.draftOf(ctx, draft)
	if err != nil {
		return nil, err
	}
	if len(d.Missing) > 0 {
		return nil, apperr.NewValidationWith("Some documents are still missing",
			map[string]string{"documents": strings.Join(d.Missing, ",")})
	}
	if err := s.repo.RecordConsent(ctx, draft.ID, LoanAssistConsentText, s.now().UTC()); err != nil {
		return nil, err
	}
	return s.orders.CreateServiceOrder(ctx, accountID, models.ProductLoanAssist, couponCode, testKey)
}

// Application answers GET /loan-assist/applications/{code}.
func (s *LoanAssistService) Application(ctx context.Context, accountID int64, code string) (*LoanAssistDetail, error) {
	req, err := s.repo.FindByCode(ctx, accountID, strings.ToUpper(strings.TrimSpace(code)))
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Application not found")
	}
	if err != nil {
		return nil, err
	}
	return s.detailOf(ctx, req)
}

func (s *LoanAssistService) detailOf(ctx context.Context, req *models.LoanAssistRequest) (*LoanAssistDetail, error) {
	docs, err := s.repo.Documents(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	// Who asked for a document again, as the customer sees them.
	cards := map[int64]*models.PartnerCard{}
	for i := range docs {
		by := docs[i].ReviewedBy
		if by == nil || docs[i].ReviewState != models.LoanDocRetake {
			continue
		}
		if _, ok := cards[*by]; !ok {
			card, err := s.repo.PartnerCard(ctx, *by)
			if err == nil {
				card.CallPhone = nil // the Call button belongs to the assigned expert
			}
			cards[*by] = card
		}
		docs[i].Reviewer = cards[*by]
	}
	sum, err := s.summaryOf(ctx, req, docs)
	if err != nil {
		return nil, err
	}
	return &LoanAssistDetail{
		LoanAssistSummary: *sum, Rented: req.Rented, Documents: docs,
		DocsVerifiedAt: req.DocsVerifiedAt, ExpertContactedAt: req.ExpertContactedAt,
		GuidanceAt: req.GuidanceAt, ClosedAt: req.ClosedAt,
	}, nil
}

// Resubmit takes a new copy of a document the partner asked for again. It
// goes back into review and is stamped "sent again".
func (s *LoanAssistService) Resubmit(
	ctx context.Context, accountID int64, code, slot, filename, mimeType string, data []byte,
) (*LoanAssistDetail, error) {
	req, err := s.repo.FindByCode(ctx, accountID, strings.ToUpper(strings.TrimSpace(code)))
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Application not found")
	}
	if err != nil {
		return nil, err
	}
	slot = strings.ToUpper(strings.TrimSpace(slot))
	docs, err := s.repo.Documents(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	asked := false
	for _, d := range docs {
		if d.Slot == slot && d.ReviewState == models.LoanDocRetake {
			asked = true
		}
	}
	if !asked {
		return nil, apperr.NewConflict("This document was not asked for again")
	}
	if _, err := s.storeIntoSlot(ctx, req, slot, filename, mimeType, data, true); err != nil {
		return nil, err
	}
	return s.detailOf(ctx, req)
}

// ---- fulfilment ---------------------------------------------------------------

// OnPaid is the LOAN_ASSIST fulfilment: the paid order turns the account's
// draft into a submitted application with a code, and the team is told.
// Idempotent — it runs on the first PAID transition and on every read of the
// paid order.
func (s *LoanAssistService) OnPaid(ctx context.Context, order *models.Order) {
	req, err := s.repo.FindByOrder(ctx, order.ID)
	if errors.Is(err, repository.ErrNotFound) {
		paidAt := s.now().UTC()
		if order.PaidAt != nil {
			paidAt = *order.PaidAt
		}
		req, err = s.repo.MarkPaid(ctx, order.AccountID, order.ID, paidAt)
		if errors.Is(err, repository.ErrNotFound) {
			// A concurrent fulfilment may have claimed the draft for this order.
			req, err = s.repo.FindByOrder(ctx, order.ID)
		}
		if errors.Is(err, repository.ErrNotFound) {
			// Paid with no draft to attach to (two checkouts in flight): real
			// money, so a person must refund or attach it.
			slog.Error("loan assist: order PAID but the account has no draft application; refund or attach by hand",
				"order_uid", order.OrderUID, "account_id", order.AccountID)
			return
		}
	}
	if err != nil {
		slog.Error("loan assist: fulfilment failed; retried on the next order read",
			"order_uid", order.OrderUID, "account_id", order.AccountID, "error", err)
		return
	}
	if req.TeamNotifiedAt == nil {
		s.notifyTeam(ctx, req, order)
	}
}

func (s *LoanAssistService) notifyTeam(ctx context.Context, req *models.LoanAssistRequest, order *models.Order) {
	code := req.Code()
	if len(s.notify) == 0 || s.mailer == nil {
		slog.Error("loan assist: application paid but loan-assist.notify-emails is empty; nobody was told",
			"request_code", code, "account_id", req.AccountID)
		return
	}
	docs, err := s.repo.Documents(ctx, req.ID)
	if err != nil {
		slog.Error("loan assist: could not list documents for the team mail", "request_code", code, "error", err)
		return
	}
	lt := models.FindLoanType(req.LoanType)
	labels := make([]string, 0, len(docs))
	for _, d := range docs {
		labels = append(labels, slotLabel(lt, d.Slot))
	}
	m := LoanAssistTeamMail{
		To: s.notify, RequestCode: code, Rented: req.Rented, Documents: labels,
		AmountPaid: inrExact(toPaise(order.Amount)), ConsoleURL: s.consoleURL,
		Loan: fmt.Sprintf("%s · %s", loanTypeName(lt), inr(req.AmountNeeded*100)),
	}
	if req.PaidAt != nil {
		m.PaidAt = req.PaidAt.In(istZone).Format("2 Jan 2006, 3:04 pm") + " IST"
	}
	if acct, err := s.accounts.FindByID(ctx, req.AccountID); err == nil {
		m.Customer = customerName(acct)
		m.Phone = strDeref(acct.PrimaryPhone)
		m.Email = strDeref(acct.PrimaryEmail)
	}
	if err := s.mailer.SendLoanAssistRequest(m); err != nil {
		return // logged by the mailer; the next order read retries
	}
	if err := s.repo.MarkTeamNotified(ctx, req.ID); err != nil {
		slog.Warn("loan assist: team notified but not recorded; may notify twice", "request_code", code, "error", err)
	}
}

// ---- partner review -----------------------------------------------------------

// Reviewer is who is calling a review route.
type Reviewer struct {
	AccountID int64
	// Admin holds loan-assist:manage: every application, every document,
	// assigning anyone.
	Admin bool
}

func (rv Reviewer) canOpen(app *models.ReviewApplication) bool {
	return rv.Admin || (app.AssignedPartnerID != nil && *app.AssignedPartnerID == rv.AccountID)
}

// ReviewList is GET /loan-assist/review.
func (s *LoanAssistService) ReviewList(
	ctx context.Context, rv Reviewer, scope, status string, limit, offset int,
) ([]models.ReviewApplication, int, error) {
	switch scope {
	case repository.ReviewScopeMine, repository.ReviewScopeUnassigned:
	case repository.ReviewScopeAll, "":
		// A partner's widest view is its own plus the unassigned; "all" is an
		// admin's, and a partner asking for it gets their own.
		if !rv.Admin {
			scope = repository.ReviewScopeMine
		} else if scope == "" {
			scope = repository.ReviewScopeAll
		}
	default:
		return nil, 0, apperr.NewValidationWith("Validation failed",
			map[string]string{"scope": "scope must be mine, unassigned or all"})
	}
	status = strings.ToUpper(strings.TrimSpace(status))
	if status != "" && !models.IsLoanAssistStage(status) {
		return nil, 0, apperr.NewValidationWith("Validation failed",
			map[string]string{"status": "unknown status"})
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, total, err := s.repo.ListForReview(ctx, repository.ReviewFilter{
		Scope: scope, PartnerID: rv.AccountID, Status: status, Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		s.redact(rv, &rows[i])
	}
	return rows, total, nil
}

// redact marks what the caller may open, and masks the customer's contact
// details on an application that is not theirs to work.
func (s *LoanAssistService) redact(rv Reviewer, app *models.ReviewApplication) {
	app.CanOpen = rv.canOpen(app)
	if app.CanOpen {
		return
	}
	if app.Phone != nil {
		m := maskPhone(*app.Phone)
		app.Phone = &m
	}
	if app.Email != nil {
		m := maskEmail(*app.Email)
		app.Email = &m
	}
}

// ReviewDetail is GET /loan-assist/review/{id}.
func (s *LoanAssistService) ReviewDetail(ctx context.Context, rv Reviewer, id int64) (*models.ReviewApplication, error) {
	app, err := s.reviewApp(ctx, rv, id)
	if err != nil {
		return nil, err
	}
	if app.Documents, err = s.repo.Documents(ctx, id); err != nil {
		return nil, err
	}
	s.redact(rv, app)
	return app, nil
}

// reviewApp loads an application the caller may at least see: theirs, an
// unassigned one, or any for an admin. Another partner's reads as missing.
func (s *LoanAssistService) reviewApp(ctx context.Context, rv Reviewer, id int64) (*models.ReviewApplication, error) {
	app, err := s.repo.FindForReview(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Application not found")
	}
	if err != nil {
		return nil, err
	}
	if !rv.Admin && app.AssignedPartnerID != nil && *app.AssignedPartnerID != rv.AccountID {
		return nil, apperr.NewNotFound("Application not found")
	}
	return app, nil
}

// workableApp is reviewApp for a write: the caller must be the assigned
// partner (or an admin). An unassigned application is taken first.
func (s *LoanAssistService) workableApp(ctx context.Context, rv Reviewer, id int64) (*models.ReviewApplication, error) {
	app, err := s.reviewApp(ctx, rv, id)
	if err != nil {
		return nil, err
	}
	if !rv.canOpen(app) {
		return nil, apperr.NewForbidden("Assign this application to yourself to work on it")
	}
	return app, nil
}

// DocumentLink presigns one document for the assigned partner or an admin.
func (s *LoanAssistService) DocumentLink(
	ctx context.Context, rv Reviewer, id int64, slot string,
) (string, time.Duration, *models.Document, error) {
	if !s.storageReady() {
		return "", 0, nil, apperr.NewServiceUnavailable("Document storage is not configured on this server")
	}
	if _, err := s.workableApp(ctx, rv, id); err != nil {
		return "", 0, nil, err
	}
	doc, err := s.repo.TeamDocument(ctx, id, strings.ToUpper(strings.TrimSpace(slot)))
	if errors.Is(err, repository.ErrNotFound) {
		return "", 0, nil, apperr.NewNotFound("Document not found")
	}
	if err != nil {
		return "", 0, nil, err
	}
	url, ttl, err := s.docs.PresignGet(ctx, doc.S3URI)
	if err != nil {
		return "", 0, nil, err
	}
	// Every open of an identity document is logged with who opened it — the
	// access record UIDAI's storage rules expect for Aadhaar copies.
	slog.Info("loan assist document opened",
		"reviewer_id", rv.AccountID, "application_id", id, "document_id", doc.ID,
		"doc_type", doc.DocType, "account_id", doc.AccountID)
	return url, ttl, doc, nil
}

// ReviewDocument accepts a document, or asks the customer for it again with a
// note they will read. Asking tells the customer's phone.
func (s *LoanAssistService) ReviewDocument(
	ctx context.Context, rv Reviewer, id int64, slot, decision, note string,
) (*models.ReviewApplication, error) {
	app, err := s.workableApp(ctx, rv, id)
	if err != nil {
		return nil, err
	}
	slot = strings.ToUpper(strings.TrimSpace(slot))
	note = strings.TrimSpace(note)
	var state string
	var notePtr *string
	switch strings.ToUpper(strings.TrimSpace(decision)) {
	case "ACCEPT", models.LoanDocAccepted:
		state = models.LoanDocAccepted
	case models.LoanDocRetake:
		state = models.LoanDocRetake
		if note == "" {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"note": "tell the customer what to fix"})
		}
		if len(note) > 500 {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"note": "at most 500 characters"})
		}
		notePtr = &note
	default:
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"decision": "decision must be ACCEPT or RETAKE"})
	}
	if app.Status != models.LoanAssistSubmitted && state == models.LoanDocRetake {
		return nil, apperr.NewConflict("Move the application back to Documents check before asking for documents again")
	}
	err = s.repo.ReviewDocument(ctx, id, slot, state, notePtr, rv.AccountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Document not found")
	}
	if err != nil {
		return nil, err
	}
	if state == models.LoanDocRetake {
		title := slotLabel(models.FindLoanType(app.LoanType), slot)
		s.notifyCustomer(ctx, app, "Please upload a document again",
			fmt.Sprintf("Your %s for %s needs to be uploaded again. Open the app to see why.",
				strings.ToLower(title), strDeref(app.RequestCode)))
	}
	return s.ReviewDetail(ctx, rv, id)
}

// SetStage moves an application to a status. Verifying the documents (and
// every stage after it) needs every required document accepted.
func (s *LoanAssistService) SetStage(ctx context.Context, rv Reviewer, id int64, status string) (*models.ReviewApplication, error) {
	app, err := s.workableApp(ctx, rv, id)
	if err != nil {
		return nil, err
	}
	status = strings.ToUpper(strings.TrimSpace(status))
	if !models.IsLoanAssistStage(status) {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"status": "unknown status"})
	}
	if status != models.LoanAssistSubmitted {
		docs, err := s.repo.Documents(ctx, id)
		if err != nil {
			return nil, err
		}
		accepted := map[string]bool{}
		for _, d := range docs {
			accepted[d.Slot] = d.ReviewState == models.LoanDocAccepted
		}
		if lt := models.FindLoanType(app.LoanType); lt != nil {
			for _, slot := range lt.RequiredSlots(app.Rented) {
				if !accepted[slot] {
					return nil, apperr.NewConflict("Accept every document before moving the application on")
				}
			}
		}
	}
	if err := s.repo.SetStage(ctx, id, status); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("Application not found")
		}
		return nil, err
	}
	if status != app.Status {
		if title, body := stageMessage(status, strDeref(app.RequestCode)); title != "" {
			s.notifyCustomer(ctx, app, title, body)
		}
	}
	return s.ReviewDetail(ctx, rv, id)
}

// Assign gives an application to a partner. An admin assigns anything to
// anyone; a partner takes an unassigned one or hands their own to another.
func (s *LoanAssistService) Assign(ctx context.Context, rv Reviewer, id int64, partnerID *int64) (*models.ReviewApplication, error) {
	app, err := s.reviewApp(ctx, rv, id)
	if err != nil {
		return nil, err
	}
	if partnerID != nil {
		target, err := s.accounts.FindByID(ctx, *partnerID)
		if err != nil || !models.HasPermission(target.Role, models.PermLoanAssistReview) {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"partnerId": "that account is not a partner"})
		}
	} else if !rv.Admin && app.AssignedPartnerID == nil {
		return app, nil // nothing to release
	}
	if err := s.repo.Assign(ctx, id, partnerID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("Application not found")
		}
		return nil, err
	}
	slog.Info("loan assist application assigned",
		"application_id", id, "by", rv.AccountID, "partner_id", partnerID)
	return s.ReviewDetail(ctx, Reviewer{AccountID: rv.AccountID, Admin: rv.Admin}, id)
}

// SetNotes replaces the partners' notes on an application.
func (s *LoanAssistService) SetNotes(ctx context.Context, rv Reviewer, id int64, notes string) error {
	if _, err := s.workableApp(ctx, rv, id); err != nil {
		return err
	}
	notes = strings.TrimSpace(notes)
	if len(notes) > 2000 {
		return apperr.NewValidationWith("Validation failed", map[string]string{"notes": "at most 2000 characters"})
	}
	return s.repo.SetNotes(ctx, id, notes)
}

// Partners lists who an application can be assigned to.
func (s *LoanAssistService) Partners(ctx context.Context) ([]models.PartnerOption, error) {
	return s.repo.Partners(ctx)
}

// MyProfile is the caller's own card, as customers will see it.
func (s *LoanAssistService) MyProfile(ctx context.Context, accountID int64) (*models.PartnerCard, error) {
	return s.repo.PartnerCard(ctx, accountID)
}

// SaveProfile updates the caller's own card.
func (s *LoanAssistService) SaveProfile(ctx context.Context, accountID int64, c models.PartnerCard) (*models.PartnerCard, error) {
	c.AccountID = accountID
	c.Name = strings.TrimSpace(c.Name)
	c.Team = strings.TrimSpace(c.Team)
	details := map[string]string{}
	if c.Name == "" || len(c.Name) > 80 {
		details["name"] = "enter the name customers will see (up to 80 characters)"
	}
	if c.Team != "Offline Executive" && c.Team != "Online Support" {
		details["team"] = "team must be Offline Executive or Online Support"
	}
	trim := func(p *string, max int, field string) *string {
		if p == nil {
			return nil
		}
		v := strings.TrimSpace(*p)
		if v == "" {
			return nil
		}
		if len(v) > max {
			details[field] = fmt.Sprintf("at most %d characters", max)
		}
		return &v
	}
	c.City = trim(c.City, 60, "city")
	c.CallPhone = trim(c.CallPhone, 20, "phone")
	if len(details) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", details)
	}
	if err := s.repo.UpsertProfile(ctx, c); err != nil {
		return nil, err
	}
	return s.repo.PartnerCard(ctx, accountID)
}

// ---- helpers -------------------------------------------------------------------

// stageMessage is the push a customer gets when their application moves on.
func stageMessage(status, code string) (string, string) {
	switch status {
	case models.LoanAssistDocsVerified:
		return "Documents verified", "Your documents for " + code + " are verified. Your expert will call you soon."
	case models.LoanAssistExpertContacted:
		return "Your expert is on it", "Your loan expert has reached out about " + code + "."
	case models.LoanAssistGuidance:
		return "Loan guidance ready", "Your expert has shared loan options for " + code + "."
	}
	return "", ""
}

func (s *LoanAssistService) notifyCustomer(ctx context.Context, app *models.ReviewApplication, title, body string) {
	if s.pusher == nil {
		return
	}
	s.pusher.NotifyAccount(ctx, app.AccountID, push.Notification{
		Title: title, Body: body,
		// The app opens the application's page for this route.
		Data: map[string]string{"route": "AssistApplication:" + strDeref(app.RequestCode)},
	})
}

func slotLabel(lt *models.LoanType, slot string) string {
	if lt != nil {
		if r := lt.Rule(slot); r != nil {
			if r.Group == "AADHAAR" {
				return r.Title + " (" + strings.ToLower(r.Hint) + ")"
			}
			return r.Title
		}
	}
	return slot
}

func loanTypeName(lt *models.LoanType) string {
	if lt == nil {
		return "Loan"
	}
	return lt.Name
}

func (s *LoanAssistService) deleteObject(ctx context.Context, uri string, accountID int64) {
	if err := s.docs.Delete(ctx, uri); err != nil {
		slog.Error("could not remove a loan assist document object", "account_id", accountID, "error", err)
	}
}

// sniffDocumentType returns the mime type the bytes actually are, or "" for
// anything that is not a JPEG, PNG or PDF.
func sniffDocumentType(data []byte) string {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "application/pdf":
		return "application/pdf"
	}
	return ""
}

// cleanFileName keeps what the customer will recognise ("pan-card.pdf") but
// never a path, and names an unnamed camera capture after its slot.
func cleanFileName(name, slot, mimeType string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if len(name) > 120 {
		name = name[len(name)-120:]
	}
	if name == "" {
		name = strings.ToLower(strings.ReplaceAll(slot, "_", "-")) + documentExt("", mimeType)
	}
	return name
}

var istZone = time.FixedZone("IST", 5*3600+30*60)
