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

func pay(date string, lessons int) model.Payment {
	n := int64(lessons)
	return model.Payment{Date: date, LessonsPaid: &n}
}

func TestRemainingPacksDrainsOldestFirst(t *testing.T) {
	// 13 + 15 paid, 12 done: one lesson left of the old pack, all of the new.
	payments := []model.Payment{pay("2026-06-24", 13), pay("2026-07-26", 15)}
	dates := UpcomingDates(tueThuSat, nil, "2026-07-30", false, 16)
	got := RemainingPacks(payments, 12, dates)

	if len(got) != 2 {
		t.Fatalf("got %d packs, want 2: %+v", len(got), got)
	}
	if got[0].Left != 1 || got[0].Size != 13 || got[0].Date != "2026-06-24" {
		t.Errorf("old pack: got %+v", got[0])
	}
	if got[1].Left != 15 || got[1].Size != 15 {
		t.Errorf("new pack: got %+v", got[1])
	}
	// The old pack's single lesson takes the first date; the new pack ends on
	// the 16th.
	if got[0].Through != dates[0] {
		t.Errorf("old pack through: got %q, want %q", got[0].Through, dates[0])
	}
	if got[1].Through != dates[15] {
		t.Errorf("new pack through: got %q, want %q", got[1].Through, dates[15])
	}
}

func TestRemainingPacksDropsSpentAndUncountable(t *testing.T) {
	monthly := model.Payment{Date: "2026-07-01"} // no LessonsPaid
	payments := []model.Payment{pay("2026-06-01", 8), monthly, pay("2026-07-18", 8)}
	got := RemainingPacks(payments, 10, []string{"2026-08-01", "2026-08-04", "2026-08-06",
		"2026-08-08", "2026-08-11", "2026-08-13"})

	if len(got) != 1 {
		t.Fatalf("got %d packs, want only the newest: %+v", len(got), got)
	}
	if got[0].Left != 6 || got[0].Date != "2026-07-18" {
		t.Errorf("got %+v", got[0])
	}
	if got[0].Through != "2026-08-13" {
		t.Errorf("through: got %q, want 2026-08-13", got[0].Through)
	}
}

func TestRemainingPacksNothingLeftOnDebt(t *testing.T) {
	payments := []model.Payment{pay("2026-06-01", 8)}
	if got := RemainingPacks(payments, 10, nil); got != nil {
		t.Errorf("want no packs, got %+v", got)
	}
}

// Fewer dates than lessons: the packs beyond the horizon keep an empty date
// instead of borrowing an earlier one.
func TestRemainingPacksShortDateList(t *testing.T) {
	payments := []model.Payment{pay("2026-06-01", 3), pay("2026-07-01", 3)}
	got := RemainingPacks(payments, 0, []string{"2026-08-01", "2026-08-04", "2026-08-06"})
	if len(got) != 2 {
		t.Fatalf("got %d packs, want 2", len(got))
	}
	if got[0].Through != "2026-08-06" {
		t.Errorf("first pack: got %q, want 2026-08-06", got[0].Through)
	}
	if got[1].Through != "" {
		t.Errorf("second pack: got %q, want empty", got[1].Through)
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
