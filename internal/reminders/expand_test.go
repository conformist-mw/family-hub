package reminders

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"familyhub/internal/model"
)

func testService(t *testing.T) *Service {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatalf("load Europe/Kyiv: %v", err)
	}
	return &Service{
		loc: loc,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		now: func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, loc) },
	}
}

func moment(s *Service, y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, s.loc)
}

func rule(id int64, validFrom, dtstart, rrule string) model.ReminderRule {
	return model.ReminderRule{ID: id, ReminderID: 1, ValidFromAt: validFrom, DTStart: dtstart, RRule: rrule}
}

func dueStrings(occ []Occurrence) []string {
	out := make([]string, len(occ))
	for i, o := range occ {
		out[i] = o.Due.Format("2006-01-02 15:04")
	}
	return out
}

func assertDue(t *testing.T, occ []Occurrence, want ...string) {
	t.Helper()
	got := dueStrings(occ)
	if len(got) != len(want) {
		t.Fatalf("got %d occurrences %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("occurrence %d = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

var choreCashback = model.Reminder{ID: 1, Title: "Кешбек", Person: "Олег", DurationMin: 15}

// With one version the versioning must be invisible — same answer as a plain
// expansion.
func TestOneVersionBehavesLikeAPlainExpansion(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{
		rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=MONTHLY;BYMONTHDAY=1"),
	}
	occ, err := s.expandVersioned(choreCashback, rules,
		moment(s, 2026, time.August, 1, 0, 0), moment(s, 2026, time.October, 31, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDue(t, occ, "2026-08-01 08:00", "2026-09-01 08:00", "2026-10-01 08:00")
}

// The case the whole design exists for: the cashback moves from the 1st to the
// 5th in mid-September. August must still read as the 1st.
func TestEachStretchOfTheWindowUsesTheRuleInForceThen(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{
		rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=MONTHLY;BYMONTHDAY=1"),
		rule(11, "2026-09-15T00:00", "2026-09-05T08:00", "FREQ=MONTHLY;BYMONTHDAY=5"),
	}
	occ, err := s.expandVersioned(choreCashback, rules,
		moment(s, 2026, time.August, 1, 0, 0), moment(s, 2026, time.November, 30, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDue(t, occ,
		"2026-08-01 08:00", // old rule
		"2026-09-01 08:00", // still the old rule, the switch is on the 15th
		"2026-10-05 08:00", // new rule
		"2026-11-05 08:00")
}

// Each occurrence records which version produced it, so a row can explain
// itself long after the rule has moved on.
func TestOccurrencesCarryTheVersionThatProducedThem(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{
		rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=MONTHLY;BYMONTHDAY=1"),
		rule(11, "2026-09-15T00:00", "2026-09-05T08:00", "FREQ=MONTHLY;BYMONTHDAY=5"),
	}
	occ, _ := s.expandVersioned(choreCashback, rules,
		moment(s, 2026, time.August, 1, 0, 0), moment(s, 2026, time.October, 31, 23, 59))

	if len(occ) != 3 {
		t.Fatalf("got %d occurrences", len(occ))
	}
	if occ[0].RuleID != 10 || occ[1].RuleID != 10 {
		t.Fatalf("August/September attributed to %d/%d, want the old version", occ[0].RuleID, occ[1].RuleID)
	}
	if occ[2].RuleID != 11 {
		t.Fatalf("October attributed to %d, want the new version", occ[2].RuleID)
	}
}

// An occurrence landing exactly on the new version's starting instant belongs
// to the new version. Off by one here and a day gets counted twice or not at
// all.
func TestAnOccurrenceOnTheBoundaryBelongsToTheNewVersion(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{
		rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=DAILY"),
		rule(11, "2026-09-10T08:00", "2026-09-10T08:00", "FREQ=DAILY"),
	}
	occ, err := s.expandVersioned(choreCashback, rules,
		moment(s, 2026, time.September, 10, 0, 0), moment(s, 2026, time.September, 10, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDue(t, occ, "2026-09-10 08:00")
	if occ[0].RuleID != 11 {
		t.Fatalf("boundary occurrence attributed to %d, want the new version", occ[0].RuleID)
	}
}

// Changing the rule at 10:00 "from now" must not claim this morning's 08:00,
// which already happened under the old one. This is why valid_from_at is a
// datetime and not a date.
func TestAVersionStartingMiddayDoesNotClaimThisMorning(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{
		rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=DAILY"),
		rule(11, "2026-09-01T10:00", "2026-09-01T20:00", "FREQ=DAILY"),
	}
	occ, err := s.expandVersioned(choreCashback, rules,
		moment(s, 2026, time.September, 1, 0, 0), moment(s, 2026, time.September, 2, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDue(t, occ,
		"2026-09-01 08:00", // the old rule's morning, already past when the switch was made
		"2026-09-01 20:00", // the new rule takes over the same day
		"2026-09-02 20:00")
	if occ[0].RuleID != 10 {
		t.Fatalf("this morning attributed to %d, want the old version", occ[0].RuleID)
	}
}

func TestTheLastVersionRunsOnIndefinitely(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{
		rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=MONTHLY;BYMONTHDAY=1"),
		rule(11, "2026-02-01T00:00", "2026-02-05T08:00", "FREQ=MONTHLY;BYMONTHDAY=5"),
	}
	occ, err := s.expandVersioned(choreCashback, rules,
		moment(s, 2030, time.June, 1, 0, 0), moment(s, 2030, time.August, 31, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDue(t, occ, "2030-06-05 08:00", "2030-07-05 08:00", "2030-08-05 08:00")
}

// A version that ends before the window starts contributes nothing, and must
// not error or leak an empty segment into the results.
func TestVersionsOutsideTheWindowContributeNothing(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{
		rule(10, "2020-01-01T00:00", "2020-01-01T08:00", "FREQ=DAILY"),
		rule(11, "2026-01-01T00:00", "2026-01-01T09:00", "FREQ=MONTHLY;BYMONTHDAY=1"),
	}
	occ, err := s.expandVersioned(choreCashback, rules,
		moment(s, 2026, time.September, 1, 0, 0), moment(s, 2026, time.September, 30, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDue(t, occ, "2026-09-01 09:00")
}

func TestNoRuleVersionsMeansNoOccurrences(t *testing.T) {
	s := testService(t)
	occ, err := s.expandVersioned(choreCashback, nil,
		moment(s, 2026, time.September, 1, 0, 0), moment(s, 2026, time.September, 30, 0, 0))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(occ) != 0 {
		t.Fatalf("got %d occurrences with no rule", len(occ))
	}
}

func TestAnInvertedWindowYieldsNothing(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=DAILY")}
	occ, err := s.expandVersioned(choreCashback, rules,
		moment(s, 2026, time.September, 30, 0, 0), moment(s, 2026, time.September, 1, 0, 0))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(occ) != 0 {
		t.Fatalf("got %d occurrences for an inverted window", len(occ))
	}
}

// Results are ordered by instant across version boundaries, not grouped by
// version — every caller renders them as one timeline.
func TestResultsAreOrderedAcrossVersions(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{
		rule(10, "2026-01-01T00:00", "2026-01-01T20:00", "FREQ=DAILY"),
		rule(11, "2026-09-02T00:00", "2026-09-02T07:00", "FREQ=DAILY"),
	}
	occ, err := s.expandVersioned(choreCashback, rules,
		moment(s, 2026, time.September, 1, 0, 0), moment(s, 2026, time.September, 3, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDue(t, occ, "2026-09-01 20:00", "2026-09-02 07:00", "2026-09-03 07:00")
}

func TestABrokenRuleBodySurfacesAnError(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=NONSENSE")}
	_, err := s.expandVersioned(choreCashback, rules,
		moment(s, 2026, time.September, 1, 0, 0), moment(s, 2026, time.September, 30, 0, 0))
	if err == nil {
		t.Fatal("expected an error for an unparseable rule")
	}
}

// --- ruleAt / occursAt ---

func TestRuleAtPicksTheVersionInForce(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{
		rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=DAILY"),
		rule(11, "2026-09-15T00:00", "2026-09-15T08:00", "FREQ=DAILY"),
	}
	for _, tc := range []struct {
		at   time.Time
		want int64
	}{
		{moment(s, 2026, time.August, 20, 8, 0), 10},
		{moment(s, 2026, time.September, 14, 23, 59), 10},
		{moment(s, 2026, time.September, 15, 0, 0), 11},
		{moment(s, 2027, time.January, 1, 8, 0), 11},
	} {
		got, ok, err := s.ruleAt(rules, tc.at)
		if err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", tc.at.Format(time.DateOnly), ok, err)
		}
		if got.ID != tc.want {
			t.Fatalf("%s -> rule %d, want %d", tc.at.Format("2006-01-02 15:04"), got.ID, tc.want)
		}
	}
}

func TestRuleAtBeforeAnyVersionFindsNothing(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=DAILY")}
	_, ok, err := s.ruleAt(rules, moment(s, 2025, time.June, 1, 8, 0))
	if err != nil {
		t.Fatalf("rule at: %v", err)
	}
	if ok {
		t.Fatal("found a rule in force before the first version began")
	}
}

// Marks are checked against this, so a hand-crafted request cannot invent an
// occurrence that was never scheduled.
func TestOccursAtAcceptsOnlyRealInstants(t *testing.T) {
	s := testService(t)
	rules := []model.ReminderRule{
		rule(10, "2026-01-01T00:00", "2026-01-01T08:00", "FREQ=MONTHLY;BYMONTHDAY=1"),
	}
	if _, ok, err := s.occursAt(choreCashback, rules, moment(s, 2026, time.September, 1, 8, 0)); err != nil || !ok {
		t.Fatalf("the real 1st-of-month instant was rejected (ok=%v err=%v)", ok, err)
	}
	for _, bad := range []time.Time{
		moment(s, 2026, time.September, 1, 9, 0), // right day, wrong time
		moment(s, 2026, time.September, 2, 8, 0), // right time, wrong day
	} {
		if _, ok, _ := s.occursAt(choreCashback, rules, bad); ok {
			t.Fatalf("%s was accepted as an occurrence", bad.Format("2006-01-02 15:04"))
		}
	}
}
