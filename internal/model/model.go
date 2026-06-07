package model

import "time"

const (
	BillingPerLesson = "per_lesson"
	BillingMonthly   = "monthly"

	StatusDone        = "done"
	StatusRescheduled = "rescheduled"
	StatusCancelled   = "cancelled"
	StatusSkipped     = "skipped"

	KindChild = "child"
	KindAdult = "adult"
)

var StatusLabels = map[string]string{
	StatusDone:        "проведено",
	StatusRescheduled: "перенесено",
	StatusCancelled:   "отменено",
	StatusSkipped:     "пропущено",
}

var WeekdayLabels = [7]string{"Вс", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб"}

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
	SlotCount    int // weekly reminder slots; only populated by ListEnrollments
}

type Slot struct {
	ID           int64
	EnrollmentID int64
	Weekday      int
	Time         string
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

// Balance is a per-enrollment rollup shown on the dashboard.
type Balance struct {
	Enrollment
	Paid          int    // sum of lessons_paid (per_lesson)
	LastPack      int    // lessons_paid of the most recent payment (per_lesson), 0 if none
	Done          int    // count of done visits (all time)
	Remaining     int    // Paid - Done (per_lesson)
	DoneThisMonth int    // done visits since the start of the current month
	CoveredNow    bool   // monthly: is today within a paid period
	CoversUntil   string // monthly: end of the contiguous block covering today, "" if none
	DaysLeft      int    // monthly: days until CoversUntil
}

// State returns one of: ok, low, empty — drives the dashboard badge.
func (b Balance) State() string {
	switch b.BillingType {
	case BillingMonthly:
		if !b.CoveredNow {
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

func ParseDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}
