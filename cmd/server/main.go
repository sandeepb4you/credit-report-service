// Package main is the credit-report-service entry point. It loads config,
// wires dependencies by hand, runs DB migrations, and starts the Fiber server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "credit-report-service/docs" // generated Swagger spec; self-registers fiberSwagger
	"credit-report-service/internal/bankdata"
	"credit-report-service/internal/config"
	"credit-report-service/internal/db"
	"credit-report-service/internal/digitap"
	"credit-report-service/internal/handler"
	"credit-report-service/internal/payments"
	"credit-report-service/internal/repository"
	"credit-report-service/internal/server"
	"credit-report-service/internal/service"
	"credit-report-service/internal/sms"
	"credit-report-service/internal/statement"
	"credit-report-service/internal/utho"
)

// @title          Credit Report Service API
// @version        1.0
// @description    REST API for the credit-report Android app: email/password auth with OTP verification, JWT sessions, profile management, and credit-report CRUD.
// @description
// @description    All routes are mounted under `/api`. Protected routes require an `Authorization: Bearer <jwt>` header obtained from login or email verification.
//
// @host      localhost:8080
// @BasePath  /api
//
// @securityDefinitions.apikey BearerAuth
// @in          header
// @name        Authorization
// @description  "Bearer <jwt>" — obtain via POST /api/auth/login or /api/auth/verify-email

