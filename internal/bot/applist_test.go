package bot

import (
	"testing"
	"time"
)

func TestWeekWindow(t *testing.T) {
	// A Wednesday, mid-morning.
	now := time.Date(2026, 7, 8, 10, 30, 0, 0, time.UTC)

	start0, end0 := weekWindow(now, 0)
	if start0.Weekday() != time.Monday {
		t.Fatalf("week 0 start = %s, want a Monday", start0.Weekday())
	}
	if !end0.Equal(start0.AddDate(0, 0, 7)) {
		t.Fatalf("week 0 end %v != start+7d %v", end0, start0.AddDate(0, 0, 7))
	}
	if now.Before(start0) || !now.Before(end0) {
		t.Fatalf("now %v not inside [%v, %v)", now, start0, end0)
	}
	if h := start0.Hour() + start0.Minute() + start0.Second(); h != 0 {
		t.Fatalf("week start not at midnight: %v", start0)
	}

	// Offset shifts the window by exactly a week.
	start1, end1 := weekWindow(now, 1)
	if !start1.Equal(end0) {
		t.Fatalf("week 1 start %v != week 0 end %v", start1, end0)
	}
	if !end1.Equal(start1.AddDate(0, 0, 7)) {
		t.Fatalf("week 1 end %v != start+7d", end1)
	}
}

func TestWeekWindowFromMonday(t *testing.T) {
	// On a Monday the window starts that same day.
	mon := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	if mon.Weekday() != time.Monday {
		t.Fatalf("test fixture not a Monday: %s", mon.Weekday())
	}
	start, _ := weekWindow(mon, 0)
	if start.Year() != 2026 || start.Month() != time.July || start.Day() != 6 {
		t.Fatalf("Monday week start = %v, want 2026-07-06", start)
	}
}

func TestWeekLabel(t *testing.T) {
	// Same-month range: [6 лип, 13 лип) -> "6–12 лип".
	from := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	if got := weekLabel(from, from.AddDate(0, 0, 7)); got != "6–12 лип" {
		t.Fatalf("same-month label = %q, want %q", got, "6–12 лип")
	}
	// Cross-month range: [28 лип, 4 сер) -> "28 лип – 3 сер".
	from = time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	if got := weekLabel(from, from.AddDate(0, 0, 7)); got != "28 лип – 3 сер" {
		t.Fatalf("cross-month label = %q, want %q", got, "28 лип – 3 сер")
	}
}

func TestSplitDataAndParseIDOffset(t *testing.T) {
	head, rest := splitData("card:42:3")
	if head != "card" || rest != "42:3" {
		t.Fatalf("splitData = (%q,%q), want (card,42:3)", head, rest)
	}
	head, rest = splitData("week:2")
	if head != "week" || rest != "2" {
		t.Fatalf("splitData = (%q,%q), want (week,2)", head, rest)
	}
	head, rest = splitData("noSep")
	if head != "noSep" || rest != "" {
		t.Fatalf("splitData = (%q,%q), want (noSep,'')", head, rest)
	}

	id, off := parseIDOffset("42:3")
	if id != 42 || off != 3 {
		t.Fatalf("parseIDOffset = (%d,%d), want (42,3)", id, off)
	}
	// Missing offset defaults to 0.
	if id, off = parseIDOffset("7"); id != 7 || off != 0 {
		t.Fatalf("parseIDOffset(7) = (%d,%d), want (7,0)", id, off)
	}
}
