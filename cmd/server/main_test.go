package main

import "testing"

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
