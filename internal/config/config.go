// Package config loads the service configuration using Viper.
//
// It mirrors the previous Spring application.yml: a base config.yaml plus an
// optional named profile (e.g. "dev") overlaid on top, with environment
// variables taking the highest precedence.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the top-level configuration tree.
type Config struct {
	Server       ServerConfig       `mapstructure:"server"`
	DB           DBConfig           `mapstructure:"db"`
	Mail         MailConfig         `mapstructure:"mail"`
	SMS          SMSConfig          `mapstructure:"sms"`
	Auth         AuthConfig         `mapstructure:"auth"`
	Multipart    MultipartConfig    `mapstructure:"multipart"`
	Registration RegistrationConfig `mapstructure:"registration"`
	Digitap      DigitapConfig      `mapstructure:"digitap"`
	Log          LogConfig          `mapstructure:"log"`
	Cashfree     CashfreeConfig     `mapstructure:"cashfree"`
	Statement    StatementConfig    `mapstructure:"statement"`
	Utho         UthoConfig         `mapstructure:"utho"`
	Demo         DemoConfig         `mapstructure:"demo"`
}

// UthoConfig holds credentials and target bucket for the Utho Cloud object-
// storage upload API. Used by the credit-analytics PDF relay to persist the
// generated report PDF. When APIToken is empty the client runs in stub mode
// (no I/O), so dev/CI works without credentials — same convention as the other
// external-capability clients.
type UthoConfig struct {
	APIToken string        `mapstructure:"api-token"` // empty -> stub
	DCSlug   string        `mapstructure:"dc-slug"`   // Utho datacenter slug
	Bucket   string        `mapstructure:"bucket"`    // target bucket name
	BaseURL  string        `mapstructure:"base-url"`  // optional override (testing)
	Timeout  time.Duration `mapstructure:"timeout"`
}

// StatementConfig holds settings for bank-statement PDF analysis. Parser
// selects the extraction engine ("pdf" for the real text-layer reader, "stub"
// for the dev-only canned parser). WorkerConcurrency/WorkerBuffer configure the
// in-process analysis pool; MaxFileSize caps a single upload; ProcessTimeout
// bounds one analysis so a pathological PDF can't pin a worker.
//
// Provider selects the *flow*: "local" (client uploads a PDF, we analyze it
// with Parser) or "digitap" (redirect/upload via Digitap's UI, we store their
// report). Both endpoints stay available regardless; provider documents the
// intended flow and is surfaced to the client. Digitap carries the Bank-Data
// API credentials (separate from the credit digitap.* block — different
// product); CallbackURL/CallbackSecret drive the public webhook.
type StatementConfig struct {
	Parser            string         `mapstructure:"parser"`             // stub | pdf (default pdf)
	MaxFileSize       string         `mapstructure:"max-file-size"`      // parsed via server.parseSize
	WorkerConcurrency int            `mapstructure:"worker-concurrency"` // worker goroutines
	WorkerBuffer      int            `mapstructure:"worker-buffer"`      // queued jobs beyond in-flight
	ProcessTimeout    time.Duration  `mapstructure:"process-timeout"`
	Provider          string         `mapstructure:"provider"` // local | digitap (default local)
	Digitap           BankDataConfig `mapstructure:"digitap"`
	// CallbackSecret, when set, requires the public Digitap webhook to echo it
	// as ?secret=. v1.20 of the API defines no HMAC, so this is our guard.
	CallbackSecret   string `mapstructure:"callback-secret"`
	DefaultReturnURL string `mapstructure:"default-return-url"`
}

// BankDataConfig holds credentials and endpoints for the Digitap Bank Data PDF
// UI API (v1.20). When ClientID is empty the client runs in stub mode (no I/O),
// so dev/CI works without credentials — same convention as the credit Digitap
// client and the Cashfree gateway.
type BankDataConfig struct {
	BaseURL      string        `mapstructure:"base-url"`  // e.g. https://svcdemo.digitap.work/bank-data/
	ClientID     string        `mapstructure:"client-id"` // empty -> stub
	ClientSecret string        `mapstructure:"client-secret"`
	CallbackURL  string        `mapstructure:"callback-url"` // public URL Digitap POSTs the callback to
	Timeout      time.Duration `mapstructure:"timeout"`
}

// DemoConfig holds flags that relax real-world gating so the product can be
// demonstrated end-to-end without external verification providers. It must stay
// disabled in production: with Enabled true, a submitted PAN is auto-verified
// (skipping the admin verification step that normally gates credit analytics).
type DemoConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

