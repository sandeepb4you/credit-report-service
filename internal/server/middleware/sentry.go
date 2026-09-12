// Error reporting to Sentry.
//
// Two things reach it and nothing else: a panic, and a response that came out as
// a 500. Everything the service maps deliberately — a wrong OTP, a 409 on an
// address someone else holds, a 402 at the paywall — is a handled condition, and
// an alerting tool that pages on those is one people learn to ignore.
//
// The PII rule here is the same one the request logger follows, and it matters
// more: a log line sits on a machine you control, a Sentry event leaves the
// country. So the event goes through [scrubEvent] on the way out, which drops the
// request headers and cookies wholesale and runs the same shape masking over
// everything that is left.
package middleware

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

// SentryConfig is what main hands over from the config file.
type SentryConfig struct {
	// DSN is empty in every environment that has not opted in, which disables
	// reporting entirely — the same shape as the mail and SMS stubs.
	DSN string
	// Environment separates prod from a developer's box in the Sentry UI.
	Environment string
	// Release ties an event to the image it came from. The deploy script tags
	// images with the git SHA; passing the same value here is what makes
	// "new in this release" mean anything.
	Release string
}

// InitSentry starts error reporting and returns a flush function for shutdown.
//
// A missing DSN is not an error: it returns a no-op and says so once at boot, so
// a developer running the binary locally reports nothing and a deployment that
// forgot the DSN is visible in the startup log rather than silently quiet.
func InitSentry(cfg SentryConfig) (flush func(), err error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		slog.Info("sentry disabled (no DSN configured)")
		return func() {}, nil
	}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:         cfg.DSN,
		Environment: cfg.Environment,
		Release:     cfg.Release,
		// Errors only. Performance tracing on a service this size is cost and
		// noise for a question nobody is asking yet.
		EnableTracing: false,
		// Never let the SDK decide what is safe to send: no request bodies, no
		// user identifiers, no cookies picked up automatically.
		SendDefaultPII: false,
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			return scrubEvent(event)
		},
	}); err != nil {
		return func() {}, err
	}
	slog.Info("sentry enabled", "environment", cfg.Environment, "release", cfg.Release)
	return func() { sentry.Flush(2 * time.Second) }, nil
}

// Recovery turns a panic into a 500 through the normal error handler.
//
// Fiber does not recover by default: without this a panicking handler drops the
// connection, the client sees a reset rather than an error, and nothing is
// logged. The panic is passed on as an error, so it lands in the central error
// handler like any other 500 and is reported exactly once from there — rather
// than being captured here as well and arriving in Sentry twice.
func Recovery() fiber.Handler {
	return recover.New(recover.Config{EnableStackTrace: true})
}

// ReportServerError sends one 500 to Sentry, tagged with the request id so the
// event, the log line and what the user was shown can be lined up.
//
// Wired into apperr.ReportServerError by main. A nil hook (tests, local runs
// without a DSN) means this is never called.
func ReportServerError(c *fiber.Ctx, err error) {
	hub := sentry.CurrentHub().Clone()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag("request_id", RequestIDOf(c))
		scope.SetTag("method", c.Method())
		// The route pattern, not the URL: "/api/credit-analytics/reports/:id/pdf"
		// groups every report id into one issue, where the path would open a new
		// one per user.
		scope.SetTag("route", c.Route().Path)
		if id, ok := AccountID(c); ok {
			// An account id, not an email or a phone number. Enough to find the
			// user in the database, useless to anyone who cannot already.
			scope.SetUser(sentry.User{ID: accountTag(id)})
		}
	})
	hub.CaptureException(err)
}

// accountTag renders an account id for the Sentry user field.
func accountTag(id int64) string {
	return "account-" + strconv.FormatInt(id, 10)
}

// scrubEvent strips everything that could carry customer data out of an event.
//
// Headers and cookies go wholesale rather than selectively: Authorization is the
// obvious one, but X-Device-Info carries a device description and any header
// added later would arrive unreviewed. What is left — message, exception values,
// breadcrumbs, contexts, tags, request URL and query string — goes through the same
// shape masking the request logger uses, so a PAN or an email that reached an
// error string is redacted on the way out.
func scrubEvent(event *sentry.Event) *sentry.Event {
	if event == nil {
		return nil
	}
	event.Message = maskShapes(event.Message)

	for i := range event.Exception {
		event.Exception[i].Value = maskShapes(event.Exception[i].Value)
	}
	for i := range event.Breadcrumbs {
		if event.Breadcrumbs[i] == nil {
			continue
		}
		event.Breadcrumbs[i].Message = maskShapes(event.Breadcrumbs[i].Message)
		event.Breadcrumbs[i].Data = nil
	}
	for _, ctx := range event.Contexts {
		for k, v := range ctx {
			if s, ok := v.(string); ok {
				ctx[k] = maskShapes(s)
			}
		}
	}
	for k, v := range event.Tags {
		event.Tags[k] = maskShapes(v)
	}
	if event.Request != nil {
		event.Request.Headers = nil
		event.Request.Cookies = ""
		event.Request.Data = ""
		event.Request.URL = maskShapes(event.Request.URL)
		event.Request.QueryString = maskShapes(event.Request.QueryString)
	}
	return event
}
