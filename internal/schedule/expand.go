package schedule

import (
	"fmt"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/recur"
	"familyhub/internal/store"
)

// byDay maps a weekday index (0=Sun..6=Sat) to its iCalendar day token.
var byDay = [7]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// Lesson is one concrete occurrence of a weekly slot — a real datetime, not a
// rule. Start is in the zone it was expanded in.
type Lesson struct {
	SlotID      int64
	Enrollment  model.Enrollment
	Start       time.Time
	DurationMin int
}

// End is when the lesson finishes.
func (l Lesson) End() time.Time {
	return l.Start.Add(time.Duration(l.DurationMin) * time.Minute)
}

// Expand turns weekly slots into the lessons they actually generate in
// [from, to], dropping any that land inside an absence of the slot's trainer.
//
// Why this exists rather than handing the calendar an RRULE. A weekly rule
// from a UTC DTSTART is expanded in UTC, so its instances are exactly 168h
// apart — which is not "the same wall-clock time next week". A Tuesday 16:00
// lesson anchored in summer was showing as 15:00 from the October clock change
// onward, all winter, and an hour late after the spring one.
//
// internal/recur keeps wall-clock time across a transition and is pinned by
// tests, so expanding here is expanding through the one place in the app that
// has already answered this question. Reminders reached the same conclusion
// for the same reason; see internal/ics.
//
// Absences are applied by simply not generating the lesson. The EXDATE
// machinery this replaces had to compute its exclusions as fixed 168h offsets
// to match what the RRULE really produced — carefully punching correct holes
// in a schedule that showed the wrong hour.
func Expand(slots []store.SlotWithEnrollment, absences []model.TrainerAbsence,
	loc *time.Location, from, to time.Time) ([]Lesson, error) {
	if to.Before(from) {
		return nil, nil
	}
	byTrainer := make(map[int64][]model.TrainerAbsence)
	for _, a := range absences {
		byTrainer[a.TrainerID] = append(byTrainer[a.TrainerID], a)
	}

	from, to = from.In(loc), to.In(loc)
	var out []Lesson
	for _, s := range slots {
		if s.Slot.Weekday < 0 || s.Slot.Weekday > 6 {
			continue // a row the schema should have refused; skip it, don't guess
		}
		anchor, err := firstOn(from, loc, s.Slot.Weekday, s.Slot.Time)
		if err != nil {
			continue // an unparseable time cannot produce a lesson
		}
		times, err := recur.Expand(anchor, "FREQ=WEEKLY;BYDAY="+byDay[s.Slot.Weekday], from, to)
		if err != nil {
			return nil, fmt.Errorf("schedule: slot %d: %w", s.Slot.ID, err)
		}
		dur := s.Slot.DurationMin
		if dur <= 0 {
			dur = DefaultDurationMin
		}
		var absent []model.TrainerAbsence
		if s.Enrollment.TrainerID != nil {
			absent = byTrainer[*s.Enrollment.TrainerID]
		}
		for _, t := range times {
			if absentOn(absent, t.Format("2006-01-02")) {
				continue
			}
			out = append(out, Lesson{
				SlotID: s.Slot.ID, Enrollment: s.Enrollment,
				Start: t, DurationMin: dur,
			})
		}
	}
	return out, nil
}

// absentOn reports whether date falls inside any absence. Dates are compared
// as strings, which is what the stored ISO format is for.
func absentOn(absences []model.TrainerAbsence, date string) bool {
	for _, a := range absences {
		if a.DateFrom <= date && date <= a.DateTo {
			return true
		}
	}
	return false
}

// firstOn returns the soonest datetime at or after the start of from's day (in
// loc) whose weekday matches, at the slot's "HH:MM". It is the recurrence
// anchor: it fixes the time of day, and the weekly rule runs from there.
func firstOn(from time.Time, loc *time.Location, weekday int, hhmm string) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	from = from.In(loc)
	delta := (weekday - int(from.Weekday()) + 7) % 7
	d := time.Date(from.Year(), from.Month(), from.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	return d.AddDate(0, 0, delta), nil
}
