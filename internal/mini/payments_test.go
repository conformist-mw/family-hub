package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

func seedMonthlyCourse(t *testing.T, st *store.Store) int64 {
	t.Helper()
	id, err := st.CreateEnrollment(model.Enrollment{
		Person:         "Єгор",
		Name:           "Футбол",
		BillingType:    model.BillingMonthly,
		CurrentPrice:   3200,
		AttendanceMode: model.AttendanceExceptionsOnly,
	})
	if err != nil {
		t.Fatalf("seed enrollment: %v", err)
	}
	return id
}

func TestCreatePaymentForAPackOfLessons(t *testing.T) {
	st := testStore(t)
	courseID := seedCourse(t, st)
	h := testRouter(t, st, []int64{42}, 42)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/courses/"+itoa(courseID)+"/payments",
		`{"date":"2026-08-06","amount":"5000","lessons":"10","month":"2026-08","comment":"готівкою"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	stored, err := st.PaymentsForEnrollment(courseID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored %d payments, want 1", len(stored))
	}
	p := stored[0]
	if p.Amount != 5000 || p.LessonsPaid == nil || *p.LessonsPaid != 10 || p.Comment != "готівкою" {
		t.Errorf("payment = %+v", p)
	}
	// The month the client always sends is ignored on a per-lesson course: a
	// coverage range here would make the balance read as an unpaid month.
	if p.CoversFrom != nil || p.CoversUntil != nil {
		t.Errorf("coverage = %v..%v, want none", p.CoversFrom, p.CoversUntil)
	}
}

func TestCreatePaymentForAMonth(t *testing.T) {
	st := testStore(t)
	courseID := seedMonthlyCourse(t, st)
	h := testRouter(t, st, []int64{42}, 42)

	// Paid in August, for September — the shape a school fee actually has.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/courses/"+itoa(courseID)+"/payments",
		`{"date":"2026-08-28","amount":"3200","lessons":"","month":"2026-09","comment":""}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	stored, err := st.PaymentsForEnrollment(courseID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored %d payments, want 1", len(stored))
	}
	p := stored[0]
	if p.Date != "2026-08-28" {
		t.Errorf("date = %q", p.Date)
	}
	if p.CoversFrom == nil || *p.CoversFrom != "2026-09-01" || p.CoversUntil == nil || *p.CoversUntil != "2026-09-30" {
		t.Errorf("coverage = %v..%v", p.CoversFrom, p.CoversUntil)
	}
	if p.LessonsPaid != nil {
		t.Errorf("lessons = %v, want none", *p.LessonsPaid)
	}
}

// The billing type comes from the course, not from what the client sent, so a
// per-lesson course still demands a lesson count however the form was filled.
func TestPaymentValidationIsReported(t *testing.T) {
	st := testStore(t)
	courseID := seedCourse(t, st)
	h := testRouter(t, st, []int64{42}, 42)

	cases := []struct {
		name  string
		body  string
		field string
	}{
		{"bad date", `{"date":"06.08.2026","amount":"5000","lessons":"10","month":"","comment":""}`, "date"},
		{"no amount", `{"date":"2026-08-06","amount":"","lessons":"10","month":"","comment":""}`, "amount"},
		{"no lessons", `{"date":"2026-08-06","amount":"5000","lessons":"","month":"2026-08","comment":""}`, "lessons"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/courses/"+itoa(courseID)+"/payments", tc.body))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body)
			}
			var body struct {
				Error struct {
					Code, Message, Field string
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Error.Code != "validation" || body.Error.Field != tc.field || body.Error.Message == "" {
				t.Fatalf("error = %+v, want field %q", body.Error, tc.field)
			}
		})
	}

	if stored, err := st.PaymentsForEnrollment(courseID); err != nil || len(stored) != 0 {
		t.Fatalf("rejected payments reached the database: %v (%d rows)", err, len(stored))
	}
}

func TestPaymentOnMissingCourse(t *testing.T) {
	h := testRouter(t, testStore(t), []int64{42}, 42)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/courses/999/payments",
		`{"date":"2026-08-06","amount":"5000","lessons":"10","month":"","comment":""}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
}

func TestPaymentRequiresAuthentication(t *testing.T) {
	st := testStore(t)
	courseID := seedCourse(t, st)
	h := testRouter(t, st, []int64{42}, 0) // fixture off

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/courses/"+itoa(courseID)+"/payments",
		`{"date":"2026-08-06","amount":"5000","lessons":"10","month":"","comment":""}`))
	assertAPIError(t, rec, http.StatusBadRequest, "bad_init_data")

	if stored, err := st.PaymentsForEnrollment(courseID); err != nil || len(stored) != 0 {
		t.Fatalf("unauthenticated write stored %d rows (err %v)", len(stored), err)
	}
}

// The form needs to know which question to ask — how many lessons, or which
// month — before the person can answer it.
func TestCoursesCarryBillingType(t *testing.T) {
	st := testStore(t)
	seedCourse(t, st)
	seedMonthlyCourse(t, st)
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
	billing := map[string]string{}
	for _, c := range body.Courses {
		billing[c.Name] = c.Billing
	}
	if billing["Логопед"] != model.BillingPerLesson || billing["Футбол"] != model.BillingMonthly {
		t.Errorf("billing = %v", billing)
	}
}
