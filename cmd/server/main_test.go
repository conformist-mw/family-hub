package main

import (
	"testing"
	"time"

	"familyhub/internal/bot"
)

// An unset DISH_SUGGEST_DOW (or any other DOW) must mean "off", not Sunday:
// the zero value of an int is a valid day.
func TestParseDOW(t *testing.T) {
	cases := map[string]int{
		"":    -1,
		"  ":  -1,
		"x":   -1,
		"-1":  -1,
		"7":   -1,
		"0":   0,
		"6":   6,
		" 5 ": 5,
	}
	for in, want := range cases {
		if got := parseDOW(in); got != want {
			t.Errorf("parseDOW(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestWithMenuEnv(t *testing.T) {
	env := map[string]string{
		"MENU_TIME":         "07:00",
		"MENU_EVENING_TIME": "20:00",
		"DISH_SUGGEST_DOW":  "6",
		"DISH_SUGGEST_TIME": "10:00",
	}
	got := withMenuEnv(bot.Config{DailyDigestTime: "08:00"}, func(k string) string { return env[k] })
	if got.MenuTime != "07:00" || got.MenuEveningTime != "20:00" || got.DishSuggestDOW != 6 ||
		got.DishSuggestTime != "10:00" {
		t.Errorf("menu config = %+v", got)
	}
	if got.DailyDigestTime != "08:00" {
		t.Errorf("the rest of the config was lost: %+v", got)
	}

	off := withMenuEnv(bot.Config{}, func(string) string { return "" })
	if off.MenuTime != "" || off.MenuEveningTime != "" || off.DishSuggestDOW != -1 || off.DishSuggestTime != "" {
		t.Errorf("unset env = %+v, want everything off", off)
	}
}

func TestSchoolMoodleFromEnv(t *testing.T) {
	full := map[string]string{
		"SCHOOL_MOODLE_URL":         "https://moodle.example",
		"SCHOOL_MOODLE_USERNAME":    "pupil",
		"SCHOOL_MOODLE_PASSWORD":    "secret",
		"SCHOOL_MOODLE_WEEK0":       "2026-08-31",
		"SCHOOL_MOODLE_REVIEW_DOW":  "5",
		"SCHOOL_MOODLE_REVIEW_TIME": "19:45",
	}
	env := func(over map[string]string) func(string) string {
		m := map[string]string{}
		for k, v := range full {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		return func(k string) string { return m[k] }
	}

	t.Run("full", func(t *testing.T) {
		got, err := schoolMoodleFromEnv(env(nil))
		if err != nil || got == nil {
			t.Fatalf("got %+v, %v", got, err)
		}
		if got.URL != "https://moodle.example" || got.Service.Username != "pupil" ||
			got.Service.Password != "secret" {
			t.Errorf("site/credentials = %+v", got)
		}
		if want := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC); !got.Service.Week0.Equal(want) {
			t.Errorf("Week0 = %v, want %v", got.Service.Week0, want)
		}
		if dow, at := got.reviewClock(); dow != 5 || at != "19:45" {
			t.Errorf("review clock = %d %q", dow, at)
		}
	})

	t.Run("interval", func(t *testing.T) {
		for in, want := range map[string]time.Duration{
			"":     2 * time.Hour,
			"junk": 2 * time.Hour,
			"0s":   2 * time.Hour,
			"-1h":  2 * time.Hour,
			"30m":  30 * time.Minute,
		} {
			got, _ := schoolMoodleFromEnv(env(map[string]string{"SCHOOL_MOODLE_SYNC_INTERVAL": in}))
			if got.Service.Interval != want {
				t.Errorf("SCHOOL_MOODLE_SYNC_INTERVAL=%q: interval = %v, want %v", in, got.Service.Interval, want)
			}
		}
	})

	t.Run("off without site or credentials", func(t *testing.T) {
		for _, key := range []string{"SCHOOL_MOODLE_URL", "SCHOOL_MOODLE_USERNAME", "SCHOOL_MOODLE_PASSWORD"} {
			got, err := schoolMoodleFromEnv(env(map[string]string{key: ""}))
			if got != nil || err != nil {
				t.Errorf("%s empty: got %+v, %v, want nil", key, got, err)
			}
			// A nil config must still switch the review clock off, not
			// leave DOW at 0 = Sunday.
			if dow, at := got.reviewClock(); dow != -1 || at != "" {
				t.Errorf("%s empty: review clock = %d %q, want off", key, dow, at)
			}
		}
	})

	t.Run("review off without anchor or day", func(t *testing.T) {
		got, err := schoolMoodleFromEnv(env(map[string]string{"SCHOOL_MOODLE_WEEK0": ""}))
		if err != nil || got == nil || !got.Service.Week0.IsZero() {
			t.Errorf("no WEEK0: got %+v, %v, want sync on and no anchor", got, err)
		}
		got, _ = schoolMoodleFromEnv(env(map[string]string{"SCHOOL_MOODLE_REVIEW_DOW": ""}))
		if dow, _ := got.reviewClock(); dow != -1 {
			t.Errorf("no REVIEW_DOW: DOW = %d, want -1", dow)
		}
	})

	t.Run("bad anchor keeps the sync", func(t *testing.T) {
		for _, w := range []string{"31.08.2026", "2026-02-30", "soon"} {
			got, err := schoolMoodleFromEnv(env(map[string]string{"SCHOOL_MOODLE_WEEK0": w}))
			if err == nil {
				t.Errorf("WEEK0=%q: no error", w)
			}
			if got == nil || !got.Service.Week0.IsZero() {
				t.Errorf("WEEK0=%q: got %+v, want sync on with no anchor", w, got)
			}
		}
	})
}
