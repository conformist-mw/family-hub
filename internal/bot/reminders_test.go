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
		"Не закрито",
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

// The invariant this feature was nearly shipped without, tested where it can
// actually break: the tick decision, not a helper beside it. In prod
// NOTIFICATIONS_ENABLED is off, because Home Assistant sends the appointment
// summaries; HA cannot send this one, since it reads a calendar and knows
// nothing about what was closed.
func TestTheChoreNagFiresWithTheAppointmentDigestsSwitchedOff(t *testing.T) {
	prod := Config{
		NotifyChat:           -100,
		NotificationsEnabled: false, // the production shape
		DailyDigestTime:      "08:00",
		WeeklyDigestDOW:      0,
		WeeklyDigestTime:     "18:00",
		ReminderNagTime:      "20:00",
		Reminders:            &reminders.Service{},
	}
	at := func(hh, mm int) time.Time { return time.Date(2026, 9, 6, hh, mm, 0, 0, time.UTC) }

	daily, weekly, nag := prod.dueThisMinute(at(20, 0), "", "", "")
	if !nag {
		t.Fatal("the chore nag did not fire in the production shape")
	}
	if daily || weekly {
		t.Fatalf("an appointment digest fired with notifications off: daily=%v weekly=%v", daily, weekly)
	}

	// 6 Sep 2026 is a Sunday, so the weekly digest's day matches — it must
	// still stay silent on the flag alone.
	if _, weekly, _ = prod.dueThisMinute(at(18, 0), "", "", ""); weekly {
		t.Fatal("the weekly digest fired with notifications off")
	}
	if daily, _, _ = prod.dueThisMinute(at(8, 0), "", "", ""); daily {
		t.Fatal("the daily digest fired with notifications off")
	}
}

// Each message goes out once a day, and one having gone must not silence the
// others.
func TestEachMessageLatchesSeparatelyPerDay(t *testing.T) {
	cfg := Config{
		NotifyChat: -100, NotificationsEnabled: true,
		DailyDigestTime: "08:00", WeeklyDigestDOW: 0, WeeklyDigestTime: "08:00",
		ReminderNagTime: "08:00", Reminders: &reminders.Service{},
	}
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC) // a Sunday
	today := "2026-09-06"

	daily, weekly, nag := cfg.dueThisMinute(now, "", "", "")
	if !daily || !weekly || !nag {
		t.Fatalf("first tick: daily=%v weekly=%v nag=%v, want all", daily, weekly, nag)
	}
	if daily, weekly, nag = cfg.dueThisMinute(now, today, today, today); daily || weekly || nag {
		t.Fatalf("re-fired within the same day: daily=%v weekly=%v nag=%v", daily, weekly, nag)
	}
	// Only the nag has gone out: the digests must still be due.
	if daily, weekly, nag = cfg.dueThisMinute(now, "", "", today); !daily || !weekly || nag {
		t.Fatalf("latches are not independent: daily=%v weekly=%v nag=%v", daily, weekly, nag)
	}
}

