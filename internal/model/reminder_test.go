package model_test

import (
	"testing"
	"time"

	"familyhub/internal/model"
)

func kyiv(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatalf("load Europe/Kyiv: %v", err)
	}
	return loc
}

// The stored strings are naive local time; parsing them anywhere but the app's
// own zone would shift every reminder by the offset.
func TestRuleTimesParseInTheGivenZone(t *testing.T) {
	loc := kyiv(t)
	r := model.ReminderRule{DTStart: "2026-08-01T08:00", ValidFromAt: "2026-09-15T10:30"}

	anchor, err := r.Anchor(loc)
	if err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if got := anchor.Format("2006-01-02 15:04 MST"); got != "2026-08-01 08:00 EEST" {
		t.Fatalf("anchor = %q", got)
	}

	from, err := r.ValidFrom(loc)
	if err != nil {
		t.Fatalf("valid from: %v", err)
	}
	if got := from.Format("2006-01-02 15:04"); got != "2026-09-15 10:30" {
		t.Fatalf("valid from = %q", got)
	}
}

func TestOccurrenceDueParses(t *testing.T) {
	loc := kyiv(t)
	o := model.ReminderOccurrence{DueAt: "2026-09-01T08:00"}
	due, err := o.Due(loc)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if got := due.Format("2006-01-02 15:04"); got != "2026-09-01 08:00" {
		t.Fatalf("due = %q", got)
	}
}

func TestUnparseableTimesSurfaceAnError(t *testing.T) {
	loc := kyiv(t)
	if _, err := (model.ReminderRule{DTStart: "nonsense"}).Anchor(loc); err == nil {
		t.Fatal("expected an error for an unparseable dtstart")
	}
	if _, err := (model.ReminderOccurrence{DueAt: ""}).Due(loc); err == nil {
		t.Fatal("expected an error for an empty due_at")
	}
}

// Closed is what the evening nag asks. A deliberate skip must silence it just
// as done does — otherwise "I watered it early" would still be nagged about.
func TestOnlyPendingIsLeftOpen(t *testing.T) {
	for _, tc := range []struct {
		status string
		closed bool
	}{
		{model.OccPending, false},
		{model.OccDone, true},
		{model.OccSkipped, true},
	} {
		o := model.ReminderOccurrence{Status: tc.status}
		if o.Closed() != tc.closed {
			t.Fatalf("%s: Closed() = %v, want %v", tc.status, o.Closed(), tc.closed)
		}
	}
}

// The API and the bot's callback data both reach the store; neither is trusted
// to stay in step with the CHECK constraint on its own.
func TestValidOccStatusAcceptsExactlyTheThreeStates(t *testing.T) {
	for _, s := range []string{model.OccPending, model.OccDone, model.OccSkipped} {
		if !model.ValidOccStatus(s) {
			t.Fatalf("%q rejected", s)
		}
	}
	for _, s := range []string{"", "PENDING", "cancelled", model.StatusRescheduled} {
		if model.ValidOccStatus(s) {
			t.Fatalf("%q accepted", s)
		}
	}
}

func TestEveryOccStatusHasALabel(t *testing.T) {
	for _, s := range []string{model.OccPending, model.OccDone, model.OccSkipped} {
		if model.OccStatusLabels[s] == "" {
			t.Fatalf("no label for %q", s)
		}
	}
}
