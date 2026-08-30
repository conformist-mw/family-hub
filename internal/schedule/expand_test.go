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

// weekly is a slot with one version reaching back indefinitely — a schedule
// that, as far as anything records, was always this.
func weekly(id int64, weekday int, hhmm string, trainer *int64) store.SlotHistory {
	return versioned(id, trainer, model.SlotVersion{
		ID: id * 100, SlotID: id, ValidFromAt: "2000-01-01T00:00",
		Weekday: weekday, Time: hhmm, DurationMin: 60,
	})
}

func versioned(id int64, trainer *int64, versions ...model.SlotVersion) store.SlotHistory {
	return store.SlotHistory{
		SlotID: id,
		Enrollment: model.Enrollment{
			ID: 5, Name: "Балет", Person: "Маша", TrainerID: trainer,
		},
		Versions: versions,
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

	lessons, err := Expand([]store.SlotHistory{weekly(11, int(time.Tuesday), "16:00", nil)},
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

	lessons, err := Expand([]store.SlotHistory{weekly(11, int(time.Tuesday), "16:00", nil)},
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
		[]store.SlotHistory{weekly(11, int(time.Tuesday), "16:00", &trainer)},
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
		[]store.SlotHistory{weekly(11, int(time.Tuesday), "16:00", &mine)},
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
		[]store.SlotHistory{weekly(11, int(time.Tuesday), "16:00", nil)},
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
	s.Versions[0].DurationMin = 0
	lessons, err := Expand([]store.SlotHistory{s}, nil, loc,
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
	lessons, err := Expand([]store.SlotHistory{weekly(11, 2, "16:00", nil)}, nil, loc,
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
	lessons, err := Expand([]store.SlotHistory{bad, good}, nil, loc,
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

// The second half of #53. Moving Логопед from Tuesday to Thursday must not
// claim it had always been Thursday: a window is expanded with the version
// that was in force over it.
func TestAScheduleChangeLeavesTheRecordedPastAlone(t *testing.T) {
	loc := kyiv(t)
	slot := versioned(11, nil,
		model.SlotVersion{ID: 1, SlotID: 11, ValidFromAt: "2000-01-01T00:00",
			Weekday: int(time.Tuesday), Time: "16:00", DurationMin: 60},
		model.SlotVersion{ID: 2, SlotID: 11, ValidFromAt: "2026-10-01T00:00",
			Weekday: int(time.Thursday), Time: "17:00", DurationMin: 45},
	)

	lessons, err := Expand([]store.SlotHistory{slot}, nil, loc,
		time.Date(2026, 9, 1, 0, 0, 0, 0, loc), time.Date(2026, 10, 31, 23, 59, 0, 0, loc))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	var sept, oct int
	for _, l := range lessons {
		switch l.Start.Month() {
		case time.September:
			sept++
			if l.Start.Weekday() != time.Tuesday || l.Start.Hour() != 16 {
				t.Errorf("September lesson %s is not the schedule that was in force then", l.Start)
			}
			if l.VersionID != 1 {
				t.Errorf("September lesson %s came from version %d", l.Start, l.VersionID)
			}
			if l.DurationMin != 60 {
				t.Errorf("September lesson lasts %d min, want the old 60", l.DurationMin)
			}
		case time.October:
			oct++
			if l.Start.Weekday() != time.Thursday || l.Start.Hour() != 17 {
				t.Errorf("October lesson %s is not the new schedule", l.Start)
			}
			if l.VersionID != 2 {
				t.Errorf("October lesson %s came from version %d", l.Start, l.VersionID)
			}
			if l.DurationMin != 45 {
				t.Errorf("October lesson lasts %d min, want the new 45", l.DurationMin)
			}
		}
	}
	if sept != 5 {
		t.Errorf("got %d September lessons, want every Tuesday", sept)
	}
	if oct != 5 {
		t.Errorf("got %d October lessons, want every Thursday", oct)
	}
}

// The boundary is exclusive at the top: a lesson landing exactly on the new
// version's starting instant belongs to the new version, and must not be
// generated twice.
func TestALessonOnTheBoundaryBelongsToOneVersionOnly(t *testing.T) {
	loc := kyiv(t)
	// Both versions are Tuesday 16:00; the change takes effect at a Tuesday
	// 16:00 exactly, so a naive inclusive cut yields the same lesson twice.
	slot := versioned(11, nil,
		model.SlotVersion{ID: 1, SlotID: 11, ValidFromAt: "2000-01-01T00:00",
			Weekday: int(time.Tuesday), Time: "16:00", DurationMin: 60},
		model.SlotVersion{ID: 2, SlotID: 11, ValidFromAt: "2026-09-08T16:00",
			Weekday: int(time.Tuesday), Time: "16:00", DurationMin: 30},
	)
	lessons, err := Expand([]store.SlotHistory{slot}, nil, loc,
		time.Date(2026, 9, 1, 0, 0, 0, 0, loc), time.Date(2026, 9, 30, 23, 59, 0, 0, loc))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	seen := map[string]int{}
	for _, l := range lessons {
		seen[l.Start.Format("2006-01-02T15:04")]++
	}
	if n := seen["2026-09-08T16:00"]; n != 1 {
		t.Fatalf("the boundary lesson appears %d times, want exactly 1", n)
	}
	for _, l := range lessons {
		if l.Start.Format("2006-01-02T15:04") == "2026-09-08T16:00" && l.VersionID != 2 {
			t.Fatalf("the boundary lesson came from version %d, want the new one", l.VersionID)
		}
	}
}

// A change made at 10:00 must not claim this morning's 08:00 lesson, which
// already happened under the old schedule. That is why valid_from_at is a
// datetime and not a date.
//
// The evening's 18:00 is correctly there as well: the new schedule applies
// from 10:00, so that Tuesday genuinely holds both — one lesson that happened
// under the old rule and one the new rule expects. What must not happen is
// the morning lesson being retimed to 18:00.
func TestAChangeMadeMidDayDoesNotClaimThatMorningsLesson(t *testing.T) {
	loc := kyiv(t)
	slot := versioned(11, nil,
		model.SlotVersion{ID: 1, SlotID: 11, ValidFromAt: "2000-01-01T00:00",
			Weekday: int(time.Tuesday), Time: "08:00", DurationMin: 60},
		model.SlotVersion{ID: 2, SlotID: 11, ValidFromAt: "2026-09-08T10:00",
			Weekday: int(time.Tuesday), Time: "18:00", DurationMin: 60},
	)
	lessons, err := Expand([]store.SlotHistory{slot}, nil, loc,
		time.Date(2026, 9, 8, 0, 0, 0, 0, loc), time.Date(2026, 9, 8, 23, 59, 0, 0, loc))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	byTime := map[string]int64{}
	for _, l := range lessons {
		byTime[l.Start.Format("15:04")] = l.VersionID
	}
	if v, ok := byTime["08:00"]; !ok || v != 1 {
		t.Errorf("that morning's lesson is gone or was retimed: %v", byTime)
	}
	if v, ok := byTime["18:00"]; !ok || v != 2 {
		t.Errorf("the new schedule does not apply that evening: %v", byTime)
	}
	if len(byTime) != 2 {
		t.Errorf("that Tuesday holds %v, want the old morning and the new evening", byTime)
	}
}

// A slot with no versions at all is a broken row, not a crash.
func TestASlotWithNoVersionsYieldsNothing(t *testing.T) {
	loc := kyiv(t)
	lessons, err := Expand([]store.SlotHistory{versioned(11, nil)}, nil, loc,
		time.Date(2026, 9, 1, 0, 0, 0, 0, loc), time.Date(2026, 9, 30, 0, 0, 0, 0, loc))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(lessons) != 0 {
		t.Fatalf("got %d lessons from a slot with no schedule", len(lessons))
	}
}

// A version that starts after the window never contributes.
func TestAFutureVersionDoesNotReachBackwards(t *testing.T) {
	loc := kyiv(t)
	slot := versioned(11, nil,
		model.SlotVersion{ID: 1, SlotID: 11, ValidFromAt: "2027-01-01T00:00",
			Weekday: int(time.Tuesday), Time: "16:00", DurationMin: 60},
	)
	lessons, err := Expand([]store.SlotHistory{slot}, nil, loc,
		time.Date(2026, 9, 1, 0, 0, 0, 0, loc), time.Date(2026, 9, 30, 0, 0, 0, 0, loc))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(lessons) != 0 {
		t.Fatalf("got %d lessons before the schedule existed", len(lessons))
	}
}

// An unreadable valid_from_at is a real error, not a silently dropped slot:
// quietly skipping it would read as "this course does not happen".
func TestAnUnreadableValidFromIsAnError(t *testing.T) {
	loc := kyiv(t)
	slot := versioned(11, nil,
		model.SlotVersion{ID: 1, SlotID: 11, ValidFromAt: "не дата",
			Weekday: int(time.Tuesday), Time: "16:00", DurationMin: 60},
	)
	if _, err := Expand([]store.SlotHistory{slot}, nil, loc,
		time.Date(2026, 9, 1, 0, 0, 0, 0, loc), time.Date(2026, 9, 30, 0, 0, 0, 0, loc)); err == nil {
		t.Fatal("an unreadable valid_from_at was accepted")
	}
}
