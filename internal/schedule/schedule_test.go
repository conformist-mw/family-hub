package schedule

import (
	"errors"
	"testing"

	"familyhub/internal/valid"
)

func TestParseValid(t *testing.T) {
	got, err := Form{Weekday: "2", Time: "13:35"}.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Weekday != 2 || got.Time != "13:35" {
		t.Errorf("got %+v", got)
	}
	if got.DurationMin != DefaultDurationMin {
		t.Errorf("duration = %d, want the default %d", got.DurationMin, DefaultDurationMin)
	}
}

// The bot compares slot times as strings, so "9:05" and "09:05" must not both
// be storable.
func TestParseNormalizesTime(t *testing.T) {
	got, err := Form{Weekday: "0", Time: "9:05"}.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Time != "09:05" {
		t.Fatalf("time = %q, want 09:05", got.Time)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name  string
		form  Form
		field string
	}{
		{"no weekday", Form{Weekday: "", Time: "13:35"}, "weekday"},
		{"weekday out of range", Form{Weekday: "7", Time: "13:35"}, "weekday"},
		{"negative weekday", Form{Weekday: "-1", Time: "13:35"}, "weekday"},
		{"weekday not a number", Form{Weekday: "вівторок", Time: "13:35"}, "weekday"},
		// This one used to reach the database: the web form only checked that
		// the time was non-empty, so a typo silently never fired a reminder.
		{"time is nonsense", Form{Weekday: "2", Time: "abc"}, "time"},
		{"time empty", Form{Weekday: "2", Time: ""}, "time"},
		{"impossible time", Form{Weekday: "2", Time: "25:99"}, "time"},
		{"duration not a number", Form{Weekday: "2", Time: "13:35", Duration: "довго"}, "duration"},
		{"duration zero", Form{Weekday: "2", Time: "13:35", Duration: "0"}, "duration"},
		{"duration negative", Form{Weekday: "2", Time: "13:35", Duration: "-30"}, "duration"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.form.Parse()
			var invalid valid.FieldError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want a FieldError", err)
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

func TestParseAcceptsBoundaryWeekdays(t *testing.T) {
	for _, wd := range []string{"0", "6"} {
		if _, err := (Form{Weekday: wd, Time: "13:35"}).Parse(); err != nil {
			t.Errorf("weekday %s rejected: %v", wd, err)
		}
	}
}
