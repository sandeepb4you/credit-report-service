// Package main is the credit-report-service entry point. It loads config,
// wires dependencies by hand, runs DB migrations, and starts the Fiber server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "credit-report-service/docs" // generated Swagger spec; self-registers fiberSwagger
	"credit-report-service/internal/config"
	"credit-report-service/internal/db"
	"credit-report-service/internal/digitap"
	"credit-report-service/internal/handler"
	"credit-report-service/internal/payments"
	"credit-report-service/internal/repository"
	"credit-report-service/internal/server"
	"credit-report-service/internal/service"
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

	// Upstream clients.
	digitapClient := digitap.New(digitap.Config{
		BaseURL:      cfg.Digitap.BaseURL,
		ClientID:     cfg.Digitap.ClientID,
		ClientSecret: cfg.Digitap.ClientSecret,
		Timeout:      cfg.Digitap.Timeout,
	})

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

	// Services.
	otpSvc := service.NewOTPService(cfg.Auth.OTP)
	mailSvc := service.NewMailService(cfg.Mail, cfg.Auth.OTP.TTL)
	tokenSvc := service.NewTokenService(cfg.Auth)
	sessionSvc := service.NewSessionService(sessionRepo, cfg.Auth)
	couponSvc := service.NewCouponService(couponRepo, orderRepo)
	authSvc := service.NewAuthService(
		accountRepo, otpSvc, mailSvc, tokenSvc, sessionSvc, couponSvc, cfg.Auth)
	analyticsSvc := service.NewCreditAnalyticsService(digitapClient, analyticsRepo, accountRepo)
	kycSvc := service.NewKycService(accountRepo)
	orderSvc := service.NewOrderService(orderRepo, accountRepo, couponSvc, gateway, cfg.Cashfree)

	// Handlers.
	healthH := handler.NewHealthHandler()
	authH := handler.NewAuthHandler(authSvc, sessionSvc, cfg.Auth.CookieSecure)
	analyticsH := handler.NewCreditAnalyticsHandler(analyticsSvc)
	kycH := handler.NewKycHandler(kycSvc)
	orderH := handler.NewOrderHandler(orderSvc)
	couponH := handler.NewCouponHandler(couponSvc)

	// One-time configuration snapshot. No secrets: just feature flags the
	// operator needs to confirm at boot (stub vs. live upstream, OCR provider,
	// whether Google login is wired up).
	slog.Info("service starting",
		"port", cfg.Server.Port,
		"profile", profile,
		"digitap_stub", digitapClient.IsStub(),
		"cashfree_stub", cashfreeStub,
		"ocr_provider", cfg.Registration.OCR.Provider,
		"google_login_enabled", cfg.Auth.Google.ClientID != "",
		// Zero behind a load balancer means every session records the
		// balancer's IP instead of the user's.
		"trusted_proxies", len(cfg.Server.TrustedProxies),
	)

	app := server.New(cfg, healthH, authH, analyticsH, kycH, orderH, couponH, tokenSvc)

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
	shutdownCtx, cancelShut := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShut()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
	slog.Info("service stopped")
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