func main() {
	profile := os.Getenv("APP_PROFILE")
	cfg, err := config.Load(profile)
	if err != nil {
		// Logger isn't initialized yet; write to stderr directly.
		slog.Error("config load failed", "error", err)
		os.Exit(1)
	}
	initLogger(cfg.Log, profile)

	rootCtx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	slog.Info("running database migrations")
	if err := db.Migrate(rootCtx, cfg.DB); err != nil {
		slog.Error("database migration failed", "error", err)
		os.Exit(1)
	}

	slog.Info("connecting to database")
	pool, err := db.New(rootCtx, cfg.DB)
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Repositories.
	accountRepo := repository.NewAccountRepo(pool)
	analyticsRepo := repository.NewCreditAnalyticsRepo(pool)
	orderRepo := repository.NewOrderRepo(pool)
	sessionRepo := repository.NewSessionRepo(pool)
	couponRepo := repository.NewCouponRepo(pool)
	loanRepo := repository.NewLoanProviderRepo(pool)
	bankOfferingRepo := repository.NewBankOfferingRepo(pool)

	// Upstream clients.
	digitapClient := digitap.New(digitap.Config{
		BaseURL:      cfg.Digitap.BaseURL,
		ClientID:     cfg.Digitap.ClientID,
		ClientSecret: cfg.Digitap.ClientSecret,
		Timeout:      cfg.Digitap.Timeout,
	})

	// Mobile to Prefill — a separate Digitap product from Credit Analytics
	// above, on its own host. Credentials fall back to the Credit Analytics
	// pair, since one Digitap account commonly covers both; empty after that
	// selects the offline stub and PAN verification stops proving anything.
	prefillID := cfg.Digitap.Prefill.ClientID
	prefillSecret := cfg.Digitap.Prefill.ClientSecret
	if prefillID == "" {
		prefillID, prefillSecret = cfg.Digitap.ClientID, cfg.Digitap.ClientSecret
	}
	prefillClient := digitap.NewPrefill(digitap.PrefillConfig{
		BaseURL:      cfg.Digitap.Prefill.BaseURL,
		ClientID:     prefillID,
		ClientSecret: prefillSecret,
		Timeout:      cfg.Digitap.Prefill.Timeout,
	})
	if prefillClient.IsStub() {
		slog.Warn("digitap prefill credentials are empty; PAN verification runs against the offline stub " +
			"and confirms nothing about the real person")
	}

	// Payment gateway: real Cashfree client when credentials are set,
	// otherwise the log-only stub (dev fallback, like the mail stub).
	var gateway payments.Gateway
	cashfreeStub := cfg.Cashfree.ClientID == ""
	if cashfreeStub {
		slog.Warn("cashfree.client-id is empty; using the stub payment gateway")
		gateway = payments.NewStubGateway(cfg.Cashfree.Mode)
	} else {
		gateway = payments.NewCashfreeClient(cfg.Cashfree)
	}

	// SMS sender for the phone sign-in OTP: real MSG91 client when an auth key
	// is set, otherwise the log-only stub (dev fallback, like the mail stub).
	//
	// sms.provider: stub forces the stub even with a working auth key, which is
	// what a local run wants — config.dev.yaml holds real credentials, and
	// nothing on a developer machine should be texting real people.
	var smsSender sms.Sender
	switch {
	case strings.EqualFold(cfg.SMS.Provider, "stub"):
		slog.Warn("sms.provider is \"stub\"; phone OTPs will not be sent to anyone")
		smsSender = sms.NewStubSender()
	case cfg.SMS.MSG91.AuthKey == "":
		slog.Warn("sms.msg91.auth-key is empty; phone OTPs will be logged, not sent")
		smsSender = sms.NewStubSender()
	default:
		if cfg.SMS.MSG91.TemplateID == "" {
			// The Flow API cannot send without a template, and the failure only
			// shows up on the first sign-in attempt — say so at boot instead.
			slog.Error("sms.msg91.auth-key is set but template-id is empty; every phone OTP send will fail")
		}
		smsSender = sms.NewMSG91Client(cfg.SMS.MSG91)
	}

	// Services.
	if cfg.Auth.OTP.MasterCode != "" {
		// Loud, and by name: config.Load has already established this is a local
		// profile, but a developer who forgets it is on will not understand why
		// every wrong code is accepted.
		slog.Warn("OTP MASTER CODE ENABLED: a fixed code is accepted in place of every real "+
			"OTP (signup, phone sign-in, password reset, contact linking); local profiles only",
			"profile", profile)
	}
	otpSvc := service.NewOTPService(cfg.Auth.OTP)
	mailSvc := service.NewMailService(cfg.Mail, cfg.Auth.OTP.TTL)
	tokenSvc := service.NewTokenService(cfg.Auth)
	sessionSvc := service.NewSessionService(sessionRepo, cfg.Auth)
	couponSvc := service.NewCouponService(couponRepo, orderRepo)
	authSvc := service.NewAuthService(
		accountRepo, otpSvc, mailSvc, smsSender, tokenSvc, sessionSvc, couponSvc, cfg.Auth)
	analyticsSvc := service.NewCreditAnalyticsService(digitapClient, analyticsRepo, accountRepo)
	if cfg.Demo.Enabled {
		slog.Warn("DEMO MODE ENABLED: submitted PANs are auto-verified without the external KYC provider; do not use in production")
	}
	kycSvc := service.NewKycService(
		accountRepo,
		service.NewPrefillVerifier(prefillClient, cfg.Registration.PAN),
		cfg.Registration.PAN,
		cfg.Demo.Enabled,
	)
	orderSvc := service.NewOrderService(orderRepo, accountRepo, couponSvc, gateway, cfg.Cashfree)
	loanSwitchSvc := service.NewLoanSwitchService(loanRepo, analyticsRepo)
	// Enrich analytics insights with interest-reduction opportunities so a single
	// analytics call surfaces both levers: raise the score and cut interest.
	analyticsSvc.SetLoanSwitch(loanSwitchSvc)
	scoreBuilderSvc := service.NewScoreBuilderService(bankOfferingRepo, analyticsRepo)
	// Enrich the score-builder toolkit (S28) with admin-curated bank offerings so
	// the FD-card hero names a real product with an apply CTA.
	analyticsSvc.SetScoreBuilder(scoreBuilderSvc)

	// Credit-report PDF relay: report_type 3 returns a short-lived Digitap URL,
	// which we download and re-upload to Utho object storage, storing the
	// permanent URL on the analytics row. Stub when the Utho token is empty, so
	// dev/CI runs the relay with a fake URL. Best-effort: a real failure just
	// leaves result_pdf_url null; the report/score are unaffected.
	uthoClient := utho.New(utho.Config{
		APIToken: cfg.Utho.APIToken,
		DCSlug:   cfg.Utho.DCSlug,
		Bucket:   cfg.Utho.Bucket,
		BaseURL:  cfg.Utho.BaseURL,
		Timeout:  cfg.Utho.Timeout,
	})
	pdfUploader := service.NewReportUploader(uthoClient, analyticsRepo, cfg.Utho.Bucket, 16)
	analyticsSvc.SetPDFUploader(pdfUploader)
	pdfUploader.Start(rootCtx)

	// Bank-statement analysis: text-layer PDF parser + async worker pool.
	// Parser follows the same empty-credentials-⇒-stub convention as the other
	// external-capability packages: "stub" returns a canned statement so the
	// analyze flow runs end-to-end without a real PDF (handy in CI/demo).
	bankStmtRepo := repository.NewBankStatementRepo(pool)
	var statementParser statement.Parser
	statementStub := cfg.Statement.Parser != "pdf"
	if statementStub {
		slog.Warn("statement.parser is not 'pdf'; using the stub parser (canned text, no real PDF)")
		statementParser = statement.NewStub()
	} else {
		statementParser = statement.NewPDFParser()
	}
	// Digitap Bank-Data client (redirect/upload flow). Stub when client-id is
	// empty, like every other external-capability package. A prod deployment
	// also needs statement.digitap.callback-url (the public webhook); warn when
	// it's missing so the operator knows the poll fallback is all that's left.
	bankDataClient := bankdata.New(bankdata.Config{
		BaseURL:      cfg.Statement.Digitap.BaseURL,
		ClientID:     cfg.Statement.Digitap.ClientID,
		ClientSecret: cfg.Statement.Digitap.ClientSecret,
		Timeout:      cfg.Statement.Digitap.Timeout,
	})
	if cfg.Statement.Provider == "digitap" && cfg.Statement.Digitap.CallbackURL == "" {
		slog.Warn("statement.digitap.callback-url is empty; the Digitap webhook will not be reachable — relying on GET /:id poll fallback")
	}
	// Service and pool are mutually dependent (the service submits jobs; the
	// pool calls the service back to process them). Break the cycle the same way
	// analytics/loan-switch do: build the service without the pool, build the
	// pool with the service as its processor, then wire the pool back in.
	bankStmtSvc := service.NewBankStatementService(
		statementParser, bankStmtRepo, bankDataClient,
		cfg.Statement.Digitap.CallbackURL, cfg.Statement.DefaultReturnURL,
	)
	bankStmtPool := service.NewWorkerPool(bankStmtSvc,
		cfg.Statement.WorkerConcurrency, cfg.Statement.WorkerBuffer, cfg.Statement.ProcessTimeout)
	bankStmtSvc.SetPool(bankStmtPool)
	// Reclaim rows left 'processing' by a previous crash, then start the workers
	// on the server ctx so they drain and stop on shutdown.
	if reclaimed, err := bankStmtRepo.ReclaimStaleProcessing(rootCtx, time.Now().Add(-service.StaleProcessingAge)); err != nil {
		slog.Warn("bank statement stale-reclaim failed", "error", err)
	} else if reclaimed > 0 {
		slog.Info("reclaimed interrupted bank statement analyses", "count", reclaimed)
	}
	bankStmtPool.Start(rootCtx)

	// Handlers.
	healthH := handler.NewHealthHandler()
	authH := handler.NewAuthHandler(authSvc, sessionSvc, cfg.Auth.CookieSecure)
	analyticsH := handler.NewCreditAnalyticsHandler(analyticsSvc)
	kycH := handler.NewKycHandler(kycSvc)
	orderH := handler.NewOrderHandler(orderSvc)
	couponH := handler.NewCouponHandler(couponSvc)
	loanH := handler.NewLoanSwitchHandler(loanSwitchSvc)
	scoreBuilderH := handler.NewScoreBuilderHandler(scoreBuilderSvc)
	// Statement handler gets the per-upload size cap and the optional webhook
	// shared-secret so it can reject oversized PDFs and unauthenticated callbacks.
	bankStmtH := handler.NewBankStatementHandler(
		bankStmtSvc, serverMaxBytes(cfg.Statement.MaxFileSize), cfg.Statement.CallbackSecret)

	// One-time configuration snapshot. No secrets: just feature flags the
	// operator needs to confirm at boot (stub vs. live upstream, OCR provider,
	// whether Google login is wired up).
	slog.Info("service starting",
		"port", cfg.Server.Port,
		"profile", profile,
		"digitap_stub", digitapClient.IsStub(),
		"cashfree_stub", cashfreeStub,
		// True means phone OTPs are only logged — nobody receives an SMS.
		"sms_stub", smsSender.IsStub(),
		// True means a fixed code is accepted in place of every real OTP.
		"otp_master_code", cfg.Auth.OTP.MasterCode != "",
		"ocr_provider", cfg.Registration.OCR.Provider,
		"statement_parser", cfg.Statement.Parser,
		"statement_provider", cfg.Statement.Provider,
		"bankdata_stub", bankDataClient.IsStub(),
		"utho_stub", uthoClient.IsStub(),
		"google_login_enabled", cfg.Auth.Google.ClientID != "",
		// Demo mode auto-verifies submitted PANs; must be false in production.
		"demo_mode", cfg.Demo.Enabled,
		// Zero behind a load balancer means every session records the
		// balancer's IP instead of the user's.
		"trusted_proxies", len(cfg.Server.TrustedProxies),
	)

	app := server.New(cfg, healthH, authH, analyticsH, kycH, orderH, couponH, loanH, scoreBuilderH, bankStmtH,
		tokenSvc, accountRepo)

	go func() {
		addr := ":" + itoa(cfg.Server.Port)
		slog.Info("http server listening", "addr", addr)
		if err := app.Listen(addr); err != nil {
			// Graceful shutdown via signal context will close the server; only
			// fatal on other errors.
			if errors.Is(err, fiberServerClosed) {
				return
			}
			slog.Error("http server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-rootCtx.Done()
	slog.Info("shutdown signal received")
	// Drain in-flight statement analyses before shutting down the server, so a
	// graceful restart doesn't leave rows stuck in 'processing' (the next boot's
	// stale-reclaim is the safety net for ungraceful stops).
	bankStmtPool.Stop()
	// Finish any in-flight credit-report PDF relay so a graceful restart doesn't
	// abandon a half-uploaded PDF. Best-effort: if shutdown outlasts the relay,
	// the row just keeps a null result_pdf_url.
	pdfUploader.Stop()
	shutdownCtx, cancelShut := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShut()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
	slog.Info("service stopped")
}

// serverMaxBytes parses a human size string from config (e.g. "10MB") into
// bytes for the per-upload cap. Falls back to 10 MiB on a parse error, matching
// the server's own body-limit default.
func serverMaxBytes(s string) int {
	if n, ok := server.ParseSize(s); ok {
		return n
	}
	return 10 * 1024 * 1024
}

// initLogger configures the package-level slog default from config. Format
// "text" (human-readable, default for dev) or "json" (log ingestion); level is
// one of debug/info/warn/error (default info). Any unrecognized value falls
// back to the safe default rather than failing startup.
func initLogger(cfg config.LogConfig, profile string) {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)
}

// fiberServerClosed is a sentinel for Listen returning due to Shutdown.
// Fiber returns net.ErrClosed wrapped under app.Shutdown; we don't import it
// to avoid the dependency, so we treat any post-shutdown error as benign.
var fiberServerClosed = errors.New("server closed")

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