// CashfreeConfig holds Cashfree Payment Gateway credentials and endpoints.
// When ClientID is empty the service falls back to a log-only stub gateway
// (mirroring the mail stub) so local dev works without credentials.
type CashfreeConfig struct {
	Mode         string        `mapstructure:"mode"`     // sandbox | production
	BaseURL      string        `mapstructure:"base-url"` // optional; derived from mode when empty
	ClientID     string        `mapstructure:"client-id"`
	ClientSecret string        `mapstructure:"client-secret"`
	APIVersion   string        `mapstructure:"api-version"`
	ReturnURL    string        `mapstructure:"return-url"` // browser redirect after payment
	NotifyURL    string        `mapstructure:"notify-url"` // public URL of our webhook endpoint
	Timeout      time.Duration `mapstructure:"timeout"`
}

// LogConfig holds structured-logging settings (log/slog). Level is one of
// debug/info/warn/error (default info); Format is json or text.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// DigitapConfig holds credentials and endpoint settings for the Digitap Credit
// Analytics API (spec V2.7). When ClientID is empty, the service runs the
// client in an offline stub mode.
type DigitapConfig struct {
	BaseURL      string        `mapstructure:"base-url"`
	ClientID     string        `mapstructure:"client-id"`
	ClientSecret string        `mapstructure:"client-secret"`
	Timeout      time.Duration `mapstructure:"timeout"`
}

// AuthConfig holds token and session settings for the auth flows.
//
// Sessions are a two-token scheme: a stateless access JWT that lives for
// AccessTTL (minutes — this bounds how long a revoked device keeps working)
// and an opaque refresh token that lives for RefreshTTL and is revocable per
// device. Keep AccessTTL short; it is the revocation lag.
type AuthConfig struct {
	JWTSecret  string        `mapstructure:"jwt-secret"`
	AccessTTL  time.Duration `mapstructure:"access-ttl"`
	RefreshTTL time.Duration `mapstructure:"refresh-ttl"`
	// CookieSecure sets the Secure flag on the web refresh-token cookie. Must
	// stay true in production; set false only for plain-http local dev, where
	// the browser would otherwise drop the cookie.
	CookieSecure bool         `mapstructure:"cookie-secure"`
	OTP          OTPConfig    `mapstructure:"otp"`
	AdminEmails  []string     `mapstructure:"admin-emails"`
	Google       GoogleConfig `mapstructure:"google"`
}

// GoogleConfig holds settings for the Google OAuth ID-token login flow. The
// ClientID is the "Web application" OAuth client ID from Google Cloud Console;
// both the Android and iOS Google Sign-In SDKs must pass it as serverClientID
// so the ID tokens they mint all carry the same `aud`. When empty, Google
// login is disabled (the handler returns 503).
type GoogleConfig struct {
	ClientID string `mapstructure:"client-id"`
}

type ServerConfig struct {
	Port           int    `mapstructure:"port"`
	MaxRequestBody string `mapstructure:"max-request-body"`
	// Comma-separated list of allowed CORS origins, or "*" for any.
	CORSOrigins string `mapstructure:"cors-origins"`
	// TrustedProxies lists the IPs or CIDRs of load balancers / reverse proxies
	// in front of this service. Only when the peer is on this list does the
	// server believe X-Forwarded-For and report the real client IP; otherwise
	// c.IP() is the socket peer, which behind a proxy means every session
	// records the balancer's address.
	//
	// Empty (the default) means "no proxy" — correct for local dev and direct
	// exposure, wrong the moment you deploy behind an ALB / nginx / Cloudflare.
	// Never set this to 0.0.0.0/0: that lets any client forge its own IP.
	// Set via SERVER_TRUSTED_PROXIES=10.0.0.0/8,192.168.1.5
	TrustedProxies []string `mapstructure:"trusted-proxies"`
}

type DBConfig struct {
	URL         string `mapstructure:"url"`
	Username    string `mapstructure:"username"`
	Password    string `mapstructure:"password"`
	MaxPoolSize int    `mapstructure:"max-pool-size"`
	MinIdle     int    `mapstructure:"min-idle"`
	// When set, takes precedence over URL/Username/Password.
	DSN string `mapstructure:"dsn"`
}

type MailConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	From     string `mapstructure:"from"`
}

// SMSConfig holds transactional-SMS delivery settings. Only the phone sign-in
// OTP goes out over SMS today.
//
// Provider is informational — the real switch is whether MSG91.AuthKey is set.
// An empty auth key falls back to the log-only stub sender, the same
// empty-credentials-⇒-stub convention as mail, Cashfree, Digitap and Utho.
type SMSConfig struct {
	Provider string      `mapstructure:"provider"` // msg91 | stub
	MSG91    MSG91Config `mapstructure:"msg91"`
}

