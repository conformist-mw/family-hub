package web

import (
	"net/http"
	"os"
	"time"

	"familyhub/internal/ics"
	"familyhub/internal/model"
	"familyhub/internal/reminders"
	"familyhub/internal/schedule"
)

// choreHorizon is how far ahead recurring chores are projected into the feed.
// Far enough that the morning summary can look a season out, short enough that
// a daily chore does not turn the calendar into a wall.
const choreHorizon = 90 * 24 * time.Hour

// lessonHorizon is how far ahead the weekly schedule is expanded. Lessons used
// to go out as an endless RRULE, so this is a new limit: the feed now stops
// somewhere. A season is past any schedule this family plans, and HA re-polls
// far more often than it, so the window keeps sliding forward.
const lessonHorizon = 90 * 24 * time.Hour

// handleCalendarICS serves the lesson schedule, trainer absences, one-off
// appointments and recurring chores as one ICS feed for HA's Remote Calendar.
// If ICS_TOKEN is set, a matching ?token= is required — this route bypasses
// the traefik auth chain (machine-to-machine fetch).
func (a *App) handleCalendarICS(w http.ResponseWriter, r *http.Request) {
	if token := os.Getenv("ICS_TOKEN"); token != "" && r.URL.Query().Get("token") != token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	slots, err := a.Store.AllActiveSlots()
	if err != nil {
		a.serverError(w, err)
		return
	}
	now := time.Now()
	absences, err := a.Store.UpcomingAbsences(now.Format("2006-01-02"))
	if err != nil {
		a.serverError(w, err)
		return
	}
	// Lessons are expanded here rather than handed over as a rule, so that a
	// weekly slot keeps its wall-clock time across a clock change. Forward
	// only: the schedule says what is expected, and what actually happened is
	// the visits journal's answer, not the calendar's.
	lessons, err := schedule.Expand(slots, absences, time.Local, now, now.Add(lessonHorizon))
	if err != nil {
		a.serverError(w, err)
		return
	}
	// Appointments reach 30 days back, unlike the forward-only lesson slots:
	// they are one-off events, and HA's calendar view is also a record of what
	// happened recently.
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -30)
	appointments, err := a.Store.ActiveAppointmentsFrom(from.Format(model.LocalDatetime))
	if err != nil {
		a.serverError(w, err)
		return
	}
	// Chores reach back exactly as far as the materialiser records them, so
	// nothing it wrote is missing from the calendar, and forward far enough to
	// answer "what is coming". A nil service means the feature is off, not an
	// error: the feed simply carries no chores.
	var chores []reminders.Occurrence
	if a.Reminders != nil {
		chores, err = a.Reminders.Upcoming(
			now.Add(-reminders.BackfillWindow), now.Add(choreHorizon))
		if err != nil {
			a.serverError(w, err)
			return
		}
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Write(ics.Render(lessons, absences, appointments, chores, time.Local, now))
}
