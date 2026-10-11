package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// dashboardMetric is one entry of the closed metric catalog: the key a widget
// names, how the console labels it, and the permission that owns the data.
//
// The permission is the one the matching console list already requires —
// signups ride on account:view because the Users screen does, revenue on
// order:view because Purchases does — so a dashboard can never show anyone a
// figure they could not already have read off an existing screen.
type dashboardMetric struct {
	key, label, description, unit, permission string
}

var dashboardMetrics = []dashboardMetric{
	{"signups", "Signups", "Accounts created, by signup day.", "count", models.PermAccountView},
	{"reports_pulled", "Reports pulled", "Credit reports the bureau returned, by pull day.", "count", models.PermAccountView},
	{"pan_verified", "PANs verified", "PAN records marked verified, by verification day.", "count", models.PermKycVerify},
	{"paid_orders", "Paid orders", "Live orders that were paid, by payment day. Test purchases are left out.", "count", models.PermOrderView},
	{"revenue", "Revenue", "Money collected on live paid orders, GST included, by payment day.", "paise", models.PermOrderView},
	{"referral_conversions", "Referral conversions", "Referred customers whose first purchase earned their referrer a reward.", "count", models.PermReferralView},
}

var dashboardMetricByKey = func() map[string]dashboardMetric {
	m := make(map[string]dashboardMetric, len(dashboardMetrics))
	for _, d := range dashboardMetrics {
		if !repository.HasMetricQuery(d.key) {
			// A catalog entry with no query behind it is a programming error;
			// failing at package init beats a tile that 500s in production.
			panic("dashboard metric " + d.key + " has no query")
		}
		m[d.key] = d
	}
	return m
}()

// Limits on what a dashboard may hold. Generous for a console, and there so an
// imported file cannot make one page fire hundreds of queries.
const (
	dashboardMaxWidgets   = 24
	dashboardMaxPerOwner  = 50
	dashboardNameMaxRunes = 80
	widgetTitleMaxRunes   = 60
	// A series is one point per day, so a window is bounded like a chart is:
	// a little over a year.
	dashboardMaxWindowDays = 366
)

var (
	dashboardVizzes = map[string]bool{"kpi": true, "line": true, "bar": true}
	// dashboardRanges maps a widget's relative range to its length in days.
	dashboardRanges = map[string]int{"7d": 7, "30d": 30, "90d": 90, "365d": 365}
)

// DashboardService backs the console's custom dashboards: saving them,
// importing and exporting their definitions, and the metric series and CSV
// exports their widgets draw on.
type DashboardService struct {
	repo *repository.DashboardRepo
}

func NewDashboardService(repo *repository.DashboardRepo) *DashboardService {
	return &DashboardService{repo: repo}
}

// Metrics is the catalog, with whether the caller's role may read each one.
func (s *DashboardService) Metrics(role string) []models.DashboardMetric {
	out := make([]models.DashboardMetric, 0, len(dashboardMetrics))
	for _, m := range dashboardMetrics {
		out = append(out, models.DashboardMetric{
			Key:         m.key,
			Label:       m.label,
			Description: m.description,
			Unit:        m.unit,
			Allowed:     models.HasPermission(role, m.permission),
		})
	}
	return out
}

// Series returns a metric per UTC day over [from, to], both inclusive and
// both required — an endpoint cannot tell "unasked" from "everything", so the
// console always says which days it means.
func (s *DashboardService) Series(ctx context.Context, role, key string, from, to time.Time) (*models.DashboardSeries, error) {
	m, err := s.metricFor(role, key)
	if err != nil {
		return nil, err
	}
	if err := checkSeriesWindow(from, to); err != nil {
		return nil, err
	}
	byDay, err := s.repo.MetricByDay(ctx, m.key, from, to.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	series := &models.DashboardSeries{
		Metric: m.key,
		Unit:   m.unit,
		From:   from.Format("2006-01-02"),
		To:     to.Format("2006-01-02"),
		Points: []models.DashboardPoint{},
	}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		v := byDay[day]
		series.Total += v
		series.Points = append(series.Points, models.DashboardPoint{Day: day, Value: v})
	}
	return series, nil
}

