package bot

import (
	"testing"

	"lessons/internal/model"
)

func perLesson(remaining, lastPack int) model.Balance {
	b := model.Balance{Paid: remaining, LastPack: lastPack, Remaining: remaining}
	b.BillingType = model.BillingPerLesson
	b.LowThreshold = 2
	return b
}

// The regression this whole line exists for: a top-up made while the previous
// pack still had lessons must not be rendered as "11 из 8".
func TestPaidFragmentPrepaidOverlap(t *testing.T) {
	bal := perLesson(11, 8)
	dates := []string{
		"2026-08-01", "2026-08-04", "2026-08-06", // 3 carried over
		"2026-08-08", "2026-08-11", "2026-08-13", "2026-08-15",
		"2026-08-18", "2026-08-20", "2026-08-22", "2026-08-25",
	}
	out := newPaidOutlook(bal, dates)
	if out.CarriedOver != 3 {
		t.Fatalf("carried over: got %d, want 3", out.CarriedOver)
	}
	got := paidFragment(bal, out)
	want := "11 — до 25.08 · последняя оплата 8 с 08.08"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPaidFragmentWithinLastPack(t *testing.T) {
	bal := perLesson(2, 8)
	out := newPaidOutlook(bal, []string{"2026-08-01", "2026-08-04"})
	if out.CarriedOver != 0 || out.LastPackFrom != "" {
		t.Fatalf("unexpected outlook: %+v", out)
	}
	got := paidFragment(bal, out)
	if want := "2 из 8 — до 04.08"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A schedule that ran past the forecast horizon leaves "through" unset: no
// date at all beats a date that is too early.
func TestPaidFragmentUnknownHorizon(t *testing.T) {
	bal := perLesson(5, 8)
	out := newPaidOutlook(bal, []string{"2026-08-01", "2026-08-04"})
	if out.Through != "" {
		t.Fatalf("through: got %q, want empty", out.Through)
	}
	if got, want := paidFragment(bal, out), "5 из 8"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPaidFragmentDebt(t *testing.T) {
	bal := perLesson(-2, 8)
	out := newPaidOutlook(bal, nil)
	if got, want := paidFragment(bal, out), "-2"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBalanceStatusLineMarkers(t *testing.T) {
	cases := []struct {
		name string
		bal  model.Balance
		want string
	}{
		{"ok", perLesson(11, 8), "🟢 Осталось оплаченных: 11"},
		{"low", perLesson(2, 8), "🟡 Осталось оплаченных: 2 из 8"},
		{"empty", perLesson(0, 8), "🔴 Нет оплаченных занятий"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := balanceStatusLine(c.bal, newPaidOutlook(c.bal, nil))
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
