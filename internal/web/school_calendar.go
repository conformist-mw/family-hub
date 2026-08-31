package web

import (
	"net/http"
	"os"
	"time"

	"familyhub/internal/ics"
	"familyhub/internal/model"
	"familyhub/internal/schooltoday"
)

// schoolFeedHorizon is how far ahead the school feed reaches. The syncer mirrors
// only a few weeks, so this is a generous ceiling, not a promise the cache is
// full that far out; it also reaches one day back so today's lessons stay
// visible until midnight.
const schoolFeedHorizon = 90 * 24 * time.Hour

// handleSchoolICS serves the mirrored academic timetable as its own ICS feed
// for a second HA Remote Calendar, separate from /calendar.ics. Guarded like
// that route: if SCHOOL_ICS_TOKEN is set, a matching ?token= is required, since
// this bypasses the traefik auth chain for HA's unattended poll.
//
// Which categories reach the feed is SCHOOL_ICS_INCLUDE (comma-separated:
// lesson,meal,daycare,routine). Unset means lessons only — the academic day
// without meals, recess and after-school care — which is what the evening
// "tomorrow's lessons" digest wants to read.
func (a *App) handleSchoolICS(w http.ResponseWriter, r *http.Request) {
	if token := os.Getenv("SCHOOL_ICS_TOKEN"); token != "" && r.URL.Query().Get("token") != token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	include := schooltoday.ParseCategories(os.Getenv("SCHOOL_ICS_INCLUDE"))
	if len(include) == 0 {
		include = map[schooltoday.Category]bool{schooltoday.CategoryLesson: true}
	}

	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -1)
	to := now.Add(schoolFeedHorizon)

	all, err := a.Store.SchoolLessons(from.Format(model.LocalDatetime), to.Format(model.LocalDatetime))
	if err != nil {
		a.serverError(w, err)
		return
	}

	lessons := make([]model.SchoolLesson, 0, len(all))
	for _, l := range all {
		if include[schooltoday.Classify(l.Subject)] {
			lessons = append(lessons, l)
		}
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Write(ics.RenderSchool(lessons, time.Local, now))
}
