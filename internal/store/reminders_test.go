package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// reminderStore hands back the raw handle as well, so a test can seed states
// the API deliberately cannot produce — an old active_since, a stale row.
func reminderStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(database), database
}

func seedReminder(t *testing.T, st *store.Store, title, rrule string) model.Reminder {
	t.Helper()
	r, err := st.CreateReminder(
		model.Reminder{Title: title, Person: "Олег", Active: true},
		model.ReminderRule{
			ValidFromAt: "2026-01-01T00:00",
			DTStart:     "2026-08-01T08:00",
			RRule:       rrule,
		})
	if err != nil {
		t.Fatalf("create reminder: %v", err)
	}
	return r
}

// materialise writes occurrences the way a real pass does — one transaction,
// DO NOTHING on conflict — so these tests exercise the code production runs
// rather than a single-row variant kept alive only for them.
func materialise(t *testing.T, st *store.Store, reminderID, ruleID int64, dueAt ...string) {
	t.Helper()
	rows := make([]model.ReminderOccurrence, 0, len(dueAt))
	for _, d := range dueAt {
		rows = append(rows, model.ReminderOccurrence{ReminderID: reminderID, RuleID: ruleID, DueAt: d})
	}
	if err := st.MaterialiseOccurrences(rows); err != nil {
		t.Fatalf("materialise %v: %v", dueAt, err)
	}
}

