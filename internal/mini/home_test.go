package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"familyhub/internal/model"
)

func TestBalanceLine(t *testing.T) {
	perLesson := func(remaining int) model.Balance {
		b := model.Balance{Remaining: remaining}
		b.BillingType = model.BillingPerLesson
		return b
	}
	cases := []struct {
		name string
		b    model.Balance
		want string
	}{
		{"some left", perLesson(3), "залишилось 3 заняття"},
		{"one left", perLesson(1), "залишилось 1 заняття"},
		{"none left", perLesson(0), "оплачених занять немає"},
		// A negative balance is normal here: lessons happen, then get paid for.
		{"owing", perLesson(-2), "борг 2 заняття"},
	}
	for _, tc := range cases {
		if got := balanceLine(tc.b); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}

	monthly := model.Balance{CoveredNow: true, CoversUntil: "2026-08-31", DaysLeft: 21}
	monthly.BillingType = model.BillingMonthly
	if got := balanceLine(monthly); got != "абонемент до 31 сер, 21 день" {
		t.Errorf("monthly = %q", got)
	}
	unpaid := model.Balance{}
	unpaid.BillingType = model.BillingMonthly
	if got := balanceLine(unpaid); got != "абонемент не оплачено" {
		t.Errorf("unpaid monthly = %q", got)
	}
}

func TestMoney(t *testing.T) {
	for in, want := range map[float64]string{800: "800 ₴", 0: "0 ₴", 1200.5: "1200.50 ₴"} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestShortDate(t *testing.T) {
	if got := shortDate("2026-08-31"); got != "31 сер" {
		t.Errorf("got %q", got)
	}
	// A value that will not parse is shown as-is rather than blanked.
	if got := shortDate("хтозна"); got != "хтозна" {
		t.Errorf("got %q", got)
	}
}

func TestHome(t *testing.T) {
	st := testStore(t)
	courseID := seedCourse(t, st)
	if _, err := st.CreateAppointment(model.Appointment{
		StartsAt: "2026-08-06T14:30", Title: "Ортодонт", Person: "Демид",
		Status: model.ApptStatusPlanned,
	}); err != nil {
		t.Fatalf("seed appointment: %v", err)
	}
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: courseID, Date: "2026-08-01", Amount: 5000, LessonsPaid: ptr(int64(10)),
	}); err != nil {
		t.Fatalf("seed payment: %v", err)
	}

	rec := httptest.NewRecorder()
	testRouter(t, st, []int64{42}, 42).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/mini/api/home", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var body homeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}

	// Today is Thursday: the appointment is on it, and the course's Tuesday
	// lesson is what comes next. Both used to be missing — the lesson because
	// the screen never asked for the schedule at all.
	if len(body.Today) != 1 {
		t.Fatalf("today = %+v", body.Today)
	}
	if it := body.Today[0]; it.Kind != "appointment" || it.Title != "Ортодонт" || it.When != "14:30" {
		t.Errorf("today[0] = %+v", it)
	}
	if len(body.Upcoming) != 1 {
		t.Fatalf("upcoming = %+v", body.Upcoming)
	}
	if it := body.Upcoming[0]; it.Kind != "lesson" || it.Title != "Логопед" || it.When != "Вт 13:35" {
		t.Errorf("upcoming[0] = %+v", it)
	}
	if len(body.Courses) != 1 {
		t.Fatalf("courses = %+v", body.Courses)
	}
	c := body.Courses[0]
	if c.Name != "Логопед" || c.Schedule != "Вт 13:35" {
		t.Errorf("course = %+v", c)
	}
	if c.State != "ok" || c.Balance != "залишилось 10 занять" {
		t.Errorf("balance = %q, state = %q", c.Balance, c.State)
	}
	if len(body.Payments) != 1 {
		t.Fatalf("payments = %+v", body.Payments)
	}
	if p := body.Payments[0]; p.Amount != "5000 ₴" || p.Detail != "10 занять" || p.Date != "1 сер" {
		t.Errorf("payment = %+v", p)
	}
	// The heading is the one date on the screen that is not about a row.
	if body.Date != "Четвер, 6 серпня" {
		t.Errorf("date = %q", body.Date)
	}
}

func TestHomeRequiresAuthentication(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter(t, testStore(t), []int64{42}, 0).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/mini/api/home", nil))
	assertAPIError(t, rec, http.StatusBadRequest, "bad_init_data")
}

func ptr[T any](v T) *T { return &v }
