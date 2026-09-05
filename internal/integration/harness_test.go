// Package integration holds end-to-end regression tests that run the real HTTP
// handlers, the real services, and a real Postgres — everything except the
// third parties.
//
// What is stubbed and why:
//   - SMS (sms.NewStubSender) and mail (empty MAIL_HOST) send nothing. Combined
//     with a master OTP code, that is what lets a test complete a phone sign-in
//     without a provider, and it is the same pair of switches a developer runs
//     locally, so the tests exercise the configuration people actually use.
//   - Digitap Mobile-to-Prefill runs its offline stub (empty credentials), which
//     claims every number belongs to JOHN DOE / ABCDE1234F. PAN verification is
//     therefore testable without billing a provider per call.
//
// What is NOT stubbed: the database. These tests run against a real local
// Postgres so the referral attribution, the partial unique index behind
// one-code-per-account, and the report's GROUP BY are exercised as written
// rather than as a fake remembers them.
//
// # Running them
//
//	go test ./internal/integration/...
//
// The DSN comes from TEST_DATABASE_URL, defaulting to the same local Postgres
// the dev profile uses but in a SEPARATE SCHEMA (report_test). The dev schema
// holds a restore of production data; a test suite that truncates tables must
// never be one typo away from it. When no database answers, every test in the
// package SKIPS rather than fails, so `go test ./...` stays green on a machine
// without Postgres.
package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/config"
	"credit-report-service/internal/db"
	"credit-report-service/internal/digitap"
	"credit-report-service/internal/handler"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
	"credit-report-service/internal/server"
	"credit-report-service/internal/service"
	"credit-report-service/internal/sms"
)

const (
	// defaultTestDSN mirrors config.dev.yaml's database but points at its own
	// schema. See the package comment for why that separation is not optional.
	defaultTestDSN = "postgres://scorr:scorr@localhost:5432/credit?sslmode=disable&search_path=report_test"

	// testMasterOTP stands in for every real code. config.Load refuses to boot a
	// non-local profile with this set; these tests build the config in memory and
	// never go through Load, so the guard is not in play — which is also why the
	// value is only ever assigned here.
	testMasterOTP = "1234"

	// The offline prefill stub answers with this person for every mobile number.
	stubPAN      = "ABCDE1234F"
	stubPANName  = "JOHN DOE"
	panNameWrong = "SOMEONE ELSE"
)

// harness is one test's view of the running service.
type harness struct {
	t       *testing.T
	app     *fiber.App
	pool    *pgxpool.Pool
	accts   *repository.AccountRepo
	baseCtx context.Context
}

// newHarness migrates a clean schema and builds the app over it.
//
// The schema is dropped and recreated per test rather than truncated between
// them: the referral report groups over every account in a window, so one test's
// leftovers would show up in another's totals as a flake that only appears when
// the suite runs in a particular order.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = defaultTestDSN
	}
	cfg := testConfig(dsn)

	if err := resetSchema(ctx, cfg.DB); err != nil {
		t.Skipf("no test database reachable (%v); set TEST_DATABASE_URL to run these", err)
	}
	if err := db.Migrate(ctx, cfg.DB); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}
	pool, err := db.New(ctx, cfg.DB)
	if err != nil {
		t.Fatalf("open test pool: %v", err)
	}
	t.Cleanup(pool.Close)

	h := &harness{t: t, pool: pool, baseCtx: ctx, accts: repository.NewAccountRepo(pool)}
	h.app = buildApp(cfg, pool)
	return h
}

