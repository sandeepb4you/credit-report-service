package models

import "time"

// Retargeting audiences — the console's Admin -> Retargeting screen
// (design/myscorr-retargeting-dashboard.html in the app repo). Eleven fixed
// lists of customers grouped by where they stalled, each exportable as a CSV
// shaped for one messaging channel.

// Audience groups, in the order the console draws them.
const (
	AudienceGroupLost = "lost" // stalled before the score
	AudienceGroupBack = "back" // had a score, worth bringing back
	AudienceGroupGrow = "grow" // healthy, asked to do one more thing
)

// Export channels. Each decides which accounts can go in the file and which
// columns it carries; "all" is every column, for analysis rather than a send.
const (
	AudienceChannelWhatsApp = "whatsapp"
	AudienceChannelSMS      = "sms"
	AudienceChannelEmail    = "email"
	AudienceChannelPush     = "push"
	AudienceChannelAll      = "all"
)

// AudienceColumn is one list-specific column. Kind tells the console how to
// draw the raw value: text, date (YYYY-MM-DD), number, money (paise), pill.
type AudienceColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// AudienceList describes one list: what it is, how to filter it, and how many
// accounts are in it right now.
type AudienceList struct {
	ID          string `json:"id"`
	Group       string `json:"group"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// DateKey names the field in AudienceRow.Fields the date filter applies to.
	DateKey   string `json:"dateKey"`
	DateLabel string `json:"dateLabel"`
	// DateIsFuture marks a list whose date runs forward (a plan's next check),
	// so the console's "7 days" preset reaches ahead as well as back.
	DateIsFuture bool `json:"dateIsFuture"`
	// DefaultDays is the date window the console opens the list on: 30 for most lists, 0 (all
	// time) for a list whose own definition already sets the dates — "a score 30 to 120 days
	// old" opened on "the last 30 days" would always be empty.
	DefaultDays int              `json:"defaultDays"`
	Columns     []AudienceColumn `json:"columns"`
	// Filters are the dropdowns this list offers, from a fixed vocabulary:
	// platform, plan, band, panStatus, product, paid, reach.
	Filters []string `json:"filters"`
	Count   int      `json:"count"`
}

// AudienceRow is one account in a list.
type AudienceRow struct {
	AccountID  int64     `json:"accountId"`
	FirstName  string    `json:"firstName"`
	LastName   string    `json:"lastName"`
	Phone      string    `json:"phone"`
	Email      string    `json:"email"`
	Platform   string    `json:"platform"`
	SignedUpAt time.Time `json:"signedUpAt"`
	// CanPush is a live session with a push token — the one reachability
	// signal the service can vouch for. Consent to WhatsApp, SMS or email
	// marketing is not recorded anywhere yet, so no row claims it.
	CanPush bool `json:"canPush"`
	// Fields carries the list's own columns as strings, keyed by
	// AudienceColumn.Key, plus any filter dimensions (plan, band, ...).
	Fields map[string]string `json:"fields"`
}

// AudienceListRows is GET /admin/audiences/{id}.
type AudienceListRows struct {
	List AudienceList  `json:"list"`
	Rows []AudienceRow `json:"rows"`
	// Truncated is set when the list held more rows than one response carries.
	Truncated bool `json:"truncated"`
}

// AudienceExport is the answer to POST /admin/audiences/{id}/export: the file,
// and how many of the selected accounts it could hold.
type AudienceExport struct {
	Filename string `json:"filename"`
	CSV      string `json:"csv"`
	Selected int    `json:"selected"`
	Included int    `json:"included"`
	// LeftOut counts selected accounts the channel cannot reach (no phone,
	// no email, no push token) and so are not in the file.
	LeftOut       int    `json:"leftOut"`
	LeftOutReason string `json:"leftOutReason,omitempty"`
}
