package bot

import (
	"testing"
	"time"

	"familyhub/internal/dish"
	"familyhub/internal/moodle"
)

// menuClocks is the production shape the menu is meant to run in:
// NOTIFICATIONS_ENABLED off, the three menu clocks on, suggestions on Saturday.
func menuClocks() Config {
	return Config{
		NotifyChat:           -100,
		NotificationsEnabled: false,
		MenuTime:             "07:00",
		MenuEveningTime:      "20:00",
		DishSuggestDOW:       6, // Saturday
		DishSuggestTime:      "10:00",
		Dish:                 &dish.Recognizer{},
	}
}

// Each menu clock fires at its own minute, once a day, and only that one: the
// morning menu must not drag the evening check or the suggestions along.
func TestTheMenuClocksFireOncePerDayAtTheirMinute(t *testing.T) {
	cfg := menuClocks()
	saturday := func(hh, mm int) time.Time { return time.Date(2026, 9, 26, hh, mm, 0, 0, time.UTC) }
	sunday := func(hh, mm int) time.Time { return time.Date(2026, 9, 27, hh, mm, 0, 0, time.UTC) }

	for _, tc := range []struct {
		name string
		at   func(hh, mm int) time.Time
		hh   int
		mm   int
		pick func(due) bool
		set  func(*lastFired, string)
		// daily is false for the suggestions, which go out once a week.
		daily bool
	}{
		{"menu", saturday, 7, 0, func(d due) bool { return d.menu }, func(l *lastFired, v string) { l.menu = v }, true},
		{"evening", saturday, 20, 0, func(d due) bool { return d.evening }, func(l *lastFired, v string) { l.evening = v }, true},
		{"suggest", saturday, 10, 0, func(d due) bool { return d.suggest }, func(l *lastFired, v string) { l.suggest = v }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := cfg.dueThisMinute(tc.at(tc.hh, tc.mm), lastFired{})
			if !tc.pick(d) {
				t.Fatal("did not fire at its minute")
			}
			if n := countDue(d); n != 1 {
				t.Fatalf("%d clocks fired at once, want only this one: %+v", n, d)
			}
			if tc.pick(cfg.dueThisMinute(tc.at(tc.hh, tc.mm+1), lastFired{})) {
				t.Error("fired at the wrong minute")
			}
			var last lastFired
			tc.set(&last, tc.at(tc.hh, tc.mm).Format("2006-01-02"))
			if tc.pick(cfg.dueThisMinute(tc.at(tc.hh, tc.mm), last)) {
				t.Error("re-fired within the same day")
			}
			// The latch is a date, so tomorrow at the same minute it is due
			// again — unless it is the weekly one and tomorrow is another day.
			if got := tc.pick(cfg.dueThisMinute(sunday(tc.hh, tc.mm), last)); got != tc.daily {
				t.Errorf("next day: fired=%v, want %v", got, tc.daily)
			}
		})
	}
}

func countDue(d due) int {
	n := 0
	for _, on := range []bool{d.daily, d.weekly, d.nag, d.menu, d.evening, d.suggest, d.schoolReview} {
		if on {
			n++
		}
	}
	return n
}

func TestTheDishSuggestionsStayOffWithoutADayOrAModel(t *testing.T) {
	saturday := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		tweak func(*Config)
	}{
		// -1 is the default the deploy gets without DISH_SUGGEST_DOW.
		{"DOW -1", func(c *Config) { c.DishSuggestDOW = -1 }},
		// No model: the clock firing would call Suggest on a nil receiver in
		// the ticker's goroutine and take the server down.
		{"no recognizer", func(c *Config) { c.Dish = nil }},
		{"no time", func(c *Config) { c.DishSuggestTime = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := menuClocks()
			tc.tweak(&cfg)
			if cfg.dishSuggestEnabled() {
				t.Fatal("the suggestions are armed")
			}
			if cfg.dueThisMinute(saturday, lastFired{}).suggest {
				t.Fatal("they fired anyway")
			}
		})
	}
}

func TestAnEmptyTimeSwitchesTheMenuOff(t *testing.T) {
	cfg := menuClocks()
	cfg.MenuTime = ""
	cfg.MenuEveningTime = ""
	if cfg.menuEnabled() || cfg.menuEveningEnabled() {
		t.Fatal("the menu is on with no time set")
	}
	// Midnight is what an empty time would match if it were compared as-is.
	for _, at := range []time.Time{
		time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 24, 7, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 24, 20, 0, 0, 0, time.UTC),
	} {
		if d := cfg.dueThisMinute(at, lastFired{}); d.menu || d.evening {
			t.Fatalf("%s fired: menu=%v evening=%v", at.Format("15:04"), d.menu, d.evening)
		}
	}
}

// The menu works without a model: the catalogue is local, so a deploy with no
// AI key still gets the morning menu and the evening check.
func TestTheMenuDoesNotNeedAModel(t *testing.T) {
	cfg := menuClocks()
	cfg.Dish = nil
	if !cfg.menuEnabled() || !cfg.menuEveningEnabled() {
		t.Fatal("the menu went off with the recognizer")
	}
}