func firstRule(t *testing.T, st *store.Store, reminderID int64) model.ReminderRule {
	t.Helper()
	rules, err := st.RulesFor(reminderID)
	if err != nil {
		t.Fatalf("rules for: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("reminder has no rule versions")
	}
	return rules[0]
}

// A reminder and its first rule go in together: one without the other is a
// chore that can never happen.
func TestCreatingAReminderStoresItsFirstRule(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1")

	if r.ID == 0 || r.Title != "Кешбек" || !r.Active {
		t.Fatalf("stored reminder = %+v", r)
	}
	if r.DurationMin != 15 {
		t.Fatalf("duration = %d, want the 15-minute default", r.DurationMin)
	}
	rule := firstRule(t, st, r.ID)
	if rule.RRule != "FREQ=MONTHLY;BYMONTHDAY=1" || rule.DTStart != "2026-08-01T08:00" {
		t.Fatalf("stored rule = %+v", rule)
	}
}

// The transaction has to roll the reminder back too, or a rejected rule
// leaves behind a chore that can never be expanded.
func TestAReminderWhoseRuleIsRejectedIsNotCreated(t *testing.T) {
	st, database := reminderStore(t)

	_, err := st.CreateReminder(
		model.Reminder{Title: "Зламане", Active: true},
		model.ReminderRule{ValidFromAt: "2026-01-01T00:00", DTStart: "2026-01-01T08:00", RRule: ""})
	if err == nil {
		t.Fatal("an empty rule body was accepted")
	}

	var n int
	if err := database.QueryRow(`SELECT count(*) FROM reminders`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d reminders survived the rolled-back transaction", n)
	}
}

// An empty title is the same class of broken row: nothing to show in the
// calendar or the nag.
func TestAnEmptyTitleIsRefused(t *testing.T) {
	st, _ := reminderStore(t)
	_, err := st.CreateReminder(
		model.Reminder{Title: "", Active: true},
		model.ReminderRule{ValidFromAt: "2026-01-01T00:00", DTStart: "2026-01-01T08:00", RRule: "FREQ=DAILY"})
	if err == nil {
		t.Fatal("an empty title was accepted")
	}
}

func TestUpdateRewritesTheChoreButNotTheSwitch(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1")

	r.Title = "Кешбек у банку"
	r.Person = "Оксана"
	r.DurationMin = 30
	r.Note = "обидві картки"
	if err := st.UpdateReminder(r); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := st.GetReminder(r.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Кешбек у банку" || got.Person != "Оксана" || got.DurationMin != 30 || got.Note != "обидві картки" {
		t.Fatalf("after update = %+v", got)
	}
	if !got.Active {
		t.Fatal("update turned the reminder off")
	}
}

// active_since is the floor for the catch-up backfill. If resuming a paused
// chore did not move it, the next tick would invent a month of "you forgot"
// rows for the time it was deliberately off.
func TestResumingAReminderMovesItsBackfillFloor(t *testing.T) {
	st, database := reminderStore(t)
	r := seedReminder(t, st, "Кактус", "FREQ=WEEKLY;INTERVAL=2;BYDAY=SA")

	if _, err := database.Exec(
		`UPDATE reminders SET active = 0, active_since = '2020-01-01T00:00' WHERE id = ?`,
		r.ID); err != nil {
		t.Fatalf("seed paused: %v", err)
	}

	if err := st.SetReminderActive(r.ID, true, "2026-09-01T12:00"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, err := st.GetReminder(r.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Active {
		t.Fatal("reminder is still off")
	}
	if got.ActiveSince == "2020-01-01T00:00" {
		t.Fatal("active_since was not moved on resume")
	}
}

// Pausing must not move the floor: it is only ever read while a reminder is on.
func TestPausingLeavesTheBackfillFloorAlone(t *testing.T) {
	st, database := reminderStore(t)
	r := seedReminder(t, st, "Кактус", "FREQ=DAILY")

	if _, err := database.Exec(
		`UPDATE reminders SET active_since = '2026-05-05T05:05' WHERE id = ?`, r.ID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.SetReminderActive(r.ID, false, "2026-09-01T12:00"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	got, _ := st.GetReminder(r.ID)
	if got.ActiveSince != "2026-05-05T05:05" {
		t.Fatalf("active_since = %q, want it untouched", got.ActiveSince)
	}
}

func TestSoftDeletedRemindersLeaveTheLists(t *testing.T) {
	st, _ := reminderStore(t)
	keep := seedReminder(t, st, "Лишається", "FREQ=DAILY")
	drop := seedReminder(t, st, "Зникає", "FREQ=DAILY")

	if err := st.SoftDeleteReminder(drop.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetReminder(drop.ID); !store.IsNotFound(err) {
		t.Fatalf("a deleted reminder is still gettable: %v", err)
	}
	all, err := st.Reminders()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 || all[0].ID != keep.ID {
		t.Fatalf("list after delete = %+v", all)
	}
	active, err := st.ActiveReminders()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 1 || active[0].ID != keep.ID {
		t.Fatalf("active after delete = %+v", active)
	}
}

// The list keeps paused chores so they can be switched back on; the
// materialiser must not see them.
func TestPausedRemindersStayListedButAreNotMaterialised(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "На паузі", "FREQ=DAILY")
	if err := st.SetReminderActive(r.ID, false, "2026-09-01T12:00"); err != nil {
		t.Fatalf("pause: %v", err)
	}

	all, _ := st.Reminders()
	if len(all) != 1 {
		t.Fatalf("paused reminder left the list: %+v", all)
	}
	active, _ := st.ActiveReminders()
	if len(active) != 0 {
		t.Fatalf("paused reminder is still active: %+v", active)
	}
}

// Order is the expander's contract: it walks versions in sequence to cut a
// window into per-version segments.
func TestRuleVersionsComeBackOldestFirst(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1")

	for _, from := range []string{"2026-11-01T00:00", "2026-09-15T00:00"} {
		if _, err := st.AddRule(model.ReminderRule{
			ReminderID: r.ID, ValidFromAt: from,
			DTStart: "2026-08-01T08:00", RRule: "FREQ=MONTHLY;BYMONTHDAY=5",
		}); err != nil {
			t.Fatalf("add rule %s: %v", from, err)
		}
	}

	rules, err := st.RulesFor(r.ID)
	if err != nil {
		t.Fatalf("rules: %v", err)
	}
	want := []string{"2026-01-01T00:00", "2026-09-15T00:00", "2026-11-01T00:00"}
	if len(rules) != len(want) {
		t.Fatalf("got %d versions, want %d", len(rules), len(want))
	}
	for i, w := range want {
		if rules[i].ValidFromAt != w {
			t.Fatalf("version %d starts %q, want %q", i, rules[i].ValidFromAt, w)
		}
	}
}

func TestRulesForAllBatchesAndKeepsOrdering(t *testing.T) {
	st, _ := reminderStore(t)
	a := seedReminder(t, st, "A", "FREQ=DAILY")
	b := seedReminder(t, st, "B", "FREQ=WEEKLY")
	if _, err := st.AddRule(model.ReminderRule{
		ReminderID: a.ID, ValidFromAt: "2026-06-01T00:00",
		DTStart: "2026-06-01T07:00", RRule: "FREQ=DAILY;INTERVAL=2",
	}); err != nil {
		t.Fatalf("add rule: %v", err)
	}

	got, err := st.RulesForAll([]int64{a.ID, b.ID})
	if err != nil {
		t.Fatalf("rules for all: %v", err)
	}
	if len(got[a.ID]) != 2 || len(got[b.ID]) != 1 {
		t.Fatalf("got %d/%d versions, want 2/1", len(got[a.ID]), len(got[b.ID]))
	}
	if got[a.ID][0].ValidFromAt > got[a.ID][1].ValidFromAt {
		t.Fatalf("versions out of order: %+v", got[a.ID])
	}
}

func TestRulesForAllHandlesAnEmptyIdList(t *testing.T) {
	st, _ := reminderStore(t)
	got, err := st.RulesForAll(nil)
	if err != nil {
		t.Fatalf("rules for all: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d entries for no ids", len(got))
	}
}

// Amending corrects the record ("it was always the 5th"); adding a version
// changes things going forward. Only the former touches an existing row.
func TestAmendRewritesTheVersionInPlace(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1")
	rule := firstRule(t, st, r.ID)

	rule.RRule = "FREQ=MONTHLY;BYMONTHDAY=5"
	if err := st.AmendRule(rule); err != nil {
		t.Fatalf("amend: %v", err)
	}

	rules, _ := st.RulesFor(r.ID)
	if len(rules) != 1 {
		t.Fatalf("amend created a version: %d total", len(rules))
	}
	if rules[0].RRule != "FREQ=MONTHLY;BYMONTHDAY=5" {
		t.Fatalf("rule = %q", rules[0].RRule)
	}
}

// The materialiser re-visits a rolling window every minute. If its insert
// updated on conflict, every closed chore would flip back to pending on the
// next tick.
func TestMaterialisingNeverReopensAClosedOccurrence(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1")
	rule := firstRule(t, st, r.ID)
	const due = "2026-09-01T08:00"

	materialise(t, st, r.ID, rule.ID, due)
	if err := st.MarkOccurrence(r.ID, rule.ID, due, model.OccDone, "Олег"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	// The next tick comes round again.
	materialise(t, st, r.ID, rule.ID, due)

	got, err := st.GetOccurrence(r.ID, due)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != model.OccDone {
		t.Fatalf("status = %q after a re-tick, want it to stay done", got.Status)
	}
	if got.DoneBy != "Олег" {
		t.Fatalf("done_by = %q, want it preserved", got.DoneBy)
	}
}

func TestMaterialisingTwiceStoresOneRow(t *testing.T) {
	st, database := reminderStore(t)
	r := seedReminder(t, st, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1")
	rule := firstRule(t, st, r.ID)

	for range 3 {
		materialise(t, st, r.ID, rule.ID, "2026-09-01T08:00")
	}
	var n int
	if err := database.QueryRow(`SELECT count(*) FROM reminder_occurrences`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("got %d rows, want 1", n)
	}
}

// A full RRULE can put two instants on one date; they are two chores, not one.
func TestTwoInstantsOnOneDateStayApart(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Ліки", "FREQ=DAILY;BYHOUR=8,20")
	rule := firstRule(t, st, r.ID)

	for _, due := range []string{"2026-09-01T08:00", "2026-09-01T20:00"} {
		materialise(t, st, r.ID, rule.ID, due)
	}
	occ, err := st.OccurrencesIn("2026-09-01T00:00", "2026-09-01T23:59")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(occ) != 2 {
		t.Fatalf("got %d occurrences on one date, want 2", len(occ))
	}
}

// Marking has to work before the ticker has written the row — a person can tap
// the moment the reminder comes due.
func TestMarkingCreatesTheRowWhenTheTickerHasNotYet(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Пробіг", "FREQ=MONTHLY;BYMONTHDAY=1")
	rule := firstRule(t, st, r.ID)
	const due = "2026-09-01T09:00"

	if err := st.MarkOccurrence(r.ID, rule.ID, due, model.OccDone, "Оксана"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, err := st.GetOccurrence(r.ID, due)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != model.OccDone || got.DoneBy != "Оксана" {
		t.Fatalf("occurrence = %+v", got)
	}
}

// Changing one's mind has to take effect — unlike the materialiser, a person's
// decision overwrites.
func TestMarkingAgainOverwritesTheDecision(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Кактус", "FREQ=DAILY")
	rule := firstRule(t, st, r.ID)
	const due = "2026-09-01T11:00"

	if err := st.MarkOccurrence(r.ID, rule.ID, due, model.OccDone, "Олег"); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if err := st.MarkOccurrence(r.ID, rule.ID, due, model.OccSkipped, "Оксана"); err != nil {
		t.Fatalf("mark skipped: %v", err)
	}
	got, _ := st.GetOccurrence(r.ID, due)
	if got.Status != model.OccSkipped || got.DoneBy != "Оксана" {
		t.Fatalf("occurrence = %+v", got)
	}
}

func TestMarkingWithAnUnknownStatusIsRefused(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Кактус", "FREQ=DAILY")
	rule := firstRule(t, st, r.ID)

	if err := st.MarkOccurrence(r.ID, rule.ID, "2026-09-01T11:00", "forgotten", "Олег"); err == nil {
		t.Fatal("an unknown status was accepted")
	}
}

// The nag asks for exactly the open ones; done and skipped both silence it.
func TestOnlyPendingOccurrencesReachTheNag(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Кешбек", "FREQ=DAILY")
	rule := firstRule(t, st, r.ID)

	open := "2026-09-01T08:00"
	done := "2026-09-02T08:00"
	skipped := "2026-09-03T08:00"
	for _, d := range []string{open, done, skipped} {
		materialise(t, st, r.ID, rule.ID, d)
	}
	if err := st.MarkOccurrence(r.ID, rule.ID, done, model.OccDone, "Олег"); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if err := st.MarkOccurrence(r.ID, rule.ID, skipped, model.OccSkipped, "Олег"); err != nil {
		t.Fatalf("mark skipped: %v", err)
	}

	pending, err := st.PendingOccurrencesIn("2026-09-01T00:00", "2026-09-30T23:59")
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0].DueAt != open {
		t.Fatalf("pending = %+v, want only %s", pending, open)
	}
}

// Occurrences are read with their reminder joined in, so no caller needs a
// second round trip just to name the thing.
func TestOccurrencesCarryTheirReminderDetails(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Кешбек", "FREQ=DAILY")
	rule := firstRule(t, st, r.ID)
	materialise(t, st, r.ID, rule.ID, "2026-09-01T08:00")

	occ, _ := st.OccurrencesIn("2026-09-01T00:00", "2026-09-01T23:59")
	if len(occ) != 1 {
		t.Fatalf("got %d occurrences", len(occ))
	}
	if occ[0].Title != "Кешбек" || occ[0].Person != "Олег" || occ[0].DurationMin != 15 {
		t.Fatalf("joined fields missing: %+v", occ[0])
	}
}

// A deleted chore must stop showing up in the calendar and the nag, while its
// rows stay on disk for history.
func TestOccurrencesOfADeletedReminderAreHidden(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Зникає", "FREQ=DAILY")
	rule := firstRule(t, st, r.ID)
	materialise(t, st, r.ID, rule.ID, "2026-09-01T08:00")
	if err := st.SoftDeleteReminder(r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	occ, _ := st.OccurrencesIn("2026-09-01T00:00", "2026-09-01T23:59")
	if len(occ) != 0 {
		t.Fatalf("deleted reminder still shows %d occurrences", len(occ))
	}
	pending, _ := st.PendingOccurrencesIn("2026-09-01T00:00", "2026-09-01T23:59")
	if len(pending) != 0 {
		t.Fatalf("deleted reminder still nags: %+v", pending)
	}
}

func TestMissingOccurrenceIsReportedAsNotFound(t *testing.T) {
	st, _ := reminderStore(t)
	r := seedReminder(t, st, "Кешбек", "FREQ=DAILY")
	_, err := st.GetOccurrence(r.ID, "2099-01-01T08:00")
	if !store.IsNotFound(err) {
		t.Fatalf("err = %v, want a not-found", err)
	}
}