// testConfig is the local-dev configuration with every outbound integration
// switched off. Built in memory rather than read from config.yaml so a change
// to a developer's gitignored overlay cannot quietly turn a real provider on
// inside the test suite.
func testConfig(dsn string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:           0,
			MaxRequestBody: "8MB",
			// A concrete origin, not the wildcard: the CORS middleware panics at
			// construction when credentials are allowed against "*", so leaving
			// this empty would fail every test with a setup panic rather than an
			// assertion. This is the web dev server's origin.
			CORSOrigins: "http://localhost:8081",
		},
		DB: config.DBConfig{DSN: dsn, MaxPoolSize: 4, MinIdle: 0},
		Auth: config.AuthConfig{
			JWTSecret:  "integration-test-secret-not-a-real-key",
			AccessTTL:  time.Hour,
			RefreshTTL: 24 * time.Hour,
			OTP: config.OTPConfig{
				Length:      4,
				TTL:         10 * time.Minute,
				MaxAttempts: 5,
				MaxSends:    5,
				// No resend cooldown: the cooldown is its own behaviour with its
				// own tests, and leaving it on here would make every test that
				// sends two codes sleep through it.
				ResendCooldown: 0,
				MasterCode:     testMasterOTP,
			},
		},
		// Empty host selects the mail stub, so nothing is sent and no code is logged.
		Mail: config.MailConfig{Host: ""},
		SMS:  config.SMSConfig{Provider: "stub"},
		Registration: config.RegistrationConfig{
			PAN: config.PANConfig{NameMatchDistance: 2, MaxVerificationAttempts: 3},
			OTP: config.OTPConfig{Length: 4, TTL: 10 * time.Minute, MaxAttempts: 5, MaxSends: 5},
		},
		// Empty prefill credentials select the offline Digitap stub.
		Digitap:   config.DigitapConfig{},
		Multipart: config.MultipartConfig{MaxFileSize: "8MB", MaxRequestSize: "8MB"},
	}
}

// resetSchema drops and recreates the test schema, so each test starts on an
// empty database. It doubles as the reachability probe: a connection failure
// here is what makes the suite skip.
func resetSchema(ctx context.Context, cfg config.DBConfig) error {
	schema := schemaOf(cfg.DSN)
	// Connect without the search_path — the schema it names is about to not exist.
	admin, err := pgxpool.New(ctx, stripSearchPath(cfg.DSN))
	if err != nil {
		return err
	}
	defer admin.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := admin.Ping(pingCtx); err != nil {
		return err
	}
	if _, err := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		return err
	}
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	return err
}

func schemaOf(dsn string) string {
	_, after, found := strings.Cut(dsn, "search_path=")
	if !found {
		return "public"
	}
	name, _, _ := strings.Cut(after, "&")
	return name
}

func stripSearchPath(dsn string) string {
	before, after, found := strings.Cut(dsn, "search_path=")
	if !found {
		return dsn
	}
	_, rest, _ := strings.Cut(after, "&")
	return strings.TrimRight(before, "?&") + func() string {
		if rest == "" {
			return ""
		}
		return "&" + rest
	}()
}

