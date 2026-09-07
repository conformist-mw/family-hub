package agenda

import (
	"time"

	"familyhub/internal/model"
	"familyhub/internal/reminders"
	"familyhub/internal/schedule"
	"familyhub/internal/store"
)

// Horizon is how far past today the agenda looks. A week: far enough that
// "what is coming" survives a Friday evening, short enough that a weekday
// name identifies a day without a date beside it.
const Horizon = 7 * 24 * time.Hour

// Source is the slice of the store the agenda reads. Narrow on purpose — this
// package decides what a day looks like, and a screen must not be able to hand
// it a different set of rows and get a different day.
type Source interface {
	SlotHistories() ([]store.SlotHistory, error)
	UpcomingAbsences(date string) ([]model.TrainerAbsence, error)
	ListVisits(store.VisitFilter) ([]model.Visit, error)
	AppointmentsBetween(from, to string) ([]model.Appointment, error)
}

// Load fetches the window both surfaces show and folds it into a Day.
//
// A nil chores service means the reminders feature is off, not an error: the
// day then simply has no chores in it, the same way the calendar feed treats
// it.
func Load(src Source, chores *reminders.Service, now time.Time, loc *time.Location) (Day, error) {
	now = now.In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	end := start.Add(Horizon)

	histories, err := src.SlotHistories()
	if err != nil {
		return Day{}, err
	}
	absences, err := src.UpcomingAbsences(start.Format(dateOnly))
	if err != nil {
		return Day{}, err
	}
	// Expanded from the start of today rather than from this minute: the day
	// is shown whole, so this morning's lesson is still part of it.
	lessons, err := schedule.Expand(histories, absences, loc, start, end)
	if err != nil {
		return Day{}, err
	}
	visits, err := src.ListVisits(store.VisitFilter{
		From: start.Format(dateOnly), To: end.Format(dateOnly),
	})
	if err != nil {
		return Day{}, err
	}
	appts, err := src.AppointmentsBetween(start.Format(model.LocalDatetime), end.Format(model.LocalDatetime))
	if err != nil {
		return Day{}, err
	}
	var occ []reminders.Occurrence
	if chores != nil {
		if occ, err = chores.Upcoming(start, end); err != nil {
			return Day{}, err
		}
	}
	return Build(lessons, visits, appts, occ, now, loc), nil
}
