package reminders

import (
	"errors"
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

func upcomingDue(t *testing.T, s *Service, from, to time.Time) []Occurrence {
	t.Helper()
	occ, err := s.Upcoming(from, to)
	if err != nil {
		t.Fatalf("upcoming: %v", err)
	}
	return occ
}

func labels(occ []Occurrence) []string {
	out := make([]string, len(occ))
	for i, o := range occ {
		kind := "projected"
		if o.Stored {
			kind = "stored"
		}
		out[i] = o.Due.Format("2006-01-02 15:04") + " " + o.Status + " " + kind
	}
	return out
}

// The bug the review caught: splitting the timeline by DATE would drop a chore
// due later today, because "today" was read from rows and its row does not
// exist yet. The split is by instant.
func TestAChoreDueLaterTodayIsStillOnTheTimeline(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	seedChore(t, st, database, "Ліки", "FREQ=DAILY;BYHOUR=8,20;BYMINUTE=0;BYSECOND=0", "2026-09-05T08:00")

	occ := upcomingDue(t, s,
		time.Date(2026, 9, 5, 0, 0, 0, 0, loc), time.Date(2026, 9, 5, 23, 59, 0, 0, loc))

	got := labels(occ)
	want := []string{
		"2026-09-05 08:00 pending stored",    // already came due, has a row
		"2026-09-05 20:00 pending projected", // still to come, from the rule
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

// Nothing may be both stored and projected: the boundary instant belongs to
// the stored half, and projecting it too would double every chore that minute.
func TestTheBoundaryInstantIsNotCountedTwice(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 8, 0, 0, 0, loc) // exactly on an occurrence
	s, st, database := storeService(t, now)
	seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-05T08:00")

	occ := upcomingDue(t, s,
		time.Date(2026, 9, 5, 0, 0, 0, 0, loc), time.Date(2026, 9, 6, 23, 59, 0, 0, loc))
	got := labels(occ)
	want := []string{
		"2026-09-05 08:00 pending stored",
		"2026-09-06 08:00 pending projected",
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

// A closed occurrence keeps its status on the timeline — the calendar marks it
// with a tick rather than dropping it.
func TestClosedOccurrencesKeepTheirStatusOnTheTimeline(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	r := seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-03T08:00")

	if err := s.Mark(r.ID, time.Date(2026, 9, 4, 8, 0, 0, 0, loc), model.OccDone, "Олег"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	occ := upcomingDue(t, s,
		time.Date(2026, 9, 4, 0, 0, 0, 0, loc), time.Date(2026, 9, 4, 23, 59, 0, 0, loc))
	if len(occ) != 1 || occ[0].Status != model.OccDone || occ[0].DoneBy != "Олег" {
		t.Fatalf("timeline = %+v", occ)
	}
}

// The scenario the whole design exists for: the cashback moves from the 1st to
// the 5th, and September must still read as the 1st, because that is when it
// actually came due.
func TestChangingTheRuleLeavesTheRecordedPastAlone(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	r := seedChore(t, st, database, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1", "2026-09-01T08:00")

	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	// From today the chore moves to the 5th.
	if _, err := s.AddRule(r.ID, "FREQ=MONTHLY;BYMONTHDAY=5",
		time.Date(2026, 10, 5, 8, 0, 0, 0, loc), now); err != nil {
		t.Fatalf("add rule: %v", err)
	}

	occ := upcomingDue(t, s,
		time.Date(2026, 9, 1, 0, 0, 0, 0, loc), time.Date(2026, 11, 30, 23, 59, 0, 0, loc))
	got := labels(occ)
	want := []string{
		"2026-09-01 08:00 pending stored",    // still the 1st, as it happened
		"2026-10-05 08:00 pending projected", // the new rule, going forward
		"2026-11-05 08:00 pending projected",
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

func TestUpcomingWithAnInvertedWindowYieldsNothing(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-01T08:00")

	occ := upcomingDue(t, s,
		time.Date(2026, 9, 30, 0, 0, 0, 0, loc), time.Date(2026, 9, 1, 0, 0, 0, 0, loc))
	if len(occ) != 0 {
		t.Fatalf("got %d occurrences for an inverted window", len(occ))
	}
}

// --- UnclosedOn ---

func TestUnclosedOnListsOnlyWhatNobodyClosed(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 21, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	a := seedChore(t, st, database, "Кешбек", "FREQ=DAILY", "2026-09-05T08:00")
	b := seedChore(t, st, database, "Пробіг", "FREQ=DAILY", "2026-09-05T09:00")
	c := seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-05T10:00")

	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	if err := s.Mark(b.ID, time.Date(2026, 9, 5, 9, 0, 0, 0, loc), model.OccDone, "Олег"); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if err := s.Mark(c.ID, time.Date(2026, 9, 5, 10, 0, 0, 0, loc), model.OccSkipped, "Олег"); err != nil {
		t.Fatalf("mark skipped: %v", err)
	}

	open, err := s.UnclosedOn(now)
	if err != nil {
		t.Fatalf("unclosed: %v", err)
	}
	if len(open) != 1 || open[0].ReminderID != a.ID {
		t.Fatalf("unclosed = %+v, want only the cashback", labels(open))
	}
}

func TestUnclosedOnIgnoresOtherDays(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 21, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-01T08:00")
	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}

	open, err := s.UnclosedOn(now)
	if err != nil {
		t.Fatalf("unclosed: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("got %d open items for one day, want 1: %v", len(open), labels(open))
	}
	if got := open[0].Due.Format("2006-01-02"); got != "2026-09-05" {
		t.Fatalf("open item is from %s", got)
	}
}

// --- Mark ---

// A row in the future would be invisible to the calendar, which projects that
// half from the rules, and orphaned the moment the rule changed.
func TestMarkingSomethingThatHasNotHappenedIsRefused(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	r := seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-01T08:00")

	err := s.Mark(r.ID, time.Date(2026, 9, 6, 8, 0, 0, 0, loc), model.OccDone, "Олег")
	if !errors.Is(err, ErrFutureMark) {
		t.Fatalf("err = %v, want ErrFutureMark", err)
	}
}

func TestMarkingAnInstantTheRulesNeverScheduledIsRefused(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	r := seedChore(t, st, database, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1", "2026-09-01T08:00")

	// Right day, wrong time.
	err := s.Mark(r.ID, time.Date(2026, 9, 1, 9, 0, 0, 0, loc), model.OccDone, "Олег")
	if !errors.Is(err, ErrNoSuchOccurrence) {
		t.Fatalf("err = %v, want ErrNoSuchOccurrence", err)
	}
}

// pending is a state the system writes, not a decision a person records.
func TestMarkingSomethingBackToPendingIsRefused(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	r := seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-01T08:00")

	if err := s.Mark(r.ID, time.Date(2026, 9, 4, 8, 0, 0, 0, loc), model.OccPending, "Олег"); err == nil {
		t.Fatal("pending was accepted as a decision")
	}
	if err := s.Mark(r.ID, time.Date(2026, 9, 4, 8, 0, 0, 0, loc), "forgotten", "Олег"); err == nil {
		t.Fatal("an unknown status was accepted")
	}
}

// A person can tap the moment a chore comes due, before the minute-ticker has
// written its row.
func TestMarkingWorksBeforeTheTickerHasWrittenTheRow(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 8, 0, 30, 0, loc) // thirty seconds after it came due
	s, st, database := storeService(t, now)
	r := seedChore(t, st, database, "Кактус", "FREQ=DAILY", "2026-09-05T08:00")

	if err := s.Mark(r.ID, time.Date(2026, 9, 5, 8, 0, 0, 0, loc), model.OccDone, "Оксана"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, err := st.GetOccurrence(r.ID, "2026-09-05T08:00")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != model.OccDone || got.DoneBy != "Оксана" {
		t.Fatalf("occurrence = %+v", got)
	}
}

func TestMarkingAnUnknownReminderIsNotFound(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)

	err := s.Mark(999, time.Date(2026, 9, 4, 8, 0, 0, 0, loc), model.OccDone, "Олег")
	if !store.IsNotFound(err) {
		t.Fatalf("err = %v, want a not-found", err)
	}
}

// --- Create / rules / preview ---

// The rule is checked by the same library that will expand it, so nothing
// storable can fail to expand later.
func TestCreateRefusesARuleItCouldNotExpand(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)

	if _, err := s.Create(model.Reminder{Title: "Зламане"}, "FREQ=NONSENSE",
		time.Date(2026, 9, 1, 8, 0, 0, 0, loc)); err == nil {
		t.Fatal("an unexpandable rule was stored")
	}
	if _, err := s.Create(model.Reminder{Title: "Порожнє"}, "",
		time.Date(2026, 9, 1, 8, 0, 0, 0, loc)); err == nil {
		t.Fatal("an empty rule was stored")
	}
}

func TestCreateStoresAnActiveChoreWithItsRule(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, _ := storeService(t, now)

	r, err := s.Create(model.Reminder{Title: "Кешбек", Person: "Олег"},
		"FREQ=MONTHLY;BYMONTHDAY=1", time.Date(2026, 9, 1, 8, 0, 0, 0, loc))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !r.Active {
		t.Fatal("a new chore is switched off")
	}
	rules, err := st.RulesFor(r.ID)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules = %+v err = %v", rules, err)
	}
	if rules[0].DTStart != "2026-09-01T08:00" {
		t.Fatalf("dtstart = %q", rules[0].DTStart)
	}
}

func TestAddRuleAndAmendRuleBothValidate(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, st, database := storeService(t, now)
	r := seedChore(t, st, database, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1", "2026-09-01T08:00")

	if _, err := s.AddRule(r.ID, "FREQ=NONSENSE",
		time.Date(2026, 10, 5, 8, 0, 0, 0, loc), now); err == nil {
		t.Fatal("AddRule stored an unexpandable rule")
	}
	rules, _ := st.RulesFor(r.ID)
	rules[0].RRule = "FREQ=NONSENSE"
	if err := s.AmendRule(rules[0]); err == nil {
		t.Fatal("AmendRule stored an unexpandable rule")
	}
}

func TestPreviewShowsTheNextInstantsWithoutStoringAnything(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)

	got, err := s.Preview("FREQ=MONTHLY;BYMONTHDAY=1", time.Date(2026, 8, 1, 8, 0, 0, 0, loc), 3)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	want := []string{"2026-10-01 08:00", "2026-11-01 08:00", "2026-12-01 08:00"}
	if len(got) != len(want) {
		t.Fatalf("got %d dates, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Format("2006-01-02 15:04") != want[i] {
			t.Fatalf("date %d = %s, want %s", i, got[i].Format("2006-01-02 15:04"), want[i])
		}
	}
}

func TestPreviewRefusesABrokenRule(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)
	if _, err := s.Preview("FREQ=NONSENSE", time.Date(2026, 8, 1, 8, 0, 0, 0, loc), 3); err == nil {
		t.Fatal("preview accepted a broken rule")
	}
}
