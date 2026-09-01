package bot

import (
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"

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

	daily, weekly, nag, _ := prod.dueThisMinute(at(20, 0), "", "", "", "")
	if !nag {
		t.Fatal("the chore nag did not fire in the production shape")
	}
	if daily || weekly {
		t.Fatalf("an appointment digest fired with notifications off: daily=%v weekly=%v", daily, weekly)
	}

	// 6 Sep 2026 is a Sunday, so the weekly digest's day matches — it must
	// still stay silent on the flag alone.
	if _, weekly, _, _ = prod.dueThisMinute(at(18, 0), "", "", "", ""); weekly {
		t.Fatal("the weekly digest fired with notifications off")
	}
	if daily, _, _, _ = prod.dueThisMinute(at(8, 0), "", "", "", ""); daily {
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

	daily, weekly, nag, _ := cfg.dueThisMinute(now, "", "", "", "")
	if !daily || !weekly || !nag {
		t.Fatalf("first tick: daily=%v weekly=%v nag=%v, want all", daily, weekly, nag)
	}
	if daily, weekly, nag, _ = cfg.dueThisMinute(now, today, today, today, today); daily || weekly || nag {
		t.Fatalf("re-fired within the same day: daily=%v weekly=%v nag=%v", daily, weekly, nag)
	}
	// Only the nag has gone out: the digests must still be due.
	if daily, weekly, nag, _ = cfg.dueThisMinute(now, "", "", today, ""); !daily || !weekly || nag {
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
		if d, w, n, _ := cfg.dueThisMinute(at, "", "", "", ""); d || w || n {
			t.Fatalf("%s fired something: daily=%v weekly=%v nag=%v", at.Format("15:04"), d, w, n)
		}
	}
	// The weekly one also has to respect the day, not just the time.
	monday := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	if _, w, _, _ := cfg.dueThisMinute(monday, "", "", "", ""); w {
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

// The label used to carry the chore's title, cut to fit — which on a real
// title left an ellipsis exactly where the meaning was. No label carries a
// title any more, so none can be cut.
func TestAButtonLabelNeverCarriesTheTitle(t *testing.T) {
	long := "Перевірити нарахування комунальних у банку"
	m := buildNagMarkup([]reminders.Occurrence{openChore(long, "", 8, 0)})

	for _, btn := range m.InlineKeyboard[0] {
		if strings.Contains(btn.Text, "…") {
			t.Fatalf("a label is still being cut: %q", btn.Text)
		}
		if strings.Contains(btn.Text, "Перевірити") {
			t.Fatalf("a label still carries the title: %q", btn.Text)
		}
	}
}

// One chore needs no number: the text above says which. Words rather than bare
// glyphs, because there is room for them.
func TestALoneChoreGetsSpelledOutButtons(t *testing.T) {
	m := buildNagMarkup([]reminders.Occurrence{openChore("Кешбек", "", 8, 0)})
	if got := m.InlineKeyboard[0][0].Text; got != "✓ Зроблено" {
		t.Fatalf("done button = %q", got)
	}
	if got := m.InlineKeyboard[0][1].Text; got != "✗ Не треба" {
		t.Fatalf("skip button = %q", got)
	}
}

// Several chores in one message need telling apart, so the line is numbered
// and the button says which number it answers.
func TestSeveralChoresAreNumberedInBothLinesAndButtons(t *testing.T) {
	items := []reminders.Occurrence{
		openChore("Кешбек", "", 8, 0),
		openChore("Пробіг", "", 9, 0),
	}
	m := buildNagMarkup(items)
	if got := m.InlineKeyboard[1][0].Text; got != "2 ✓" {
		t.Fatalf("second chore's done button = %q, want \"2 ✓\"", got)
	}

	body := choreLines(items)
	if !strings.Contains(body, "2 • Пробіг") {
		t.Fatalf("the second line is not numbered to match its button:\n%s", body)
	}
}

// The answer replaces the bullet, so a message still says what it was about
// after it has been answered.
func TestAnAnsweredChoreKeepsItsLineAndGainsAMark(t *testing.T) {
	done := openChore("Кешбек", "Олег", 8, 0)
	done.Status = model.OccDone
	skipped := openChore("Пробіг", "", 9, 0)
	skipped.Status = model.OccSkipped

	body := choreLines([]reminders.Occurrence{done, skipped})
	if !strings.Contains(body, "1 ✓ Кешбек · Олег") {
		t.Fatalf("a closed chore lost its line or its mark:\n%s", body)
	}
	if !strings.Contains(body, "2 ✗ Пробіг") {
		t.Fatalf("a skipped chore is not marked as skipped:\n%s", body)
	}
}

// The redraw rebuilds the message from its own keyboard, so what it lists can
// never drift from what it listed — even days later, when every window has
// moved on.
func TestAMessageRemembersItsChoresThroughItsKeyboard(t *testing.T) {
	items := []reminders.Occurrence{
		openChore("Кешбек", "", 8, 0),
		openChore("Пробіг", "", 9, 0),
	}
	refs := choreRefsFromMarkup(asTelegramSentIt(buildNagMarkup(items)))
	if len(refs) != 2 {
		t.Fatalf("recovered %d chores from the keyboard, want 2: %+v", len(refs), refs)
	}
	if refs[0].id != items[0].ReminderID || refs[1].id != items[1].ReminderID {
		t.Fatalf("recovered the wrong chores: %+v", refs)
	}
	if refs[0].dueAt != items[0].Due.Format(model.LocalDatetime) {
		t.Fatalf("recovered due_at = %q", refs[0].dueAt)
	}
}

// asTelegramSentIt turns a keyboard we built into the shape one comes back in:
// Unique gone, callback_data fused. Reading our own outgoing markup would test
// the easy half and miss the path that actually runs.
func asTelegramSentIt(m *tele.ReplyMarkup) *tele.ReplyMarkup {
	out := &tele.ReplyMarkup{}
	for _, row := range m.InlineKeyboard {
		var got []tele.InlineButton
		for _, btn := range row {
			if btn.Unique != "" {
				btn.Data = "\f" + btn.Unique + "|" + btn.Data
				btn.Unique = ""
			}
			got = append(got, btn)
		}
		out.InlineKeyboard = append(out.InlineKeyboard, got)
	}
	return out
}

// A foreign button in the same keyboard — the "open the app" row is added to
// every message — must not be read as a chore.
func TestTheAppButtonIsNotMistakenForAChore(t *testing.T) {
	m := buildNagMarkup([]reminders.Occurrence{openChore("Кешбек", "", 8, 0)})
	m.InlineKeyboard = append(m.InlineKeyboard,
		[]tele.InlineButton{{Text: "Відкрити застосунок", URL: "https://example.test"}})

	if refs := choreRefsFromMarkup(m); len(refs) != 1 {
		t.Fatalf("recovered %d chores, want 1: %+v", len(refs), refs)
	}
}

// "Пора" and "не закрито" are different sentences about different moments; a
// redraw must not turn one into the other.
func TestARedrawKeepsTheMessagesOwnOpeningLine(t *testing.T) {
	if got := choreHeaderOf("⏰ Пора:\n\n• Кешбек"); got != dueHeader {
		t.Fatalf("the due push was redrawn as %q", got)
	}
	if got := choreHeaderOf("🔔 Не закрито:\n\n• Кешбек"); got != nagHeader {
		t.Fatalf("the nag was redrawn as %q", got)
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

// The bug: two chores came due in prod (08:00 and 09:00), both were recorded,
// and neither was announced.
//
// The push and the materialiser are separate tickers over the same minute. If
// the push queries before the row is written, it sees an empty window — and
// the old code took that as "this minute is done" and moved the mark. The next
// tick then asked about 08:01, so the 08:00 row, written a fraction of a
// second late, was outside every window that would ever be built.
//
// A pass that saw nothing has not seen the minute; it has only seen it empty
// so far.
func TestAMinuteThatLookedEmptyIsAskedAboutAgain(t *testing.T) {
	at := func(hh, mm, ss int) time.Time {
		return time.Date(2026, 9, 1, hh, mm, ss, 0, time.UTC)
	}
	// Ticks land at :40 — a container started at :40, as prod's did.
	mark := at(7, 59, 40)

	// 08:00 tick: the row is not written yet, so the pass finds nothing.
	from, to := pushWindow(at(8, 0, 40), mark)
	if !covers(from, to, at(8, 0, 0)) {
		t.Fatalf("the 08:00 window does not even cover 08:00: [%s, %s]", from, to)
	}
	mark = markAfterPass(at(8, 0, 40), mark, 0)

	// 08:01 tick: the row exists now. It has to be inside this window.
	from, to = pushWindow(at(8, 1, 40), mark)
	if !covers(from, to, at(8, 0, 0)) {
		t.Fatalf("a chore due at 08:00 is lost once its minute looked empty: [%s, %s]",
			from.Format("15:04"), to.Format("15:04"))
	}
}

// The other half of the same rule: once a chore has actually been announced,
// its minute must never come back, or every unclosed chore is re-sent for as
// long as the clamp reaches it.
func TestAnAnnouncedMinuteIsNeverAskedAboutAgain(t *testing.T) {
	at := func(hh, mm, ss int) time.Time {
		return time.Date(2026, 9, 1, hh, mm, ss, 0, time.UTC)
	}
	mark := markAfterPass(at(8, 0, 40), at(7, 59, 40), 1) // one chore sent at 08:00

	// Four quiet minutes later the mark has not moved, so the window has
	// grown — but it must still start after the minute already announced.
	for _, tick := range []time.Time{at(8, 1, 40), at(8, 2, 40), at(8, 5, 40)} {
		from, to := pushWindow(tick, mark)
		if covers(from, to, at(8, 0, 0)) {
			t.Fatalf("tick %s re-announces the 08:00 chore: [%s, %s]",
				tick.Format("15:04"), from.Format("15:04"), to.Format("15:04"))
		}
		mark = markAfterPass(tick, mark, 0)
	}
}

// covers mirrors what the store does with the bounds: due_at has minute
// resolution, so the comparison truncates seconds.
func covers(from, to, due time.Time) bool {
	f := from.Format(model.LocalDatetime)
	t := to.Format(model.LocalDatetime)
	d := due.Format(model.LocalDatetime)
	return d >= f && d <= t
}

// An answered chore loses its buttons. Keeping them meant a row that could
// only ever reply "вже закрито" — a control that looks live and is not.
func TestAnAnsweredChoreLosesItsButtons(t *testing.T) {
	items := []reminders.Occurrence{
		openChore("Кешбек", "", 8, 0),
		openChore("Пробіг", "", 9, 0),
		openChore("Кактус", "", 11, 0),
	}
	items[1].Status = model.OccDone

	m := buildNagMarkup(items)
	if len(m.InlineKeyboard) != 2 {
		t.Fatalf("got %d rows for three chores with one answered, want 2", len(m.InlineKeyboard))
	}
	// The survivors keep the numbers their lines show: 1 and 3, not 1 and 2.
	if got := m.InlineKeyboard[0][0].Text; got != "1 ✓" {
		t.Fatalf("first surviving button = %q, want \"1 ✓\"", got)
	}
	if got := m.InlineKeyboard[1][0].Text; got != "3 ✓" {
		t.Fatalf("second surviving button = %q, want \"3 ✓\" — numbers must follow the lines", got)
	}
	// And the answered one still has its line.
	if !strings.Contains(choreLines(items), "2 ✓ Пробіг") {
		t.Fatalf("the answered chore lost its line:\n%s", choreLines(items))
	}
}

// Once everything is answered the keyboard is empty, so nothing is left to tap.
func TestAFullyAnsweredMessageHasNoChoreButtons(t *testing.T) {
	items := []reminders.Occurrence{openChore("Кешбек", "", 8, 0)}
	items[0].Status = model.OccSkipped

	if m := buildNagMarkup(items); len(m.InlineKeyboard) != 0 {
		t.Fatalf("got %d rows, want none: %+v", len(m.InlineKeyboard), m.InlineKeyboard)
	}
}
