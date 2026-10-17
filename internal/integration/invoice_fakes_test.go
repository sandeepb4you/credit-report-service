package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"credit-report-service/internal/push"
	"credit-report-service/internal/service"
)

// invoiceWiring is the invoicing half of the harness.
type invoiceWiring struct {
	svc       *service.InvoiceService
	deliverer *service.InvoiceDeliverer
	renderer  *fakeRenderer
	store     *memStore
	mailer    *recordingMailer
	// Loan Assist rides the same wiring struct: its own bucket (so its
	// document count is not mixed with invoice PDFs) and team mailer.
	loanStore *memStore
	loanMail  *teamMailer
	loanPush  *recordingPusher
}

// recordingPusher keeps every customer notification instead of sending it.
type recordingPusher struct {
	mu   sync.Mutex
	sent []push.Notification
	to   []int64
}

func (p *recordingPusher) NotifyAccount(_ context.Context, accountID int64, n push.Notification) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, n)
	p.to = append(p.to, accountID)
}

func (p *recordingPusher) all() []push.Notification {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]push.Notification(nil), p.sent...)
}

// teamMailer keeps every Loan Assist team mail instead of sending it.
type teamMailer struct {
	mu   sync.Mutex
	sent []service.LoanAssistTeamMail
}

func (m *teamMailer) SendLoanAssistRequest(in service.LoanAssistTeamMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, in)
	return nil
}

func (m *teamMailer) all() []service.LoanAssistTeamMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]service.LoanAssistTeamMail(nil), m.sent...)
}

// fakeRenderer stands in for the Chromium sidecar. It keeps the HTML it was
// given, so a test can assert on what the invoice would have printed.
type fakeRenderer struct {
	mu    sync.Mutex
	pages []string
	down  bool
}

func (r *fakeRenderer) Available() bool { return true }

func (r *fakeRenderer) PDF(_ context.Context, html string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.down {
		return nil, errors.New("renderer down")
	}
	r.pages = append(r.pages, html)
	return []byte("%PDF-1.4 fake"), nil
}

func (r *fakeRenderer) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pages) == 0 {
		return ""
	}
	return r.pages[len(r.pages)-1]
}

func (r *fakeRenderer) setDown(down bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.down = down
}

// memStore is a bucket in a map.
type memStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (s *memStore) IsStub() bool { return false }

func (s *memStore) UploadAs(_ context.Context, key, _, _ string, body []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	uri := "s3://test-bucket/" + key
	s.objects[uri] = body
	return uri, nil
}

func (s *memStore) Delete(_ context.Context, uri string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, uri)
	return nil
}

func (s *memStore) has(uri string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[uri]
	return ok
}

func (s *memStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

func (s *memStore) PresignGet(_ context.Context, uri string) (string, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objects[uri]; !ok {
		return "", 0, errors.New("no such object")
	}
	return "https://signed.example/" + strings.TrimPrefix(uri, "s3://"), 10 * time.Minute, nil
}

// recordingMailer keeps every invoice mail instead of sending it.
type recordingMailer struct {
	mu   sync.Mutex
	sent []service.InvoiceMail
}

func (m *recordingMailer) SendInvoice(in service.InvoiceMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, in)
	return nil
}

func (m *recordingMailer) to() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.sent))
	for _, s := range m.sent {
		out = append(out, s.To)
	}
	return out
}