// ExportCSV renders a metric's series as CSV and records the export. The
// audit row is written before the bytes are returned, and a failure to write
// it fails the export: an unrecorded download is the thing the table is for.
func (s *DashboardService) ExportCSV(ctx context.Context, accountID int64, role, key string, from, to time.Time) (filename string, body []byte, err error) {
	series, err := s.Series(ctx, role, key, from, to)
	if err != nil {
		return "", nil, err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	valueHeader := "count"
	if series.Unit == "paise" {
		// Rupees with two decimals: what a spreadsheet user expects to sum.
		valueHeader = "amount_inr"
	}
	_ = w.Write([]string{"date", valueHeader})
	for _, p := range series.Points {
		_ = w.Write([]string{p.Day, formatMetricValue(series.Unit, p.Value)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", nil, err
	}
	if err := s.repo.RecordExport(ctx, accountID, key, from, to, len(series.Points)); err != nil {
		return "", nil, err
	}
	filename = fmt.Sprintf("myscorr-%s-%s-to-%s.csv", strings.ReplaceAll(key, "_", "-"), series.From, series.To)
	return filename, buf.Bytes(), nil
}

func formatMetricValue(unit string, v int64) string {
	if unit != "paise" {
		return strconv.FormatInt(v, 10)
	}
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

func (s *DashboardService) metricFor(role, key string) (dashboardMetric, error) {
	m, ok := dashboardMetricByKey[key]
	if !ok {
		return dashboardMetric{}, apperr.NewNotFound("No such metric")
	}
	if !models.HasPermission(role, m.permission) {
		return dashboardMetric{}, apperr.NewForbidden(
			fmt.Sprintf("You don't have access to %s (needs %s)", strings.ToLower(m.label), m.permission))
	}
	return m, nil
}

func checkSeriesWindow(from, to time.Time) error {
	details := map[string]string{}
	if from.IsZero() {
		details["from"] = "required, as YYYY-MM-DD"
	}
	if to.IsZero() {
		details["to"] = "required, as YYYY-MM-DD"
	}
	if len(details) == 0 {
		switch {
		case to.Before(from):
			details["to"] = "the end date cannot be before the start date"
		case to.Sub(from) >= dashboardMaxWindowDays*24*time.Hour:
			details["from"] = fmt.Sprintf("the range cannot be longer than %d days", dashboardMaxWindowDays)
		}
	}
	if len(details) > 0 {
		return apperr.NewValidationWith("Validation failed", details)
	}
	return nil
}

// ---- saved dashboards ------------------------------------------------------

func (s *DashboardService) List(ctx context.Context, ownerID int64) ([]models.AdminDashboard, error) {
	return s.repo.ListDashboards(ctx, ownerID)
}

func (s *DashboardService) Get(ctx context.Context, id, ownerID int64) (*models.AdminDashboard, error) {
	d, err := s.repo.GetDashboard(ctx, id, ownerID)
	return d, mapDashboardErr(err)
}

// Create saves a new dashboard after validating it.
func (s *DashboardService) Create(ctx context.Context, ownerID int64, name string, widgets []models.DashboardWidget) (*models.AdminDashboard, error) {
	name, widgets, err := validateDashboard(name, widgets)
	if err != nil {
		return nil, err
	}
	n, err := s.repo.CountDashboards(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	if n >= dashboardMaxPerOwner {
		return nil, apperr.NewConflict(
			fmt.Sprintf("You already have %d dashboards. Delete one to make another.", dashboardMaxPerOwner))
	}
	return s.repo.CreateDashboard(ctx, ownerID, name, widgets)
}

// Update replaces a dashboard's name and widgets.
func (s *DashboardService) Update(ctx context.Context, id, ownerID int64, name string, widgets []models.DashboardWidget) (*models.AdminDashboard, error) {
	name, widgets, err := validateDashboard(name, widgets)
	if err != nil {
		return nil, err
	}
	d, err := s.repo.UpdateDashboard(ctx, id, ownerID, name, widgets)
	return d, mapDashboardErr(err)
}

func (s *DashboardService) Delete(ctx context.Context, id, ownerID int64) error {
	return mapDashboardErr(s.repo.DeleteDashboard(ctx, id, ownerID))
}

// Export returns a dashboard's portable definition.
func (s *DashboardService) Export(ctx context.Context, id, ownerID int64) (*models.DashboardDefinition, error) {
	d, err := s.Get(ctx, id, ownerID)
	if err != nil {
		return nil, err
	}
	return &models.DashboardDefinition{
		Format:  models.DashboardDefinitionFormat,
		Version: models.DashboardDefinitionVersion,
		Name:    d.Name,
		Widgets: d.Widgets,
	}, nil
}

// Import saves a definition as a new dashboard of the caller's. Its widgets
// are validated exactly as a hand-built dashboard's are — an imported file is
// untrusted input — but a widget naming a metric the importer may not read is
// kept: it draws as a locked tile, which is more useful than a dashboard that
// silently lost a row in transit.
func (s *DashboardService) Import(ctx context.Context, ownerID int64, def models.DashboardDefinition) (*models.AdminDashboard, error) {
	if def.Format != models.DashboardDefinitionFormat {
		return nil, apperr.NewValidationWith("This file isn't a myScorr dashboard", map[string]string{
			"format": "expected " + models.DashboardDefinitionFormat,
		})
	}
	if def.Version != models.DashboardDefinitionVersion {
		return nil, apperr.NewValidationWith("This dashboard was exported by a newer version", map[string]string{
			"version": fmt.Sprintf("expected %d", models.DashboardDefinitionVersion),
		})
	}
	return s.Create(ctx, ownerID, def.Name, def.Widgets)
}

func mapDashboardErr(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.NewNotFound("Dashboard not found")
	}
	return err
}

// validateDashboard trims and checks a dashboard. Every widget field must come
// from its fixed list; details name the widget by position so the console can
// point at the offending tile.
func validateDashboard(name string, widgets []models.DashboardWidget) (string, []models.DashboardWidget, error) {
	details := map[string]string{}
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		details["name"] = "required"
	case utf8.RuneCountInString(name) > dashboardNameMaxRunes:
		details["name"] = fmt.Sprintf("at most %d characters", dashboardNameMaxRunes)
	}
	if len(widgets) > dashboardMaxWidgets {
		details["widgets"] = fmt.Sprintf("at most %d widgets", dashboardMaxWidgets)
	}
	out := make([]models.DashboardWidget, 0, len(widgets))
	for i, w := range widgets {
		at := fmt.Sprintf("widgets[%d]", i)
		w.Metric = strings.TrimSpace(w.Metric)
		w.Viz = strings.ToLower(strings.TrimSpace(w.Viz))
		w.Range = strings.ToLower(strings.TrimSpace(w.Range))
		w.Title = strings.TrimSpace(w.Title)
		if _, ok := dashboardMetricByKey[w.Metric]; !ok {
			details[at+".metric"] = "unknown metric " + strconv.Quote(w.Metric)
		}
		if !dashboardVizzes[w.Viz] {
			details[at+".viz"] = "expected kpi, line or bar"
		}
		if _, ok := dashboardRanges[w.Range]; !ok {
			details[at+".range"] = "expected 7d, 30d, 90d or 365d"
		}
		if utf8.RuneCountInString(w.Title) > widgetTitleMaxRunes {
			details[at+".title"] = fmt.Sprintf("at most %d characters", widgetTitleMaxRunes)
		}
		out = append(out, w)
	}
	if len(details) > 0 {
		return "", nil, apperr.NewValidationWith("Validation failed", details)
	}
	return name, out, nil
}
