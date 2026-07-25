// Package main is the credit-report-service entry point. It loads config,
// wires dependencies by hand, runs DB migrations, and starts the Fiber server.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "credit-report-service/docs" // generated Swagger spec; self-registers fiberSwagger
	"credit-report-service/internal/config"
	"credit-report-service/internal/db"
	"credit-report-service/internal/digitap"
	"credit-report-service/internal/handler"
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
		log.Fatalf("config load: %v", err)
	}

	rootCtx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("running database migrations...")
	if err := db.Migrate(rootCtx, cfg.DB); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	log.Printf("connecting to database...")
	pool, err := db.New(rootCtx, cfg.DB)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	// Repositories.
	creditRepo := repository.NewCreditReportRepo(pool)
	accountRepo := repository.NewAccountRepo(pool)
	analyticsRepo := repository.NewCreditAnalyticsRepo(pool)

	// Upstream clients.
	digitapClient := digitap.New(digitap.Config{
		BaseURL:      cfg.Digitap.BaseURL,
		ClientID:     cfg.Digitap.ClientID,
		ClientSecret: cfg.Digitap.ClientSecret,
		Timeout:      cfg.Digitap.Timeout,
	})

	// Services.
	creditSvc := service.NewCreditReportService(creditRepo)
	otpSvc := service.NewOTPService(cfg.Auth.OTP)
	mailSvc := service.NewMailService(cfg.Mail, cfg.Auth.OTP.TTL)
	tokenSvc := service.NewTokenService(cfg.Auth)
	authSvc := service.NewAuthService(accountRepo, otpSvc, mailSvc, tokenSvc)
	analyticsSvc := service.NewCreditAnalyticsService(digitapClient, analyticsRepo)

	// Handlers.
	healthH := handler.NewHealthHandler()
	creditH := handler.NewCreditReportHandler(creditSvc)
	authH := handler.NewAuthHandler(authSvc)
	analyticsH := handler.NewCreditAnalyticsHandler(analyticsSvc)

	app := server.New(cfg, healthH, creditH, authH, analyticsH, tokenSvc)

	go func() {
		addr := ":" + itoa(cfg.Server.Port)
		log.Printf("listening on %s", addr)
		if err := app.Listen(addr); err != nil {
			// Graceful shutdown via signal context will close the server; only
			// fatal on other errors.
			if errors.Is(err, fiberServerClosed) {
				return
			}
			log.Fatalf("listen: %v", err)
		}
	}()

	<-rootCtx.Done()
	log.Printf("shutting down...")
	shutdownCtx, cancelShut := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShut()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Printf("bye")
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
