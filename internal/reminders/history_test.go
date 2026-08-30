package reminders

import (
	"testing"
	"time"

	"familyhub/internal/model"
)

// seedRunningChore makes a chore that has been going for a while, with the
// materialiser pass the ticker would have run. A chore created just now has no
// past on purpose, so nothing about a record can be tested from one.
func seedRunningChore(t *testing.T, s *Service, title, rrule string, dtstart time.Time) int64 {
	t.Helper()
	rem, err := s.store.CreateReminder(
		model.Reminder{
			Title: title, Person: "Оксана", DurationMin: 15, Active: true,
			ActiveSince: dtstart.Add(-time.Hour).Format(model.LocalDatetime),
		},
		model.ReminderRule{
			ValidFromAt: dtstart.Add(-time.Hour).Format(model.LocalDatetime),
			DTStart:     dtstart.Format(model.LocalDatetime),
			RRule:       rrule,
		})
	if err != nil {
		t.Fatalf("seed %q: %v", title, err)
	}
	return rem.ID
}

// The question the screen exists to answer: how often did I forget this.
func TestHistoryCountsWhatHappenedToEachChore(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)

	// Daily at 08:00 from the 1st: nine mornings have come and gone by noon
	// on the 10th, and the 10th's own 08:00 makes ten.
	id := seedRunningChore(t, s, "Кешбек", "FREQ=DAILY", time.Date(2026, 9, 1, 8, 0, 0, 0, loc))
	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	// Answer three of them, leave the rest open.
	for _, d := range []int{1, 3, 5} {
		if err := s.Mark(id, time.Date(2026, 9, d, 8, 0, 0, 0, loc), model.OccDone, "Оксана"); err != nil {
			t.Fatalf("mark %d: %v", d, err)
		}
	}
	if err := s.Mark(id, time.Date(2026, 9, 7, 8, 0, 0, 0, loc), model.OccSkipped, "Олег"); err != nil {
		t.Fatalf("skip: %v", err)
	}

	h, err := s.History(time.Date(2026, 9, 1, 0, 0, 0, 0, loc), now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(h.Chores) != 1 {
		t.Fatalf("got %d chores, want 1", len(h.Chores))
	}
	got := h.Chores[0].Tally
	if got.Done != 3 {
		t.Errorf("done = %d, want 3", got.Done)
	}
	if got.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", got.Skipped)
	}
	if got.Missed != 6 {
		t.Errorf("missed = %d, want the six nobody answered", got.Missed)
	}
	if got.Settled() != 10 {
		t.Errorf("settled = %d, want ten mornings", got.Settled())
	}
}

// Yesterday's open chore is "you forgot"; tonight's is "not yet". Counting
// them together turns the screen into a shaming wall.
func TestWhatHasNotComeDueIsNotAMiss(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)
	seedRunningChore(t, s, "Ліки", "FREQ=DAILY;BYHOUR=8,20;BYMINUTE=0;BYSECOND=0",
		time.Date(2026, 9, 10, 8, 0, 0, 0, loc))
	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}

	// The whole of the 10th: 08:00 has passed, 20:00 has not.
	h, err := s.History(time.Date(2026, 9, 10, 0, 0, 0, 0, loc),
		time.Date(2026, 9, 10, 23, 59, 0, 0, loc))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(h.Chores) != 1 {
		t.Fatalf("got %d chores", len(h.Chores))
	}
	got := h.Chores[0].Tally
	if got.Missed != 1 {
		t.Errorf("missed = %d, want only this morning's", got.Missed)
	}
	if got.Waiting != 1 {
		t.Errorf("waiting = %d, want tonight's", got.Waiting)
	}
	// And the rate is measured against what came due, not against everything.
	if got.MissRate() != 1 {
		t.Errorf("miss rate = %v, want 1 — one came due and was not answered", got.MissRate())
	}
}

// The ordering is the point of the overview: worst first.
func TestTheWorstKeptChoreComesFirst(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)

	start := time.Date(2026, 9, 1, 8, 0, 0, 0, loc)
	kept := seedRunningChore(t, s, "Кактус", "FREQ=DAILY", start)
	seedRunningChore(t, s, "Кешбек", "FREQ=DAILY", start)
	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	// Кактус is answered every day; Кешбек never is.
	for d := 1; d <= 10; d++ {
		if err := s.Mark(kept, time.Date(2026, 9, d, 8, 0, 0, 0, loc), model.OccDone, "Оксана"); err != nil {
			t.Fatalf("mark: %v", err)
		}
	}

	h, err := s.History(time.Date(2026, 9, 1, 0, 0, 0, 0, loc), now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(h.Chores) != 2 {
		t.Fatalf("got %d chores, want 2", len(h.Chores))
	}
	if h.Chores[0].Reminder.Title != "Кешбек" {
		t.Fatalf("first is %q, want the one that keeps getting forgotten",
			h.Chores[0].Reminder.Title)
	}
	if h.Chores[1].Tally.Missed != 0 {
		t.Errorf("the kept chore has %d misses", h.Chores[1].Tally.Missed)
	}
}

