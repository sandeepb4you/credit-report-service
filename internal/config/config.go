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
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/viper"
)

// Config is the top-level configuration tree.
type Config struct {
	Server          ServerConfig          `mapstructure:"server"`
	DB              DBConfig              `mapstructure:"db"`
	Mail            MailConfig            `mapstructure:"mail"`
	SMS             SMSConfig             `mapstructure:"sms"`
	Auth            AuthConfig            `mapstructure:"auth"`
	Multipart       MultipartConfig       `mapstructure:"multipart"`
	Registration    RegistrationConfig    `mapstructure:"registration"`
	Digitap         DigitapConfig         `mapstructure:"digitap"`
	CreditAnalytics CreditAnalyticsConfig `mapstructure:"credit-analytics"`
	ScheduledChecks ScheduledChecksConfig `mapstructure:"scheduled-checks"`
	Log             LogConfig             `mapstructure:"log"`
	Cashfree        CashfreeConfig        `mapstructure:"cashfree"`
	Statement       StatementConfig       `mapstructure:"statement"`
	S3              S3Config              `mapstructure:"s3"`
	Renderer        RendererConfig        `mapstructure:"renderer"`
	Invoice         InvoiceConfig         `mapstructure:"invoice"`
	Demo            DemoConfig            `mapstructure:"demo"`
	Sentry          SentryConfig          `mapstructure:"sentry"`

	// Warnings holds problems found while loading that are worth an operator's
	// attention but not worth refusing to boot over. Load runs before the
	// logger exists (see cmd/server/main.go), so they are collected here and
	// emitted by the caller once logging is configured. Never contains a
	// secret's value -- only its shape.
	Warnings []string `mapstructure:"-"`
}

// InvoiceConfig is the supplier half of every tax invoice: who is selling, from
// where, under which GSTIN, and at what rate.
//
// Config rather than constants because none of it is the code's to know. The
// legal name and registered office are the company's (they match the website's
// footer today), the GSTIN and SAC are its tax registration, and the rate is
// the government's. A change to any of them must not need a release, and must
// not rewrite an invoice already issued — which is why each invoice snapshots
// these at issue rather than reading them at render.
//
// An empty GSTIN or SAC means no tax invoices. Live orders are still taken and
// fulfilled; they are simply not invoiced, and the boot log says so. That is
// the fail-closed choice: a document headed TAX INVOICE carrying a placeholder
// GSTIN is a false document, and one with no GSTIN is not a tax invoice at all.
// Sandbox orders get SPECIMEN invoices either way, so the feature can be seen
// end to end before the registration details arrive.
type InvoiceConfig struct {
	LegalName string `mapstructure:"legal-name"`
	Address   string `mapstructure:"address"`
	// StateName and StateCode are the supplier's state, e.g. Karnataka / 29. The
	// code is the first two digits of any GSTIN registered there.
	StateName string `mapstructure:"state-name"`
	StateCode string `mapstructure:"state-code"`
	GSTIN     string `mapstructure:"gstin"`
	// SAC is the services accounting code printed on each line. To be confirmed
	// with the company's CA; there is deliberately no default.
	SAC string `mapstructure:"sac"`
	// GSTRatePercent is the combined rate. Prices are GST-inclusive, so this
	// decides the split of what was charged, never the amount charged.
	GSTRatePercent float64 `mapstructure:"gst-rate-percent"`
	// Series is the number prefix: MSC/26-27/000184. Three letters, because the
	// whole number must fit rule 46's sixteen characters.
	Series string `mapstructure:"series"`
	// MailFrom overrides mail.from for invoice emails (e.g. billing@). The SMTP
	// account must be allowed to send as it, or the provider rewrites it.
	MailFrom     string `mapstructure:"mail-from"`
	SupportEmail string `mapstructure:"support-email"`
	Website      string `mapstructure:"website"`
}

// gstinPattern is the GSTIN's shape: state code, PAN, entity number, 'Z', and a
// check character.
var gstinPattern = regexp.MustCompile(`^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][1-9A-Z]Z[0-9A-Z]$`)

