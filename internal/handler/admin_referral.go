package handler

import (
	"strconv"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/service"
	"github.com/gofiber/fiber/v2"
)

// ReferralReportResponse is the body of GET /admin/referrals. It is an alias
// rather than its own struct so the API docs resolve the type from this
// package while the service keeps returning the model it builds.
type ReferralReportResponse = models.ReferralReport

// AdminReferralHandler serves the back-office referral report.
type AdminReferralHandler struct {
	svc *service.ReferralService
}

func NewAdminReferralHandler(svc *service.ReferralService) *AdminReferralHandler {
	return &AdminReferralHandler{svc: svc}
}

// Report godoc
//
// @Summary      Referral report for a date range (admin only)
// @Description  Who referred whom, over a window of whole UTC days. Returns the period total, a leaderboard of referrers (busiest first, capped at 100) and a page of the individual referred accounts. Omitting both dates gives the last 30 days; naming only one fills the other in. `referrerId` narrows the account list to one referrer without moving the headline total or the leaderboard, so drilling in never hides the rest of the period. Rows carry the referred user's phone and email, which is why this needs the 'referral:view' permission.
// @Tags         admin
// @Produce      json
// @Security     BearerAuth
// @Param        from        query     string  false  "First day, inclusive (YYYY-MM-DD). Defaults to 30 days before `to`."
// @Param        to          query     string  false  "Last day, inclusive (YYYY-MM-DD). Defaults to today (UTC)."
// @Param        referrerId  query     int     false  "Show only this referrer's signups"
// @Param        limit       query     int     false  "Max referred accounts to return (default 50, max 200)"
// @Param        offset      query     int     false  "Referred accounts to skip (default 0)"
// @Success      200  {object}  ReferralReportResponse
// @Failure      400  {object}  apperr.ErrorBody  "Unparseable date, inverted range, or a range over a year"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403  {object}  apperr.ErrorBody  "Missing the 'referral:view' permission"
// @Router       /admin/referrals [get]
func (h *AdminReferralHandler) Report(c *fiber.Ctx) error {
	from, err := queryDate(c, "from")
	if err != nil {
		return err
	}
	to, err := queryDate(c, "to")
	if err != nil {
		return err
	}
	referrerID, err := queryAccountID(c, "referrerId")
	if err != nil {
		return err
	}
	limit, err := queryInt(c, "limit")
	if err != nil {
		return err
	}
	offset, err := queryInt(c, "offset")
	if err != nil {
		return err
	}

	report, err := h.svc.Report(c.Context(), service.ReferralQuery{
		From:       from,
		To:         to,
		ReferrerID: referrerID,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		return err
	}
	return c.JSON(report)
}

// queryDate parses an optional YYYY-MM-DD query parameter into midnight UTC.
// An absent value is the zero time, which the service reads as "use the
// default"; anything unparseable is a 400 rather than a silent default, so a
// typo cannot quietly serve last month's numbers as this month's.
func queryDate(c *fiber.Ctx, name string) (time.Time, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
	if err != nil {
		return time.Time{}, apperr.NewValidationWith("Validation failed",
			map[string]string{name: "expected a date as YYYY-MM-DD"})
	}
	return t, nil
}

// queryAccountID parses an optional account id. Absent reads as nil (no
// filter); a non-numeric or non-positive one is a 400, because filtering on a
// nonsense id would otherwise render as "this referrer has no signups".
func queryAccountID(c *fiber.Ctx, name string) (*int64, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{name: "expected a positive account id"})
	}
	return &id, nil
}
