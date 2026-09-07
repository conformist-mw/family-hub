package store_test

import (
	"testing"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The agenda asks for one window rather than for everything: it needs to know
// which of the week's lessons have already been answered, and reading the whole
// journal to find out would grow with the journal.
func TestVisitsCanBeReadForOneDateWindow(t *testing.T) {
	st := testStore(t)
	id := seedCourse(t, st, model.Enrollment{BillingType: model.BillingPerLesson})
	for _, date := range []string{"2026-09-01", "2026-09-07", "2026-09-09", "2026-09-20"} {
		if _, err := st.CreateVisit(id, date, model.StatusDone, ""); err != nil {
			t.Fatalf("seed %s: %v", date, err)
		}
	}

	got, err := st.ListVisits(store.VisitFilter{From: "2026-09-07", To: "2026-09-14"})
	if err != nil {
		t.Fatalf("ListVisits: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d visits, want the two inside the window: %+v", len(got), got)
	}
	// Both bounds are inclusive: the window is a range of days, and a lesson on
	// its first day is in it.
	dates := map[string]bool{got[0].Date: true, got[1].Date: true}
	if !dates["2026-09-07"] || !dates["2026-09-09"] {
		t.Fatalf("wrong visits in window: %+v", got)
	}

	// An open-ended side stays open: the lists ask for everything.
	all, err := st.ListVisits(store.VisitFilter{})
	if err != nil {
		t.Fatalf("ListVisits(all): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d visits unfiltered, want 4", len(all))
	}
	from, err := st.ListVisits(store.VisitFilter{From: "2026-09-09"})
	if err != nil {
		t.Fatalf("ListVisits(from): %v", err)
	}
	if len(from) != 2 {
		t.Fatalf("got %d visits from 09-09, want 2: %+v", len(from), from)
	}
}
