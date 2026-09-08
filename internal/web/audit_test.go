package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The reconciliation is assembled in internal/audit now, shared with the Mini
// App. This is the page that has to keep rendering from it.
func TestAuditPageRenders(t *testing.T) {
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
		Person: "Демид", Name: "Логопед", BillingType: model.BillingPerLesson,
		CurrentPrice: 500, LowThreshold: 2, AttendanceMode: model.AttendancePerSession,
	})
	if err != nil {
		t.Fatalf("seed enrollment: %v", err)
	}
	lessons := int64(10)
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: id, Date: "2026-08-01", Amount: 5000, LessonsPaid: &lessons,
	}); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	if _, err := st.CreateVisit(id, "2026-08-04", model.StatusDone, "перше"); err != nil {
		t.Fatalf("seed visit: %v", err)
	}

	h := NewRouter(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "", nil, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lessons/enrollments/1/audit", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Звірка", "з останньої оплати (01.08.2026)", "оплата <b>+10</b>", "перше"} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// The web ledger is the third renderer of the same rows. An extra must name
// itself here rather than fall through to the visit branch, which would print
// an empty status pill.
func TestAuditPageNamesAnExtra(t *testing.T) {
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
	lessons := int64(8)
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: id, Date: "2026-09-01", Amount: 3200, LessonsPaid: &lessons,
	}); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: id, Kind: model.PaymentKindExtra,
		Date: "2026-09-05", Amount: 1500, Label: "Кімоно",
	}); err != nil {
		t.Fatalf("seed extra: %v", err)
	}

	h := NewRouter(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "", nil, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lessons/enrollments/1/audit", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"додатково: <b>Кімоно</b>",
		"ledger-extra",
		"додатково: <b>1500 ₴</b>", // the summary line, apart from "оплачено"
		"оплачено: <b>8</b> зан.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}
