package store_test

import (
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// extra builds the row a course's one-off charge is stored as: an amount and a
// label, and neither a lesson count nor a coverage range.
func extra(enrollmentID int64, date, label string, amount float64) model.Payment {
	return model.Payment{
		EnrollmentID: enrollmentID, Kind: model.PaymentKindExtra,
		Date: date, Amount: amount, Label: label,
	}
}

func pack(enrollmentID int64, date string, amount float64, lessons int64) model.Payment {
	return model.Payment{
		EnrollmentID: enrollmentID, Kind: model.PaymentKindCourse,
		Date: date, Amount: amount, LessonsPaid: &lessons,
	}
}

func mustCreatePayment(t *testing.T, st *store.Store, p model.Payment) int64 {
	t.Helper()
	id, err := st.CreatePayment(p)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	return id
}

func TestPaymentKindAndLabelSurviveARoundTrip(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson, CurrentPrice: 400})
	payID := mustCreatePayment(t, st, extra(id, "2026-09-05", "Кімоно", 1500))

	got, err := st.GetPayment(payID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if !got.IsExtra() || got.Label != "Кімоно" {
		t.Errorf("payment = %+v, want an extra labelled Кімоно", got)
	}

	// An edit must not quietly turn an extra into a course payment: kind and
	// label go through UPDATE too.
	got.Label = "Кімоно (розмір 140)"
	if err := st.UpdatePayment(got); err != nil {
		t.Fatalf("update payment: %v", err)
	}
	after, err := st.GetPayment(payID)
	if err != nil {
		t.Fatalf("get payment after update: %v", err)
	}
	if !after.IsExtra() || after.Label != "Кімоно (розмір 140)" {
		t.Errorf("payment after update = %+v", after)
	}
}

// The load-bearing regression of the whole feature. Extras live in the same
// table as the money that buys lessons, so the only thing keeping a kimono out
// of the balance is that it carries neither a lesson count nor a coverage
// range. A future query that forgets to care breaks this test first.
func TestAnExtraDoesNotMoveThePerLessonBalance(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson, CurrentPrice: 400})
	mustCreatePayment(t, st, pack(id, "2026-09-01", 3200, 8))

	before, err := st.BalanceFor(id)
	if err != nil {
		t.Fatalf("balance before: %v", err)
	}
	mustCreatePayment(t, st, extra(id, "2026-09-05", "Кімоно", 1500))
	after, err := st.BalanceFor(id)
	if err != nil {
		t.Fatalf("balance after: %v", err)
	}

	if after.Paid != before.Paid || after.Remaining != before.Remaining {
		t.Errorf("balance moved: paid %d→%d, remaining %d→%d",
			before.Paid, after.Paid, before.Remaining, after.Remaining)
	}
	if before.Paid != 8 {
		t.Fatalf("paid = %d, want the 8 lessons actually bought", before.Paid)
	}
}

// The same property on the monthly side, where the balance is a covered period
// rather than a count. Asserted through Balance because coveragePeriods is
// unexported and these tests live in store_test.
func TestAnExtraDoesNotMoveTheMonthlyCoverage(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingMonthly, CurrentPrice: 12500})

	// A month that covers today, so CoveredNow/DaysLeft are meaningful.
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local).Format("2006-01-02")
	until := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.Local).Format("2006-01-02")
	mustCreatePayment(t, st, model.Payment{
		EnrollmentID: id, Kind: model.PaymentKindCourse, Date: from, Amount: 12500,
		CoversFrom: &from, CoversUntil: &until,
	})

	before, err := st.BalanceFor(id)
	if err != nil {
		t.Fatalf("balance before: %v", err)
	}
	if !before.CoveredNow {
		t.Fatalf("the seeded month does not cover today: %+v", before)
	}

	mustCreatePayment(t, st, extra(id, from, "Поїздка на змагання", 2000))
	after, err := st.BalanceFor(id)
	if err != nil {
		t.Fatalf("balance after: %v", err)
	}
	if after.CoveredNow != before.CoveredNow ||
		after.CoversUntil != before.CoversUntil ||
		after.DaysLeft != before.DaysLeft ||
		after.PrepaidFrom != before.PrepaidFrom {
		t.Errorf("coverage moved: %+v → %+v", before, after)
	}
}