// gstinChecksumOK verifies the GSTIN's fifteenth character, a mod-36 check
// over the first fourteen (the GSTN's Luhn variant, weights 2,1,2,1... from the
// right). The shape check alone passes a placeholder like the design's
// 29XXXXX0000X1ZX, and a GSTIN is exactly the value an operator will paste from
// a document; this is what stops a sample or a typo reaching an invoice.
func gstinChecksumOK(g string) bool {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	if len(g) != 15 {
		return false
	}
	factor, sum := 2, 0
	for i := 13; i >= 0; i-- {
		cp := strings.IndexByte(alphabet, g[i])
		if cp < 0 {
			return false
		}
		d := factor * cp
		factor = 3 - factor
		sum += d/36 + d%36
	}
	return alphabet[(36-sum%36)%36] == g[14]
}

// Issuable reports whether live orders can be given tax invoices.
func (c InvoiceConfig) Issuable() bool {
	return c.GSTIN != "" && c.SAC != ""
}

// validate returns the problems worth an operator's attention. A malformed
// GSTIN is not a boot failure — payments must keep working — but it is cleared,
// so live orders go un-invoiced (and say so) rather than print a bad number on
// every invoice until somebody notices.
func (c *InvoiceConfig) validate() []string {
	var warns []string
	c.GSTIN = strings.ToUpper(strings.TrimSpace(c.GSTIN))
	if c.GSTIN != "" {
		switch {
		case !gstinPattern.MatchString(c.GSTIN) || !gstinChecksumOK(c.GSTIN):
			warns = append(warns, "invoice.gstin is not a valid GSTIN; live orders will not be invoiced")
			c.GSTIN = ""
		case c.GSTIN[:2] != c.StateCode:
			warns = append(warns, fmt.Sprintf("invoice.gstin is registered in state %s but invoice.state-code is %s; "+
				"live orders will not be invoiced", c.GSTIN[:2], c.StateCode))
			c.GSTIN = ""
		}
	}
	if len(c.Series) != 3 {
		warns = append(warns, fmt.Sprintf("invoice.series must be 3 characters, got %q; using MSC", c.Series))
		c.Series = "MSC"
	}
	if c.GSTRatePercent <= 0 || c.GSTRatePercent >= 100 {
		warns = append(warns, fmt.Sprintf("invoice.gst-rate-percent %v is out of range; using 18", c.GSTRatePercent))
		c.GSTRatePercent = 18
	}
	return warns
}

// RendererConfig points at the headless-Chromium sidecar that prints the
// myScorr Advanced Report.
//
// A separate container rather than a browser inside this image: `apk add
// chromium` takes the API image from 17 MB to 1.16 GB, and a render's few
// hundred MB of RAM would spike inside the process serving requests. Empty URL
// -> no renderer, and that endpoint reports the report unavailable, the same
// unconfigured-upstream convention as S3, SMS and mail.
type RendererConfig struct {
	// URL is the DevTools websocket endpoint, e.g. ws://renderer:9222.
	URL string `mapstructure:"url"`
	// Timeout bounds one render: a user is waiting on this.
	Timeout time.Duration `mapstructure:"timeout"`
}

