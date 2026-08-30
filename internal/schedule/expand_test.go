package schedule

import (
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

func kyiv(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatalf("load Europe/Kyiv: %v", err)
	}
	return loc
}

func weekly(id int64, weekday int, hhmm string, trainer *int64) store.SlotWithEnrollment {
	return store.SlotWithEnrollment{
		Slot: model.Slot{ID: id, Weekday: weekday, Time: hhmm, DurationMin: 60, Active: true},
		Enrollment: model.Enrollment{
			ID: 5, Name: "Балет", Person: "Маша", TrainerID: trainer,
		},
	}
}

// The bug this package exists to fix. A weekly lesson sent as a UTC DTSTART
// plus RRULE:FREQ=WEEKLY was expanded in UTC, so its instances were exactly
// 168h apart — which is not "the same time next week". From the October clock
// change the calendar showed every lesson an hour early, all winter.
func TestAWeeklyLessonKeepsItsWallClockTimeAcrossTheClockChange(t *testing.T) {
	loc := kyiv(t)
	// A Tuesday 16:00 lesson, expanded from summer time through the change.
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
	to := time.Date(2026, 11, 30, 23, 59, 0, 0, loc)

	lessons, err := Expand([]store.SlotWithEnrollment{weekly(11, int(time.Tuesday), "16:00", nil)},
		nil, loc, from, to)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(lessons) < 8 {
		t.Fatalf("got %d lessons over two months, want at least 8", len(lessons))
	}

	offsets := map[int]bool{}
	for _, l := range lessons {
		if l.Start.Weekday() != time.Tuesday {
			t.Errorf("%s is not a Tuesday", l.Start)
		}
		if h, m := l.Start.Hour(), l.Start.Minute(); h != 16 || m != 0 {
			t.Errorf("%s is at %02d:%02d, want 16:00 — the lesson drifted", l.Start, h, m)
		}
		_, off := l.Start.Zone()
		offsets[off] = true
	}
	// If every occurrence had the same UTC offset the window never crossed a
	// transition, and the assertion above would prove nothing.
	if len(offsets) < 2 {
		t.Fatalf("the window did not cross a DST transition (offsets %v) — the test is not testing anything", offsets)
	}
}

// The spring transition drifts the other way: an hour late all summer.
func TestTheSpringChangeDoesNotDriftEither(t *testing.T) {
	loc := kyiv(t)
	from := time.Date(2027, 3, 1, 0, 0, 0, 0, loc)
	to := time.Date(2027, 4, 30, 23, 59, 0, 0, loc)

	lessons, err := Expand([]store.SlotWithEnrollment{weekly(11, int(time.Tuesday), "16:00", nil)},
		nil, loc, from, to)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	offsets := map[int]bool{}
	for _, l := range lessons {
		if h, m := l.Start.Hour(), l.Start.Minute(); h != 16 || m != 0 {
			t.Errorf("%s is at %02d:%02d, want 16:00", l.Start, h, m)
		}
		_, off := l.Start.Zone()
		offsets[off] = true
	}
	if len(offsets) < 2 {
		t.Fatalf("the window did not cross a DST transition (offsets %v)", offsets)
	}
}

// An absence no longer punches an EXDATE hole; the lesson is simply not
// generated, so there is nothing to exclude.
func TestATrainersAbsenceRemovesTheLessonsInside(t *testing.T) {
	loc := kyiv(t)
	trainer := int64(1)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, loc)
	to := time.Date(2026, 9, 30, 23, 59, 0, 0, loc)

	lessons, err := Expand(
		[]store.SlotWithEnrollment{weekly(11, int(time.Tuesday), "16:00", &trainer)},
		[]model.TrainerAbsence{{
			ID: 4, TrainerID: trainer, DateFrom: "2026-09-08", DateTo: "2026-09-15",
			Kind: model.AbsenceVacation,
		}},
		loc, from, to)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	for _, l := range lessons {
		if d := l.Start.Format("2006-01-02"); d >= "2026-09-08" && d <= "2026-09-15" {
			t.Errorf("%s falls inside the absence", d)
		}
	}
	// The Tuesdays either side survive: an absence is a hole, not a stop.
	var dates []string
	for _, l := range lessons {
		dates = append(dates, l.Start.Format("2006-01-02"))
	}
	for _, want := range []string{"2026-09-01", "2026-09-22", "2026-09-29"} {
		if !contains(dates, want) {
			t.Errorf("%s is missing from %v", want, dates)
		}
	}
}

