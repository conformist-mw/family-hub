package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// seedLedger gives a per-lesson course a payment and three marked lessons, so
// the ledger has something to reconcile and the running balance has somewhere
// to move.
func seedLedger(t *testing.T, st *store.Store) int64 {
	t.Helper()
	id := seedCourse(t, st)
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: id, Date: "2026-08-01", Amount: 5000, LessonsPaid: ptr(int64(10)),
	}); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	for _, v := range []struct{ date, status, comment string }{
		{"2026-08-04", model.StatusDone, ""},
		{"2026-08-05", model.StatusCancelled, "хворів"},
		{"2026-08-06", model.StatusDone, ""},
	} {
		if _, err := st.CreateVisit(id, v.date, v.status, v.comment); err != nil {
			t.Fatalf("seed visit: %v", err)
		}
	}
	return id
}

func fetchAudit(t *testing.T, h http.Handler, path string) auditDTO {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var body auditDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	return body
}

func TestAuditLedger(t *testing.T) {
	st := testStore(t)
	id := seedLedger(t, st)
	h := testRouter(t, st, []int64{42}, 42)

	body := fetchAudit(t, h, "/mini/api/courses/"+itoa(id)+"/audit")

	// Default period is "since the last payment", so the payment opens the
	// ledger and the three visits follow it.
	if body.Period != "з останньої оплати (01.08.2026) по 06.08.2026" {
		t.Errorf("period = %q", body.Period)
	}
	if len(body.Rows) != 4 {
		t.Fatalf("rows = %+v", body.Rows)
	}
	if r := body.Rows[0]; r.Kind != "payment" || r.Label != "оплата +10" || r.Amount != "5000 ₴" || r.Balance != "10" {
		t.Errorf("payment row = %+v", r)
	}
	// The running balance drops on a done lesson and stands still on a
	// cancelled one — the whole point of reading this screen.
	if b := []string{body.Rows[1].Balance, body.Rows[2].Balance, body.Rows[3].Balance}; b[0] != "9" || b[1] != "9" || b[2] != "8" {
		t.Errorf("balances = %v, want 9, 9, 8", b)
	}
	if r := body.Rows[2]; r.Status != model.StatusCancelled || r.Label != "скасовано" || r.Comment != "хворів" {
		t.Errorf("cancelled row = %+v", r)
	}
	want := []string{"2 проведено", "1 скасовано", "оплачено: 10 занять · 5000 ₴", "залишок: 0 → 8"}
	if len(body.Summary) != len(want) {
		t.Fatalf("summary = %v, want %v", body.Summary, want)
	}
	for i, w := range want {
		if body.Summary[i] != w {
			t.Errorf("summary[%d] = %q, want %q", i, body.Summary[i], w)
		}
	}
}

// A monthly course counts no lessons, so the balance column stays empty rather
// than showing a number that means nothing.
func TestAuditOfAMonthlyCourseHasNoBalance(t *testing.T) {
	st := testStore(t)
	id := seedMonthlyCourse(t, st)
	from, until := "2026-08-01", "2026-08-31"
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: id, Date: "2026-07-28", Amount: 3200, CoversFrom: &from, CoversUntil: &until,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := testRouter(t, st, []int64{42}, 42)

	body := fetchAudit(t, h, "/mini/api/courses/"+itoa(id)+"/audit?range=all")
	if len(body.Rows) != 1 {
		t.Fatalf("rows = %+v", body.Rows)
	}
	if r := body.Rows[0]; r.Label != "абонемент до 31 сер" || r.Balance != "" {
		t.Errorf("row = %+v", r)
	}
	for _, s := range body.Summary {
		if s == "залишок: 0 → 0" {
			t.Error("monthly course reported a lesson balance")
		}
	}
}

// A custom period reaching past today is how the forecast is asked for; the
// uncovered lessons in it are what says money is due.
func TestAuditForecast(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st) // one slot: Tuesday 13:35
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: id, Date: "2026-08-01", Amount: 500, LessonsPaid: ptr(int64(1)),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := testRouter(t, st, []int64{42}, 42)

	// testNow is 6 August 2026; three Tuesdays follow before the 1st of Sept.
	body := fetchAudit(t, h, "/mini/api/courses/"+itoa(id)+"/audit?range=custom&from=2026-08-01&to=2026-08-31")
	if body.Period != "01.08.2026 — 31.08.2026" {
		t.Errorf("period = %q", body.Period)
	}
	var future, uncovered int
	for _, r := range body.Rows {
		if r.Kind != "future" {
			continue
		}
		future++
		if !r.Covered {
			uncovered++
		}
		if r.Balance != "" {
			t.Errorf("future row carries a balance: %+v", r)
		}
	}
	if future == 0 || uncovered == 0 {
		t.Fatalf("future = %d, uncovered = %d, want both non-zero (rows %+v)", future, uncovered, body.Rows)
	}
	if len(body.Forecast) == 0 {
		t.Fatal("no forecast lines")
	}
}

// A period that will not parse must not blank the screen: the default one is
// shown and the payload says why.
func TestAuditRejectsBadPeriodButStillAnswers(t *testing.T) {
	st := testStore(t)
	id := seedLedger(t, st)
	h := testRouter(t, st, []int64{42}, 42)

	body := fetchAudit(t, h, "/mini/api/courses/"+itoa(id)+"/audit?range=custom&from=позавчора&to=2026-08-31")
	if body.Notice == "" {
		t.Error("no notice about the rejected period")
	}
	if body.Range != "last_payment" || len(body.Rows) == 0 {
		t.Errorf("fell back to %q with %d rows", body.Range, len(body.Rows))
	}
}

func TestAuditOfMissingCourse(t *testing.T) {
	h := testRouter(t, testStore(t), []int64{42}, 42)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mini/api/courses/999/audit", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestAuditRequiresAuthentication(t *testing.T) {
	st := testStore(t)
	id := seedLedger(t, st)
	rec := httptest.NewRecorder()
	testRouter(t, st, []int64{42}, 0).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/mini/api/courses/"+itoa(id)+"/audit", nil))
	assertAPIError(t, rec, http.StatusBadRequest, "bad_init_data")
}
