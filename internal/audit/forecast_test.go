package audit

import (
	"testing"

	"lessons/internal/model"
)

// Tue/Thu/Sat, the gymnastics schedule.
var tueThuSat = []model.Slot{{Weekday: 2}, {Weekday: 4}, {Weekday: 6}}

func TestUpcomingDatesWalksTheSchedule(t *testing.T) {
	// 2026-07-30 is a Thursday: the walk starts on it.
	got := UpcomingDates(tueThuSat, nil, "2026-07-30", false, 4)
	want := []string{"2026-07-30", "2026-08-01", "2026-08-04", "2026-08-06"}
	assertDates(t, got, want)
}

func TestUpcomingDatesSkipsTodayWhenAlreadyMarked(t *testing.T) {
	got := UpcomingDates(tueThuSat, nil, "2026-07-30", true, 2)
	assertDates(t, got, []string{"2026-08-01", "2026-08-04"})
}

func TestUpcomingDatesSkipsTrainerAbsence(t *testing.T) {
	absences := []model.TrainerAbsence{{DateFrom: "2026-08-01", DateTo: "2026-08-04"}}
	got := UpcomingDates(tueThuSat, absences, "2026-07-30", false, 3)
	assertDates(t, got, []string{"2026-07-30", "2026-08-06", "2026-08-08"})
}

func TestUpcomingDatesEdgeCases(t *testing.T) {
	if got := UpcomingDates(nil, nil, "2026-07-30", false, 3); got != nil {
		t.Errorf("no slots: want nil, got %v", got)
	}
	if got := UpcomingDates(tueThuSat, nil, "2026-07-30", false, 0); got != nil {
		t.Errorf("zero count: want nil, got %v", got)
	}
	if got := UpcomingDates(tueThuSat, nil, "not-a-date", false, 3); got != nil {
		t.Errorf("bad date: want nil, got %v", got)
	}
	// A balance far beyond the two-year horizon is truncated, not looped over.
	if got := UpcomingDates([]model.Slot{{Weekday: 2}}, nil, "2026-07-30", false, 500); len(got) >= 500 {
		t.Errorf("horizon: want a truncated walk, got %d dates", len(got))
	}
}

func assertDates(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