// RunDigests returns early when nothing is enabled. Each menu clock on its own
// has to keep the loop alive, or a deploy that turns on only the menu gets a
// ticker that never starts.
func TestEachMenuClockAloneKeepsTheDigestLoopAlive(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"menu", Config{NotifyChat: -100, MenuTime: "07:00", DishSuggestDOW: -1}},
		{"evening", Config{NotifyChat: -100, MenuEveningTime: "20:00", DishSuggestDOW: -1}},
		{"suggest", Config{NotifyChat: -100, DishSuggestDOW: 6, DishSuggestTime: "10:00", Dish: &dish.Recognizer{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.cfg
			if c.appointmentDigestsEnabled() || c.reminderNagEnabled() || c.reminderPushEnabled() {
				t.Fatal("the fixture is not the menu-only shape")
			}
			if !c.anyDigestEnabled() {
				t.Fatal("RunDigests would return early")
			}
		})
	}
	if (Config{NotifyChat: -100, DishSuggestDOW: -1}).anyDigestEnabled() {
		t.Fatal("an empty config keeps the loop alive")
	}
}

// anchoredMoodle is a Moodle service whose weeks can be numbered: week 0 is
// the one of 31.08.2026. The clock only asks it HasWeekAnchor.
func anchoredMoodle() *moodle.Service {
	return moodle.NewService(nil, nil, moodle.Config{Week0: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)}, time.UTC, nil, nil)
}

// schoolReviewClock is the production shape of the review: Friday 19:45, next
// to the menu clocks.
func schoolReviewClock() Config {
	cfg := menuClocks()
	cfg.Moodle = anchoredMoodle()
	cfg.SchoolReviewDOW = 5 // Friday
	cfg.SchoolReviewTime = "19:45"
	return cfg
}

// The review fires once, on its day at its minute, and on nothing else: the
// latch is a date, so next Friday it is due again.
func TestTheSchoolReviewFiresOnceAWeekAtItsMinute(t *testing.T) {
	cfg := schoolReviewClock()
	friday := time.Date(2026, 11, 27, 19, 45, 0, 0, time.UTC)

	d := cfg.dueThisMinute(friday, lastFired{})
	if !d.schoolReview {
		t.Fatal("did not fire at its minute")
	}
	if n := countDue(d); n != 1 {
		t.Fatalf("%d clocks fired at once, want only the review: %+v", n, d)
	}
	if cfg.dueThisMinute(friday.Add(time.Minute), lastFired{}).schoolReview {
		t.Error("fired at the wrong minute")
	}
	if cfg.dueThisMinute(friday.AddDate(0, 0, 1), lastFired{}).schoolReview {
		t.Error("fired on Saturday")
	}
	last := lastFired{schoolReview: friday.Format("2006-01-02")}
	if cfg.dueThisMinute(friday, last).schoolReview {
		t.Error("re-fired within the same day")
	}
	if !cfg.dueThisMinute(friday.AddDate(0, 0, 7), last).schoolReview {
		t.Error("did not fire the next Friday")
	}
}

func TestTheSchoolReviewStaysOffWithoutAServiceOrAnAnchor(t *testing.T) {
	friday := time.Date(2026, 11, 27, 19, 45, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		tweak func(*Config)
	}{
		// No Moodle credentials: the clock firing would call a nil service in
		// the ticker's goroutine.
		{"no service", func(c *Config) { c.Moodle = nil }},
		// No SCHOOL_MOODLE_WEEK0: there is no week number to review.
		{"no anchor", func(c *Config) {
			c.Moodle = moodle.NewService(nil, nil, moodle.Config{}, time.UTC, nil, nil)
		}},
		{"DOW -1", func(c *Config) { c.SchoolReviewDOW = -1 }},
		{"no time", func(c *Config) { c.SchoolReviewTime = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := schoolReviewClock()
			tc.tweak(&cfg)
			if cfg.schoolReviewEnabled() {
				t.Fatal("the review is armed")
			}
			if cfg.dueThisMinute(friday, lastFired{}).schoolReview {
				t.Fatal("it fired anyway")
			}
		})
	}
}

// The review on its own keeps RunDigests' loop alive, and only once it is
// fully armed: an unanchored service must not start a ticker with nothing to
// fire.
func TestTheSchoolReviewAloneKeepsTheDigestLoopAlive(t *testing.T) {
	cfg := Config{NotifyChat: -100, DishSuggestDOW: -1,
		Moodle: anchoredMoodle(), SchoolReviewDOW: 5, SchoolReviewTime: "19:45"}
	if !cfg.anyDigestEnabled() {
		t.Fatal("RunDigests would return early")
	}
	cfg.Moodle = moodle.NewService(nil, nil, moodle.Config{}, time.UTC, nil, nil)
	if cfg.anyDigestEnabled() {
		t.Fatal("an unanchored service keeps the loop alive")
	}
}
