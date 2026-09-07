// Package agenda answers "what is happening today" once, for every surface
// that asks it.
//
// Three sources feed a day: lessons expanded from the weekly schedule,
// one-off appointments, and occurrences of recurring chores. Every screen
// that shows a day used to merge some subset of them on its own — the web hub
// and the Mini App home both listed appointments and chores but never a
// lesson, so the schedule was the one thing "Сьогодні" would not tell you —
// and each formatted the "when" column in its own template helper.
//
// Build is a pure function over already-loaded rows, the same shape as
// ics.Render: each caller fetches what it is already able to fetch, and this
// package only decides order, wording, and which day a thing belongs to. That
// keeps it testable without a database, and keeps the web and the Mini App
// from drifting apart on the answer.
package agenda

import (
	"sort"
	"strconv"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/reminders"
	"familyhub/internal/schedule"
)

// dateOnly is the stored date layout, the one visits and absences are keyed
// by. The repo spells it inline elsewhere; a day is compared often enough here
// to earn a name.
const dateOnly = "2006-01-02"

// Kinds of thing a day is made of. The surface uses this to pick an icon and
// to decide where a tap leads; the wording of a row is already rendered.
const (
	KindLesson      = "lesson"
	KindAppointment = "appointment"
	KindChore       = "chore"
)

// Item is one thing that is happening, whatever kind of thing it is.
type Item struct {
	Kind string
	// ID identifies the row within its kind: the enrollment for a lesson (a
	// lesson has no identity of its own until it is marked), the appointment,
	// or the reminder.
	ID     int64
	Start  time.Time
	When   string // "16:00" inside today, "Чт 16:00" further out
	Title  string
	Person string
	// Status is the Ukrainian label of what already happened to this item, and
	// empty while nothing has. A lesson marked "проведено" in the bot has to
	// keep its place in the day rather than vanish from it — a row that
	// disappears once answered leaves no way to tell it apart from one nobody
	// has answered yet.
	Status string
	// Place is an appointment's address. Lessons carry the trainer's hall in
	// the course itself and chores happen at home, so both leave it empty.
	Place string
	// CanMark says a chore occurrence is open and materialised, so a surface
	// with buttons may offer to close it. A projection of a future rule is not
	// markable: it has not happened.
	CanMark bool
}

// Day is one screen's worth of agenda: what is left of today, and what is
// coming after it. Both are sorted by time, soonest first.
type Day struct {
	// Today holds the whole calendar day, including items whose time has
	// already passed. This screen is read as the picture of the day, not as a
	// countdown, so a lesson at 10:00 stays visible at 18:00 — with its status
	// if it has one.
	Today    []Item
	Upcoming []Item
}

// Build merges the three sources into a day. Anything before today is dropped;
// how far Upcoming reaches is decided by the window the caller loaded, and how
// much of it a screen shows by the caller slicing the result.
func Build(lessons []schedule.Lesson, visits []model.Visit, appts []model.Appointment,
	chores []reminders.Occurrence, now time.Time, loc *time.Location) Day {
	now = now.In(loc)
	today := now.Format(dateOnly)
	marked := visitStatuses(visits)

	var out Day
	add := func(it Item, start time.Time) {
		date := start.Format(dateOnly)
		switch {
		case date == today:
			it.When = start.Format("15:04")
			out.Today = append(out.Today, it)
		case date > today:
			it.When = dayPrefix(start, now) + start.Format("15:04")
			out.Upcoming = append(out.Upcoming, it)
		}
	}

	for _, l := range lessons {
		start := l.Start.In(loc)
		add(Item{
			Kind:   KindLesson,
			ID:     l.Enrollment.ID,
			Start:  start,
			Title:  l.Enrollment.Name,
			Person: l.Enrollment.Person,
			Status: model.StatusLabels[marked[visitKey(l.Enrollment.ID, start)]],
		}, start)
	}
	for _, a := range appts {
		start, err := a.Start(loc)
		if err != nil {
			continue
		}
		it := Item{
			Kind:   KindAppointment,
			ID:     a.ID,
			Start:  start,
			Title:  a.Title,
			Person: a.Person,
			Place:  a.Location,
		}
		// Only a closed appointment is labelled: "заплановано" on every other
		// row would be a column of noise.
		if a.Status == model.ApptStatusDone {
			it.Status = model.ApptStatusLabels[a.Status]
		}
		add(it, start)
	}
	for _, c := range chores {
		due := c.Due.In(loc)
		// Chores are a today matter. A daily one projected over a week would
		// fill "what is coming" with seven identical rows and push the lessons
		// and appointments out of it, and a chore that has not come due is not
		// yet anybody's business — the Справи screen is where the forward view
		// of the rules lives.
		if due.Format(dateOnly) != today {
			continue
		}
		it := Item{
			Kind:    KindChore,
			ID:      c.ReminderID,
			Start:   due,
			Title:   c.Title,
			Person:  c.Person,
			CanMark: c.Stored && !c.Closed(),
		}
		if c.Closed() {
			it.Status = model.OccStatusLabels[c.Status]
		}
		add(it, due)
	}

	byTime(out.Today)
	byTime(out.Upcoming)
	return out
}

// byTime orders a list soonest first, and puts the kinds in a fixed order
// within one minute so that two things at 16:00 do not swap places between
// requests.
func byTime(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].Start.Equal(items[j].Start) {
			return items[i].Start.Before(items[j].Start)
		}
		return items[i].Kind < items[j].Kind
	})
}

// dayPrefix names the day an upcoming item falls on, with a trailing space so
// it can be glued to a time. Beyond tomorrow a weekday is enough: the screens
// that show this look a week ahead at most, and a date there read as noise.
func dayPrefix(start, now time.Time) string {
	if start.Format(dateOnly) == now.AddDate(0, 0, 1).Format(dateOnly) {
		return "Завтра "
	}
	return model.WeekdayLabels[int(start.Weekday())] + " "
}

// visitStatuses indexes what has already been recorded against a lesson.
// A visit is unique per enrollment and date (store.ErrVisitExists), which is
// also how the bot decides whether today's lesson is already answered, so a
// day-level key is the whole truth here.
func visitStatuses(visits []model.Visit) map[string]string {
	out := make(map[string]string, len(visits))
	for _, v := range visits {
		out[strconv.FormatInt(v.EnrollmentID, 10)+" "+v.Date] = v.Status
	}
	return out
}

func visitKey(enrollmentID int64, start time.Time) string {
	return strconv.FormatInt(enrollmentID, 10) + " " + start.Format(dateOnly)
}