// S3Config configures the credit-report PDF store.
//
// No credentials: the service runs on EC2 with an instance role, so the AWS
// default credential chain supplies them and there is no long-lived key in the
// environment to leak or rotate. A developer machine picks up whatever the local
// AWS profile has, and an empty bucket selects the stub — the same
// unconfigured-upstream convention the Digitap, Cashfree and MSG91 clients use.
//
// Utho was the previous destination and is gone. Its API is S3-compatible, so
// moving back would mean an endpoint override here, not a second client.
type S3Config struct {
	// Bucket holds the encrypted report PDFs. Empty -> stub (no uploads).
	Bucket string `mapstructure:"bucket"`
	Region string `mapstructure:"region"`
	// PresignTTL bounds a download link's life. Short by default: the link goes
	// straight to a browser that follows it at once, so a longer window only
	// widens the period in which a leaked URL still works.
	PresignTTL time.Duration `mapstructure:"presign-ttl"`
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

	// Sandbox is a second credential pair, for internal builds while Mode is
	// production. ClientID/ClientSecret above always mean "the credentials for
	// Mode", so a deployment that has only ever run in sandbox needs nothing
	// here — its one gateway already is the sandbox one. Going live means
	// moving the TEST keys into this block and the live keys into the fields
	// above.
	Sandbox CashfreeCredentials `mapstructure:"sandbox"`

	// TestModeKey is the shared secret an internal build presents on
	// POST /orders to be put through the SANDBOX gateway. The store app and
	// the web app are built without it and so always pay in Mode.
	//
	// Why a key and not simply a flag the app sends: the order's environment
	// decides whether real money moves, and anything a client merely asserts,
	// a client can assert falsely. A flag would let anyone edit a request from
	// the store app, pay with sandbox money, and receive a real bureau pull.
	// The key is baked only into the APK shared with the internal WhatsApp
	// group (build-apk.sh in the app repo), so extracting it needs that APK.
	// Empty disables test payments entirely.
	TestModeKey string `mapstructure:"test-mode-key"`
}

// CashfreeCredentials is one Cashfree App ID / Secret Key pair.
type CashfreeCredentials struct {
	ClientID     string `mapstructure:"client-id"`
	ClientSecret string `mapstructure:"client-secret"`
}

// LogConfig holds structured-logging settings (log/slog). Level is one of
// debug/info/warn/error (default info); Format is json or text.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// SentryConfig configures error reporting. Errors only — panics and unmapped
// 500s — never the handled conditions the service maps deliberately.
type SentryConfig struct {
	// DSN empty disables reporting entirely, which is the default and what a
	// developer's machine runs with. Set it per environment, never in the
	// tracked config: it is a write credential for your Sentry project.
	DSN string `mapstructure:"dsn"`
	// Environment labels events in the Sentry UI ("production", "dev"). Left
	// empty it falls back to APP_PROFILE, so a deployment that sets neither is
	// still distinguishable from a laptop.
	Environment string `mapstructure:"environment"`
	// Release ties an event to the build it came from. The deploy script tags
	// images with the git SHA; passing the same value here is what makes
	// "first seen in this release" mean anything.
	Release string `mapstructure:"release"`
}

// CreditAnalyticsConfig holds policy for the paid bureau pull that is ours
// rather than the provider's.
type CreditAnalyticsConfig struct {
	// ReuseWindow is how recent a successful report must be to satisfy a PAID
	// check in place of calling Digitap. Checked after the paywall, never before,
	// and the purchase is spent on a reused report — see reusableReport.
	//
	// Bureau files move on lender-reporting cycles of roughly a month — the same
	// assumption behind the 30-day reportFreshWindow that decides whether a
	// report is shown as current — so a report a few days old is very nearly the
	// answer a fresh call would give, at no cost and no latency.
	//
	// An account WITH a purchase always gets a live pull; see reusableReport for
	// why. Zero disables reuse entirely. Set it to zero wherever you are testing
	// the upstream integration itself, since otherwise an unentitled caller never
	// reaches Digitap and an upstream regression would go unnoticed.
	ReuseWindow time.Duration `mapstructure:"reuse-window"`
}

