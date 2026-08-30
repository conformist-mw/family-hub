package bot

import (
	"strings"
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/reminders"
)

func openChore(title, person string, hh, mm int) reminders.Occurrence {
	loc := time.UTC
	return reminders.Occurrence{
		ReminderID: int64(hh), Title: title, Person: person,
		Due:    time.Date(2026, 9, 5, hh, mm, 0, 0, loc),
		Status: model.OccPending, Stored: true,
	}
}

// Each line says when the chore came due: "you did not do it" is easier to act
// on when it names which of the morning's three it means.
func TestTheNagNamesEachChoreAndWhenItWasDue(t *testing.T) {
	got := reminderNagText([]reminders.Occurrence{
		openChore("Виставити кешбек", "Олег", 8, 0),
		openChore("Записати пробіг авто", "", 9, 30),
	})

	for _, want := range []string{
		"Сьогодні не закрито",
		"• Виставити кешбек · Олег <i>(08:00)</i>",
		"• Записати пробіг авто <i>(09:30)</i>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

// A chore with nobody named must not trail a lonely separator.
func TestAChoreWithoutAPersonCarriesNoSeparator(t *testing.T) {
	got := reminderNagText([]reminders.Occurrence{openChore("Полити кактус", "", 11, 0)})
	if strings.Contains(got, "· <i>") || strings.Contains(got, " ·  ") {
		t.Fatalf("stray separator:\n%s", got)
	}
}

func TestOneOpenChoreIsAOneLineList(t *testing.T) {
	got := reminderNagText([]reminders.Occurrence{openChore("Кешбек", "", 8, 0)})
	if n := strings.Count(got, "•"); n != 1 {
		t.Fatalf("got %d bullets, want 1:\n%s", n, got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Fatalf("message ends with a blank line:\n%q", got)
	}
}

// The message is sent as HTML, so a title carrying angle brackets would break
// the markup — or worse, inject some.
func TestATitleWithMarkupIsEscaped(t *testing.T) {
	got := reminderNagText([]reminders.Occurrence{
		openChore("Полити <b>кактус</b>", "Оксана & Олег", 11, 0),
	})
	if strings.Contains(got, "<b>кактус</b>") {
		t.Fatalf("markup from a title survived into the message:\n%s", got)
	}
	if !strings.Contains(got, "&lt;b&gt;кактус&lt;/b&gt;") {
		t.Fatalf("title not escaped:\n%s", got)
	}
	if !strings.Contains(got, "Оксана &amp; Олег") {
		t.Fatalf("person not escaped:\n%s", got)
	}
}

// The invariant this feature was nearly shipped without: the chore nag must
// run in prod, where NOTIFICATIONS_ENABLED is off because Home Assistant sends
// the appointment summaries. HA cannot send this one — it reads a calendar and
// knows nothing about what was closed.
func TestTheChoreNagDoesNotAnswerToTheAppointmentDigestFlag(t *testing.T) {
	svc := &reminders.Service{}

	prod := Config{NotifyChat: -100, NotificationsEnabled: false,
		ReminderNagTime: "20:00", Reminders: svc}
	if !prod.reminderNagEnabled() {
		t.Fatal("the nag is silent in the production shape")
	}
	if prod.appointmentDigestsEnabled() {
		t.Fatal("the appointment digests turned themselves on")
	}
}

func TestTheNagStaysOffWithoutATimeOrAService(t *testing.T) {
	svc := &reminders.Service{}
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"no time set", Config{NotifyChat: -100, Reminders: svc}},
		{"no service wired", Config{NotifyChat: -100, ReminderNagTime: "20:00"}},
		{"neither", Config{NotifyChat: -100}},
	} {
		if tc.cfg.reminderNagEnabled() {
			t.Fatalf("%s: the nag is on", tc.name)
		}
	}
}

// --- callback data ---

// due_at carries its own colon, so the payload cannot use the ":" the visit
// buttons do. This is the round trip that has to survive a message sitting in
// the group for days.
func TestChoreCallbackDataSurvivesTheRoundTrip(t *testing.T) {
	loc := time.UTC
	m := buildNagMarkup([]reminders.Occurrence{
		{ReminderID: 42, Title: "Виставити кешбек",
			Due: time.Date(2026, 8, 29, 11, 0, 0, 0, loc), Status: model.OccPending},
	})

	if len(m.InlineKeyboard) != 1 || len(m.InlineKeyboard[0]) != 2 {
		t.Fatalf("keyboard = %+v, want one row of two", m.InlineKeyboard)
	}
	done := m.InlineKeyboard[0][0]
	skip := m.InlineKeyboard[0][1]

	id, due, status, err := parseChoreData(done.Data, loc)
	if err != nil {
		t.Fatalf("parse done: %v (data %q)", err, done.Data)
	}
	if id != 42 || status != model.OccDone {
		t.Fatalf("id=%d status=%q", id, status)
	}
	if got := due.Format(model.LocalDatetime); got != "2026-08-29T11:00" {
		t.Fatalf("due = %q", got)
	}
	if _, _, status, err = parseChoreData(skip.Data, loc); err != nil || status != model.OccSkipped {
		t.Fatalf("parse skip: status=%q err=%v", status, err)
	}
}

// Telegram caps callback data at 64 bytes. A payload that overflows is
// rejected at send time, which would take the whole message down.
func TestChoreCallbackDataFitsTelegramsLimit(t *testing.T) {
	m := buildNagMarkup([]reminders.Occurrence{
		{ReminderID: 999999, Title: "Що завгодно",
			Due: time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC), Status: model.OccPending},
	})
	for _, btn := range m.InlineKeyboard[0] {
		// The wire form is "\f<unique>|<data>".
		if n := len("\f" + "rem_chore" + "|" + btn.Data); n > 64 {
			t.Fatalf("callback payload is %d bytes, over Telegram's 64", n)
		}
	}
}

// Callback data is echoed back from a message that may be days old, so every
// field is checked rather than assumed.
func TestBadChoreCallbackDataIsRejected(t *testing.T) {
	for _, data := range []string{
		"",
		"42",
		"42|2026-08-29T11:00",
		"нечисло|2026-08-29T11:00|done",
		"42|не дата|done",
		"42|2026-08-29T11:00|forgotten",
		// pending is a state the system writes, never a person's decision.
		"42|2026-08-29T11:00|pending",
		"42|2026-08-29T11:00|done|extra",
	} {
		if _, _, _, err := parseChoreData(data, time.UTC); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}

// A long title must not wrap the button into a wall.
func TestALongChoreTitleIsShortenedForItsButton(t *testing.T) {
	long := "Виставити кешбек в обох банківських застосунках"
	got := shortTitle(long)
	if len([]rune(got)) > 18 {
		t.Fatalf("label is %d runes: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("shortened label does not say it was cut: %q", got)
	}
	if short := shortTitle("Кешбек"); short != "Кешбек" {
		t.Fatalf("a short title was touched: %q", short)
	}
}

// One row per chore, so several open chores stay tappable without the rows
// running together.
func TestEachOpenChoreGetsItsOwnRow(t *testing.T) {
	m := buildNagMarkup([]reminders.Occurrence{
		openChore("Кешбек", "", 8, 0),
		openChore("Пробіг", "", 9, 0),
		openChore("Кактус", "", 11, 0),
	})
	if len(m.InlineKeyboard) != 3 {
		t.Fatalf("got %d rows for three chores", len(m.InlineKeyboard))
	}
}
