package agenda

import (
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/reminders"
	"familyhub/internal/schedule"
)

var kyiv = time.FixedZone("EET", 2*60*60)

// monday is a Monday, so a weekday label in a test is checkable by eye.
var monday = time.Date(2026, 9, 7, 14, 0, 0, 0, kyiv)

func lesson(eid int64, name, person string, t time.Time) schedule.Lesson {
	return schedule.Lesson{
		Enrollment:  model.Enrollment{ID: eid, Name: name, Person: person},
		Start:       t,
		DurationMin: 45,
	}
}

func TestBuildSplitsTodayFromUpcoming(t *testing.T) {
	day := Build(
		[]schedule.Lesson{
			lesson(1, "Карате", "Демид", time.Date(2026, 9, 7, 16, 0, 0, 0, kyiv)),
			lesson(2, "Логопед", "Єгор", time.Date(2026, 9, 9, 13, 35, 0, 0, kyiv)),
		},
		nil,
		[]model.Appointment{{ID: 7, Title: "Стоматолог", Person: "Демид", StartsAt: "2026-09-08T10:30", Location: "Калинова 53г"}},
		[]reminders.Occurrence{{ReminderID: 3, Title: "Винести смiття", Due: time.Date(2026, 9, 7, 20, 0, 0, 0, kyiv), Status: model.OccPending, Stored: true}},
		monday, kyiv,
	)

	if len(day.Today) != 2 {
		t.Fatalf("today = %d items, want 2: %+v", len(day.Today), day.Today)
	}
	// Sorted by time, so the 16:00 lesson leads the 20:00 chore.
	if day.Today[0].Title != "Карате" || day.Today[0].When != "16:00" {
		t.Errorf("today[0] = %+v", day.Today[0])
	}
	if day.Today[1].Kind != KindChore || day.Today[1].When != "20:00" {
		t.Errorf("today[1] = %+v", day.Today[1])
	}
	if len(day.Upcoming) != 2 {
		t.Fatalf("upcoming = %d items, want 2: %+v", len(day.Upcoming), day.Upcoming)
	}
	// Tomorrow is named; anything further out is a weekday.
	if day.Upcoming[0].When != "Завтра 10:30" || day.Upcoming[0].Place != "Калинова 53г" {
		t.Errorf("upcoming[0] = %+v", day.Upcoming[0])
	}
	if day.Upcoming[1].When != "Ср 13:35" {
		t.Errorf("upcoming[1] = %+v", day.Upcoming[1])
	}
}

// A lesson that has already been answered keeps its place in the day, labelled.
// Dropping it would leave no way to tell an answered lesson from a pending one.
func TestBuildLabelsMarkedLessons(t *testing.T) {
	day := Build(
		[]schedule.Lesson{
			lesson(1, "Карате", "Демид", time.Date(2026, 9, 7, 10, 0, 0, 0, kyiv)),
			lesson(2, "Логопед", "Єгор", time.Date(2026, 9, 7, 18, 0, 0, 0, kyiv)),
		},
		[]model.Visit{{EnrollmentID: 1, Date: "2026-09-07", Status: model.StatusDone}},
		nil, nil, monday, kyiv,
	)
	if len(day.Today) != 2 {
		t.Fatalf("today = %+v", day.Today)
	}
	// This morning's lesson is still on the screen at 14:00.
	if day.Today[0].Status != "проведено" {
		t.Errorf("marked lesson status = %q", day.Today[0].Status)
	}
	if day.Today[1].Status != "" {
		t.Errorf("unmarked lesson status = %q", day.Today[1].Status)
	}
}

// A visit on another date must not label this day's lesson.
func TestBuildMatchesVisitsByDay(t *testing.T) {
	day := Build(
		[]schedule.Lesson{lesson(1, "Карате", "Демид", time.Date(2026, 9, 7, 16, 0, 0, 0, kyiv))},
		[]model.Visit{{EnrollmentID: 1, Date: "2026-09-04", Status: model.StatusCancelled}},
		nil, nil, monday, kyiv,
	)
	if day.Today[0].Status != "" {
		t.Errorf("status leaked from another date: %q", day.Today[0].Status)
	}
}

