package audit

import (
	"time"

	"lessons/internal/model"
)

// Forecast is the future part of the audit: scheduled lessons up to the
// period's end, marked covered/uncovered by what's already paid, plus the
// top-up math for the header.
type Forecast struct {
	Rows        []Row
	PaidThrough string // last covered future date; "" if nothing is covered
	Uncovered   int
	Debt        int     // per-lesson: lessons already owed (negative closing balance)
	TopUpCount  int     // per-lesson: lessons to pay; monthly: months to pay
	TopUpAmount float64 // TopUpCount × current price
}

// BuildForecast expands the enrollment's active slots into concrete dates
// after the ledger's end and marks each as covered or not.
//
// The expansion runs from today — or tomorrow when today already has a
// visit recorded (then it is part of the ledger, not the forecast) — through
// `to` inclusive; dates inside a trainer absence are dropped, mirroring the
// muted reminders and the ICS holes. Returns a zero Forecast when `to` is
// not in the future.
//
// Coverage: per-lesson allocates max(0, closing) to dates in order; monthly
// covers dates ≤ coversUntil. Top-up: per-lesson pays uncovered dates plus
// any existing debt; monthly buys whole months, each extending coversUntil
// (or the day before the first forecast date when there is no coverage) by
// one month, until `to` is reached.
func BuildForecast(e model.Enrollment, slots []model.Slot, absences []model.TrainerAbsence,
	closing int, coversUntil string, today, to string, hasVisitToday bool) Forecast {

	var f Forecast
	if to <= today || len(slots) == 0 {
		return f
	}
	start, err1 := model.ParseDate(today)
	end, err2 := model.ParseDate(to)
	if err1 != nil || err2 != nil {
		return f
	}
	if hasVisitToday {
		start = start.AddDate(0, 0, 1)
	}

	weekdays := map[int]bool{}
	for _, sl := range slots {
		weekdays[sl.Weekday] = true
	}

	// The per-lesson running balance funds the future; a negative one is
	// existing debt. Monthly enrollments don't count lessons at all — their
	// coverage is the pass boundary alone.
	remaining := 0
	if e.BillingType != model.BillingMonthly {
		remaining = closing
		if remaining < 0 {
			f.Debt = -remaining
			remaining = 0
		}
	}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if !weekdays[int(d.Weekday())] {
			continue
		}
		date := d.Format("2006-01-02")
		if absentOn(date, absences) {
			continue
		}
		r := Row{Date: date, Kind: KindFuture}
		if e.BillingType == model.BillingMonthly {
			r.Covered = coversUntil != "" && date <= coversUntil
		} else {
			r.Covered = remaining > 0
			if remaining > 0 {
				remaining--
			}
		}
		if r.Covered {
			f.PaidThrough = date
		} else {
			f.Uncovered++
		}
		f.Rows = append(f.Rows, r)
	}

	if e.BillingType == model.BillingMonthly {
		f.TopUpCount = monthsToCover(coversUntil, f.Rows, end)
	} else {
		f.TopUpCount = f.Uncovered + f.Debt
	}
	f.TopUpAmount = float64(f.TopUpCount) * e.CurrentPrice
	return f
}

func absentOn(date string, absences []model.TrainerAbsence) bool {
	for _, a := range absences {
		if a.DateFrom <= date && date <= a.DateTo {
			return true
		}
	}
	return false
}

// monthsToCover counts whole months needed to extend coverage through end.
// Each bought month moves the boundary by one calendar month, starting from
// coversUntil, or from the day before the first uncovered forecast date when
// nothing is covered now. No uncovered dates → nothing to buy.
func monthsToCover(coversUntil string, rows []Row, end time.Time) int {
	uncovered := false
	for _, r := range rows {
		if !r.Covered {
			uncovered = true
			break
		}
	}
	if !uncovered {
		return 0
	}
	var cur time.Time
	if coversUntil != "" {
		t, err := model.ParseDate(coversUntil)
		if err != nil {
			return 0
		}
		cur = t
	} else {
		first := rows[0].Date
		t, err := model.ParseDate(first)
		if err != nil {
			return 0
		}
		cur = t.AddDate(0, 0, -1)
	}
	n := 0
	for cur.Before(end) {
		cur = cur.AddDate(0, 1, 0)
		n++
	}
	return n
}
