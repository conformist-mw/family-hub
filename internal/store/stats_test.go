package store_test

import (
	"testing"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

func ptr(s string) *string { return &s }

func amountFor(t *testing.T, rows []store.MonthSpend, month string) float64 {
	t.Helper()
	for _, r := range rows {
		if r.Month == month {
			return r.Amount
		}
	}
	return 0
}

// The month a school fee is paid in and the month it buys are different facts,
// and the stats page shows both.
func TestSpendSplitsTransferMonthFromCoveredMonth(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingMonthly, CurrentPrice: 12500})

	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: id,
		Date:         "2026-08-28",
		Amount:       12500,
		CoversFrom:   ptr("2026-09-01"),
		CoversUntil:  ptr("2026-09-30"),
	}); err != nil {
		t.Fatalf("create payment: %v", err)
	}

	stats, err := st.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got := amountFor(t, stats.ByMonth, "2026-08"); got != 12500 {
		t.Errorf("cash flow should land in August, got %v in 2026-08", got)
	}
	if got := amountFor(t, stats.ByMonth, "2026-09"); got != 0 {
		t.Errorf("cash flow should not land in September, got %v", got)
	}
	if got := amountFor(t, stats.ByPeriod, "2026-09"); got != 12500 {
		t.Errorf("covered period should land in September, got %v", got)
	}
	if got := amountFor(t, stats.ByPeriod, "2026-08"); got != 0 {
		t.Errorf("covered period should not land in August, got %v", got)
	}
}

// A month nobody paid for is invisible to coverageFromToday, which merges
// adjacent periods. The by-period chart is where it shows up.
func TestSpendByPeriodExposesASkippedMonth(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingMonthly, CurrentPrice: 12500})

	for _, p := range []model.Payment{
		{Date: "2026-08-28", CoversFrom: ptr("2026-09-01"), CoversUntil: ptr("2026-09-30")},
		{Date: "2026-10-30", CoversFrom: ptr("2026-11-01"), CoversUntil: ptr("2026-11-30")},
	} {
		p.EnrollmentID = id
		p.Amount = 12500
		if _, err := st.CreatePayment(p); err != nil {
			t.Fatalf("create payment: %v", err)
		}
	}

	stats, err := st.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got := amountFor(t, stats.ByPeriod, "2026-10"); got != 0 {
		t.Errorf("October was never paid for, got %v", got)
	}
	if got := amountFor(t, stats.ByPeriod, "2026-11"); got != 12500 {
		t.Errorf("November should be covered, got %v", got)
	}
}

// Paying in August for September must not read as "unpaid" for the whole of
// August — that is the shape a school fee normally has.
func TestPrepaidMonthIsNotAnEmptyBalance(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingMonthly, CurrentPrice: 12500})

	// Far enough out that "today" is always before it while this test is run.
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: id, Date: "2026-08-28", Amount: 12500,
		CoversFrom: ptr("2126-09-01"), CoversUntil: ptr("2126-09-30"),
	}); err != nil {
		t.Fatalf("create payment: %v", err)
	}

	bal, err := st.BalanceFor(id)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if bal.CoveredNow {
		t.Fatal("the period has not started yet")
	}
	if bal.PrepaidFrom != "2126-09-01" {
		t.Errorf("prepaid start: got %q, want 2126-09-01", bal.PrepaidFrom)
	}
	if bal.CoversUntil != "2126-09-30" {
		t.Errorf("prepaid end: got %q, want 2126-09-30", bal.CoversUntil)
	}
	if got := bal.State(); got != "ok" {
		t.Errorf("a prepaid course should not show as %q", got)
	}
}

// Nothing paid at all is still an empty balance — the prepaid branch must not
// swallow the case it was added next to.
func TestUnpaidMonthlyCourseIsStillEmpty(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingMonthly, CurrentPrice: 12500})

	bal, err := st.BalanceFor(id)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if bal.PrepaidFrom != "" || bal.State() != "empty" {
		t.Errorf("got prepaid %q state %q, want empty", bal.PrepaidFrom, bal.State())
	}
}

// Per-lesson payments have no coverage range; they must still be counted, in
// the month the money moved.
func TestSpendByPeriodFallsBackToPaymentDate(t *testing.T) {
	st := testStore(t)
	lessons := int64(8)
	id := seedCourse(t, st, model.Enrollment{
		Name: "Логопед", BillingType: model.BillingPerLesson, CurrentPrice: 500,
	})
	if _, err := st.CreatePayment(model.Payment{
		EnrollmentID: id, Date: "2026-09-03", Amount: 4000, LessonsPaid: &lessons,
	}); err != nil {
		t.Fatalf("create payment: %v", err)
	}

	stats, err := st.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got := amountFor(t, stats.ByPeriod, "2026-09"); got != 4000 {
		t.Errorf("per-lesson payment should count in its own month, got %v", got)
	}
}
