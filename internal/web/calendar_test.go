package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/reminders"
	"familyhub/internal/store"
)

// The ICS route had no test at all: ics.Render's chore loop was well covered,
// its only caller was not. Nothing pinned that reminders reach the feed, that
// a nil service degrades to "no chores" rather than a 500, or that the window
// is the one the materialiser records into.
func calendarHandler(t *testing.T, withChores bool) (http.Handler, *store.Store, *reminders.Service) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(database)

	var svc *reminders.Service
	if withChores {
		svc = reminders.NewService(st, time.Local, logger, time.Now)
	}
	return NewRouter(database, logger, "", nil, nil, svc), st, svc
}

func fetchICS(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/calendar.ics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /calendar.ics = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Fatalf("content-type = %q", ct)
	}
	return strings.ReplaceAll(rec.Body.String(), "\r\n", "\n")
}

func TestTheFeedCarriesRecurringChores(t *testing.T) {
	h, st, svc := calendarHandler(t, true)
	now := time.Now()

	// Running for a while, so it has both a recorded past and a future.
	r, err := st.CreateReminder(
		model.Reminder{
			Title: "Кешбек", Person: "Олег", Active: true,
			ActiveSince: now.AddDate(0, 0, -10).Format(model.LocalDatetime),
		},
		model.ReminderRule{
			ValidFromAt: now.AddDate(0, 0, -10).Format(model.LocalDatetime),
			DTStart:     now.AddDate(0, 0, -10).Format(model.LocalDatetime),
			RRule:       "FREQ=DAILY",
		})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}

	body := fetchICS(t, h)
	if !strings.Contains(body, "UID:reminder-") {
		t.Fatalf("no chore reached the feed:\n%s", body)
	}
	if !strings.Contains(body, "SUMMARY:Кешбек · Олег") {
		t.Fatalf("chore summary missing:\n%s", body)
	}

	// Both halves of the timeline: something already recorded, and something
	// still to come.
	past := now.AddDate(0, 0, -5).Format("20060102")
	future := now.AddDate(0, 0, 5).Format("20060102")
	if !strings.Contains(body, "UID:reminder-"+strconv.FormatInt(r.ID, 10)+"-"+past) {
		t.Fatalf("the recorded past is missing from the feed (looked for %s)", past)
	}
	if !strings.Contains(body, "UID:reminder-"+strconv.FormatInt(r.ID, 10)+"-"+future) {
		t.Fatalf("the projected future is missing from the feed (looked for %s)", future)
	}
}

// A closed chore keeps its place in the calendar and says how it was closed.
func TestTheFeedMarksClosedChores(t *testing.T) {
	h, st, svc := calendarHandler(t, true)
	now := time.Now()
	start := now.AddDate(0, 0, -3)

	r, err := st.CreateReminder(
		model.Reminder{Title: "Пробіг", Active: true,
			ActiveSince: start.Format(model.LocalDatetime)},
		model.ReminderRule{
			ValidFromAt: start.Format(model.LocalDatetime),
			DTStart:     start.Format(model.LocalDatetime),
			RRule:       "FREQ=DAILY",
		})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	closed := time.Date(start.Year(), start.Month(), start.Day(),
		start.Hour(), start.Minute(), 0, 0, time.Local).AddDate(0, 0, 1)
	if err := svc.Mark(r.ID, closed, model.OccDone, "Олег"); err != nil {
		t.Fatalf("mark: %v", err)
	}

	if body := fetchICS(t, h); !strings.Contains(body, "SUMMARY:✓ Пробіг") {
		t.Fatalf("a closed chore is not marked in the feed:\n%s", body)
	}
}

// The feature being off is not an error: the feed carries everything else.
func TestTheFeedWorksWithoutTheReminderService(t *testing.T) {
	h, _, _ := calendarHandler(t, false)
	body := fetchICS(t, h)
	if strings.Contains(body, "UID:reminder-") {
		t.Fatalf("chores appeared with no service wired:\n%s", body)
	}
	if !strings.HasPrefix(body, "BEGIN:VCALENDAR") || !strings.Contains(body, "END:VCALENDAR") {
		t.Fatalf("not a calendar:\n%s", body)
	}
}
