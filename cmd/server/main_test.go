package main

import (
	"testing"

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