func TestNothingFiresAtTheWrongMinute(t *testing.T) {
	cfg := Config{
		NotifyChat: -100, NotificationsEnabled: true,
		DailyDigestTime: "08:00", WeeklyDigestDOW: 0, WeeklyDigestTime: "18:00",
		ReminderNagTime: "20:00", Reminders: &reminders.Service{},
	}
	for _, at := range []time.Time{
		time.Date(2026, 9, 6, 7, 59, 0, 0, time.UTC),
		time.Date(2026, 9, 6, 20, 1, 0, 0, time.UTC),
		time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
	} {
		if d, w, n := cfg.dueThisMinute(at, "", "", ""); d || w || n {
			t.Fatalf("%s fired something: daily=%v weekly=%v nag=%v", at.Format("15:04"), d, w, n)
		}
	}
	// The weekly one also has to respect the day, not just the time.
	monday := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	if _, w, _ := cfg.dueThisMinute(monday, "", "", ""); w {
		t.Fatal("the weekly digest fired on the wrong weekday")
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

// The message at the moment a chore comes due is a different sentence from the
// evening one. "Пора" opens by saying it is time; the nag opens by saying it
// was not done. A push that told you off for a chore you are about to do is one
// you learn to dismiss.
func TestTheDuePushSaysItIsTimeRatherThanScolding(t *testing.T) {
	got := choreDueText([]reminders.Occurrence{openChore("Прокрутити пластину", "Демид", 18, 0)})
	if !strings.Contains(got, "Пора") {
		t.Fatalf("the push does not say it is time:\n%s", got)
	}
	if strings.Contains(got, "не закрито") || strings.Contains(got, "Не закрито") {
		t.Fatalf("the push scolds for a chore that just came due:\n%s", got)
	}
	// Same body as the nag, so the two read as one feature.
	if !strings.Contains(got, "• Прокрутити пластину · Демид <i>(18:00)</i>") {
		t.Fatalf("the push lost the chore line:\n%s", got)
	}
}

// The bug this window exists to fix. The nag fires at 20:00, so a chore due at
// 21:00 has no row yet when it runs; a day-shaped window then asked about the
// next day and nothing ever mentioned it.
func TestTheNagWindowReachesBackPastItsOwnFiringTime(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	b := &Bot{cfg: Config{ReminderNagTime: "20:00", Loc: loc}}

	// The nag firing on the 11th at 20:00.
	from, to := b.nagWindow(time.Date(2026, 9, 11, 20, 0, 0, 0, loc))
	if got := to.Format("2006-01-02 15:04"); got != "2026-09-11 20:00" {
		t.Fatalf("window ends at %s, want its own firing time", got)
	}
	// Yesterday's 21:00 chore — the one the old day-window lost — is inside.
	late := time.Date(2026, 9, 10, 21, 0, 0, 0, loc)
	if late.Before(from) || late.After(to) {
		t.Fatalf("a chore due at %s falls outside [%s, %s]",
			late.Format("15:04"), from.Format("01-02 15:04"), to.Format("01-02 15:04"))
	}
}

// Every occurrence must fall in exactly one nag window, or a chore left open
// is either reported twice or not at all.
func TestConsecutiveNagWindowsDoNotOverlapOrGap(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	b := &Bot{cfg: Config{ReminderNagTime: "20:00", Loc: loc}}

	_, prevTo := b.nagWindow(time.Date(2026, 9, 10, 20, 0, 0, 0, loc))
	from, _ := b.nagWindow(time.Date(2026, 9, 11, 20, 0, 0, 0, loc))
	if gap := from.Sub(prevTo); gap != time.Minute {
		t.Fatalf("consecutive windows are %s apart, want one minute (no gap, no overlap)", gap)
	}
}

// Before the day's nag has run, the window is still yesterday's — so tapping a
// button on last night's message redraws the list it was showing.
func TestBeforeTodaysNagTheWindowIsStillYesterdays(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	b := &Bot{cfg: Config{ReminderNagTime: "20:00", Loc: loc}}

	_, to := b.nagWindow(time.Date(2026, 9, 11, 9, 0, 0, 0, loc)) // morning
	if got := to.Format("2006-01-02 15:04"); got != "2026-09-10 20:00" {
		t.Fatalf("window ends at %s, want the last nag that actually fired", got)
	}
}

// The flood guard. After downtime the materialiser backfills up to a month of
// occurrences; a push that trusted its mark would deliver all of them at once,
// into the family chat, as one message.
func TestAPushNeverReachesBackFurtherThanItsClamp(t *testing.T) {
	now := time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)

	// A mark from a month ago — what a stalled ticker would hand it.
	from, to := pushWindow(now, now.AddDate(0, -1, 0))
	if reach := to.Sub(from); reach > maxPushLookback {
		t.Fatalf("the push reached back %s, want at most %s", reach, maxPushLookback)
	}

	// An ordinary tick is not clamped: it asks about the minute that passed.
	from, to = pushWindow(now, now.Add(-time.Minute))
	if !from.Equal(now) || !to.Equal(now) {
		t.Fatalf("an ordinary tick asked about [%s, %s]", from, to)
	}
}

// Consecutive ticks must not announce the same minute twice.
func TestConsecutivePushWindowsDoNotOverlap(t *testing.T) {
	now := time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)
	_, firstTo := pushWindow(now, now.Add(-time.Minute))
	secondFrom, _ := pushWindow(now.Add(time.Minute), firstTo)
	if !secondFrom.After(firstTo) {
		t.Fatalf("the next window starts at %s, on or before the previous end %s",
			secondFrom, firstTo)
	}
}