// buildApp wires the slice of the service the signup and referral flows touch.
//
// Handlers outside that slice are passed as nil. server.New only stores them
// into route closures, so an unexercised nil never gets dereferenced — and
// building the real analytics, orders and statement stacks here would drag in
// S3, Cashfree and a worker pool for tests that call none of them. Routing and
// the auth/permission middleware are the real ones, which is the part that
// matters: the admin report's permission gate is under test, not mocked out.
func buildApp(cfg *config.Config, pool *pgxpool.Pool) *fiber.App {
	accountRepo := repository.NewAccountRepo(pool)
	sessionRepo := repository.NewSessionRepo(pool)
	couponRepo := repository.NewCouponRepo(pool)
	orderRepo := repository.NewOrderRepo(pool)

	otpSvc := service.NewOTPService(cfg.Auth.OTP)
	mailSvc := service.NewMailService(cfg.Mail, cfg.Auth.OTP.TTL)
	tokenSvc := service.NewTokenService(cfg.Auth)
	sessionSvc := service.NewSessionService(sessionRepo, cfg.Auth)
	couponSvc := service.NewCouponService(couponRepo, orderRepo)
	authSvc := service.NewAuthService(
		accountRepo, otpSvc, mailSvc, sms.NewStubSender(), tokenSvc, sessionSvc, couponSvc, cfg.Auth)

	prefillID, prefillSecret, _ := cfg.Digitap.ResolvePrefillCredentials()
	prefillClient := digitap.NewPrefill(digitap.PrefillConfig{
		BaseURL:      cfg.Digitap.Prefill.BaseURL,
		ClientID:     prefillID,
		ClientSecret: prefillSecret,
		Timeout:      cfg.Digitap.Prefill.Timeout,
	})
	kycSvc := service.NewKycService(
		accountRepo,
		service.NewPrefillVerifier(prefillClient, cfg.Registration.PAN),
		cfg.Registration.PAN,
		false, // demo mode off: the stub is the seam, not an auto-verify shortcut
	)

	// Credit analytics is wired because the app's Home screen depends on how this
	// endpoint answers for a report that carries no score — see the no-score test.
	// The Digitap client is the offline stub; nothing here calls upstream.
	analyticsSvc := service.NewCreditAnalyticsService(
		digitap.New(digitap.Config{}),
		repository.NewCreditAnalyticsRepo(pool),
		accountRepo,
		orderRepo,
		cfg.CreditAnalytics,
	)

	referralH := handler.NewAdminReferralHandler(
		service.NewReferralService(repository.NewReferralRepo(pool), accountRepo))

	return server.New(
		cfg,
		handler.NewHealthHandler(),
		handler.NewAuthHandler(authSvc, sessionSvc, cfg.Auth.CookieSecure),
		handler.NewCreditAnalyticsHandler(analyticsSvc),
		// 10MB doc cap, mirroring the registration.pan.document-max-size default.
		handler.NewKycHandler(kycSvc, 10_000_000),
		nil, // orders
		handler.NewCouponHandler(couponSvc),
		nil, // loans
		nil, // score builder
		nil, // bank statements
		nil, // admin account reset
		referralH,
		tokenSvc,
		accountRepo,
	)
}

// ---- HTTP helpers ---------------------------------------------------------

// response is a decoded HTTP reply. Body is kept as a map so a test can assert
// on one field without a DTO per endpoint.
type response struct {
	Status int
	Body   map[string]any
	Raw    string
}

func (h *harness) post(path, token string, body any) response {
	return h.do(http.MethodPost, path, token, body)
}

func (h *harness) get(path, token string) response {
	return h.do(http.MethodGet, path, token, nil)
}

func (h *harness) do(method, path, token string, body any) response {
	h.t.Helper()

	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("encode request body: %v", err)
		}
		reader = strings.NewReader(string(encoded))
	} else {
		reader = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	// -1 disables Fiber's test timeout; a cold pool plus a first-call migration
	// check can outrun the default and turn a passing test into a flake.
	res, err := h.app.Test(req, -1)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read %s %s body: %v", method, path, err)
	}
	out := response{Status: res.StatusCode, Raw: string(raw)}
	// A 204 and an error page are both legitimately un-decodable; the caller
	// asserts on Status and Raw in those cases.
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

// ---- flow helpers ---------------------------------------------------------

// signInByPhone runs the full phone sign-in: send the code, then verify it with
// the master OTP. Returns the access token and the account id.
//
// referralCode is passed through to the verify call, where the server honours it
// only if this is the number's first sign-in.
func (h *harness) signInByPhone(phone, referralCode string) (token string, accountID int64) {
	h.t.Helper()

	sent := h.post("/api/auth/otp/phone/send", "", map[string]string{"phone": phone})
	if sent.Status != http.StatusOK {
		h.t.Fatalf("send phone otp for %s: %d %s", phone, sent.Status, sent.Raw)
	}

	body := map[string]string{"phone": phone, "otp": testMasterOTP}
	if referralCode != "" {
		body["referralCode"] = referralCode
	}
	verified := h.post("/api/auth/otp/phone/verify", "", body)
	if verified.Status != http.StatusOK {
		h.t.Fatalf("verify phone otp for %s: %d %s", phone, verified.Status, verified.Raw)
	}
	return h.tokenAndID(verified)
}

