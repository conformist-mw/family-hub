package ics

import (
	"strings"
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/reminders"
	"familyhub/internal/schedule"
)

func kyiv(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatalf("load Europe/Kyiv: %v", err)
	}
	return loc
}

// events splits the rendered calendar into one string per VEVENT, so a test
// can assert about the event it means rather than about the whole document.
func events(body []byte) []string {
	var out []string
	var cur []string
	inside := false
	for _, line := range strings.Split(string(body), "\r\n") {
		switch line {
		case "BEGIN:VEVENT":
			inside, cur = true, nil
		case "END:VEVENT":
			inside = false
			out = append(out, strings.Join(cur, "\n"))
		default:
			if inside {
				cur = append(cur, line)
			}
		}
	}
	return out
}

func find(t *testing.T, body []byte, uidPart string) string {
	t.Helper()
	for _, e := range events(body) {
		if strings.Contains(e, uidPart) {
			return e
		}
	}
	t.Fatalf("no event with uid containing %q in:\n%s", uidPart, body)
	return ""
}

// lesson is one expanded slot, the way schedule.Expand hands them over.
func lesson(slotID int64, start time.Time) schedule.Lesson {
	return schedule.Lesson{
		SlotID:      slotID,
		Enrollment:  model.Enrollment{ID: 5, Name: "Балет", Person: "Маша"},
		Start:       start,
		DurationMin: 60,
	}
}

func chore(id int64, title, person string, due time.Time, status string) reminders.Occurrence {
	return reminders.Occurrence{
		ReminderID: id, Title: title, Person: person, Due: due,
		DurationMin: 15, Status: status, Stored: true,
	}
}