// The 30-day floor is not something a screen should have to rediscover: before
// it, the absence of a row means nothing was recorded, not that nothing
// happened.
func TestAPeriodReachingPastTheFloorSaysSo(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)

	short, err := s.History(now.Add(-7*24*time.Hour), now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if short.Truncated {
		t.Error("a week back is inside the window and was reported as truncated")
	}
	long, err := s.History(now.AddDate(0, -3, 0), now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if !long.Truncated {
		t.Error("a quarter reaches past the floor and was not reported as truncated")
	}
	if !long.Floor.Equal(now.Add(-BackfillWindow)) {
		t.Errorf("floor = %s, want now minus the backfill window", long.Floor)
	}
}

// A rule that moved underneath the numbers has to be named, or a period with
// legitimately fewer rows reads as data loss.
func TestARuleChangeInsideThePeriodIsReported(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)

	id := seedRunningChore(t, s, "Кешбек", "FREQ=WEEKLY", time.Date(2026, 9, 1, 8, 0, 0, 0, loc))
	if _, err := s.AddRule(id, "FREQ=DAILY", time.Date(2026, 9, 10, 8, 0, 0, 0, loc),
		time.Date(2026, 9, 10, 0, 0, 0, 0, loc)); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}

	h, err := s.History(time.Date(2026, 9, 1, 0, 0, 0, 0, loc), now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(h.Chores) != 1 {
		t.Fatalf("got %d chores", len(h.Chores))
	}
	changes := h.Chores[0].RuleChanges
	if len(changes) != 1 {
		t.Fatalf("got %d rule changes, want 1: %+v", len(changes), changes)
	}
	if changes[0].Text != "щодня" {
		t.Errorf("change text = %q, want the new rule in words", changes[0].Text)
	}
	if changes[0].At.Day() != 10 {
		t.Errorf("change at %s, want the 10th", changes[0].At)
	}
	// The rule shown for the chore is the one in force at the end of the
	// period, not the one it started with.
	if h.Chores[0].RuleText != "щодня" {
		t.Errorf("rule text = %q", h.Chores[0].RuleText)
	}
}

// A chore's first version is not a change — there was no schedule before it.
func TestTheFirstVersionIsNotAChange(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)
	seedRunningChore(t, s, "Кактус", "FREQ=DAILY", time.Date(2026, 9, 1, 8, 0, 0, 0, loc))
	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}

	h, err := s.History(time.Date(2026, 9, 1, 0, 0, 0, 0, loc), now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if n := len(h.Chores[0].RuleChanges); n != 0 {
		t.Fatalf("got %d rule changes for a chore that never changed", n)
	}
}

// A chore with nothing in the period is left out. A screen padded with chores
// that had nothing to do is one nobody reads twice.
func TestAChoreWithNothingInThePeriodIsNotListed(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)
	seedRunningChore(t, s, "Щорічне", "FREQ=YEARLY", time.Date(2026, 1, 1, 8, 0, 0, 0, loc))
	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}

	h, err := s.History(time.Date(2026, 9, 1, 0, 0, 0, 0, loc), now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(h.Chores) != 0 {
		t.Fatalf("got %+v, want nothing", h.Chores)
	}
	if h.Totals().Total() != 0 {
		t.Errorf("totals = %+v", h.Totals())
	}
}

// The drill-down names the chore even when the period holds nothing, so the
// screen says "nothing here" about a chore rather than about nothing.
func TestTheDrillDownNamesAChoreWithAnEmptyPeriod(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)
	id := seedRunningChore(t, s, "Щорічне", "FREQ=YEARLY", time.Date(2026, 1, 1, 8, 0, 0, 0, loc))

	c, err := s.ChoreHistoryFor(id, time.Date(2026, 9, 1, 0, 0, 0, 0, loc), now)
	if err != nil {
		t.Fatalf("ChoreHistoryFor: %v", err)
	}
	if c.Reminder.Title != "Щорічне" {
		t.Fatalf("chore = %+v", c.Reminder)
	}
	if len(c.Occurrences) != 0 {
		t.Fatalf("got %d occurrences", len(c.Occurrences))
	}
	if c.RuleText != "щороку" {
		t.Fatalf("rule text = %q", c.RuleText)
	}
}

// The drill-down carries the occurrences themselves — the per-moment record is
// the whole reason they are stored.
func TestTheDrillDownCarriesEveryOccurrence(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)
	id := seedRunningChore(t, s, "Кешбек", "FREQ=DAILY", time.Date(2026, 9, 1, 8, 0, 0, 0, loc))
	if err := s.Materialise(now); err != nil {
		t.Fatalf("materialise: %v", err)
	}
	if err := s.Mark(id, time.Date(2026, 9, 2, 8, 0, 0, 0, loc), model.OccDone, "Оксана"); err != nil {
		t.Fatalf("mark: %v", err)
	}

	c, err := s.ChoreHistoryFor(id, time.Date(2026, 9, 1, 0, 0, 0, 0, loc), now)
	if err != nil {
		t.Fatalf("ChoreHistoryFor: %v", err)
	}
	if len(c.Occurrences) != 5 {
		t.Fatalf("got %d occurrences, want the 1st through the 5th", len(c.Occurrences))
	}
	// Oldest first, so the ledger reads down the page.
	for i := 1; i < len(c.Occurrences); i++ {
		if c.Occurrences[i].Due.Before(c.Occurrences[i-1].Due) {
			t.Fatalf("occurrences are not in order: %+v", c.Occurrences)
		}
	}
	if c.Occurrences[1].Status != model.OccDone || c.Occurrences[1].DoneBy != "Оксана" {
		t.Errorf("the answered one lost its answer: %+v", c.Occurrences[1])
	}
}

// An inverted period is nothing, not an error.
func TestAnInvertedPeriodIsEmpty(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Kyiv")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	s, _, _ := storeService(t, now)
	h, err := s.History(now, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(h.Chores) != 0 {
		t.Fatalf("got %d chores", len(h.Chores))
	}
}