func TestBuildChoreMarkability(t *testing.T) {
	cases := []struct {
		name string
		occ  reminders.Occurrence
		mark bool
		want string
	}{
		// Materialised and open: the surface with buttons may close it.
		{"open", reminders.Occurrence{Status: model.OccPending, Stored: true}, true, ""},
		// A projection of a rule that has not fired cannot be closed.
		{"projected", reminders.Occurrence{Status: model.OccPending}, false, ""},
		{"done", reminders.Occurrence{Status: model.OccDone, Stored: true}, false, "зроблено"},
		{"skipped", reminders.Occurrence{Status: model.OccSkipped, Stored: true}, false, "пропущено"},
	}
	for _, tc := range cases {
		tc.occ.Due = time.Date(2026, 9, 7, 9, 0, 0, 0, kyiv)
		tc.occ.Title = "Полити квіти"
		day := Build(nil, nil, nil, []reminders.Occurrence{tc.occ}, monday, kyiv)
		if len(day.Today) != 1 {
			t.Fatalf("%s: today = %+v", tc.name, day.Today)
		}
		if got := day.Today[0]; got.CanMark != tc.mark || got.Status != tc.want {
			t.Errorf("%s: canMark = %v (want %v), status = %q (want %q)",
				tc.name, got.CanMark, tc.mark, got.Status, tc.want)
		}
	}
}

// Only a closed appointment is labelled: a "заплановано" on every row would be
// a column of noise.
func TestBuildLabelsOnlyClosedAppointments(t *testing.T) {
	day := Build(nil, nil, []model.Appointment{
		{ID: 1, Title: "Стоматолог", StartsAt: "2026-09-07T11:00", Status: model.ApptStatusDone},
		{ID: 2, Title: "Терапевт", StartsAt: "2026-09-07T12:00"},
		// An unparseable datetime is skipped rather than crashing the screen.
		{ID: 3, Title: "Хтозна", StartsAt: "не дата"},
	}, nil, monday, kyiv)
	if len(day.Today) != 2 {
		t.Fatalf("today = %+v", day.Today)
	}
	if day.Today[0].Status != "було" || day.Today[1].Status != "" {
		t.Errorf("statuses = %q, %q", day.Today[0].Status, day.Today[1].Status)
	}
}

// Yesterday is nobody's business on this screen: the day is the floor.
func TestBuildDropsThePast(t *testing.T) {
	day := Build(
		[]schedule.Lesson{lesson(1, "Карате", "Демид", time.Date(2026, 9, 6, 16, 0, 0, 0, kyiv))},
		nil, nil, nil, monday, kyiv,
	)
	if len(day.Today) != 0 || len(day.Upcoming) != 0 {
		t.Errorf("past leaked in: %+v %+v", day.Today, day.Upcoming)
	}
}

// A chore belongs to the day it came due. Projecting a daily one across the
// window would leave "what is coming" as seven copies of "take the bins out",
// with the lessons pushed off the end of it.
func TestBuildKeepsChoresToToday(t *testing.T) {
	day := Build(nil, nil, nil, []reminders.Occurrence{
		{ReminderID: 1, Title: "Смiття", Due: time.Date(2026, 9, 7, 7, 0, 0, 0, kyiv), Status: model.OccPending, Stored: true},
		{ReminderID: 1, Title: "Смiття", Due: time.Date(2026, 9, 8, 7, 0, 0, 0, kyiv), Status: model.OccPending},
		{ReminderID: 1, Title: "Смiття", Due: time.Date(2026, 9, 9, 7, 0, 0, 0, kyiv), Status: model.OccPending},
	}, monday, kyiv)
	if len(day.Today) != 1 {
		t.Errorf("today = %+v", day.Today)
	}
	if len(day.Upcoming) != 0 {
		t.Errorf("tomorrow's chores leaked into upcoming: %+v", day.Upcoming)
	}
}
