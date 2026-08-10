package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"familyhub/internal/model"
)

func TestPlural(t *testing.T) {
	// Ukrainian needs three forms, and the teens are the trap: 11 and 12 take
	// the same form as 5, not the same as 1 and 2.
	cases := map[int]string{
		1: "1 заняття", 2: "2 заняття", 4: "4 заняття", 5: "5 занять",
		11: "11 занять", 12: "12 занять", 14: "14 занять",
		21: "21 заняття", 22: "22 заняття", 25: "25 занять",
		101: "101 заняття", 111: "111 занять", 0: "0 занять",
	}
	for n, want := range cases {
		if got := plural(n, "заняття", "заняття", "занять"); got != want {
			t.Errorf("plural(%d) = %q, want %q", n, got, want)
		}
	}
}

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

	if len(body.Upcoming) != 1 || body.Upcoming[0].Title != "Ортодонт" {
		t.Errorf("upcoming = %+v", body.Upcoming)
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
	if body.Today != "Четвер, 6 серпня" {
		t.Errorf("today = %q", body.Today)
	}
}

func TestHomeRequiresAuthentication(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter(t, testStore(t), []int64{42}, 0).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/mini/api/home", nil))
	assertAPIError(t, rec, http.StatusBadRequest, "bad_init_data")
}

func ptr[T any](v T) *T { return &v }