// The other half of the same guard, and the one that bites hardest if it is
// missing: the `kind = 'course'` filters must not swallow ordinary payments.
// A course payment written through CreatePayment has to stay findable, or the
// bot's /packs goes empty and the default audit period silently resets to all
// time — with no error anywhere.
func TestACoursePaymentStaysVisibleToTheLessonQueries(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson, CurrentPrice: 400})
	mustCreatePayment(t, st, pack(id, "2026-09-01", 3200, 8))

	packs, err := st.PaymentsForEnrollment(id)
	if err != nil {
		t.Fatalf("payments for enrollment: %v", err)
	}
	if len(packs) != 1 || packs[0].LessonsPaid == nil || *packs[0].LessonsPaid != 8 {
		t.Fatalf("packs = %+v, want the one 8-lesson payment", packs)
	}

	last, err := st.LastPaymentDate(id)
	if err != nil {
		t.Fatalf("last payment date: %v", err)
	}
	if last != "2026-09-01" {
		t.Fatalf("last payment date = %q, want the course payment's date", last)
	}
}

func TestTheLessonQueriesIgnoreExtras(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson, CurrentPrice: 400})
	mustCreatePayment(t, st, pack(id, "2026-09-01", 3200, 8))
	// Later than the course payment, so a missing filter shows up as a moved date.
	mustCreatePayment(t, st, extra(id, "2026-09-20", "Кімоно", 1500))

	packs, err := st.PaymentsForEnrollment(id)
	if err != nil {
		t.Fatalf("payments for enrollment: %v", err)
	}
	if len(packs) != 1 {
		t.Fatalf("packs = %+v, want only the course payment", packs)
	}

	last, err := st.LastPaymentDate(id)
	if err != nil {
		t.Fatalf("last payment date: %v", err)
	}
	if last != "2026-09-01" {
		t.Errorf("last payment date = %q, want 2026-09-01 — the kimono moved it", last)
	}
}

// The list behind /lessons/payments and the dashboard's "Останні оплати" is
// where an extra is supposed to show up, so it must NOT be filtered there.
func TestListPaymentsIncludesExtras(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson, CurrentPrice: 400})
	mustCreatePayment(t, st, pack(id, "2026-09-01", 3200, 8))
	mustCreatePayment(t, st, extra(id, "2026-09-20", "Кімоно", 1500))

	rows, err := st.ListPayments(store.PaymentFilter{})
	if err != nil {
		t.Fatalf("list payments: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want both payments listed", len(rows))
	}
	var labels []string
	for _, r := range rows {
		if r.IsExtra() {
			labels = append(labels, r.Label)
		}
	}
	if len(labels) != 1 || labels[0] != "Кімоно" {
		t.Errorf("extras in the list = %v, want [Кімоно]", labels)
	}
}

// The column default stops applying the moment a write names `kind`, and both
// writes do. A caller that builds a model.Payment by hand — every seeding
// helper in this repo's tests, and any future code path that skips
// payments.Form.Parse — must still produce a course payment rather than a
// kind the rest of the code has no branch for.
func TestAPaymentWrittenWithoutAKindIsACoursePayment(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson, CurrentPrice: 400})

	lessons := int64(8)
	payID := mustCreatePayment(t, st, model.Payment{
		EnrollmentID: id, Date: "2026-09-01", Amount: 3200, LessonsPaid: &lessons,
	})

	got, err := st.GetPayment(payID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if got.Kind != model.PaymentKindCourse {
		t.Fatalf("kind = %q, want %q", got.Kind, model.PaymentKindCourse)
	}

	// And it must remain findable by the queries that filter on the kind.
	last, err := st.LastPaymentDate(id)
	if err != nil {
		t.Fatalf("last payment date: %v", err)
	}
	if last != "2026-09-01" {
		t.Errorf("last payment date = %q — a kindless payment fell out of the filter", last)
	}

	// The same on the way through an edit.
	got.Kind = ""
	if err := st.UpdatePayment(got); err != nil {
		t.Fatalf("update payment: %v", err)
	}
	after, err := st.GetPayment(payID)
	if err != nil {
		t.Fatalf("get payment after update: %v", err)
	}
	if after.Kind != model.PaymentKindCourse {
		t.Errorf("kind after update = %q, want %q", after.Kind, model.PaymentKindCourse)
	}
}
