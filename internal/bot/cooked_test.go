package bot

import (
	"strings"
	"testing"
	"time"

	"familyhub/internal/cooking"
	"familyhub/internal/dish"
	"familyhub/internal/mealie"
)

var kyiv = time.FixedZone("EEST", 3*3600)

func TestSlotFrom(t *testing.T) {
	tests := []struct {
		name      string
		modelSlot string
		hour      int
		want      cooking.Slot
	}{
		{"model read the hint", "vecheria", 12, cooking.SlotDinner},
		{"model read lunch", "obid", 20, cooking.SlotLunch},
		{"nothing said, midday", "", 12, cooking.SlotLunch},
		{"nothing said, evening", "", 19, cooking.SlotDinner},
		{"nothing said, late afternoon is dinner", "", 16, cooking.SlotDinner},
		{"a slot this house does not eat falls back to the clock", "snidanok", 9, cooking.SlotLunch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 9, 10, tt.hour, 0, 0, 0, kyiv)
			if got := slotFrom(tt.modelSlot, now); got != tt.want {
				t.Fatalf("slotFrom(%q, %d:00) = %q, want %q", tt.modelSlot, tt.hour, got, tt.want)
			}
		})
	}
}

func TestDateFrom(t *testing.T) {
	now := time.Date(2026, 9, 10, 13, 30, 0, 0, kyiv)
	today := time.Date(2026, 9, 10, 0, 0, 0, 0, kyiv)

	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{"empty means today", "", today},
		{"yesterday", "2026-09-09", today.AddDate(0, 0, -1)},
		{"garbage means today", "вчора", today},
		// A future date is always a misread: nobody photographs tomorrow's
		// dinner, and a wrong day entering the record silently is worse than
		// the cook tapping the day button.
		{"future falls back to today", "2026-09-11", today},
		{"a year off falls back to today", "2025-09-09", today},
		{"a week back is still allowed", "2026-09-03", today.AddDate(0, 0, -7)},
		{"older than a week is not", "2026-09-02", today},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dateFrom(tt.in, now, kyiv)
			if !got.Equal(tt.want) {
				t.Fatalf("dateFrom(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// The record answers "which day, which meal", so the hour is the meal's, not
// the moment the photo happened to be sent.
func TestCookedAtPinsCanonicalHour(t *testing.T) {
	day := time.Date(2026, 9, 10, 0, 0, 0, 0, kyiv)
	if got := cookedAt(day, cooking.SlotLunch); got.Hour() != 13 {
		t.Errorf("lunch at %d:00, want 13:00", got.Hour())
	}
	if got := cookedAt(day, cooking.SlotDinner); got.Hour() != 19 {
		t.Errorf("dinner at %d:00, want 19:00", got.Hour())
	}
	if got := cookedAt(day, cooking.SlotLunch); got.Location() != kyiv {
		t.Errorf("lost the location: %s", got.Location())
	}
}

func TestDayLabel(t *testing.T) {
	now := time.Date(2026, 9, 10, 21, 0, 0, 0, kyiv)
	day := func(d int) time.Time { return time.Date(2026, 9, 10+d, 0, 0, 0, 0, kyiv) }

	for _, tt := range []struct {
		date time.Time
		want string
	}{
		{day(0), "сьогодні"},
		{day(-1), "вчора"},
		{day(-2), "позавчора"},
		{day(-5), "05.09"},
	} {
		if got := dayLabel(tt.date, now); got != tt.want {
			t.Errorf("dayLabel(%s) = %q, want %q", tt.date.Format("02.01"), got, tt.want)
		}
	}
}

func TestSplitCommand(t *testing.T) {
	for _, tt := range []struct {
		in        string
		cmd, rest string
	}{
		{"/cooked драники на обед", "/cooked", "драники на обед"},
		{"/cooked@family_core_hub_bot борщ", "/cooked", "борщ"},
		{"/cooked", "/cooked", ""},
		{"просто підпис", "", "просто підпис"},
		{"", "", ""},
		{"/cooked\nборщ", "/cooked", "борщ"},
	} {
		cmd, rest := splitCommand(tt.in)
		if cmd != tt.cmd || rest != tt.rest {
			t.Errorf("splitCommand(%q) = (%q, %q), want (%q, %q)", tt.in, cmd, rest, tt.cmd, tt.rest)
		}
	}
}

// telebot joins callback arguments with "|", not the ":" the appointment
// buttons use — sharing the wrong splitter would silently mangle the key.
func TestSplitCookedData(t *testing.T) {
	key, arg := splitCookedData("2f|1")
	if key != "2f" || arg != "1" {
		t.Fatalf("got (%q, %q)", key, arg)
	}
	if key, arg := splitCookedData("2f"); key != "2f" || arg != "" {
		t.Fatalf("got (%q, %q)", key, arg)
	}
}

// Two taps on the same card must not write the meal down twice.
func TestCookedPendingClaimIsOnce(t *testing.T) {
	p := newCookedPending()
	now := time.Now()
	key := p.put(&cookedEntry{}, now)

	if _, ok := p.claim(key); !ok {
		t.Fatal("first claim must succeed")
	}
	if _, ok := p.claim(key); ok {
		t.Fatal("second claim must not")
	}
	// The entry survives the claim: the photo is still needed for the
	// "make it the main picture" button.
	if _, ok := p.get(key); !ok {
		t.Fatal("entry must outlive the claim")
	}
}

func TestCookedPendingEvictsStaleCards(t *testing.T) {
	p := newCookedPending()
	old := time.Now().Add(-2 * time.Hour)
	stale := p.put(&cookedEntry{}, old)
	p.put(&cookedEntry{}, time.Now()) // eviction runs on write

	if _, ok := p.get(stale); ok {
		t.Fatal("an hour-old card should be gone")
	}
}

// A failed write releases the card, so the retry button works instead of
// forcing the cook to send the photo again.
func TestCookedPendingReleaseAllowsRetry(t *testing.T) {
	p := newCookedPending()
	key := p.put(&cookedEntry{}, time.Now())

	if _, ok := p.claim(key); !ok {
		t.Fatal("first claim must succeed")
	}
	p.release(key)
	if _, ok := p.claim(key); !ok {
		t.Fatal("a released card must be claimable again")
	}
	if _, ok := p.claim(key); ok {
		t.Fatal("and only once after that")
	}
}

func entryWith(main []string, sides []string) *cookedEntry {
	e := &cookedEntry{dropped: map[string]bool{}}
	for _, s := range main {
		e.guess.Candidates = append(e.guess.Candidates,
			dish.Candidate{Recipe: mealie.Recipe{Slug: s, Name: s}})
	}
	for _, s := range sides {
		e.guess.Sides = append(e.guess.Sides,
			dish.Candidate{Recipe: mealie.Recipe{Slug: s, Name: s}})
	}
	return e
}

func slugsOf(rs []mealie.Recipe) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Slug)
	}
	return out
}