// ScheduledChecksConfig drives the runner that executes the prepaid report
// runs a myScorr Plus purchase minted (scheduled_score_checks).
//
// The schedule itself is durable rows in the database; these knobs only shape
// how often we LOOK and how failures are retried. A missed sweep delays a run,
// it never loses one.
type ScheduledChecksConfig struct {
	// PollInterval is how often the runner sweeps for due rows. The semantics
	// are daily — a run is owed on a DATE — but the sweep is one cheap indexed
	// query, so polling hourly bounds both how late a run starts and how soon a
	// failed one is retried. The first sweep fires immediately at boot, so a
	// restart never skips a day.
	PollInterval time.Duration `mapstructure:"poll-interval"`
	// Timezone is the business day boundary (IANA name). "Due on 3 Oct" means
	// 3 Oct in this zone, whatever the server's own clock zone is.
	Timezone string `mapstructure:"timezone"`
	// MaxAttempts is when the runner stops retrying a failing run and marks it
	// FAILED for an operator. A paid run must never vanish silently.
	MaxAttempts int `mapstructure:"max-attempts"`
	// StaleAfter reclaims RUNNING rows a crash orphaned: a claim older than
	// this with no completion goes back to PENDING. The retry is safe — the
	// run's idempotency key replays the stored report if the pull landed.
	StaleAfter time.Duration `mapstructure:"stale-after"`
	// BatchSize caps rows claimed per sweep so one tick cannot monopolize the
	// vendor; the next tick picks up the rest.
	BatchSize int `mapstructure:"batch-size"`
}

// Location resolves Timezone, falling back to IST (the market this product
// serves) when the name doesn't load — a schedule that shifts by a few hours
// is better than a boot failure over a tzdata gap.
func (c ScheduledChecksConfig) Location() *time.Location {
	if loc, err := time.LoadLocation(c.Timezone); err == nil && c.Timezone != "" {
		return loc
	}
	return time.FixedZone("IST", 5*3600+1800)
}

// DigitapConfig holds credentials and endpoint settings for the Digitap Credit
// Analytics API (spec V2.7). When ClientID is empty, the service runs the
// client in an offline stub mode.
type DigitapConfig struct {
	BaseURL      string        `mapstructure:"base-url"`
	ClientID     string        `mapstructure:"client-id"`
	ClientSecret string        `mapstructure:"client-secret"`
	Timeout      time.Duration `mapstructure:"timeout"`

	// LogRequestCurl logs each Credit Analytics call as a copy-pasteable curl
	// command just before it goes out, for reproducing an upstream failure by
	// hand.
	//
	// DEVELOPER MACHINES ONLY. The command embeds the account's PAN, full name
	// and mobile number, plus our Digitap client secret in the -u flag — so
	// enabling it turns the application log into a store of bureau-grade
	// personal data and a credential leak. Load refuses to start the service if
	// it is set under any profile other than dev/local, the same way it refuses
	// a non-local auth.otp.master-code.
	LogRequestCurl bool `mapstructure:"log-request-curl"`

	Prefill PrefillConfig `mapstructure:"prefill"`
}

// PrefillStubSentinel, set as digitap.prefill.client-id, forces the offline
// prefill stub even when digitap.client-id holds working credentials. It mirrors
// sms.provider: "stub".
//
// It exists because an EMPTY prefill client id means "borrow the Credit
// Analytics pair", so emptiness cannot also mean "run offline". Without the
// sentinel there is no way to stub PAN verification while credit-analytics talks
// to a real upstream — which is exactly what Digitap's UAT needs, since the UAT
// client id carries Credit Analytics but not the prefill name-lookup service.
const PrefillStubSentinel = "stub"

// ResolvePrefillCredentials picks the credentials the Mobile to Prefill client
// should use, and reports whether the offline stub was explicitly forced.
//
// Precedence: the sentinel wins, then an explicit prefill pair, then the Credit
// Analytics pair. A forced stub returns empty strings, which is what
// digitap.NewPrefill reads as "stub".
func (d DigitapConfig) ResolvePrefillCredentials() (clientID, clientSecret string, forcedStub bool) {
	if strings.EqualFold(strings.TrimSpace(d.Prefill.ClientID), PrefillStubSentinel) {
		return "", "", true
	}
	if strings.TrimSpace(d.Prefill.ClientID) != "" {
		return d.Prefill.ClientID, d.Prefill.ClientSecret, false
	}
	return d.ClientID, d.ClientSecret, false
}