// MSG91Config holds credentials and template bindings for MSG91's v5 Flow API.
//
// In India an SMS can only be delivered through a template pre-registered on
// the DLT registry, so there is no message text here: TemplateID selects the
// approved wording and OTPVar names the placeholder the code is substituted
// into. Both must match the MSG91 panel exactly — variable names are
// case-sensitive, and an unknown one is dropped silently, delivering an SMS
// with the raw "##OTP##" still in it.
type MSG91Config struct {
	// AuthKey is the MSG91 account auth key. SECRET — never commit it; set it
	// via SMS_MSG91_AUTH_KEY or the gitignored config.dev.yaml. Empty -> stub.
	AuthKey string `mapstructure:"auth-key"`
	// TemplateID is MSG91's own template id, from the panel (SMS -> Templates).
	// It is NOT the 19-digit DLT template id the wording is registered under on
	// the telecom registry — the Flow API only accepts MSG91's id, and passing
	// the DLT one comes back as {"type":"error"} naming the template.
	TemplateID string `mapstructure:"template-id"`
	// SenderID is the 6-character DLT-approved header (e.g. REAOUT). Optional:
	// templates that already bind a sender ignore it.
	SenderID string `mapstructure:"sender-id"`
	// OTPVar is the template placeholder name for the code. "OTP" matches a
	// template written with ##OTP##.
	OTPVar string `mapstructure:"otp-var"`
	// AppSignature is the 11-character hash Google's SMS Retriever requires an
	// SMS to end with before Android will auto-read the code from it, and
	// AppSignatureVar is the trailing template placeholder it is substituted
	// into. BOTH must be set for the hash to be sent, and the DLT template must
	// actually end with that placeholder — see docs/sms-otp.md. Leave empty to
	// send the plain template; the app then falls back to manual entry.
	AppSignature    string `mapstructure:"app-signature"`
	AppSignatureVar string `mapstructure:"app-signature-var"`
	// BaseURL overrides the API root; exists for pointing tests at a mock.
	BaseURL string        `mapstructure:"base-url"`
	Timeout time.Duration `mapstructure:"timeout"`
}

type MultipartConfig struct {
	MaxFileSize    string `mapstructure:"max-file-size"`
	MaxRequestSize string `mapstructure:"max-request-size"`
}

type RegistrationConfig struct {
	PanImageDir string    `mapstructure:"pan-image-dir"`
	OTP         OTPConfig `mapstructure:"otp"`
	PAN         PANConfig `mapstructure:"pan"`
	OCR         OCRConfig `mapstructure:"ocr"`
}

type OTPConfig struct {
	Length         int           `mapstructure:"length"`
	TTL            time.Duration `mapstructure:"ttl"`
	ResendCooldown time.Duration `mapstructure:"resend-cooldown"`
	MaxAttempts    int           `mapstructure:"max-attempts"`
	MaxSends       int           `mapstructure:"max-sends"`
}

type PANConfig struct {
	NameMatchDistance int `mapstructure:"name-match-distance"`
}

type OCRConfig struct {
	Provider      string  `mapstructure:"provider"`
	MinConfidence float64 `mapstructure:"min-confidence"`
}

// Load reads config.yaml (and config.<profile>.yaml if profile is non-empty),
// then overlays environment variables. Env keys are uppercased, dot-separated
// keys become underscore-separated (e.g. registration.otp.length ->
// REGISTRATION_OTP_LENGTH). Env values override file values.
func Load(profile string) (*Config, error) {
	v := viper.New()
	v.SetConfigName("config") // config.yaml / config.yml
	v.SetConfigType("yaml")
	v.AddConfigPath(".") // project root when run from repo
	v.AddConfigPath("./config")

	setDefaults(v)

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, fmt.Errorf("read config: %w", err)
		}
		// No base file — proceed; env vars + defaults may still cover it.
	}

	if profile != "" {
		v.SetConfigName(fmt.Sprintf("config.%s", profile))
		// Merge rather than replace so the dev file only overrides what it sets.
		if err := v.MergeInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) {
				return nil, fmt.Errorf("merge profile %q: %w", profile, err)
			}
		}
	}

	// Bind every key under the same name (env: REGISTRATION_OTP_LENGTH).
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
	// Make sure nested keys resolve from env without needing the full prefix.
	_ = bindEnvForKeys(v, allKeys())

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Viper won't split a comma-separated env value into a slice, so handle the
	// list-valued keys explicitly. The file form is already a YAML list.
	if raw := os.Getenv("AUTH_ADMIN_EMAILS"); raw != "" {
		cfg.Auth.AdminEmails = splitList(raw)
	}
	if raw := os.Getenv("SERVER_TRUSTED_PROXIES"); raw != "" {
		cfg.Server.TrustedProxies = splitList(raw)
	}

	return &cfg, nil
}

