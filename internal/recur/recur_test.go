package recur

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

// kyiv is the zone the app runs in, and the one both DST transitions below
// are expressed in.
func kyiv(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatalf("load Europe/Kyiv: %v", err)
	}
	return loc
}

func at(loc *time.Location, y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, loc)
}

// dates renders occurrences for comparison. Wall-clock, because that is what
// the app stores and what a person reads.
func dates(occ []time.Time) []string {
	out := make([]string, len(occ))
	for i, o := range occ {
		out[i] = o.Format("2006-01-02 15:04")
	}
	return out
}

func assertDates(t *testing.T, got []time.Time, want ...string) {
	t.Helper()
	g := dates(got)
	if len(g) != len(want) {
		t.Fatalf("got %d occurrences %v, want %d %v", len(g), g, len(want), want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("occurrence %d = %q, want %q (full: %v)", i, g[i], want[i], g)
		}
	}
}

// The month-end case is why this package carries a full RRULE parser instead
// of a closed set of modes: "the last day of the month" is 31, 30 or 28
// depending on the month, and no BYMONTHDAY=28..31 approximation gets it right.
func TestExpandsTheLastDayOfEachMonth(t *testing.T) {
	loc := kyiv(t)
	occ, err := Expand(at(loc, 2026, time.August, 1, 9, 0), "FREQ=MONTHLY;BYMONTHDAY=-1",
		at(loc, 2026, time.August, 1, 0, 0), at(loc, 2026, time.October, 31, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDates(t, occ, "2026-08-31 09:00", "2026-09-30 09:00", "2026-10-31 09:00")
}

// "Every two weeks" is meaningless without an anchor — it only says which of
// the two weeks is yours once DTSTART fixes the phase.
func TestEveryOtherWeekTakesItsPhaseFromTheAnchor(t *testing.T) {
	loc := kyiv(t)
	occ, err := Expand(at(loc, 2026, time.August, 1, 11, 0), "FREQ=WEEKLY;INTERVAL=2;BYDAY=SA",
		at(loc, 2026, time.August, 1, 0, 0), at(loc, 2026, time.September, 30, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDates(t, occ,
		"2026-08-01 11:00", "2026-08-15 11:00", "2026-08-29 11:00",
		"2026-09-12 11:00", "2026-09-26 11:00")
}

func TestExpandsEveryOtherDay(t *testing.T) {
	loc := kyiv(t)
	occ, err := Expand(at(loc, 2026, time.September, 1, 20, 0), "FREQ=DAILY;INTERVAL=2",
		at(loc, 2026, time.September, 1, 0, 0), at(loc, 2026, time.September, 7, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDates(t, occ,
		"2026-09-01 20:00", "2026-09-03 20:00", "2026-09-05 20:00", "2026-09-07 20:00")
}

func TestExpandsEveryMonday(t *testing.T) {
	loc := kyiv(t)
	occ, err := Expand(at(loc, 2026, time.August, 31, 8, 0), "FREQ=WEEKLY;BYDAY=MO",
		at(loc, 2026, time.August, 31, 0, 0), at(loc, 2026, time.September, 21, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDates(t, occ,
		"2026-08-31 08:00", "2026-09-07 08:00", "2026-09-14 08:00", "2026-09-21 08:00")
}

// The whole point of expanding in a location: 08:00 stays 08:00 across the
// autumn transition. Fixed 7-day UTC arithmetic would drift it to 07:00 and
// silently stop matching everything keyed on the wall clock.
func TestWallClockSurvivesTheAutumnTransition(t *testing.T) {
	loc := kyiv(t)
	occ, err := Expand(at(loc, 2026, time.October, 20, 8, 0), "FREQ=WEEKLY;BYDAY=TU",
		at(loc, 2026, time.October, 20, 0, 0), at(loc, 2026, time.November, 10, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDates(t, occ,
		"2026-10-20 08:00", "2026-10-27 08:00", "2026-11-03 08:00", "2026-11-10 08:00")

	// The clock went back on 25.10, so the same wall time is a different offset
	// either side of it. That is the drift this package exists to prevent.
	if before, after := occ[0].Format("MST"), occ[1].Format("MST"); before == after {
		t.Fatalf("expected the offset to change across 25.10, both are %s", before)
	}
}

// On 29.03.2026 the clock jumps 03:00 -> 04:00, so 03:30 does not exist that
// day. It normalises forward rather than being skipped or erroring: the
// reminder still happens, an hour later.
func TestNonexistentLocalTimeMovesForward(t *testing.T) {
	loc := kyiv(t)
	occ, err := Expand(at(loc, 2026, time.March, 27, 3, 30), "FREQ=DAILY",
		at(loc, 2026, time.March, 27, 0, 0), at(loc, 2026, time.March, 31, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDates(t, occ,
		"2026-03-27 03:30", "2026-03-28 03:30",
		"2026-03-29 04:30", // the gap day
		"2026-03-30 03:30", "2026-03-31 03:30")
}

// On 25.10.2026 the clock goes back 04:00 -> 03:00, so 03:30 happens twice.
// Only one occurrence comes back, at the second instant — a reminder set for
// that half hour must not fire twice.
func TestDoubledLocalTimeYieldsOneOccurrence(t *testing.T) {
	loc := kyiv(t)
	occ, err := Expand(at(loc, 2026, time.October, 23, 3, 30), "FREQ=DAILY",
		at(loc, 2026, time.October, 25, 0, 0), at(loc, 2026, time.October, 25, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDates(t, occ, "2026-10-25 03:30")

	// It is the post-transition instant: 25 hours after the previous day's,
	// not 24, because that day is 25 hours long.
	prev, err := Expand(at(loc, 2026, time.October, 23, 3, 30), "FREQ=DAILY",
		at(loc, 2026, time.October, 24, 0, 0), at(loc, 2026, time.October, 24, 23, 59))
	if err != nil {
		t.Fatalf("expand previous day: %v", err)
	}
	if gap := occ[0].Sub(prev[0]); gap != 25*time.Hour {
		t.Fatalf("gap across the transition = %v, want 25h (would be 24h at the first instant)", gap)
	}
}

// A full RRULE can put several occurrences on one calendar date, which is why
// an occurrence is identified by its whole datetime and never by its date.
func TestSeveralOccurrencesCanShareOneDate(t *testing.T) {
	loc := kyiv(t)
	occ, err := Expand(at(loc, 2026, time.September, 1, 8, 0),
		"FREQ=DAILY;BYHOUR=8,20;BYMINUTE=0;BYSECOND=0",
		at(loc, 2026, time.September, 1, 0, 0), at(loc, 2026, time.September, 2, 23, 59))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	assertDates(t, occ,
		"2026-09-01 08:00", "2026-09-01 20:00",
		"2026-09-02 08:00", "2026-09-02 20:00")
}

func TestExpandReturnsNothingForAnInvertedWindow(t *testing.T) {
	loc := kyiv(t)
	occ, err := Expand(at(loc, 2026, time.September, 1, 8, 0), "FREQ=DAILY",
		at(loc, 2026, time.September, 10, 0, 0), at(loc, 2026, time.September, 1, 0, 0))
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(occ) != 0 {
		t.Fatalf("got %d occurrences for an inverted window, want none", len(occ))
	}
}

// A pathological rule must fail loudly. Returning a truncated slice would read
// as "nothing more is scheduled" to every caller downstream.
func TestRuleDenserThanTheCapIsAnError(t *testing.T) {
	loc := kyiv(t)
	_, err := Expand(at(loc, 2026, time.September, 1, 0, 0), "FREQ=MINUTELY",
		at(loc, 2026, time.September, 1, 0, 0), at(loc, 2026, time.September, 11, 0, 0))
	if err == nil {
		t.Fatal("expected an error for a minutely rule over ten days")
	}
}

// The preview in the form needs the next few dates without the caller having
// to guess how wide a window a yearly rule needs.
func TestNextLooksAheadWithoutAWindow(t *testing.T) {
	loc := kyiv(t)
	occ, err := Next(at(loc, 2026, time.March, 8, 9, 0), "FREQ=YEARLY",
		at(loc, 2026, time.August, 29, 0, 0), 3)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	assertDates(t, occ, "2027-03-08 09:00", "2028-03-08 09:00", "2029-03-08 09:00")
}

// A finite rule yields fewer than asked rather than erroring or looping.
func TestNextStopsWhenTheRuleRunsOut(t *testing.T) {
	loc := kyiv(t)
	occ, err := Next(at(loc, 2026, time.September, 1, 8, 0), "FREQ=DAILY;COUNT=3",
		at(loc, 2026, time.August, 31, 0, 0), 10)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	assertDates(t, occ, "2026-09-01 08:00", "2026-09-02 08:00", "2026-09-03 08:00")
}

func TestNextReturnsNothingForANonPositiveCount(t *testing.T) {
	loc := kyiv(t)
	occ, err := Next(at(loc, 2026, time.September, 1, 8, 0), "FREQ=DAILY",
		at(loc, 2026, time.August, 31, 0, 0), 0)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(occ) != 0 {
		t.Fatalf("got %d occurrences for n=0, want none", len(occ))
	}
}

func TestValidateRejectsEmptyAndUnparseableRules(t *testing.T) {
	if err := Validate("   "); !errors.Is(err, ErrEmptyRule) {
		t.Fatalf("blank rule error = %v, want ErrEmptyRule", err)
	}
	if err := Validate("NOT A RULE"); err == nil {
		t.Fatal("expected an error for an unparseable rule")
	}
}

// Rules are copied out of ICS files and documentation, where they carry the
// property name. Rejecting that would be a papercut with no upside.
func TestValidateAcceptsTheRRULEPrefix(t *testing.T) {
	if err := Validate("RRULE:FREQ=MONTHLY;BYMONTHDAY=1"); err != nil {
		t.Fatalf("prefixed rule rejected: %v", err)
	}
}

func TestExpandSurfacesTheParseErrorInsteadOfEmptyResults(t *testing.T) {
	loc := kyiv(t)
	occ, err := Expand(at(loc, 2026, time.September, 1, 8, 0), "FREQ=NONSENSE",
		at(loc, 2026, time.September, 1, 0, 0), at(loc, 2026, time.September, 30, 0, 0))
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if occ != nil {
		t.Fatalf("got %v alongside the error, want nil", dates(occ))
	}
}

// A rule no window can bound is refused at the door rather than stored and
// left to fail on every later read — a stored one would drop its chore from
// the calendar silently, since callers log an expansion error and carry on.
func TestFrequenciesTooDenseToExpandAreRefused(t *testing.T) {
	for _, rule := range []string{"FREQ=SECONDLY", "FREQ=MINUTELY", "RRULE:FREQ=SECONDLY;INTERVAL=30"} {
		if err := Validate(rule); err == nil {
			t.Fatalf("%q was accepted", rule)
		} else if !errors.Is(err, ErrBadRule) {
			t.Fatalf("%q rejected as %v, want ErrBadRule", rule, err)
		}
	}
	// HOURLY stays allowed: the cap bounds it, and a few thousand entries over
	// the feed's window is legitimate.
	if err := Validate("FREQ=HOURLY"); err != nil {
		t.Fatalf("FREQ=HOURLY refused: %v", err)
	}
}

// The cap has to stop the allocation, not measure it afterwards. Asking the
// library for the whole window first would build the pathological slice before
// rejecting it — which is what this code used to do.
func TestTheCapBoundsMemoryRatherThanReportingIt(t *testing.T) {
	loc := kyiv(t)
	from := at(loc, 2026, time.January, 1, 0, 0)
	to := from.AddDate(2, 0, 0) // two years of hourly occurrences: ~17500

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := Expand(from, "FREQ=HOURLY", from, to)
	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatal("a rule past the cap expanded without error")
	}
	if !errors.Is(err, ErrBadRule) {
		t.Fatalf("err = %v, want ErrBadRule", err)
	}
	// The bound is maxOccurrences time.Times plus slice growth. Anything near
	// the full two-year expansion means the cap ran after the allocation.
	const budget = 4 << 20 // 4 MiB
	if used := after.TotalAlloc - before.TotalAlloc; used > budget {
		t.Fatalf("allocated %d bytes past the cap, budget %d — the fuse runs too late", used, budget)
	}
}

// Every rejection about a rule's text carries ErrBadRule, so an HTTP layer can
// tell "the person's rule is wrong" from "our database failed" without
// matching on strings.
func TestRuleRejectionsAreDistinguishableFromOtherFailures(t *testing.T) {
	for _, rule := range []string{"NOT A RULE", "FREQ=NONSENSE", "FREQ=SECONDLY"} {
		if err := Validate(rule); !errors.Is(err, ErrBadRule) {
			t.Fatalf("%q -> %v, want ErrBadRule", rule, err)
		}
	}
	if err := Validate(""); !errors.Is(err, ErrEmptyRule) {
		t.Fatalf("empty rule -> %v, want ErrEmptyRule", err)
	}
}
