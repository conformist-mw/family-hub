package model

import "time"

// Appointment statuses. They are deliberately prefixed: the plain Status*
// names belong to lesson visits (see model.go), and both domains live in this
// package. The stored values stay bare so the DB is readable.
const (
	ApptStatusPlanned   = "planned"
	ApptStatusDone      = "done"
	ApptStatusCancelled = "cancelled"
)

var ApptStatusLabels = map[string]string{
	ApptStatusPlanned:   "заплановано",
	ApptStatusDone:      "було",
	ApptStatusCancelled: "скасовано",
}

// LocalDatetime is the layout used for appointment starts_at/ends_at. Naive
// local time — the service runs in a fixed timezone (TZ in the container).
const LocalDatetime = "2006-01-02T15:04"

// Appointment is one one-off scheduled visit (orthodontist, manicure, ...).
// It is the source of truth in SQLite and the unit exported to Home
// Assistant's calendar.
type Appointment struct {
	ID       int64
	Title    string
	Person   string
	Location string
	StartsAt string // LocalDatetime
	EndsAt   string // LocalDatetime, "" if none
	Status   string
	Note     string
	Raw      string
	// Cost is nil when nothing was recorded; 0 means it was free. Both the web
	// form and the bot's post-visit prompt write it.
	Cost *float64
	// CostPromptMsgID is the notify-chat message id of the "how much was it?"
	// prompt. Replies to that message carry the amount, and a non-nil value
	// means the prompt has already been sent.
	CostPromptMsgID *int64
	HaUID           string
	HaSyncedAt      string
	CreatedAt       string
	UpdatedAt       string
	DeletedAt       string
}

// Start parses StartsAt in loc. Callers that need to compare against "now"
// should pass the same location the scheduler uses.
func (a Appointment) Start(loc *time.Location) (time.Time, error) {
	return time.ParseInLocation(LocalDatetime, a.StartsAt, loc)
}