// tokenAndID pulls the access token and account id out of an auth response.
func (h *harness) tokenAndID(res response) (string, int64) {
	h.t.Helper()

	token, _ := res.Body["token"].(string)
	if token == "" {
		h.t.Fatalf("no token in auth response: %s", res.Raw)
	}
	account, _ := res.Body["account"].(map[string]any)
	id, _ := account["id"].(float64)
	if id == 0 {
		h.t.Fatalf("no account id in auth response: %s", res.Raw)
	}
	return token, int64(id)
}

// referralCodeOf reads (and therefore mints) an account's own referral code.
func (h *harness) referralCodeOf(token string) string {
	h.t.Helper()

	res := h.get("/api/coupons/referral", token)
	if res.Status != http.StatusOK {
		h.t.Fatalf("get referral code: %d %s", res.Status, res.Raw)
	}
	code, _ := res.Body["code"].(string)
	if code == "" {
		h.t.Fatalf("no code in referral response: %s", res.Raw)
	}
	return code
}

// makeAdmin promotes an account and returns a token carrying the new role.
//
// The role is written directly and the account signs in again, rather than
// going through PUT /admin/accounts/:id/role — that route needs an admin to
// call it, and a test suite starting from an empty database has none.
func (h *harness) makeAdmin(phone string) string {
	h.t.Helper()

	_, id := h.signInByPhone(phone, "")
	if _, err := h.accts.SetRole(h.baseCtx, id, models.RoleAdmin); err != nil {
		h.t.Fatalf("promote %d to admin: %v", id, err)
	}
	// The role is a JWT claim, so the token issued before the promotion still
	// says "user". Sign in again to mint one that says admin.
	token, _ := h.signInByPhone(phone, "")
	return token
}

// verifyPAN submits a PAN through the offline Digitap prefill stub.
func (h *harness) verifyPAN(token, pan, name string) response {
	return h.post("/api/kyc/pan", token, map[string]string{"pan": pan, "fullName": name})
}

