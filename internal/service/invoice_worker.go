package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"credit-report-service/internal/models"
)

// InvoiceDeliverer mails invoices on payment, and issues any a fulfilment
// failed to.
//
// Asynchronous because an invoice is issued inside the payment webhook (or the
// app's reconcile poll), and a browser render plus an SMTP round trip is
// seconds of work that neither should wait on — Cashfree retries a webhook that
// is slow to answer. Issue kicks this worker, so in the ordinary case the mail
// leaves within a second or two of the payment; the ticker is the backstop for
// a kick that found the renderer or the mail server down.
//
// Durability is in the rows, as with the deletion sweeper: the predicate is
// auto_email = PENDING and due, so a restart loses nothing and a failed send is
// retried on a backoff until its attempts run out.
type InvoiceDeliverer struct {
	svc      *InvoiceService
	interval time.Duration

	wg   sync.WaitGroup
	once sync.Once
}

const (
	invoiceDeliveryBatch = 20
	// invoiceMaxAttempts with a doubling backoff capped at an hour is roughly
	// half a day of trying before the automatic email is given up as FAILED —
	// long enough to ride out a renderer or SMTP outage, short enough that a
	// permanently broken address stops being retried. The invoice itself is
	// unaffected either way and stays downloadable.
	invoiceMaxAttempts = 12
	// invoiceHealWindow is how far back the sweep looks for paid orders with no
	// invoice. See repository.PaidOrdersMissingInvoice.
	invoiceHealWindow = 48 * time.Hour
)

func NewInvoiceDeliverer(svc *InvoiceService, interval time.Duration) *InvoiceDeliverer {
	if interval <= 0 {
		interval = time.Minute
	}
	return &InvoiceDeliverer{svc: svc, interval: interval}
}

// Start runs the loop until ctx ends. The first pass fires at boot, so mail
// owed from before a restart goes out now rather than a tick from now.
func (d *InvoiceDeliverer) Start(ctx context.Context) {
	d.once.Do(func() {
		d.wg.Add(1)
		go d.run(ctx)
	})
}

// Stop waits for an in-flight pass to finish.
func (d *InvoiceDeliverer) Stop() { d.wg.Wait() }

func (d *InvoiceDeliverer) run(ctx context.Context) {
	defer d.wg.Done()
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	slog.Info("invoice deliverer started", "interval", d.interval.String())
	d.Pass(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.svc.kick:
		}
		d.Pass(ctx)
	}
}

// Pass heals missing invoices, then sends the automatic emails that are due.
// Exported so tests can run one deterministically.
func (d *InvoiceDeliverer) Pass(ctx context.Context) {
	d.heal(ctx)
	due, err := d.svc.repo.DuePendingEmails(ctx, invoiceDeliveryBatch)
	if err != nil {
		slog.Error("invoice deliverer: listing due emails failed", "error", err)
		return
	}
	for i := range due {
		if ctx.Err() != nil {
			return
		}
		d.deliver(ctx, &due[i])
	}
}

// heal issues invoices for recently paid orders that have none: fulfilment's
// issue failed, or the order was paid live before a GSTIN was configured.
func (d *InvoiceDeliverer) heal(ctx context.Context) {
	s := d.svc
	orders, err := s.repo.PaidOrdersMissingInvoice(ctx, s.now().Add(-invoiceHealWindow), invoiceDeliveryBatch)
	if err != nil {
		slog.Error("invoice deliverer: heal query failed", "error", err)
		return
	}
	for i := range orders {
		o := &orders[i]
		// Without a GSTIN a live order cannot be invoiced, and Issue would log
		// that once a minute for two days. Say nothing here; Issue said it once
		// at fulfilment.
		if o.PaymentMode == "production" && !s.cfg.Issuable() {
			continue
		}
		product, err := s.orders.FindProduct(ctx, o.ProductCode)
		if err != nil {
			slog.Error("invoice heal: product lookup failed", "order_uid", o.OrderUID, "error", err)
			continue
		}
		if _, err := s.Issue(ctx, o, product, nil); err != nil {
			slog.Error("invoice heal: issue failed", "order_uid", o.OrderUID, "error", err)
		} else {
			slog.Warn("invoice heal: issued an invoice fulfilment had missed", "order_uid", o.OrderUID)
		}
	}
}

// deliver sends one automatic email, or schedules its retry.
func (d *InvoiceDeliverer) deliver(ctx context.Context, inv *models.Invoice) {
	s := d.svc
	acc, err := s.accounts.FindByID(ctx, inv.AccountID)
	if err != nil {
		d.retry(ctx, inv, "account lookup: "+err.Error())
		return
	}
	to := normalizeEmail(strDeref(acc.PrimaryEmail))
	if to == "" {
		// The address went (a purge in flight). Nothing to send to, ever.
		_ = s.repo.SetAutoEmail(ctx, inv.ID, models.InvoiceEmailNoAddress)
		return
	}
	if s.mailer == nil {
		d.retry(ctx, inv, "no mailer configured")
		return
	}
	pdf, err := s.render(ctx, inv)
	if err != nil {
		d.retry(ctx, inv, err.Error())
		return
	}
	if inv.PDFURI == nil && s.store != nil && !s.store.IsStub() {
		_, _ = s.upload(ctx, inv, pdf)
	}
	if err := s.send(inv, to, pdf); err != nil {
		d.retry(ctx, inv, "send: "+scrubEmail(err.Error(), to))
		return
	}
	if err := s.repo.RecordSend(ctx, inv.ID, inv.AccountID, models.InvoiceSendAuto, hashEmail(to)); err != nil {
		slog.Error("invoice: auto email sent but not logged", "invoice", inv.Number, "error", err)
	}
	if err := s.repo.SetAutoEmail(ctx, inv.ID, models.InvoiceEmailSent); err != nil {
		// Worst case the next pass sends it again; logged because a duplicate
		// invoice email is a support ticket.
		slog.Error("invoice: auto email sent but not marked SENT", "invoice", inv.Number, "error", err)
		return
	}
	slog.Info("invoice emailed", "invoice", inv.Number, "recipient_hash", hashEmail(to))
}

func (d *InvoiceDeliverer) retry(ctx context.Context, inv *models.Invoice, cause string) {
	attempt := inv.AutoEmailAttempts + 1
	var next *time.Time
	if attempt < invoiceMaxAttempts {
		backoff := time.Minute << min(attempt-1, 6) // 1m, 2m, 4m ... 64m
		if backoff > time.Hour {
			backoff = time.Hour
		}
		t := d.svc.now().Add(backoff)
		next = &t
	}
	if err := d.svc.repo.RetryAutoEmail(ctx, inv.ID, cause, next); err != nil {
		slog.Error("invoice deliverer: could not record retry", "invoice", inv.Number, "error", err)
		return
	}
	level := slog.LevelWarn
	if next == nil {
		level = slog.LevelError
	}
	slog.Log(ctx, level, "invoice auto email failed", "invoice", inv.Number,
		"attempt", attempt, "final", next == nil, "error", cause)
}