// PrefillConfig configures the Digitap Mobile to Prefill API (spec v1.4), used
// to confirm at signup that a PAN and name belong to the mobile number the user
// just verified over SMS.
//
// It is a separate product from Credit Analytics above, on a different host and
// — per the spec — a separately provisioned client id. ClientID/ClientSecret
// therefore stand alone, but fall back to the Credit Analytics credentials when
// left empty, since one Digitap account often covers both. Empty after that
// fallback means the offline stub.
type PrefillConfig struct {
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
// Provider selects the sender outright: "stub" never contacts a provider, and
// is how a local run avoids texting real people while a real auth key sits in
// config.dev.yaml. Any other value falls back to the empty-credentials-⇒-stub
// convention shared with mail, Cashfree, Digitap and S3 — an empty auth key
// also yields the stub.
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
	// MasterCode is a fixed code accepted in place of any real OTP, so a sign-in
	// can be completed on a machine with no SMS or SMTP provider configured.
	//
	// It is an unconditional authentication bypass across every OTP flow, so it
	// is empty by default — which is the value baked into the tracked config and
	// therefore into every image — and Load REFUSES TO START the service if it
	// is set under a non-local APP_PROFILE. Set it in the gitignored
	// config.dev.yaml, or via AUTH_OTP_MASTER_CODE, and nowhere else.
	MasterCode string `mapstructure:"master-code"`
}

type PANConfig struct {
	NameMatchDistance int `mapstructure:"name-match-distance"`
	// MaxVerificationAttempts caps failed provider checks per submitted PAN.
	// PAN-plus-name is guessable for a known person, so an uncapped retry loop
	// is a brute-force oracle billed to us per call.
	MaxVerificationAttempts int `mapstructure:"max-verification-attempts"`
	// DocumentMaxSize caps a PAN card document upload (parsed via
	// server.ParseSize, e.g. "10MB").
	DocumentMaxSize string `mapstructure:"document-max-size"`
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

	cfg.sanitizeMailPassword()

	if err := cfg.validateLocalOnly(profile); err != nil {
		return nil, err
	}
	if err := cfg.Cashfree.validate(); err != nil {
		return nil, err
	}
	cfg.Warnings = append(cfg.Warnings, cfg.Invoice.validate()...)

	return &cfg, nil
}

// validate refuses a payments configuration that would take money wrongly.
//
// The hard failure is production mode without credentials. An empty client-id
// selects the stub gateway, and the stub accepts ANY webhook signature — the
// right behaviour on a laptop with no keys, and in production a way for anyone
// to POST a "payment succeeded" webhook and have an order fulfilled. So a live
// deployment with no live keys does not start rather than start as that.
func (c CashfreeConfig) validate() error {
	switch c.Mode {
	case "sandbox", "production":
	default:
		return fmt.Errorf("cashfree.mode must be sandbox or production, got %q", c.Mode)
	}
	if c.Mode == "production" && (c.ClientID == "" || c.ClientSecret == "") {
		return fmt.Errorf("cashfree.mode is production but cashfree.client-id/client-secret " +
			"are empty; refusing to start on the stub gateway, which accepts any webhook")
	}
	if (c.Sandbox.ClientID == "") != (c.Sandbox.ClientSecret == "") {
		return fmt.Errorf("cashfree.sandbox needs both client-id and client-secret, or neither")
	}
	return nil
}

// googleSMTPHosts are Google's SMTP endpoints. An app password for one of these
// is provably 16 lower-case letters with no spaces, which is what lets
// sanitizeMailPassword repair a pasted one instead of only complaining about it.
var googleSMTPHosts = map[string]bool{
	"smtp.gmail.com":      true,
	"smtp.googlemail.com": true,
}

// googleAppPasswordLen is the length Google generates. Not a validation rule --
// only the basis for a warning, since Google could change it.
const googleAppPasswordLen = 16

