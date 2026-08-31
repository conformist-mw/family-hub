package store_test

import (
	"path/filepath"
	"testing"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

func schoolStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(database)
}

// A replace touches only its own [from, to) window: a lesson outside it — a
// second pupil's, or one further in the future — must survive, and to is
// exclusive so a lesson exactly at the upper bound is not swept up.
func TestReplaceSchoolLessonsIsWindowScoped(t *testing.T) {
	st := schoolStore(t)

	// Pre-existing rows: one inside the window, one exactly at the exclusive
	// upper bound, one well beyond it.
	seed := []model.SchoolLesson{
		{EventID: 1, PupilID: 79311, Subject: "Старий [9]", StartsAt: "2026-09-01T09:00", EndsAt: "2026-09-01T09:40"},
		{EventID: 2, PupilID: 79311, Subject: "Межа [9]", StartsAt: "2026-09-07T00:00", EndsAt: "2026-09-07T00:40"},
		{EventID: 3, PupilID: 79311, Subject: "Далекий [9]", StartsAt: "2026-09-20T09:00", EndsAt: "2026-09-20T09:40"},
	}
	if err := st.ReplaceSchoolLessons(79311, "2026-08-31T00:00", "2026-10-01T00:00", seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Replace only the first week. Event 1 is inside and gets swapped for 10;
	// events 2 (at the exclusive bound) and 3 (beyond) are untouched.
	fresh := []model.SchoolLesson{
		{EventID: 10, PupilID: 79311, Subject: "Новий [9]", StartsAt: "2026-09-01T10:00", EndsAt: "2026-09-01T10:40"},
	}
	if err := st.ReplaceSchoolLessons(79311, "2026-08-31T00:00", "2026-09-07T00:00", fresh); err != nil {
		t.Fatalf("replace: %v", err)
	}

	got, err := st.SchoolLessons("2026-08-01T00:00", "2026-11-01T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	ids := map[int64]bool{}
	for _, l := range got {
		ids[l.EventID] = true
	}
	if ids[1] {
		t.Error("event 1 was inside the window and should be gone")
	}
	for _, want := range []int64{10, 2, 3} {
		if !ids[want] {
			t.Errorf("event %d should have survived, have %v", want, ids)
		}
	}
}