// splitList parses a comma-separated env value, dropping blanks.
func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.max-request-body", "10MB")
	v.SetDefault("server.cors-origins", "*")
	// No proxy trusted by default: X-Forwarded-For is ignored and the socket
	// peer is the client IP. Set this in any environment behind a load balancer.
	v.SetDefault("server.trusted-proxies", []string{})

	v.SetDefault("db.max-pool-size", 10)
	v.SetDefault("db.min-idle", 2)

	v.SetDefault("mail.port", 587)
	v.SetDefault("mail.from", "Scorr.club <noreply@scorr.club>")

	// Transactional SMS (phone sign-in OTP) via MSG91's v5 Flow API. The
	// template ID and sender ID are not secrets and are committed so a fresh
	// checkout is one env var away from sending; the auth key is a secret and
	// defaults to empty, which selects the log-only stub sender.
	// Set it via SMS_MSG91_AUTH_KEY.
	v.SetDefault("sms.provider", "msg91")
	v.SetDefault("sms.msg91.auth-key", "")
	v.SetDefault("sms.msg91.template-id", "6a845b1f9fa9adf1da0c06d3")
	v.SetDefault("sms.msg91.sender-id", "REAOUT")
	v.SetDefault("sms.msg91.otp-var", "OTP")
	// Empty: the approved template has no trailing hash placeholder, so Android
	// SMS auto-read stays off until one is added. See docs/sms-otp.md.
	v.SetDefault("sms.msg91.app-signature", "")
	v.SetDefault("sms.msg91.app-signature-var", "")
	v.SetDefault("sms.msg91.base-url", "")
	v.SetDefault("sms.msg91.timeout", "15s")

	v.SetDefault("auth.jwt-secret", "dev-insecure-change-me")
	// Access token: short by design. This is the window in which a revoked
	// device can still call the API, so don't stretch it for convenience —
	// clients refresh silently on 401.
	v.SetDefault("auth.access-ttl", "15m")
	// Refresh token: how long a device stays signed in without re-entering
	// credentials. Rotated on every use.
	v.SetDefault("auth.refresh-ttl", "720h") // 30 days
	v.SetDefault("auth.cookie-secure", true)
	v.SetDefault("auth.otp.length", 6)
	v.SetDefault("auth.otp.ttl", "10m")
	v.SetDefault("auth.otp.resend-cooldown", "30s")
	v.SetDefault("auth.otp.max-attempts", 5)
	v.SetDefault("auth.otp.max-sends", 5)
	// Admin allowlist: accounts whose email matches get role=admin at verify/login.
	// Defaults to empty (no admins). Set via AUTH_ADMIN_EMAILS=a@x.com,b@y.com.
	v.SetDefault("auth.admin-emails", []string{})
	// Google OAuth ID-token login. Empty client-id disables the flow.
	// Set via AUTH_GOOGLE_CLIENT_ID (or google.client-id in the config file).
	v.SetDefault("auth.google.client-id", "")

	v.SetDefault("multipart.max-file-size", "5MB")
	v.SetDefault("multipart.max-request-size", "10MB")

	v.SetDefault("registration.pan-image-dir", "./data/pan-images")
	v.SetDefault("registration.otp.length", 6)
	v.SetDefault("registration.otp.ttl", "5m")
	v.SetDefault("registration.otp.resend-cooldown", "30s")
	v.SetDefault("registration.otp.max-attempts", 5)
	v.SetDefault("registration.otp.max-sends", 5)
	v.SetDefault("registration.pan.name-match-distance", 2)
	v.SetDefault("registration.ocr.provider", "stub")
	v.SetDefault("registration.ocr.min-confidence", 0.8)

	// Digitap Credit Analytics API. Empty client-id -> offline stub client.
	v.SetDefault("digitap.base-url", "https://apidemo.digitap.work/")
	v.SetDefault("digitap.client-id", "")
	v.SetDefault("digitap.client-secret", "")
	v.SetDefault("digitap.timeout", "30s")

	// Structured logging (log/slog). Override level via LOG_LEVEL.
	// Format defaults to "text" (human-readable) and falls back to it on any
	// unrecognized value; set "json" for production/structured ingestion.
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")

	// Utho Cloud object storage (credit-report PDF relay). Empty api-token ->
	// stub client, so dev/CI runs the relay with a fake URL. Set bucket + dc-slug
	// in any environment that should store real PDFs.
	v.SetDefault("utho.api-token", "")
	v.SetDefault("utho.dc-slug", "")
	v.SetDefault("utho.bucket", "")
	v.SetDefault("utho.base-url", "")
	v.SetDefault("utho.timeout", "60s")

	v.SetDefault("cashfree.mode", "sandbox")
	v.SetDefault("cashfree.api-version", "2025-01-01")
	v.SetDefault("cashfree.timeout", "15s")

	// Bank-statement analysis. Parser defaults to "pdf" (the real text-layer
	// reader); set "stub" for offline/CI runs that don't have a PDF. The worker
	// pool is small by default — analysis is CPU-bound, not latency-sensitive.
	v.SetDefault("statement.parser", "pdf")
	v.SetDefault("statement.max-file-size", "10MB")
	v.SetDefault("statement.worker-concurrency", 4)
	v.SetDefault("statement.worker-buffer", 16)
	v.SetDefault("statement.process-timeout", "2m")
	// Provider defaults to "local" (in-process analyzer). Set "digitap" to use
	// the Digitap redirect/upload flow as the recommended path; both endpoints
	// remain callable either way.
	v.SetDefault("statement.provider", "local")
	v.SetDefault("statement.default-return-url", "")
	v.SetDefault("statement.callback-secret", "")
	// Digitap Bank-Data API (v1.20). Empty client-id -> offline stub client, so
	// the flow is exercised end-to-end in dev/CI without credentials.
	v.SetDefault("statement.digitap.base-url", "https://svcdemo.digitap.work/bank-data/")
	v.SetDefault("statement.digitap.client-id", "")
	v.SetDefault("statement.digitap.client-secret", "")
	v.SetDefault("statement.digitap.callback-url", "")
	v.SetDefault("statement.digitap.timeout", "30s")

	// Demo mode: OFF by default. Enable only for demos/UAT where the real KYC
	// verification provider is unavailable. Set via DEMO_ENABLED=true.
	v.SetDefault("demo.enabled", false)
}

