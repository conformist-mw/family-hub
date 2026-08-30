package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// seedCourse creates the course the schedule screen edits. It builds one
// rather than leaning on the seed migration, which ships no enrollments — the
// tests that matter here must not quietly skip themselves.
func seedCourse(t *testing.T, st *store.Store) int64 {
	t.Helper()
	id, err := st.CreateEnrollment(model.Enrollment{
		Person:         "Демид",
		Name:           "Логопед",
		BillingType:    model.BillingPerLesson,
		CurrentPrice:   500,
		LowThreshold:   2,
		AttendanceMode: model.AttendancePerSession,
	})
	if err != nil {
		t.Fatalf("seed enrollment: %v", err)
	}
	// A schedule that predates the window under test, the way a real course does.
	if err := st.CreateSlot(id, 2, "13:35", 60, "2000-01-01T00:00"); err != nil {
		t.Fatalf("seed slot: %v", err)
	}
	return id
}

func TestCoursesCarryScheduleAndWeekdayNames(t *testing.T) {
	st := testStore(t)
	seedCourse(t, st)
	h := testRouter(t, st, []int64{42}, 42)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mini/api/courses", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var body coursesDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The weekday vocabulary comes from the server so the client never has to
	// carry its own copy of the names.
	if len(body.Weekdays) != 7 || body.Weekdays[1] != model.WeekdayLabels[1] {
		t.Fatalf("weekdays = %v", body.Weekdays)
	}
	if len(body.Courses) != 1 {
		t.Fatalf("got %d courses, want the seeded one", len(body.Courses))
	}
	c := body.Courses[0]
	if c.Name != "Логопед" || c.Person != "Демид" {
		t.Errorf("course = %+v", c)
	}
	if len(c.Schedule) != 1 || c.Schedule[0].Time != "13:35" || c.Schedule[0].WeekdayName != model.WeekdayLabels[2] {
		t.Errorf("schedule = %+v", c.Schedule)
	}
	// Nothing has been paid for this course, so the card says so here rather
	// than sending the reader to the home tab for it.
	if c.State != "empty" || c.Balance != "оплачених занять немає" {
		t.Errorf("balance = %q, state = %q", c.Balance, c.State)
	}
}

func TestSlotLifecycle(t *testing.T) {
	st := testStore(t)
	courseID := seedCourse(t, st)
	h := testRouter(t, st, []int64{42}, 42)

	// Add a Thursday 13:35.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost,
		"/mini/api/courses/"+itoa(courseID)+"/slots", `{"weekday":"4","time":"13:35","duration":"45"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}

	added := findSlot(t, h, courseID, 4, "13:35")
	if added.DurationMin != 45 {
		t.Errorf("duration = %d, want 45", added.DurationMin)
	}

	// Move it to Tuesday 14:00 — the operation the app never had.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPut,
		"/mini/api/slots/"+itoa(added.ID), `{"weekday":"2","time":"14:00","duration":"45"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}

	moved := findSlot(t, h, courseID, 2, "14:00")
	// The row id survives the move: the ICS feed uses it as the event uid, and
	// a new one would duplicate the lesson in the family calendar.
	if moved.ID != added.ID {
		t.Fatalf("slot id changed on edit: %d -> %d", added.ID, moved.ID)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/mini/api/slots/"+itoa(added.ID), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if slotExists(t, h, courseID, added.ID) {
		t.Fatal("deleted slot still in the schedule")
	}
}

func TestSlotValidationIsReported(t *testing.T) {
	st := testStore(t)
	courseID := seedCourse(t, st)
	h := testRouter(t, st, []int64{42}, 42)

	cases := []struct {
		name, body, field string
	}{
		{"nonsense time", `{"weekday":"2","time":"abc"}`, "time"},
		{"weekday out of range", `{"weekday":"9","time":"13:35"}`, "weekday"},
		{"bad duration", `{"weekday":"2","time":"13:35","duration":"довго"}`, "duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, jsonRequest(http.MethodPost,
				"/mini/api/courses/"+itoa(courseID)+"/slots", tc.body))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body)
			}
			var body struct {
				Error struct{ Code, Message, Field string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Error.Field != tc.field || body.Error.Message == "" {
				t.Fatalf("error = %+v, want field %q", body.Error, tc.field)
			}
		})
	}
}

func TestSlotWritesRequireAuthentication(t *testing.T) {
	h := testRouter(t, testStore(t), []int64{42}, 0) // fixture off

	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/mini/api/courses", nil),
		jsonRequest(http.MethodPost, "/mini/api/courses/1/slots", `{"weekday":"2","time":"13:35"}`),
		jsonRequest(http.MethodPut, "/mini/api/slots/1", `{"weekday":"2","time":"13:35"}`),
		httptest.NewRequest(http.MethodDelete, "/mini/api/slots/1", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		assertAPIError(t, rec, http.StatusBadRequest, "bad_init_data")
	}
}

func TestSlotWriteOnMissingRow(t *testing.T) {
	h := testRouter(t, testStore(t), []int64{42}, 42)

	for _, r := range []*http.Request{
		jsonRequest(http.MethodPost, "/mini/api/courses/99999/slots", `{"weekday":"2","time":"13:35"}`),
		jsonRequest(http.MethodPut, "/mini/api/slots/99999", `{"weekday":"2","time":"13:35"}`),
		httptest.NewRequest(http.MethodDelete, "/mini/api/slots/99999", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s -> %d, want 404", r.Method, r.URL.Path, rec.Code)
		}
	}
}

func courseSchedule(t *testing.T, h http.Handler, courseID int64) []slotDTO {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mini/api/courses", nil))
	var body coursesDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, c := range body.Courses {
		if c.ID == courseID {
			return c.Schedule
		}
	}
	t.Fatalf("course %d not in the response", courseID)
	return nil
}

func findSlot(t *testing.T, h http.Handler, courseID int64, weekday int, hhmm string) slotDTO {
	t.Helper()
	for _, s := range courseSchedule(t, h, courseID) {
		if s.Weekday == weekday && s.Time == hhmm {
			return s
		}
	}
	t.Fatalf("no slot %d %s in course %d", weekday, hhmm, courseID)
	return slotDTO{}
}

func slotExists(t *testing.T, h http.Handler, courseID, slotID int64) bool {
	t.Helper()
	for _, s := range courseSchedule(t, h, courseID) {
		if s.ID == slotID {
			return true
		}
	}
	return false
}
