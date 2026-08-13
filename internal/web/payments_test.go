package web

import "testing"

func TestMonthRangeSpansTheWholeCalendarMonth(t *testing.T) {
	cases := []struct {
		in          string
		from, until string
	}{
		{"2026-09", "2026-09-01", "2026-09-30"},
		{"2026-01", "2026-01-01", "2026-01-31"},
		{"2027-02", "2027-02-01", "2027-02-28"},
		{"2028-02", "2028-02-01", "2028-02-29"}, // leap year
		{"2026-12", "2026-12-01", "2026-12-31"},
	}
	for _, c := range cases {
		from, until, err := monthRange(c.in)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.in, err)
			continue
		}
		if from != c.from || until != c.until {
			t.Errorf("%s: got %s..%s, want %s..%s", c.in, from, until, c.from, c.until)
		}
	}
}

func TestMonthRangeRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "2026", "2026-13", "вересень", "2026-09-01"} {
		if _, _, err := monthRange(in); err == nil {
			t.Errorf("%q should not parse as a month", in)
		}
	}
}
