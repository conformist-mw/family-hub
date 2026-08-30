package reminders

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// storeService wires a real store, because everything the materialiser does is
// about which rows exist afterwards.
func storeService(t *testing.T, now time.Time) (*Service, *store.Store, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatalf("load Europe/Kyiv: %v", err)
	}
	st := store.New(database)
	return NewService(st, loc, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return now }), st, database
}

// seedChore backdates active_since as it goes. The column defaults to SQLite's
// own "now" — the real wall clock — while these tests drive the service from an
// injected one. Left to disagree, the backfill floor would depend on the day
// the suite happens to run, and these tests would start failing on their own
// once the real date drifted past the fixtures.
//
// Tests whose subject IS the pause floor set active_since explicitly afterwards.
func seedChore(t *testing.T, st *store.Store, database *sql.DB, title, rrule, dtstart string) model.Reminder {
	t.Helper()
	r, err := st.CreateReminder(
		model.Reminder{Title: title, Person: "Олег", Active: true},
		model.ReminderRule{ValidFromAt: "2020-01-01T00:00", DTStart: dtstart, RRule: rrule})
	if err != nil {
		t.Fatalf("create %s: %v", title, err)
	}
	if _, err := database.Exec(
		`UPDATE reminders SET active_since = '2000-01-01T00:00' WHERE id = ?`, r.ID); err != nil {
		t.Fatalf("backdate active_since: %v", err)
	}
	r.ActiveSince = "2000-01-01T00:00"
	return r
}

func storedDue(t *testing.T, st *store.Store, from, to string) []string {
	t.Helper()
	occ, err := st.OccurrencesIn(from, to)
	if err != nil {
		t.Fatalf("read occurrences: %v", err)
	}
	out := make([]string, len(occ))
	for i, o := range occ {
		out[i] = o.DueAt
	}
	return out
}