// sanitizeMailPassword repairs a mail password pasted in Google's display form,
// recording what it did in Warnings.
//
// Google shows an app password as four groups of four ("xxxx xxxx xxxx xxxx")
// and expects it entered without the spaces. Pasting what is displayed is the
// obvious mistake, and it took every transactional email down for two days in
// August 2026: SMTP AUTH fails, so the password-reset OTP, the signup OTP,
// contact linking and report delivery all break together while phone sign-in
// over SMS keeps working -- which reads as a mail-account problem rather than a
// config one, and is why this is worth catching at boot.
//
// On a Google host the whitespace cannot be part of the secret, so it is
// stripped. Refusing to boot would trade broken email for a dead API, and mail
// here already degrades rather than dies (an empty host selects the stub
// sender). On any other host the password is passed through exactly as
// configured -- a space may genuinely belong to it -- and only reported.
//
// The warnings carry lengths, never the password.
func (c *Config) sanitizeMailPassword() {
	if c.Mail.Host == "" || c.Mail.Password == "" {
		return
	}
	host := strings.ToLower(strings.TrimSpace(c.Mail.Host))
	google := googleSMTPHosts[host]

	if strings.ContainsFunc(c.Mail.Password, unicode.IsSpace) {
		if !google {
			c.Warnings = append(c.Warnings, fmt.Sprintf(
				"mail.password for host %q contains whitespace, which most SMTP servers "+
					"reject; sending it exactly as configured", host))
			return
		}
		stripped := strings.Join(strings.Fields(c.Mail.Password), "")
		c.Mail.Password = stripped
		c.Warnings = append(c.Warnings, fmt.Sprintf(
			"mail.password contained whitespace and was stripped to %d characters: a Google "+
				"app password is displayed in groups of four but must be sent without the "+
				"spaces. Correct MAIL_PASSWORD so this stops appearing", len(stripped)))
	}

	if google && len(c.Mail.Password) != googleAppPasswordLen {
		c.Warnings = append(c.Warnings, fmt.Sprintf(
			"mail.password is %d characters; a Google app password is %d. If this is the "+
				"account's own password, SMTP AUTH will reject it -- generate an app password",
			len(c.Mail.Password), googleAppPasswordLen))
	}
}

// localProfiles are the APP_PROFILE values that count as a developer machine.
//
// An empty profile is deliberately NOT one of them: a deployment runs config.yaml
// with no overlay, so treating "" as local would make the bypasses below available
// in exactly the place they must never be. Fail closed — a new local profile has
// to be added here on purpose.
var localProfiles = map[string]bool{"dev": true, "local": true}

