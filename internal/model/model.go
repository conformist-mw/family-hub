package model

import "time"

const (
	BillingPerLesson = "per_lesson"
	BillingMonthly   = "monthly"

	StatusDone        = "done"
	StatusRescheduled = "rescheduled"
	StatusCancelled   = "cancelled"
	StatusSkipped     = "skipped"
)

var StatusLabels = map[string]string{
	StatusDone:        "проведено",
	StatusRescheduled: "перенесено",
	StatusCancelled:   "отменено",
	StatusSkipped:     "пропущено",
}

var WeekdayLabels = [7]string{"Вс", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб"}

type Child struct {
	ID     int64
	Name   string
	Active bool
}

type Activity struct {
	ID   int64
	Name string
}

type Enrollment struct {
	ID           int64
	ChildID      int64
	ActivityID   int64
	Child        string
	Activity     string
	BillingType  string
	CurrentPrice float64
	LowThreshold int
	Active       bool
	Notes        string
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
	Child        string
	Activity     string
	Date         string
	Status       string
	Comment      string
}

type Payment struct {
	ID           int64
	EnrollmentID int64
	Child        string
	Activity     string
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
	Paid        int     // sum of lessons_paid (per_lesson)
	Done        int     // count of done visits
	Remaining   int     // Paid - Done (per_lesson)
	CoversUntil string  // latest covers_until (monthly), "" if none
	DaysLeft    int     // days until CoversUntil (monthly)
}

// State returns one of: ok, low, empty — drives the dashboard badge.
func (b Balance) State() string {
	switch b.BillingType {
	case BillingMonthly:
		if b.CoversUntil == "" {
			return "empty"
		}
		if b.DaysLeft < 0 {
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
