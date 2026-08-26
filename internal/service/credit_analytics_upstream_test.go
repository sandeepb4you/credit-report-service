package service

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"credit-report-service/internal/apperr"
)

// classifyUpstream decides what a failed Digitap call looks like to the caller,
// and it was wrong three separate ways before these tests existed: a dead
// client credential of ours, an unreachable provider and a vendor 500 all
// reached the app as 4xx, which renders them as the user's own mistake. The
// mapping lived inline in Request, where exercising it meant standing up an
// account, a KYC record, a database and an HTTP stub — so nothing guarded it.

// apperrKind names the concrete apperr type behind err so a table can state the
// expected mapping as a string rather than a closure per row.
func apperrKind(err error) string {
	var (
		v  *apperr.Validation
		ua *apperr.Unauthorized
		pf *apperr.PanFailure
		su *apperr.ServiceUnavailable
		bg *apperr.BadGateway
	)
	switch {
	case err == nil:
		return "nil"
	case errors.As(err, &v):
		return "Validation"
	case errors.As(err, &ua):
		return "Unauthorized"
	case errors.As(err, &pf):
		return "PanFailure"
	case errors.As(err, &su):
		return "ServiceUnavailable"
	case errors.As(err, &bg):
		return "BadGateway"
	}
	return "other"
}

func TestClassifyUpstream_StatusMapping(t *testing.T) {
	const upstreamMsg = "PAN not found in bureau records"

	cases := []struct {
		name      string
		status    int
		wantKind  string
		wantLevel slog.Level
		// wantPassesMsg: the upstream wording is both safe and useful to show
		// this user, so it must reach the caller verbatim. False means it must
		// stay in the log — it names our vendor, or a fault they cannot act on.
		wantPassesMsg bool
	}{
		{"400 is the caller's own input", http.StatusBadRequest, "Validation", slog.LevelWarn, true},
		{"401 is our dead client credential", http.StatusUnauthorized, "ServiceUnavailable", slog.LevelError, false},
		{"422 is a bureau tradeline rejection", http.StatusUnprocessableEntity, "PanFailure", slog.LevelWarn, true},
		{"403 means the product is not provisioned to us", http.StatusForbidden, "BadGateway", slog.LevelError, false},
		{"429 is a vendor quota we exceeded", http.StatusTooManyRequests, "BadGateway", slog.LevelError, false},
		{"500 is a vendor fault", http.StatusInternalServerError, "BadGateway", slog.LevelError, false},
		{"503 is a vendor outage", http.StatusServiceUnavailable, "BadGateway", slog.LevelError, false},
		{"504 is a proxy timeout", http.StatusGatewayTimeout, "BadGateway", slog.LevelError, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyUpstream(tc.status, upstreamMsg)
			if got.err == nil {
				t.Fatalf("upstream %d classified as a success", tc.status)
			}
			if kind := apperrKind(got.err); kind != tc.wantKind {
				t.Errorf("upstream %d: got %s, want %s", tc.status, kind, tc.wantKind)
			}
			if got.level != tc.wantLevel {
				t.Errorf("upstream %d: log level = %v, want %v", tc.status, got.level, tc.wantLevel)
			}
			if got.logMsg == "" {
				t.Errorf("upstream %d: empty log message; the status alone will not explain this in prod", tc.status)
			}
			if passes := strings.Contains(got.err.Error(), upstreamMsg); passes != tc.wantPassesMsg {
				t.Errorf("upstream %d: upstream wording reached the caller = %v, want %v (got %q)",
					tc.status, passes, tc.wantPassesMsg, got.err.Error())
			}
		})
	}
}

// TestClassifyUpstream_NeverBlamesTheUserForOurFailures is the regression guard
// the three fixes needed.
//
// Validation, PanFailure and Unauthorized are the apperr types that reach the
// app as 4xx, where it presents them as something the user did wrong — and a
// 401 additionally as an expired session (ApiErrors.kt), sending them round a
// sign-in loop that cannot possibly fix a credential held on our server. No
// status the user could not have caused may map to any of the three.
func TestClassifyUpstream_NeverBlamesTheUserForOurFailures(t *testing.T) {
	// The real message Digitap returns for a rejected client credential, so the
	// leak check below is testing the exact string that prompted all of this.
	const vendorMsg = "Client Authentication Failed"

	notTheUsersFault := []int{
		http.StatusUnauthorized,        // our client credential is dead
		http.StatusForbidden,           // the product is not provisioned to us
		http.StatusTooManyRequests,     // we exceeded a vendor quota
		http.StatusInternalServerError, // vendor fault
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		599, // an unrecognised status must fail safe, not fall through to 4xx
	}

	for _, status := range notTheUsersFault {
		got := classifyUpstream(status, vendorMsg)
		if got.err == nil {
			t.Errorf("upstream %d classified as a success", status)
			continue
		}
		switch kind := apperrKind(got.err); kind {
		case "Validation", "PanFailure", "Unauthorized":
			t.Errorf("upstream %d maps to %s, which the app shows as the user's own error", status, kind)
		}
		if strings.Contains(got.err.Error(), vendorMsg) {
			t.Errorf("upstream %d leaks the vendor's wording to the caller: %q", status, got.err.Error())
		}
	}
}

// TestClassifyUpstream_UserAttributableStatusesStayUserFacing is the other
// half of the guard: over-correcting the above into "every upstream failure is
// our fault" would bury the two cases a user genuinely can fix by retyping,
// leaving them with a retry button and no idea what was wrong.
func TestClassifyUpstream_UserAttributableStatusesStayUserFacing(t *testing.T) {
	const upstreamMsg = "invalid pan format"

	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		got := classifyUpstream(status, upstreamMsg)
		switch kind := apperrKind(got.err); kind {
		case "Validation", "PanFailure":
			// correct: reaches the app as 400 / 422
		default:
			t.Errorf("upstream %d maps to %s; the user can fix this one and needs to be told how", status, kind)
		}
		if !strings.Contains(got.err.Error(), upstreamMsg) {
			t.Errorf("upstream %d drops the wording naming the offending field: %q", status, got.err.Error())
		}
	}
}