// validateLocalOnly refuses to start when a local-development bypass is set under
// a profile that is not local.
//
// Refusing rather than quietly ignoring it: a deployment carrying an
// authentication bypass in its config is a mistake that must be seen, and a
// service that boots anyway hides it until someone tries "1234" in production.
//
// Three keys are checked. registration.otp.master-code is deliberately not one
// of them: it is not wired to any service (NewOTPService is built from Auth.OTP
// alone), so a master code set there does nothing, and guarding it would imply
// otherwise.
func (c *Config) validateLocalOnly(profile string) error {
	// demo.enabled auto-verifies any submitted PAN with provider='demo' and no
	// provider call at all, which reduces KYC to a text field: nothing checks
	// that the PAN exists, belongs to the user, or matches their mobile. It is
	// the most consequential of the three, because credit analytics is gated on
	// a VERIFIED PAN and this hands out that status for free.
	//
	// Checked before the master-code block below, which returns early.
	if c.Demo.Enabled && !localProfiles[profile] {
		return fmt.Errorf(
			"demo.enabled is true under APP_PROFILE=%q, which is not a local profile: "+
				"demo mode auto-verifies every PAN submitted to POST /api/kyc/pan without "+
				"calling the verification provider, so no account's identity would be checked "+
				"at all (local profiles: dev, local). Unset it in config, or set "+
				"DEMO_ENABLED=false",
			profile)
	}

	if c.Digitap.LogRequestCurl && !localProfiles[profile] {
		return fmt.Errorf(
			"digitap.log-request-curl is set under APP_PROFILE=%q, which is not a local profile: "+
				"the logged command embeds the account's PAN, full name and mobile number "+
				"together with our Digitap client secret, so it must only ever be written on a "+
				"developer machine (local profiles: dev, local). Unset it in config, or unset "+
				"DIGITAP_LOG_REQUEST_CURL",
			profile)
	}

	if c.Auth.OTP.MasterCode == "" {
		return nil
	}
	if !localProfiles[profile] {
		return fmt.Errorf(
			"auth.otp.master-code is set under APP_PROFILE=%q, which is not a local profile: "+
				"the master code is an authentication bypass for every OTP flow and must only "+
				"exist on a developer machine (local profiles: dev, local). Unset it in config, "+
				"or unset AUTH_OTP_MASTER_CODE",
			profile)
	}
	if len(c.Auth.OTP.MasterCode) != c.Auth.OTP.Length {
		return fmt.Errorf(
			"auth.otp.master-code must be %d digits to match auth.otp.length; the app's code "+
				"field stops accepting input at that many, so a longer one cannot be typed in",
			c.Auth.OTP.Length)
	}
	return nil
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
	v.SetDefault("mail.from", "myScorr <noreply@myscorr.com>")

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
	v.SetDefault("auth.otp.length", 4)
	v.SetDefault("auth.otp.ttl", "10m")
	v.SetDefault("auth.otp.resend-cooldown", "30s")
	v.SetDefault("auth.otp.max-attempts", 5)
	v.SetDefault("auth.otp.max-sends", 5)
	// Empty: no master code. See OTPConfig.MasterCode — this default is what
	// ships in the image, and the service refuses to boot with it set outside a
	// local profile.
	v.SetDefault("auth.otp.master-code", "")
	// Admin allowlist: accounts whose email matches get role=admin at verify/login.
	// Defaults to empty (no admins). Set via AUTH_ADMIN_EMAILS=a@x.com,b@y.com.
	v.SetDefault("auth.admin-emails", []string{})
	// Google OAuth ID-token login. Empty client-id disables the flow.
	// Set via AUTH_GOOGLE_CLIENT_ID (or google.client-id in the config file).
	v.SetDefault("auth.google.client-id", "")

	v.SetDefault("multipart.max-file-size", "5MB")
	v.SetDefault("multipart.max-request-size", "10MB")

	v.SetDefault("registration.pan-image-dir", "./data/pan-images")
	v.SetDefault("registration.otp.length", 4)
	v.SetDefault("registration.otp.ttl", "5m")
	v.SetDefault("registration.otp.resend-cooldown", "30s")
	v.SetDefault("registration.otp.max-attempts", 5)
	v.SetDefault("registration.otp.max-sends", 5)
	v.SetDefault("registration.pan.name-match-distance", 2)
	v.SetDefault("registration.pan.max-verification-attempts", 3)
	v.SetDefault("registration.pan.document-max-size", "10MB")
	v.SetDefault("registration.ocr.provider", "stub")
	v.SetDefault("registration.ocr.min-confidence", 0.8)

	// Digitap Credit Analytics API. Empty client-id -> offline stub client.
	// 7 days: long enough to make repeated testing free, short enough that a
	// report handed to a user is never described as current when the bureau may
	// have moved on. See CreditAnalyticsConfig.ReuseWindow.
	v.SetDefault("credit-analytics.reuse-window", "168h")
	// Scheduled score checks (myScorr Plus). Hourly sweep of a daily schedule:
	// see ScheduledChecksConfig for why the poll is tighter than the semantics.
	v.SetDefault("scheduled-checks.poll-interval", "1h")
	v.SetDefault("scheduled-checks.timezone", "Asia/Kolkata")
	v.SetDefault("scheduled-checks.max-attempts", 5)
	v.SetDefault("scheduled-checks.stale-after", "15m")
	v.SetDefault("scheduled-checks.batch-size", 25)
	v.SetDefault("digitap.base-url", "https://api.digitap.ai/")
	v.SetDefault("digitap.client-id", "")
	v.SetDefault("digitap.client-secret", "")
	v.SetDefault("digitap.timeout", "30s")
	v.SetDefault("digitap.log-request-curl", false)
	// Mobile to Prefill. Production host by default: unlike Credit Analytics
	// there is no demo tier in use here, and a UAT host answering production
	// credentials fails as a 401 that reads like bad credentials.
	v.SetDefault("digitap.prefill.base-url", "https://api.digitap.ai/")
	v.SetDefault("digitap.prefill.client-id", "")
	v.SetDefault("digitap.prefill.client-secret", "")
	v.SetDefault("digitap.prefill.timeout", "30s")

	// Structured logging (log/slog). Override level via LOG_LEVEL.
	// Format defaults to "text" (human-readable) and falls back to it on any
	// unrecognized value; set "json" for production/structured ingestion.
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
	// Reporting is opt-in: no DSN, no events. See SentryConfig.
	v.SetDefault("sentry.dsn", "")
	v.SetDefault("sentry.environment", "")
	v.SetDefault("sentry.release", "")

	v.SetDefault("cashfree.mode", "sandbox")
	v.SetDefault("cashfree.api-version", "2025-01-01")
	v.SetDefault("cashfree.timeout", "15s")

	// Bank-statement analysis. Parser defaults to "pdf" (the real text-layer
	// reader); set "stub" for offline/CI runs that don't have a PDF. The worker
	// pool is small by default — analysis is CPU-bound, not latency-sensitive.
	// Report PDF store. Empty bucket -> stub, so a dev machine needs no AWS.
	v.SetDefault("s3.bucket", "")
	v.SetDefault("s3.region", "ap-south-1")
	v.SetDefault("s3.presign-ttl", "10m")

	v.SetDefault("renderer.url", "")
	v.SetDefault("renderer.timeout", "45s")

	// Tax invoices. The legal name and address are the company's public
	// details (the website footer carries the same); GSTIN and SAC have no
	// default on purpose -- see InvoiceConfig.
	v.SetDefault("invoice.legal-name", "Reachout Tech Private Limited")
	v.SetDefault("invoice.address", "22, 4th Floor, 1st Main, Royal Placid, Haralur, HSR Layout, "+
		"Bangalore South, Karnataka 560102")
	v.SetDefault("invoice.state-name", "Karnataka")
	v.SetDefault("invoice.state-code", "29")
	v.SetDefault("invoice.gstin", "")
	v.SetDefault("invoice.sac", "")
	v.SetDefault("invoice.gst-rate-percent", 18)
	v.SetDefault("invoice.series", "MSC")
	v.SetDefault("invoice.mail-from", "")
	v.SetDefault("invoice.support-email", "alerts@myscorr.com")
	v.SetDefault("invoice.website", "myscorr.com")
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
		"registration.pan.max-verification-attempts",
		"registration.pan.document-max-size",
		"registration.ocr.provider", "registration.ocr.min-confidence",
		"credit-analytics.reuse-window",
		"scheduled-checks.poll-interval", "scheduled-checks.timezone",
		"scheduled-checks.max-attempts", "scheduled-checks.stale-after",
		"scheduled-checks.batch-size",
		"digitap.base-url", "digitap.client-id", "digitap.client-secret", "digitap.timeout",
		"digitap.log-request-curl",
		"digitap.prefill.base-url", "digitap.prefill.client-id",
		"digitap.prefill.client-secret", "digitap.prefill.timeout",
		"log.level", "log.format",
		"sentry.dsn", "sentry.environment", "sentry.release",
		"cashfree.mode", "cashfree.base-url", "cashfree.client-id",
		"cashfree.client-secret", "cashfree.api-version",
		"cashfree.return-url", "cashfree.notify-url", "cashfree.timeout",
		"cashfree.sandbox.client-id", "cashfree.sandbox.client-secret",
		"cashfree.test-mode-key",
		"s3.bucket", "s3.region", "s3.presign-ttl",
		"renderer.url", "renderer.timeout",
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
