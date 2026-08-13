package model

import "time"

const (
	BillingPerLesson = "per_lesson"
	BillingMonthly   = "monthly"

	// AttendancePerSession expects a marked result for every scheduled day —
	// the bot asks in the evening. AttendanceExceptionsOnly treats a scheduled
	// day as simply "по розкладу" and asks nothing; a school with a fixed
	// monthly fee owes the same amount either way, so the daily question buys
	// nothing. It gates reminders only, never money.
	AttendancePerSession     = "per_session"
	AttendanceExceptionsOnly = "exceptions_only"

	StatusDone        = "done"
	StatusRescheduled = "rescheduled"
	StatusCancelled   = "cancelled"
	StatusSkipped     = "skipped"

	KindChild = "child"
	KindAdult = "adult"

	AbsenceVacation = "vacation"
	AbsenceSick     = "sick"
	AbsenceOther    = "other"
)

var StatusLabels = map[string]string{
	StatusDone:        "проведено",
	StatusRescheduled: "перенесено",
	StatusCancelled:   "скасовано",
	StatusSkipped:     "пропущено",
}

var AbsenceKindLabels = map[string]string{
	AbsenceVacation: "відпустка",
	AbsenceSick:     "хвороба",
	AbsenceOther:    "інше",
}

var AttendanceModeLabels = map[string]string{
	AttendancePerSession:     "відмічати кожне заняття",
	AttendanceExceptionsOnly: "без щоденних відміток",
}

var WeekdayLabels = [7]string{"Нд", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб"}

// WeekdayFull is for a screen heading, where the two-letter form reads as an
// abbreviation of something rather than as the name of today.
var WeekdayFull = [7]string{
	"Неділя", "Понеділок", "Вівторок", "Середа", "Четвер", "П'ятниця", "Субота",
}

// MonthsGenitive is the "6 серпня" form, for day headers that have room for a
// whole word.
var MonthsGenitive = [13]string{
	"", "січня", "лютого", "березня", "квітня", "травня", "червня",
	"липня", "серпня", "вересня", "жовтня", "листопада", "грудня",
}

// MonthsShort is the compact "6 сер" form, for lists and cards where a whole
// word would push the line onto a second row.
var MonthsShort = [13]string{
	"", "січ", "лют", "бер", "кві", "тра", "чер",
	"лип", "сер", "вер", "жов", "лис", "гру",
}

type Person struct {
	ID     int64
	Name   string
	Kind   string
	Active bool
	Notes  string
}

type Enrollment struct {
	ID           int64
	PersonID     int64
	Person       string
	Name         string
	Description  string
	BillingType  string
	CurrentPrice float64
	LowThreshold int
	Active       bool
	Notes        string
	SlotCount    int    // weekly reminder slots; only populated by ListEnrollments
	TrainerID    *int64 // nil — no trainer attached
	Trainer      string // joined trainer name, read-only

	// AttendanceMode is per_session | exceptions_only.
	AttendanceMode string
	// PaymentInstructions is free text shown in the payment reminder: payee,
	// IBAN, reference. Never rendered into the ICS feed.
	PaymentInstructions string
}

// SkipsDailyMarks reports whether the bot should stay silent about this
// enrollment's scheduled days. The schedule itself stays — the calendar and
// the forecast still need it.
func (e Enrollment) SkipsDailyMarks() bool {
	return e.AttendanceMode == AttendanceExceptionsOnly
}

type Trainer struct {
	ID     int64
	Name   string
	Notes  string
	Active bool
}

// TrainerAbsence is a date range (both ends inclusive) during which the
// trainer's enrollments get no bot reminders and their lessons drop out of
// the ICS feed. Kind is informational: vacation | sick | other.
type TrainerAbsence struct {
	ID        int64
	TrainerID int64
	Trainer   string // joined name, read-only
	DateFrom  string
	DateTo    string
	Kind      string
	Comment   string
}

type Slot struct {
	ID           int64
	EnrollmentID int64
	Weekday      int
	Time         string
	DurationMin  int
	Active       bool
}

type Visit struct {
	ID           int64
	EnrollmentID int64
	Person       string
	Class        string
	ClassDesc    string
	Date         string
	Status       string
	Comment      string
	Created      string
}

type Payment struct {
	ID           int64
	EnrollmentID int64
	Person       string
	Class        string
	ClassDesc    string
	Date         string
	Amount       float64
	LessonsPaid  *int64
	CoversFrom   *string
	CoversUntil  *string
	Comment      string
}

// CoversMonth renders the coverage range as the "YYYY-MM" an <input type=month>
// wants, or "" when there is no coverage. The payment form only ever writes
// whole calendar months, so the start of the range identifies the month.
func (p Payment) CoversMonth() string {
	if p.CoversFrom == nil || len(*p.CoversFrom) < 7 {
		return ""
	}
	return (*p.CoversFrom)[:7]
}

// Balance is a per-enrollment rollup shown on the dashboard.
type Balance struct {
	Enrollment
	Paid          int    // sum of lessons_paid (per_lesson)
	Done          int    // count of done visits (all time)
	Remaining     int    // Paid - Done (per_lesson)
	DoneThisMonth int    // done visits since the start of the current month
	CoveredNow    bool   // monthly: is today within a paid period
	CoversUntil   string // monthly: end of the covering block, or of the prepaid one; "" if none
	DaysLeft      int    // monthly: days until CoversUntil; 0 while prepaid
	// PrepaidFrom is set when nothing covers today but a later period is
	// already paid — September settled on 28 August. Empty otherwise.
	PrepaidFrom string
}

// State returns one of: ok, low, empty — drives the dashboard badge.
func (b Balance) State() string {
	switch b.BillingType {
	case BillingMonthly:
		if !b.CoveredNow {
			// Paid ahead of the period it buys — the normal shape for a school
			// fee, and not a reason to ask for money.
			if b.PrepaidFrom != "" {
				return "ok"
			}
			return "empty"
		}
		if b.DaysLeft <= b.LowThreshold {
			return "low"
		}
		return "ok"
	default:
		if b.Remaining <= 0 {
			return "empty"
		}
		if b.Remaining <= b.LowThreshold {
			return "low"
		}
		return "ok"
	}
}

// ParseDate parses a stored "YYYY-MM-DD" into midnight in the local zone.
// Local matters: balance/coverage code compares these against local "today"
// (see store.coverageFromToday) — parsing in UTC made the first day of a paid
// period read as unpaid in any zone east of UTC.
func ParseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.Local)
}
