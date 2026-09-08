package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// paymentsFixture gives a karate course to hang payments on, and returns the
// router alongside the store so a test can assert on both the screen and the
// row behind it.
func paymentsFixture(t *testing.T) (http.Handler, *store.Store, int64) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(database)
	id, err := st.CreateEnrollment(model.Enrollment{
		Person: "Демид", Name: "Карате", BillingType: model.BillingPerLesson,
		CurrentPrice: 400, LowThreshold: 2, AttendanceMode: model.AttendancePerSession,
	})
	if err != nil {
		t.Fatalf("seed enrollment: %v", err)
	}
	h := NewRouter(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "", nil, nil, nil)
	return h, st, id
}

func TestPostingAnExtraStoresItWithItsLabel(t *testing.T) {
	h, st, id := paymentsFixture(t)

	rec := post(t, h, "/lessons/payments", url.Values{
		"enrollment_id": {itoa(id)},
		"kind":          {"extra"},
		"date":          {"2026-09-05"},
		"amount":        {"1500"},
		"label":         {"Кімоно"},
		// The lesson field rides along on every submit; an extra must ignore
		// it rather than be validated against it.
		"lessons_paid": {""},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	rows, err := st.ListPayments(store.PaymentFilter{})
	if err != nil {
		t.Fatalf("list payments: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	if !rows[0].IsExtra() || rows[0].Label != "Кімоно" {
		t.Errorf("stored = %+v, want an extra labelled Кімоно", rows[0])
	}
	if rows[0].LessonsPaid != nil || rows[0].CoversFrom != nil {
		t.Errorf("stored = %+v, want no lessons and no coverage", rows[0])
	}
}

// The label is the one field only the person can supply, and an unnamed extra
// is a number with no answer to "за що".
func TestPostingAnExtraWithoutALabelIsRejected(t *testing.T) {
	h, st, id := paymentsFixture(t)

	rec := post(t, h, "/lessons/payments", url.Values{
		"enrollment_id": {itoa(id)},
		"kind":          {"extra"},
		"date":          {"2026-09-05"},
		"amount":        {"1500"},
		"label":         {"  "},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "вкажи, за що оплата") {
		t.Errorf("the form came back without the reason:\n%s", rec.Body)
	}

	rows, _ := st.ListPayments(store.PaymentFilter{})
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want nothing stored", rows)
	}
}

// A form rejected for something else is re-rendered from what Parse returned.
// If the kind did not survive, an extra with a typo in the amount would redraw
// as a course payment and the label the person typed would be gone.
func TestARejectedExtraComesBackAsAnExtra(t *testing.T) {
	h, _, id := paymentsFixture(t)

	rec := post(t, h, "/lessons/payments", url.Values{
		"enrollment_id": {itoa(id)},
		"kind":          {"extra"},
		"date":          {"05.09.2026"}, // wrong format — fails before the label
		"amount":        {"1500"},
		"label":         {"Кімоно"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="extra" checked`) {
		t.Errorf("the kind switch fell back to a course payment:\n%s", body)
	}
	if !strings.Contains(body, `value="Кімоно"`) {
		t.Errorf("the label was dropped from the re-render:\n%s", body)
	}
}

// Opening an existing extra for edit has to show it as one. Rendered as a
// course payment, saving it again would either lose the label or fail asking
// for a lesson count the person never meant to give.
func TestEditingAnExtraRoundTrips(t *testing.T) {
	h, st, id := paymentsFixture(t)
	payID, err := st.CreatePayment(model.Payment{
		EnrollmentID: id, Kind: model.PaymentKindExtra,
		Date: "2026-09-05", Amount: 1500, Label: "Кімоно",
	})
	if err != nil {
		t.Fatalf("seed extra: %v", err)
	}

	form := get(t, h, "/lessons/payments/"+itoa(payID)+"/edit").Body.String()
	if !strings.Contains(form, `value="extra" checked`) {
		t.Errorf("the edit form opened as a course payment:\n%s", form)
	}
	if !strings.Contains(form, `value="Кімоно"`) {
		t.Errorf("the edit form is missing the label:\n%s", form)
	}

	// Re-submitting unchanged must leave it an extra.
	rec := post(t, h, "/lessons/payments/"+itoa(payID), url.Values{
		"enrollment_id": {itoa(id)},
		"kind":          {"extra"},
		"date":          {"2026-09-05"},
		"amount":        {"1500"},
		"label":         {"Кімоно"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	after, err := st.GetPayment(payID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if !after.IsExtra() || after.Label != "Кімоно" {
		t.Errorf("after re-saving = %+v", after)
	}
}

// The "за що" column carries "8 зан." and "абон. 01.09—30.09" too, so a bare
// "Кімоно" there reads as a kind of lesson. Both lists that render a payment
// have to mark it.
func TestBothPaymentListsMarkAnExtra(t *testing.T) {
	h, st, id := paymentsFixture(t)
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: id, Kind: model.PaymentKindExtra,
		Date: "2026-09-05", Amount: 1500, Label: "Кімоно",
	}); err != nil {
		t.Fatalf("seed extra: %v", err)
	}

	for _, path := range []string{"/lessons/payments", "/lessons"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Кімоно") {
			t.Errorf("%s does not name the extra:\n%s", path, body)
		}
		if !strings.Contains(body, "badge-extra") {
			t.Errorf("%s renders the extra unmarked", path)
		}
	}
}
