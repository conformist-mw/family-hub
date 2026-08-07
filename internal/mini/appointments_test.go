package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

func appt(id int64, startsAt, endsAt, title, status string) model.Appointment {
	return model.Appointment{
		ID: id, StartsAt: startsAt, EndsAt: endsAt, Title: title, Status: status,
	}
}

func TestGroupByDay(t *testing.T) {
	// testNow is 2026-08-06 12:00, a Thursday.
	items := []model.Appointment{
		appt(1, "2026-08-06T09:00", "", "Ранкове", model.ApptStatusPlanned),
		appt(2, "2026-08-06T14:30", "2026-08-06T15:30", "Ортодонт", model.ApptStatusPlanned),
		appt(3, "2026-08-07T10:00", "", "Манікюр", model.ApptStatusDone),
		appt(4, "2026-08-13T11:00", "", "Лікар", model.ApptStatusPlanned),
	}

	days := groupByDay(items, testNow, time.UTC)

	if len(days) != 3 {
		t.Fatalf("got %d day sections, want 3", len(days))
	}

	wantLabels := []string{"Сьогодні, 6 серпня", "Завтра, 7 серпня", "Чт, 13 серпня"}
	for i, want := range wantLabels {
		if days[i].Label != want {
			t.Errorf("day %d label = %q, want %q", i, days[i].Label, want)
		}
	}

	if n := len(days[0].Items); n != 2 {
		t.Fatalf("today holds %d items, want 2 (the 09:00 one must not be hidden)", n)
	}
	if got := days[0].Items[1]; got.Time != "14:30" || got.EndTime != "15:30" {
		t.Errorf("times = %q–%q, want 14:30–15:30", got.Time, got.EndTime)
	}
	if got := days[0].Items[0].EndTime; got != "" {
		t.Errorf("missing ends_at rendered as %q, want empty", got)
	}
	if got := days[0].Date; got != "2026-08-06" {
		t.Errorf("date key = %q, want 2026-08-06", got)
	}
}

// One unparseable row must not take the whole screen down with it.
func TestGroupByDaySkipsUnparseableRows(t *testing.T) {
	items := []model.Appointment{
		appt(1, "definitely not a date", "", "Сміття", model.ApptStatusPlanned),
		appt(2, "2026-08-06T14:30", "", "Ортодонт", model.ApptStatusPlanned),
	}

	days := groupByDay(items, testNow, time.UTC)
	if len(days) != 1 || len(days[0].Items) != 1 {
		t.Fatalf("got %+v, want the one good row", days)
	}
	if days[0].Items[0].Title != "Ортодонт" {
		t.Fatalf("kept the wrong row: %+v", days[0].Items[0])
	}
}

func TestGroupByDayEmpty(t *testing.T) {
	if days := groupByDay(nil, testNow, time.UTC); len(days) != 0 {
		t.Fatalf("got %+v, want no sections", days)
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(database)
}

// testRouter mounts the Mini App the way main.go does, with the dev fixture on
// so requests can be made without signing a payload.
func testRouter(t *testing.T, st *store.Store, allowed []int64, devUser int64) http.Handler {
	t.Helper()
	h, err := NewRouter(st, discardLogger(), Config{
		BotToken:     testToken,
		AllowedUsers: allowed,
		DevUser:      devUser,
		Loc:          time.UTC,
		Now:          func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return h
}

func TestHandleAppointments(t *testing.T) {
	st := testStore(t)
	seed := []model.Appointment{
		{StartsAt: "2026-08-05T10:00", Title: "Вчорашній", Person: "Демид", Status: model.ApptStatusPlanned},
		{StartsAt: "2026-08-06T09:00", Title: "Ранкове", Person: "Єгор", Status: model.ApptStatusPlanned},
		{StartsAt: "2026-08-06T14:30", EndsAt: "2026-08-06T15:30", Title: "Ортодонт",
			Person: "Демид", Location: "Хрещатик 1", Status: model.ApptStatusPlanned},
		{StartsAt: "2026-08-07T10:00", Title: "Скасований", Person: "Демид", Status: model.ApptStatusCancelled},
	}
	for _, a := range seed {
		if _, err := st.CreateAppointment(a); err != nil {
			t.Fatalf("seed %q: %v", a.Title, err)
		}
	}

	rec := httptest.NewRecorder()
	testRouter(t, st, []int64{42}, 42).ServeHTTP(rec, request(""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}

	var body appointmentsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}

	// Yesterday's row is behind the window and the cancelled one is filtered
	// out by the store, so only today survives.
	if len(body.Days) != 1 {
		t.Fatalf("got %d days, want 1: %+v", len(body.Days), body.Days)
	}
	day := body.Days[0]
	if day.Label != "Сьогодні, 6 серпня" {
		t.Errorf("label = %q", day.Label)
	}
	if len(day.Items) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(day.Items), day.Items)
	}
	if got := day.Items[1]; got.Title != "Ортодонт" || got.Location != "Хрещатик 1" || got.Time != "14:30" {
		t.Errorf("second item = %+v", got)
	}
}

func TestHandleAppointmentsRejectsUnauthenticated(t *testing.T) {
	st := testStore(t)

	t.Run("no credentials and no fixture", func(t *testing.T) {
		rec := httptest.NewRecorder()
		testRouter(t, st, []int64{42}, 0).ServeHTTP(rec, request(""))
		assertAPIError(t, rec, http.StatusBadRequest, "bad_init_data")
	})

	t.Run("known Telegram user outside the family", func(t *testing.T) {
		raw := signInitData(t, testToken, launchData(t, 777, testNow.Add(-time.Minute)))
		rec := httptest.NewRecorder()
		testRouter(t, st, []int64{42}, 0).ServeHTTP(rec, request(raw))
		assertAPIError(t, rec, http.StatusForbidden, "forbidden")
	})
}

func assertAPIError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body)
	}
	var body struct {
		Error apiError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if body.Error.Code != code {
		t.Fatalf("code = %q, want %q", body.Error.Code, code)
	}
	if body.Error.Message == "" {
		t.Error("empty message")
	}
}

func TestNewRouterRequiresBotToken(t *testing.T) {
	if _, err := NewRouter(testStore(t), discardLogger(), Config{}); err == nil {
		t.Fatal("NewRouter accepted an empty bot token")
	}
}

// The shell is deliberately public — it carries no family data — while the API
// beside it is not.
func TestShellIsServedWithoutCredentials(t *testing.T) {
	h := testRouter(t, testStore(t), []int64{42}, 0)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mini/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("shell status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mini/assets/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("asset status = %d", rec.Code)
	}
}