func TestMaterialiseWritesEveryOccurrenceThatHasComeDue(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-01T08:00")

	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	got := storedDue(t, st, "2026-09-01T00:00", "2026-09-30T23:59")
	want := []string{
		"2026-09-01T08:00", "2026-09-02T08:00", "2026-09-03T08:00",
		"2026-09-04T08:00", "2026-09-05T08:00",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// The boundary is the instant, not the day: today's later occurrence has not
// happened yet and must stay unwritten, so it keeps being served from the rule.
func TestOccurrencesLaterTodayAreNotMaterialisedYet(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	seedChore(t, st, database, "Ліки", "FREQ=DAILY;BYHOUR=8,20;BYMINUTE=0;BYSECOND=0", "2026-09-05T08:00")

	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	got := storedDue(t, st, "2026-09-05T00:00", "2026-09-05T23:59")
	if len(got) != 1 || got[0] != "2026-09-05T08:00" {
		t.Fatalf("got %v, want only this morning's 08:00", got)
	}
}

// A pass runs every minute over a rolling window, so it re-visits instants it
// already wrote. Running it repeatedly must change nothing.
func TestRepeatedPassesAreIdempotent(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-01T08:00")

	for range 3 {
		if err := s.Materialise(now); err != nil {
			t.Fatalf("materialise: %v", err)
		}
	}
	if got := storedDue(t, st, "2026-09-01T00:00", "2026-09-30T23:59"); len(got) != 5 {
		t.Fatalf("got %d rows after three passes, want 5: %v", len(got), got)
	}
}

// The self-healing half: five days of downtime are caught up on the next tick,
// with no watermark and nothing to reset by hand.
func TestDowntimeIsCaughtUpOnTheNextPass(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	s, st, database := storeService(t, time.Date(2026, 9, 1, 12, 0, 0, 0, loc))
	seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-01T08:00")

	if err := s.Materialise(time.Date(2026, 9, 1, 12, 0, 0, 0, loc)); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if got := storedDue(t, st, "2026-09-01T00:00", "2026-09-30T23:59"); len(got) != 1 {
		t.Fatalf("after the first pass: %v", got)
	}

	// ... the container is down until the 6th.
	if err := s.Materialise(time.Date(2026, 9, 6, 12, 0, 0, 0, loc)); err != nil {
		t.Fatalf("catch-up pass: %v", err)
	}
	if got := storedDue(t, st, "2026-09-01T00:00", "2026-09-30T23:59"); len(got) != 6 {
		t.Fatalf("got %d rows after catch-up, want 6: %v", len(got), got)
	}
}

// Anything older than the rolling window stays unwritten — the documented
// limit of self-healing.
func TestNothingOlderThanTheBackfillWindowIsWritten(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-01-01T08:00")

	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	got := storedDue(t, st, "2020-01-01T00:00", "2026-12-31T23:59")
	if len(got) == 0 {
		t.Fatal("nothing was written at all")
	}
	oldest, err := time.ParseInLocation(model.LocalDatetime, got[0], loc)
	if err != nil {
		t.Fatalf("parse oldest: %v", err)
	}
	if oldest.Before(now.Add(-BackfillWindow)) {
		t.Fatalf("oldest row %s predates the %v window", got[0], BackfillWindow)
	}
}

// Resuming a chore paused for a month must not invent a month of "you forgot"
// rows covering exactly the time it was deliberately off.
func TestResumingAPausedChoreDoesNotBackfillThePause(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	r := seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-08-01T08:00")

	if err := st.SetReminderActive(r.ID, false, "2026-09-01T12:00"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	// Resumed on the 18th, after being off since the start of the month.
	// Seeded directly: the API only ever stamps "now", and the point here is a
	// resume that happened days ago.
	if _, err := database.Exec(
		`UPDATE reminders SET active = 1, active_since = ? WHERE id = ?`,
		"2026-09-18T09:00", r.ID); err != nil {
		t.Fatalf("seed resume: %v", err)
	}

	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	got := storedDue(t, st, "2026-08-01T00:00", "2026-09-30T23:59")
	want := []string{"2026-09-19T08:00", "2026-09-20T08:00"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want only what followed the resume: %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestPausedAndDeletedChoresProduceNothing(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	paused := seedChore(t, st, database, "Пауза", "FREQ=DAILY", "2026-09-01T08:00")
	deleted := seedChore(t, st, database, "Видалене", "FREQ=DAILY", "2026-09-01T09:00")
	live := seedChore(t, st, database, "Живе", "FREQ=DAILY", "2026-09-01T10:00")

	if err := st.SetReminderActive(paused.ID, false, "2026-09-01T12:00"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := st.SoftDeleteReminder(deleted.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}

	occ, err := st.OccurrencesIn("2026-09-01T00:00", "2026-09-30T23:59")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, o := range occ {
		if o.ReminderID != live.ID {
			t.Fatalf("reminder %d produced occurrences it should not have", o.ReminderID)
		}
	}
	if len(occ) == 0 {
		t.Fatal("the live reminder produced nothing")
	}
}

// A single unparseable rule must not freeze the history of every other chore.
func TestOneBrokenRuleDoesNotStopTheRest(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	broken := seedChore(t, st, database, "Зламане", "FREQ=NONSENSE", "2026-09-01T08:00")
	good := seedChore(t, st, database, "Ціле", "FREQ=DAILY", "2026-09-01T09:00")

	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise returned an error instead of skipping the broken rule: %v", err)
	}
	occ, _ := st.OccurrencesIn("2026-09-01T00:00", "2026-09-30T23:59")
	if len(occ) == 0 {
		t.Fatal("the intact reminder produced nothing")
	}
	for _, o := range occ {
		if o.ReminderID == broken.ID {
			t.Fatal("the broken reminder produced occurrences")
		}
		if o.ReminderID != good.ID {
			t.Fatalf("unexpected reminder %d", o.ReminderID)
		}
	}
}

// The whole reason the materialiser is not hung off RunDigests: a pass must
// not reopen something a person already closed.
func TestAPassNeverReopensAClosedOccurrence(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	r := seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-01T08:00")

	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	rules, _ := st.RulesFor(r.ID)
	if err := st.MarkOccurrence(r.ID, rules[0].ID, "2026-09-03T08:00", model.OccDone, "Олег"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := s.Materialise(now.Add(time.Minute)); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	got, err := st.GetOccurrence(r.ID, "2026-09-03T08:00")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != model.OccDone {
		t.Fatalf("status = %q after a later pass, want it to stay done", got.Status)
	}
}

// A pass runs before the first tick, so a restart closes the downtime gap
// straight away instead of waiting a minute.
func TestTheMaterialiserRunsAPassBeforeItsFirstTick(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-01T08:00")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.RunMaterialiser(ctx); close(done) }()

	// The immediate pass is synchronous inside RunMaterialiser, but the
	// goroutine still has to get there; poll rather than sleep a fixed amount.
	var got []string
	for range 200 {
		if got = storedDue(t, st, "2026-09-01T00:00", "2026-09-30T23:59"); len(got) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if len(got) != 5 {
		t.Fatalf("got %d rows from the start-up pass, want 5: %v", len(got), got)
	}
}

func TestMaterialiseWithNoRemindersIsANoOp(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)
	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
}

// The materialiser must not depend on the bot, a notify chat, or
// NOTIFICATIONS_ENABLED. Hanging it off RunDigests would have put the record
// of what came due behind a flag for optional messages — switch the digests
// off and the history silently stops being written.
//
// The invariant is mostly held by the types: Service takes a store, a zone, a
// logger and a clock, and has nowhere to put a notifier. This pins it, so a
// future refactor that reaches for one has to delete a test that says why not.
func TestTheMaterialiserNeedsNothingFromTheBot(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(database)
	seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-01T08:00")

	// Everything the constructor needs, and nothing bot-shaped in sight.
	svc := NewService(st, loc, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return now })
	if err := svc.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	if got := storedDue(t, st, "2026-09-01T00:00", "2026-09-30T23:59"); len(got) != 5 {
		t.Fatalf("got %d rows with no bot configured, want 5: %v", len(got), got)
	}
}
