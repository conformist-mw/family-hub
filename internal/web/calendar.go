package web

import (
	"net/http"
	"os"
	"time"

	"familyhub/internal/ics"
	"familyhub/internal/model"
)

// handleCalendarICS serves the lesson schedule, trainer absences and one-off
// appointments as one ICS feed for HA's Remote Calendar. If ICS_TOKEN is set,
// a matching ?token= is required —
// this route bypasses the traefik auth chain (machine-to-machine fetch).
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
	// Appointments reach 30 days back, unlike the forward-only lesson slots:
	// they are one-off events, and HA's calendar view is also a record of what
	// happened recently.
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -30)
	appointments, err := a.Store.ActiveAppointmentsFrom(from.Format(model.LocalDatetime))
	if err != nil {
		a.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Write(ics.Render(slots, absences, appointments, time.Local, now))
}