// referredAtFor back-dates a referral so a test can put a signup outside the
// report window. There is no API for this by design — attribution is written
// once, at signup — so the test reaches for the column directly.
func (h *harness) backdateReferral(accountID int64, at time.Time) {
	h.t.Helper()

	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE accounts SET referred_at = $2 WHERE id = $1`, accountID, at); err != nil {
		h.t.Fatalf("backdate referral for %d: %v", accountID, err)
	}
}

// attributionOf reads the referral columns straight off the account row.
//
// Asserted at the database rather than through an API response because the
// attribution IS the feature: an endpoint could report a referrer it never
// persisted, and that is exactly the regression these tests exist to catch.
// Returns (0, "") for an unattributed account.
func (h *harness) attributionOf(accountID int64) (referrerID int64, code string) {
	h.t.Helper()

	var gotID *int64
	var gotCode *string
	err := h.pool.QueryRow(h.baseCtx,
		`SELECT referred_by_account_id, referred_by_code FROM accounts WHERE id = $1`,
		accountID).Scan(&gotID, &gotCode)
	if err != nil {
		h.t.Fatalf("read attribution for %d: %v", accountID, err)
	}
	if gotID != nil {
		referrerID = *gotID
	}
	if gotCode != nil {
		code = *gotCode
	}
	return referrerID, code
}

func (h *harness) countAccounts() int {
	h.t.Helper()

	var n int
	if err := h.pool.QueryRow(h.baseCtx, `SELECT COUNT(*) FROM accounts`).Scan(&n); err != nil {
		h.t.Fatalf("count accounts: %v", err)
	}
	return n
}

// referralReport calls the admin endpoint. Blank dates ask for the server's
// default window; a zero referrerID leaves the list unfiltered.
func (h *harness) referralReport(token, from, to string, referrerID int64) map[string]any {
	h.t.Helper()

	path := "/api/admin/referrals"
	params := url.Values{}
	if from != "" {
		params.Set("from", from)
	}
	if to != "" {
		params.Set("to", to)
	}
	if referrerID != 0 {
		params.Set("referrerId", strconv.FormatInt(referrerID, 10))
	}
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	res := h.get(path, token)
	if res.Status != http.StatusOK {
		h.t.Fatalf("referral report: %d %s", res.Status, res.Raw)
	}
	return res.Body
}

// ---- assertion helpers ----------------------------------------------------

// intOf reads a JSON number, which decodes into any as a float64.
func intOf(v any) int {
	n, _ := v.(float64)
	return int(n)
}

func listOf(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// findReferred returns the report row for an account, failing the test when it
// is absent — "not in the report" is the failure worth naming, and every caller
// would otherwise repeat the same nil check.
func findReferred(t *testing.T, report map[string]any, accountID int64) map[string]any {
	t.Helper()

	page, _ := report["referred"].(map[string]any)
	for _, row := range listOf(page["items"]) {
		if int64(intOf(row["accountId"])) == accountID {
			return row
		}
	}
	t.Fatalf("account %d is not in the referral report: %v", accountID, page)
	return nil
}

// referralReportBy narrows the report to whoever holds a phone number or email
// address, the way an operator would.
func (h *harness) referralReportBy(token, identifier string) map[string]any {
	h.t.Helper()

	res := h.get("/api/admin/referrals?identifier="+url.QueryEscape(identifier), token)
	if res.Status != http.StatusOK {
		h.t.Fatalf("referral report by %q: %d %s", identifier, res.Status, res.Raw)
	}
	return res.Body
}

// insertNoRecordReport stores the report a bureau pull writes when Digitap
// answers result 102, "no record found": a real row, a real payload, and no
// SCORE block at all. Written directly because reproducing it through the pull
// would mean driving the upstream client to return nothing, which is the one
// thing the offline stub does not do — it always synthesizes a scored report.
func (h *harness) insertNoRecordReport(accountID int64) int64 {
	h.t.Helper()

	const body = `{"result_json":{"INProfileResponse":{
		"Header":{"SystemCode":"0","ReportDate":"20260830","ReportTime":"101500"},
		"UserMessage":{"UserMessageText":"No record found"},
		"CAIS_Account":{"CAIS_Account_DETAILS":[]},
		"TotalCAPS_Summary":{"TotalCAPSLast180Days":"0","TotalCAPSLast90Days":"0",
		                     "TotalCAPSLast30Days":"0","TotalCAPSLast7Days":"0"}}}}`

	return h.insertReport(accountID, body, nil, 102, "no record found")
}

// insertScoredReport stores an ordinary successful pull carrying a bureau score.
func (h *harness) insertScoredReport(accountID int64, score int) int64 {
	h.t.Helper()

	body := `{"result_json":{"INProfileResponse":{
		"Header":{"SystemCode":"0","ReportDate":"20260830","ReportTime":"101500"},
		"CAIS_Account":{"CAIS_Account_DETAILS":[]},
		"TotalCAPS_Summary":{"TotalCAPSLast180Days":"0","TotalCAPSLast90Days":"0",
		                     "TotalCAPSLast30Days":"0","TotalCAPSLast7Days":"0"},
		"SCORE":{"BureauScore":"` + strconv.Itoa(score) + `","BureauScoreConfidLevel":"H"}}}}`

	return h.insertReport(accountID, body, &score, 101, "success")
}

func (h *harness) insertReport(
	accountID int64, body string, score *int, resultCode int, message string,
) int64 {
	h.t.Helper()

	var id int64
	err := h.pool.QueryRow(h.baseCtx,
		`INSERT INTO credit_analytics_requests
		     (account_id, client_ref_num, mobile_no, request_id, result_code,
		      http_status, message, request_body, response_body, credit_score)
		 VALUES ($1, 'CA-TEST', 'XXXXXX0000', 'test-req', $2, 200, $3,
		         '{}'::jsonb, $4::jsonb, $5)
		 RETURNING id`,
		accountID, resultCode, message, body, score).Scan(&id)
	if err != nil {
		h.t.Fatalf("insert report for account %d: %v", accountID, err)
	}
	return id
}
