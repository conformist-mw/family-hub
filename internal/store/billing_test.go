package store_test

import (
	"path/filepath"
	"testing"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(database)
}

func seedCourse(t *testing.T, st *store.Store, e model.Enrollment) int64 {
	t.Helper()
	if e.Person == "" {
		e.Person = "Маша"
	}
	if e.Name == "" {
		e.Name = "Школа"
	}
	if e.AttendanceMode == "" {
		e.AttendanceMode = model.AttendancePerSession
	}
	e.Active = true
	id, err := st.CreateEnrollment(e)
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	return id
}

func TestClaimBillingReminderIsGrantedOnce(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingMonthly, CurrentPrice: 12500})

	first, err := st.ClaimBillingReminder(id, "2026-09-30")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !first {
		t.Fatal("first claim should be granted")
	}

	second, err := st.ClaimBillingReminder(id, "2026-09-30")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second {
		t.Fatal("second claim for the same period should be refused")
	}

	// A different period is a different warning and must be claimable.
	next, err := st.ClaimBillingReminder(id, "2026-10-31")
	if err != nil {
		t.Fatalf("next-period claim: %v", err)
	}
	if !next {
		t.Fatal("claim for the next period should be granted")
	}
}

// The school's whole point: no evening "was there a lesson?" question, while
// the schedule itself stays for the calendar.
func TestExceptionsOnlyCoursesAreNotRemindedAboutLessons(t *testing.T) {
	st := testStore(t)
	marked := seedCourse(t, st, model.Enrollment{
		Name: "Гімнастика", BillingType: model.BillingPerLesson, CurrentPrice: 300,
		AttendanceMode: model.AttendancePerSession,
	})
	silent := seedCourse(t, st, model.Enrollment{
		Name: "Школа", BillingType: model.BillingMonthly, CurrentPrice: 12500,
		AttendanceMode: model.AttendanceExceptionsOnly,
	})
	for _, id := range []int64{marked, silent} {
		if err := st.CreateSlot(id, 1, "09:00", 60, "2000-01-01T00:00"); err != nil {
			t.Fatalf("create slot: %v", err)
		}
	}

	due, err := st.SlotsForWeekday(1, "2026-09-07")
	if err != nil {
		t.Fatalf("slots for weekday: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("want 1 slot to remind about, got %d", len(due))
	}
	if due[0].Enrollment.ID != marked {
		t.Fatalf("the reminded course should be the per_session one, got %d", due[0].Enrollment.ID)
	}

	// The silenced course keeps its slot: the ICS feed still needs it.
	all, err := st.AllActiveSlots()
	if err != nil {
		t.Fatalf("all active slots: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want both slots in the calendar feed, got %d", len(all))
	}
}

func TestEnrollmentRoundTripsSchoolFields(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{
		BillingType:         model.BillingMonthly,
		CurrentPrice:        12500,
		AttendanceMode:      model.AttendanceExceptionsOnly,
		PaymentInstructions: "ФОП Іваненко\nUA12 3456",
	})

	got, err := st.GetEnrollment(id)
	if err != nil {
		t.Fatalf("get enrollment: %v", err)
	}
	if got.AttendanceMode != model.AttendanceExceptionsOnly {
		t.Errorf("attendance mode: got %q", got.AttendanceMode)
	}
	if got.PaymentInstructions != "ФОП Іваненко\nUA12 3456" {
		t.Errorf("payment instructions: got %q", got.PaymentInstructions)
	}
	if !got.SkipsDailyMarks() {
		t.Error("exceptions_only course should skip daily marks")
	}

	// Balances feed the reminder, so the details must survive that path too.
	balances, err := st.Balances()
	if err != nil {
		t.Fatalf("balances: %v", err)
	}
	if len(balances) != 1 || balances[0].PaymentInstructions == "" {
		t.Fatalf("balance should carry payment instructions, got %+v", balances)
	}
}
