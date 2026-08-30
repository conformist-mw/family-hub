package model

import "time"

// Occurrence statuses. Prefixed like the appointment ones: the bare Status*
// names belong to lesson visits (see model.go), Appt* to appointments, and all
// three domains live in this package. Stored values stay bare so the DB reads
// plainly.
const (
	// OccPending means the moment arrived and nobody closed it. It is a real
	// row, not a derived state — in the past it is the evidence that the
	// reminder came due and was forgotten, which a recomputation from today's
	// rule could never prove.
	OccPending = "pending"
	OccDone    = "done"
	// OccSkipped is a deliberate pass: the cactus was watered early, this
	// round is not needed. It silences the nag without counting as a miss.
	OccSkipped = "skipped"
)

var OccStatusLabels = map[string]string{
	OccPending: "не закрито",
	OccDone:    "зроблено",
	OccSkipped: "пропущено",
}

// Reminder is a recurring chore — "enable cashback", "log the mileage". It
// holds only what the chore IS; how it repeats lives in its ReminderRule
// versions, and what actually came due lives in ReminderOccurrence.
type Reminder struct {
	ID          int64
	Title       string
	Person      string // decorative, as written; routes no notifications
	DurationMin int
	Active      bool
	// ActiveSince is the floor for the catch-up backfill. Switching a paused
	// reminder back on must not invent "you forgot" rows for the time it was
	// deliberately off.
	ActiveSince string // LocalDatetime
	Note        string
	CreatedAt   string
	UpdatedAt   string
	DeletedAt   string
}

// ReminderRule is one version of how a reminder repeats. A reminder owns a
// list of these ordered by ValidFromAt, and any window is expanded with the
// version that was in force over it — which is what keeps the past honest when
// a schedule changes.
type ReminderRule struct {
	ID         int64
	ReminderID int64
	// ValidFromAt is inclusive, and a datetime rather than a date: a version
	// created "from today" at 10:00 must not claim today's 08:00 occurrence.
	ValidFromAt string // LocalDatetime
	// DTStart fixes the time of day and the phase of any INTERVAL. "Every two
	// weeks" is undefined without it.
	DTStart   string // LocalDatetime
	RRule     string // RFC 5545 body, e.g. FREQ=MONTHLY;BYMONTHDAY=1
	CreatedAt string
}

// ReminderOccurrence is one instance that came due. Rows exist only for
// due_at <= now; anything later is expanded from the rules instead, so an
// occurrence is never both stored and computed.
type ReminderOccurrence struct {
	ID         int64
	ReminderID int64
	// RuleID records which version produced this row, so the record explains
	// itself after the rule has moved on.
	RuleID int64
	DueAt  string // LocalDatetime, local wall clock
	Status string
	DoneAt string
	DoneBy string
	// Title, Person and DurationMin are filled by the store when an occurrence
	// is read for display, so the calendar and the nag do not each re-join.
	Title       string
	Person      string
	DurationMin int
}

// Anchor parses DTStart in loc. The location matters: it is what keeps 08:00
// at 08:00 across a DST transition — see internal/recur.
func (r ReminderRule) Anchor(loc *time.Location) (time.Time, error) {
	return time.ParseInLocation(LocalDatetime, r.DTStart, loc)
}

// ValidFrom parses ValidFromAt in loc.
func (r ReminderRule) ValidFrom(loc *time.Location) (time.Time, error) {
	return time.ParseInLocation(LocalDatetime, r.ValidFromAt, loc)
}

// Due parses DueAt in loc.
func (o ReminderOccurrence) Due(loc *time.Location) (time.Time, error) {
	return time.ParseInLocation(LocalDatetime, o.DueAt, loc)
}

// Closed reports whether the occurrence needs no further action — done or
// deliberately skipped. The evening nag asks exactly this.
func (o ReminderOccurrence) Closed() bool {
	return o.Status == OccDone || o.Status == OccSkipped
}

// ValidOccStatus guards writes coming from the API and the bot's callback
// data, neither of which is trusted to stay in step with the CHECK constraint.
func ValidOccStatus(s string) bool {
	_, ok := OccStatusLabels[s]
	return ok
}
