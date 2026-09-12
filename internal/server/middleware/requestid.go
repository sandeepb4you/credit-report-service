package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"credit-report-service/internal/apperr"
)

// RequestIDHeader is the header the id is read from and echoed back on.
const RequestIDHeader = "X-Request-Id"

// RequestID stamps every request with an id, puts it in the response header and
// stores it for the logger and the error envelope.
//
// One id ties together the log line, the Sentry event and the error the user was
// shown. Without it, "a customer says checkout failed around 3pm" means scanning
// by timestamp; with it they quote the id from the error and it is one query.
//
// An inbound [RequestIDHeader] is honoured so a trace started at nginx or in the
// app survives into these logs — but only after [safeRequestID] agrees, because
// the value is attacker-controlled and ends up in a log line. A caller who can
// put a newline or a quote in there can forge log entries, which is the cheap
// version of covering your tracks. Anything unconvincing is replaced rather than
// sanitised: a caller who sends rubbish gets a working id, just not theirs.
func RequestID() fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Get(RequestIDHeader)
		if !safeRequestID(id) {
			id = uuid.NewString()
		}
		c.Locals(apperr.RequestIDKey, id)
		c.Set(RequestIDHeader, id)
		return c.Next()
	}
}

// safeRequestID accepts what a tracing system would plausibly send and nothing
// else: 8-64 characters of ASCII letters, digits, dash, underscore or dot.
//
// The lower bound is there because a one-character id is no more useful than
// none and would collide constantly; the upper bound keeps a log line bounded.
func safeRequestID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		switch {
		case ch >= 'a' && ch <= 'z',
			ch >= 'A' && ch <= 'Z',
			ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.':
		default:
			return false
		}
	}
	return true
}

// RequestIDOf returns the id stamped on this request, or "" before [RequestID]
// has run.
func RequestIDOf(c *fiber.Ctx) string {
	id, _ := c.Locals(apperr.RequestIDKey).(string)
	return id
}
