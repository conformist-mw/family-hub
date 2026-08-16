package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestUpdatePayment(t *testing.T) {
	st := testStore(t)
	courseID := seedCourse(t, st)
	id, err := st.CreatePayment(model.Payment{
		EnrollmentID: courseID, Date: "2026-08-01", Amount: 500, LessonsPaid: ptr(int64(1)),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := testRouter(t, st, []int64{42}, 42)

	// The amount was typed wrong on a phone — the case this route exists for.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPut, "/mini/api/payments/"+itoa(id),
		`{"date":"2026-08-01","amount":"5000","lessons":"10","month":"","comment":"виправлено"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	p, err := st.GetPayment(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.Amount != 5000 || p.LessonsPaid == nil || *p.LessonsPaid != 10 || p.Comment != "виправлено" {
		t.Errorf("payment = %+v", p)
	}
	// The course is not the phone's to change, so the edit keeps it.
	if p.EnrollmentID != courseID {
		t.Errorf("enrollment = %d, want %d", p.EnrollmentID, courseID)
	}
}

func TestDeletePayment(t *testing.T) {
	st := testStore(t)
	courseID := seedCourse(t, st)
	id, err := st.CreatePayment(model.Payment{
		EnrollmentID: courseID, Date: "2026-08-01", Amount: 500, LessonsPaid: ptr(int64(1)),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := testRouter(t, st, []int64{42}, 42)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/mini/api/payments/"+itoa(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if _, err := st.GetPayment(id); err == nil {
		t.Error("payment still there after delete")
	}
}

func TestWriteOnMissingPayment(t *testing.T) {
	h := testRouter(t, testStore(t), []int64{42}, 42)
	for _, r := range []*http.Request{
		jsonRequest(http.MethodPut, "/mini/api/payments/999",
			`{"date":"2026-08-01","amount":"500","lessons":"1","month":"","comment":""}`),
		httptest.NewRequest(http.MethodDelete, "/mini/api/payments/999", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s -> %d, want 404", r.Method, rec.Code)
		}
	}
}

// Money is the thing two people have to agree on, so every change to it is
// announced the way a visit is.
func TestPaymentsReachTheFamilyGroup(t *testing.T) {
	st := testStore(t)
	courseID := seedCourse(t, st)
	fam := &group{}
	h := testRouterWithNotifier(t, st, []int64{42}, 0, fam)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodPost, "/mini/api/courses/"+itoa(courseID)+"/payments",
		`{"date":"2026-08-06","amount":"5000","lessons":"10","month":"","comment":""}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(fam.sent) != 1 {
		t.Fatalf("group got %d messages, want 1", len(fam.sent))
	}
	// Attributed to whoever opened the Mini App, and naming the course the
	// money went to — the group has no other way of knowing which one.
	if !strings.Contains(fam.sent[0], "💸 Оплата (Тест)") || !strings.Contains(fam.sent[0], "Логопед") ||
		!strings.Contains(fam.sent[0], "5000 ₴") {
		t.Errorf("group message = %q", fam.sent[0])
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedRequest(t, http.MethodDelete, "/mini/api/payments/"+itoa(created.ID), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status = %d, body = %s", rec.Code, rec.Body)
	}
	if len(fam.sent) != 2 || !strings.Contains(fam.sent[1], "🗑 Оплату видалено (Тест)") {
		t.Errorf("group messages = %q", fam.sent)
	}
}

// Tapping a payment row opens a filled form, so the row has to carry the form's
// values and not only the strings the list shows.
func TestHomePaymentRowsCarryTheFormValues(t *testing.T) {
	st := testStore(t)
	courseID := seedMonthlyCourse(t, st)
	from, until := "2026-09-01", "2026-09-30"
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: courseID, Date: "2026-08-28", Amount: 3200,
		CoversFrom: &from, CoversUntil: &until, Comment: "на вересень",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	testRouter(t, st, []int64{42}, 42).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/mini/api/home", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var body homeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Payments) != 1 {
		t.Fatalf("payments = %+v", body.Payments)
	}
	p := body.Payments[0]
	if p.Billing != model.BillingMonthly || p.DateISO != "2026-08-28" || p.Month != "2026-09" {
		t.Errorf("row = %+v", p)
	}
	// The display string is "3200 ₴"; an input cannot take that back.
	if p.Value != "3200" || p.Comment != "на вересень" {
		t.Errorf("value = %q, comment = %q", p.Value, p.Comment)
	}
}