// Somebody else's absence is not this lesson's problem.
func TestAnotherTrainersAbsenceLeavesTheLessonAlone(t *testing.T) {
	loc := kyiv(t)
	mine, theirs := int64(1), int64(2)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, loc)
	to := time.Date(2026, 9, 30, 23, 59, 0, 0, loc)

	lessons, err := Expand(
		[]store.SlotWithEnrollment{weekly(11, int(time.Tuesday), "16:00", &mine)},
		[]model.TrainerAbsence{{ID: 4, TrainerID: theirs, DateFrom: "2026-09-01", DateTo: "2026-09-30"}},
		loc, from, to)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(lessons) != 5 {
		t.Fatalf("got %d lessons, want every Tuesday in September 2026", len(lessons))
	}
}

// An enrollment with no trainer can never match an absence.
func TestALessonWithoutATrainerIsNeverCancelled(t *testing.T) {
	loc := kyiv(t)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, loc)
	to := time.Date(2026, 9, 30, 23, 59, 0, 0, loc)

	lessons, err := Expand(
		[]store.SlotWithEnrollment{weekly(11, int(time.Tuesday), "16:00", nil)},
		[]model.TrainerAbsence{{ID: 4, TrainerID: 1, DateFrom: "2026-09-01", DateTo: "2026-09-30"}},
		loc, from, to)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(lessons) != 5 {
		t.Fatalf("got %d lessons, want every Tuesday", len(lessons))
	}
}

func TestASlotWithNoDurationGetsTheDefault(t *testing.T) {
	loc := kyiv(t)
	s := weekly(11, int(time.Tuesday), "16:00", nil)
	s.Slot.DurationMin = 0
	lessons, err := Expand([]store.SlotWithEnrollment{s}, nil, loc,
		time.Date(2026, 9, 1, 0, 0, 0, 0, loc), time.Date(2026, 9, 8, 0, 0, 0, 0, loc))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(lessons) == 0 {
		t.Fatal("no lessons")
	}
	if lessons[0].DurationMin != DefaultDurationMin {
		t.Fatalf("duration = %d, want %d", lessons[0].DurationMin, DefaultDurationMin)
	}
	if got := lessons[0].End().Sub(lessons[0].Start); got != DefaultDurationMin*time.Minute {
		t.Fatalf("End() - Start = %s", got)
	}
}

// A window that ends before it starts is not an error, just nothing.
func TestAnInvertedWindowYieldsNothing(t *testing.T) {
	loc := kyiv(t)
	lessons, err := Expand([]store.SlotWithEnrollment{weekly(11, 2, "16:00", nil)}, nil, loc,
		time.Date(2026, 9, 30, 0, 0, 0, 0, loc), time.Date(2026, 9, 1, 0, 0, 0, 0, loc))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(lessons) != 0 {
		t.Fatalf("got %d lessons", len(lessons))
	}
}

// A row the schema should have refused must not take the whole feed down.
func TestUnusableSlotsAreSkippedNotFatal(t *testing.T) {
	loc := kyiv(t)
	bad := weekly(11, int(time.Tuesday), "не час", nil)
	good := weekly(12, int(time.Tuesday), "16:00", nil)
	lessons, err := Expand([]store.SlotWithEnrollment{bad, good}, nil, loc,
		time.Date(2026, 9, 1, 0, 0, 0, 0, loc), time.Date(2026, 9, 8, 0, 0, 0, 0, loc))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	for _, l := range lessons {
		if l.SlotID == 11 {
			t.Fatal("an unparseable time produced a lesson")
		}
	}
	if len(lessons) == 0 {
		t.Fatal("the good slot was lost with the bad one")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