func allKeys() []string {
	return []string{
		"server.port", "server.max-request-body", "server.cors-origins",
		"server.trusted-proxies",
		"db.url", "db.username", "db.password", "db.dsn", "db.max-pool-size", "db.min-idle",
		"mail.host", "mail.port", "mail.username", "mail.password", "mail.from",
		"sms.provider",
		"sms.msg91.auth-key", "sms.msg91.template-id", "sms.msg91.sender-id",
		"sms.msg91.otp-var", "sms.msg91.app-signature", "sms.msg91.app-signature-var",
		"sms.msg91.base-url", "sms.msg91.timeout",
		"auth.jwt-secret", "auth.access-ttl", "auth.refresh-ttl", "auth.cookie-secure",
		"auth.otp.length", "auth.otp.ttl", "auth.otp.resend-cooldown",
		"auth.otp.max-attempts", "auth.otp.max-sends",
		"auth.admin-emails",
		"auth.google.client-id",
		"multipart.max-file-size", "multipart.max-request-size",
		"registration.pan-image-dir",
		"registration.otp.length", "registration.otp.ttl",
		"registration.otp.resend-cooldown", "registration.otp.max-attempts",
		"registration.otp.max-sends",
		"registration.pan.name-match-distance",
		"registration.ocr.provider", "registration.ocr.min-confidence",
		"digitap.base-url", "digitap.client-id", "digitap.client-secret", "digitap.timeout",
		"log.level", "log.format",
		"utho.api-token", "utho.dc-slug", "utho.bucket", "utho.base-url", "utho.timeout",
		"cashfree.mode", "cashfree.base-url", "cashfree.client-id",
		"cashfree.client-secret", "cashfree.api-version",
		"cashfree.return-url", "cashfree.notify-url", "cashfree.timeout",
		"statement.parser", "statement.max-file-size",
		"statement.worker-concurrency", "statement.worker-buffer",
		"statement.process-timeout",
		"statement.provider", "statement.default-return-url", "statement.callback-secret",
		"statement.digitap.base-url", "statement.digitap.client-id",
		"statement.digitap.client-secret", "statement.digitap.callback-url",
		"statement.digitap.timeout",
		"demo.enabled",
	}
}

// bindEnvForKeys makes viper check the environment for each dot-key explicitly,
// because AutomaticEnv only resolves keys that the file already contains or
// that have a default. With this, e.g. setting REGISTRATION_OTP_LENGTH=8 works
// even without a file entry.
func bindEnvForKeys(v *viper.Viper, keys []string) error {
	for _, k := range keys {
		if err := v.BindEnv(k); err != nil {
			return err
		}
	}
	return nil
}