func TestChosenSides(t *testing.T) {
	t.Run("everything the model saw is included by default", func(t *testing.T) {
		e := entryWith([]string{"guliash"}, []string{"piure", "salat"})
		if got := strings.Join(slugsOf(e.chosenSides()), ","); got != "piure,salat" {
			t.Fatalf("sides = %q", got)
		}
	})

	t.Run("an un-ticked side is left out", func(t *testing.T) {
		e := entryWith([]string{"guliash"}, []string{"piure", "salat"})
		e.dropped["salat"] = true
		if got := strings.Join(slugsOf(e.chosenSides()), ","); got != "piure" {
			t.Fatalf("sides = %q", got)
		}
	})

	// Picking the side as the main dish must not write it down twice.
	t.Run("the chosen main never doubles as a side", func(t *testing.T) {
		e := entryWith([]string{"guliash", "piure"}, []string{"piure"})
		e.main = 1
		if got := e.chosenSides(); len(got) != 0 {
			t.Fatalf("sides = %v, want none", slugsOf(got))
		}
		if main, _ := e.mainDish(); main.Slug != "piure" {
			t.Fatalf("main = %q", main.Slug)
		}
	})

	t.Run("no candidates means no main dish", func(t *testing.T) {
		e := entryWith(nil, []string{"piure"})
		if _, ok := e.mainDish(); ok {
			t.Fatal("want no main dish")
		}
	})
}
