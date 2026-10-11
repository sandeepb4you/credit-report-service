package models

import "time"

// DashboardWidget is one tile on a console dashboard. Every field is a choice
// from a fixed list the service validates against, so a stored definition
// can never name a query of its own — it only ever picks one of ours.
type DashboardWidget struct {
	// Metric is a key from the server's catalog (GET /admin/dashboards/metrics).
	Metric string `json:"metric"`
	// Viz is how the tile draws it: "kpi" (one number), "line" or "bar".
	Viz string `json:"viz"`
	// Range is relative — "7d", "30d", "90d" or "365d", ending today (UTC) —
	// so a dashboard stays current, and one imported a month later shows the
	// importer's last 30 days rather than the exporter's.
	Range string `json:"range"`
	// Title overrides the metric's label; empty means use the label.
	Title string `json:"title,omitempty"`
}

// AdminDashboard is a saved dashboard as the console reads it.
type AdminDashboard struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Widgets   []DashboardWidget `json:"widgets"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// DashboardDefinitionFormat names the export file's shape. The import refuses
// anything else, so a stray JSON file (a coupon export, a report) fails with a
// sentence instead of being half-read as a dashboard.
const (
	DashboardDefinitionFormat  = "myscorr.dashboard"
	DashboardDefinitionVersion = 1
)

// DashboardDefinition is the portable form of a dashboard: what export
// writes and import reads. No ids, owners or timestamps — those belong to the
// account that holds the copy, not to the definition.
type DashboardDefinition struct {
	Format  string            `json:"format"`
	Version int               `json:"version"`
	Name    string            `json:"name"`
	Widgets []DashboardWidget `json:"widgets"`
}

// DashboardMetric is one entry of the metric catalog, as the console lists it.
type DashboardMetric struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// Unit is "count" or "paise"; the console formats paise as rupees.
	Unit string `json:"unit"`
	// Allowed is whether the caller holds the permission that owns the data.
	// A widget the caller cannot read still renders — as a locked tile —
	// rather than vanishing from an imported dashboard.
	Allowed bool `json:"allowed"`
}

// DashboardPoint is one day of a metric's series.
type DashboardPoint struct {
	Day   string `json:"day"` // YYYY-MM-DD, UTC
	Value int64  `json:"value"`
}

// DashboardSeries is a metric over a window of whole UTC days, gap-filled:
// every day in [From, To] has a point, zero when nothing happened, so a chart
// never draws a quiet day as a missing one.
type DashboardSeries struct {
	Metric string           `json:"metric"`
	Unit   string           `json:"unit"`
	From   string           `json:"from"`
	To     string           `json:"to"`
	Total  int64            `json:"total"`
	Points []DashboardPoint `json:"points"`
}
