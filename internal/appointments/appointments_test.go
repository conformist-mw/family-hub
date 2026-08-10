package appointments

import (
	"errors"
	"testing"
	"time"

	"familyhub/internal/model"
)

func validForm() Form {
	return Form{
		Title:  "Ортодонт",
		Person: "Демид",
		Date:   "2026-08-10",
		Time:   "14:30",
		Status: model.ApptStatusPlanned,
	}
}

func TestParseValid(t *testing.T) {
	f := validForm()
	f.EndTime = "15:30"
	f.Location = "  Хрещатик 1  "
	f.Note = " взяти картку "
	f.Cost = "1 200,50"

	a, err := f.Parse(time.UTC)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.StartsAt != "2026-08-10T14:30" || a.EndsAt != "2026-08-10T15:30" {
		t.Errorf("times = %q .. %q", a.StartsAt, a.EndsAt)
	}
	if a.Location != "Хрещатик 1" || a.Note != "взяти картку" {
		t.Errorf("fields not trimmed: %q / %q", a.Location, a.Note)
	}
	if a.Cost == nil || *a.Cost != 1200.5 {
		t.Errorf("cost = %v, want 1200.5", a.Cost)
	}
}

// An open-ended appointment is normal — most of them have no known end.
func TestParseWithoutEndTime(t *testing.T) {
	a, err := validForm().Parse(time.UTC)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.EndsAt != "" {
		t.Errorf("EndsAt = %q, want empty", a.EndsAt)
	}
}

// An empty amount means nobody wrote it down; 0 means it was free. These must
// not collapse into each other.
func TestParseCostEmptyIsNotZero(t *testing.T) {
	f := validForm()
	f.Cost = ""
	a, err := f.Parse(time.UTC)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Cost != nil {
		t.Fatalf("empty cost became %v, want nil", *a.Cost)
	}

	f.Cost = "0"
	a, err = f.Parse(time.UTC)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Cost == nil || *a.Cost != 0 {
		t.Fatalf("zero cost = %v, want a stored 0", a.Cost)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Form)
		field  string
	}{
		{"no title", func(f *Form) { f.Title = "   " }, "title"},
		{"no date", func(f *Form) { f.Date = "" }, "date"},
		{"nonsense date", func(f *Form) { f.Date = "10.08.2026" }, "date"},
		{"impossible date", func(f *Form) { f.Date = "2026-02-31" }, "date"},
		{"no time", func(f *Form) { f.Time = "" }, "date"},
		{"nonsense time", func(f *Form) { f.Time = "25:99" }, "date"},
		{"nonsense end time", func(f *Form) { f.EndTime = "abc" }, "endTime"},
		{"end before start", func(f *Form) { f.EndTime = "13:00" }, "endTime"},
		{"end equal to start", func(f *Form) { f.EndTime = "14:30" }, "endTime"},
		{"empty status", func(f *Form) { f.Status = "" }, "status"},
		{"unknown status", func(f *Form) { f.Status = "maybe" }, "status"},
		{"cost not a number", func(f *Form) { f.Cost = "багато" }, "cost"},
		{"negative cost", func(f *Form) { f.Cost = "-5" }, "cost"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := validForm()
			tc.mutate(&f)

			_, err := f.Parse(time.UTC)
			var invalid InvalidField
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want an InvalidField", err)
			}
			if invalid.Field != tc.field {
				t.Errorf("field = %q, want %q", invalid.Field, tc.field)
			}
			if invalid.Message == "" {
				t.Error("empty message — nothing to show the person")
			}
		})
	}
}

func TestParseCost(t *testing.T) {
	ok := map[string]float64{
		"800":        800,
		"1 200":      1200,
		"1\u00a0200": 1200,
		"1200,50":    1200.5,
		"1200.50":    1200.5,
		"0":          0,
	}
	for in, want := range ok {
		got, valid := ParseCost(in)
		if !valid || got != want {
			t.Errorf("ParseCost(%q) = %v, %v; want %v, true", in, got, valid, want)
		}
	}
	for _, in := range []string{"", "абв", "-1", "8 0 0 грн"} {
		if _, valid := ParseCost(in); valid {
			t.Errorf("ParseCost(%q) accepted, want rejected", in)
		}
	}
}

func TestFormatCostRoundTrip(t *testing.T) {
	if got := FormatCost(nil); got != "" {
		t.Errorf("FormatCost(nil) = %q, want empty", got)
	}
	for _, v := range []float64{0, 800, 1200.5} {
		got := FormatCost(&v)
		back, ok := ParseCost(got)
		if !ok || back != v {
			t.Errorf("%v -> %q -> %v, %v", v, got, back, ok)
		}
	}
}

func TestSplitStart(t *testing.T) {
	if d, hhmm := SplitStart("2026-08-10T14:30"); d != "2026-08-10" || hhmm != "14:30" {
		t.Errorf("got %q %q", d, hhmm)
	}
	// A value that will not split still shows the person something.
	if d, hhmm := SplitStart("сміття"); d != "сміття" || hhmm != "" {
		t.Errorf("got %q %q", d, hhmm)
	}
}

// The phone form asks how long something takes rather than when it ends; the
// end time is derived here so no date arithmetic happens on the client.
func TestParseDuration(t *testing.T) {
	f := validForm() // 2026-08-10 14:30
	f.Duration = "45"

	a, err := f.Parse(time.UTC)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.EndsAt != "2026-08-10T15:15" {
		t.Fatalf("EndsAt = %q, want 2026-08-10T15:15", a.EndsAt)
	}
}

// A late appointment may legitimately run past midnight — the duration is
// added to a parsed time, not to a string.
func TestParseDurationCrossesMidnight(t *testing.T) {
	f := validForm()
	f.Time = "23:30"
	f.Duration = "120"

	a, err := f.Parse(time.UTC)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.EndsAt != "2026-08-11T01:30" {
		t.Fatalf("EndsAt = %q, want 2026-08-11T01:30", a.EndsAt)
	}
}

// An explicit end time is the web form's way of saying it, and it wins.
func TestExplicitEndTimeBeatsDuration(t *testing.T) {
	f := validForm()
	f.EndTime = "16:00"
	f.Duration = "45"

	a, err := f.Parse(time.UTC)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.EndsAt != "2026-08-10T16:00" {
		t.Fatalf("EndsAt = %q, want the explicit 16:00", a.EndsAt)
	}
}

func TestParseDurationRejects(t *testing.T) {
	for _, bad := range []string{"довго", "0", "-30", "2000"} {
		f := validForm()
		f.Duration = bad

		_, err := f.Parse(time.UTC)
		var invalid InvalidField
		if !errors.As(err, &invalid) || invalid.Field != "duration" {
			t.Errorf("duration %q -> %v, want a duration field error", bad, err)
		}
	}
}

func TestDurationOfIsTheInverse(t *testing.T) {
	for _, minutes := range []string{"30", "45", "60", "120"} {
		f := validForm()
		f.Duration = minutes

		a, err := f.Parse(time.UTC)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got := DurationOf(a, time.UTC); got != minutes {
			t.Errorf("DurationOf = %q, want %q", got, minutes)
		}
	}
	// No end recorded means no chip is pre-selected.
	if got := DurationOf(model.Appointment{StartsAt: "2026-08-10T14:30"}, time.UTC); got != "" {
		t.Errorf("DurationOf without an end = %q, want empty", got)
	}
}