func TestARecurringChoreBecomesOneEventPerOccurrence(t *testing.T) {
	loc := kyiv(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	body := Render(nil, nil, nil, []reminders.Occurrence{
		chore(3, "Кешбек", "Олег", time.Date(2026, 9, 1, 8, 0, 0, 0, loc), model.OccPending),
		chore(3, "Кешбек", "Олег", time.Date(2026, 10, 1, 8, 0, 0, 0, loc), model.OccPending),
	}, loc, now)

	if n := len(events(body)); n != 2 {
		t.Fatalf("got %d events, want one per occurrence", n)
	}
	ev := find(t, body, "reminder-3-20260901T0800")
	// 08:00 Kyiv in September is UTC+3.
	if !strings.Contains(ev, "DTSTART:20260901T050000Z") {
		t.Fatalf("dtstart wrong:\n%s", ev)
	}
	if !strings.Contains(ev, "DTEND:20260901T051500Z") {
		t.Fatalf("dtend is not start + 15 minutes:\n%s", ev)
	}
	if !strings.Contains(ev, "SUMMARY:Кешбек · Олег") {
		t.Fatalf("summary wrong:\n%s", ev)
	}
}

// A full RRULE can put two occurrences on one date. A uid keyed on the date
// would collapse them into one, and the calendar would quietly lose half the
// chores.
func TestTwoOccurrencesOnOneDateGetDifferentUids(t *testing.T) {
	loc := kyiv(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	body := Render(nil, nil, nil, []reminders.Occurrence{
		chore(7, "Ліки", "", time.Date(2026, 9, 6, 8, 0, 0, 0, loc), model.OccPending),
		chore(7, "Ліки", "", time.Date(2026, 9, 6, 20, 0, 0, 0, loc), model.OccPending),
	}, loc, now)

	morning := find(t, body, "reminder-7-20260906T0800")
	evening := find(t, body, "reminder-7-20260906T2000")
	if morning == evening {
		t.Fatal("both occurrences rendered as the same event")
	}
}

// A closed occurrence stays in the feed: the calendar is a record of what was
// planned, and an entry that vanishes once acted on cannot show that it was.
func TestClosedChoresStayInTheFeedAndSayHow(t *testing.T) {
	loc := kyiv(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	body := Render(nil, nil, nil, []reminders.Occurrence{
		chore(1, "Зроблено", "", time.Date(2026, 9, 1, 8, 0, 0, 0, loc), model.OccDone),
		chore(2, "Пропущено", "", time.Date(2026, 9, 1, 9, 0, 0, 0, loc), model.OccSkipped),
		chore(3, "Відкрито", "", time.Date(2026, 9, 1, 10, 0, 0, 0, loc), model.OccPending),
	}, loc, now)

	if got := find(t, body, "reminder-1-"); !strings.Contains(got, "SUMMARY:✓ Зроблено") {
		t.Fatalf("done chore:\n%s", got)
	}
	// done and skipped are different answers; one shared tick would hide that.
	if got := find(t, body, "reminder-2-"); !strings.Contains(got, "SUMMARY:✗ Пропущено") {
		t.Fatalf("skipped chore:\n%s", got)
	}
	if got := find(t, body, "reminder-3-"); !strings.Contains(got, "SUMMARY:Відкрито") {
		t.Fatalf("open chore should carry no mark:\n%s", got)
	}
}

func TestAChoreWithoutAPersonIsJustItsTitle(t *testing.T) {
	loc := kyiv(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	body := Render(nil, nil, nil, []reminders.Occurrence{
		chore(1, "Пробіг авто", "", time.Date(2026, 9, 1, 9, 0, 0, 0, loc), model.OccPending),
	}, loc, now)
	if got := find(t, body, "reminder-1-"); !strings.Contains(got, "SUMMARY:Пробіг авто\n") &&
		!strings.HasSuffix(got, "SUMMARY:Пробіг авто") {
		t.Fatalf("summary carries a stray separator:\n%s", got)
	}
}

func TestAChoreWithNoDurationGetsTheDefaultQuarterHour(t *testing.T) {
	loc := kyiv(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	o := chore(1, "Кешбек", "", time.Date(2026, 9, 1, 8, 0, 0, 0, loc), model.OccPending)
	o.DurationMin = 0
	body := Render(nil, nil, nil, []reminders.Occurrence{o}, loc, now)

	if got := find(t, body, "reminder-1-"); !strings.Contains(got, "DTEND:20260901T051500Z") {
		t.Fatalf("dtend wrong for a zero duration:\n%s", got)
	}
}

// Every uid in the feed shares one suffix. A calendar keys its events on the
// uid, so a mixed feed would read as two unrelated sources.
func TestEveryUidCarriesTheSameSuffix(t *testing.T) {
	loc := kyiv(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	body := Render(
		[]schedule.Lesson{lesson(11, time.Date(2026, 9, 8, 16, 0, 0, 0, loc))},
		[]model.TrainerAbsence{{
			ID: 4, TrainerID: 1, Trainer: "Ірина",
			DateFrom: "2026-09-10", DateTo: "2026-09-12", Kind: model.AbsenceVacation,
		}},
		[]model.Appointment{{
			ID: 9, Title: "Ортодонт", Person: "Демид", StartsAt: "2026-09-08T14:30",
			Status: model.ApptStatusPlanned,
		}},
		[]reminders.Occurrence{
			chore(3, "Кешбек", "", time.Date(2026, 9, 1, 8, 0, 0, 0, loc), model.OccPending),
		},
		loc, now)

	all := events(body)
	if len(all) != 4 {
		t.Fatalf("got %d events, want one from each source", len(all))
	}
	for _, e := range all {
		for _, line := range strings.Split(e, "\n") {
			if !strings.HasPrefix(line, "UID:") {
				continue
			}
			if !strings.HasSuffix(line, "@familyhub") {
				t.Fatalf("uid does not carry the shared suffix: %q", line)
			}
		}
	}
	if strings.Contains(string(body), "@lessons") {
		t.Fatal("the old suffix survives somewhere in the feed")
	}
}

// The other three sources have to keep working unchanged; the suffix rename
// touched every one of them.
func TestTheOtherSourcesStillRender(t *testing.T) {
	loc := kyiv(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	body := Render(
		[]schedule.Lesson{lesson(11, time.Date(2026, 9, 8, 16, 0, 0, 0, loc))},
		[]model.TrainerAbsence{{
			ID: 4, TrainerID: 1, Trainer: "Ірина",
			DateFrom: "2026-09-10", DateTo: "2026-09-12", Kind: model.AbsenceVacation,
		}},
		[]model.Appointment{{
			ID: 9, Title: "Ортодонт", Person: "Демид", StartsAt: "2026-09-08T14:30",
			Status: model.ApptStatusPlanned,
		}},
		nil, loc, now)

	slot := find(t, body, "slot-11-20260908T1600@familyhub")
	// No RRULE any more: the lesson arrives already expanded, so the feed
	// carries the instant rather than a rule a calendar would expand in UTC.
	if strings.Contains(slot, "RRULE") {
		t.Fatalf("a lesson still goes out as a rule:\n%s", slot)
	}
	if !strings.Contains(slot, "DTSTART:20260908T130000Z") {
		t.Fatalf("lesson start:\n%s", slot)
	}
	if !strings.Contains(slot, "SUMMARY:Маша · Балет") {
		t.Fatalf("slot summary:\n%s", slot)
	}
	absence := find(t, body, "absence-4@familyhub")
	if !strings.Contains(absence, "DTSTART;VALUE=DATE:20260910") ||
		!strings.Contains(absence, "DTEND;VALUE=DATE:20260913") {
		t.Fatalf("absence is not the inclusive range as an all-day event:\n%s", absence)
	}
	appt := find(t, body, "appointment-9@familyhub")
	if !strings.Contains(appt, "SUMMARY:Ортодонт · Демид") {
		t.Fatalf("appointment summary:\n%s", appt)
	}
}

func TestAnEmptyCalendarIsStillValid(t *testing.T) {
	loc := kyiv(t)
	body := string(Render(nil, nil, nil, nil, loc, time.Date(2026, 9, 5, 12, 0, 0, 0, loc)))
	if !strings.HasPrefix(body, "BEGIN:VCALENDAR\r\n") || !strings.HasSuffix(body, "END:VCALENDAR\r\n") {
		t.Fatalf("not a wrapped calendar:\n%s", body)
	}
}

// Commas and semicolons are structural in RFC 5545; a title carrying one has
// to be escaped or the event after it is misparsed.
func TestSpecialCharactersInATitleAreEscaped(t *testing.T) {
	loc := kyiv(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	body := Render(nil, nil, nil, []reminders.Occurrence{
		chore(1, "Кешбек, картки; обидві", "", time.Date(2026, 9, 1, 8, 0, 0, 0, loc), model.OccPending),
	}, loc, now)

	if got := find(t, body, "reminder-1-"); !strings.Contains(got, `SUMMARY:Кешбек\, картки\; обидві`) {
		t.Fatalf("title not escaped:\n%s", got)
	}
}

// The default test alone cannot tell "the default was applied" from "the field
// is ignored entirely" — every fixture used 15 minutes.
func TestAChoreCarriesItsOwnDuration(t *testing.T) {
	loc := kyiv(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	o := chore(1, "Довга справа", "", time.Date(2026, 9, 1, 8, 0, 0, 0, loc), model.OccPending)
	o.DurationMin = 45
	body := Render(nil, nil, nil, []reminders.Occurrence{o}, loc, now)

	if got := find(t, body, "reminder-1-"); !strings.Contains(got, "DTEND:20260901T054500Z") {
		t.Fatalf("dtend is not start + 45 minutes:\n%s", got)
	}
}
