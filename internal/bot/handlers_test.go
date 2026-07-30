package bot

import (
	"testing"

	"lessons/internal/audit"
	"lessons/internal/model"
)

func perLesson(remaining int) model.Balance {
	b := model.Balance{Remaining: remaining}
	b.BillingType = model.BillingPerLesson
	b.LowThreshold = 2
	return b
}

func TestPaidFragmentSinglePack(t *testing.T) {
	bal := perLesson(2)
	packs := []audit.Pack{{Date: "2026-07-18", Size: 8, Left: 2, Through: "2026-08-04"}}
	if got, want := paidFragment(bal, packs), "2 из 8 — до 04.08"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The case this line exists for: a top-up made while lessons remained. One
// "X из Y" would have to pick a pack and lie, so each gets its own line.
func TestPaidFragmentPrepaidSpansPacks(t *testing.T) {
	bal := perLesson(16)
	packs := []audit.Pack{
		{Date: "2026-06-24", Size: 13, Left: 1, Through: "2026-07-27"},
		{Date: "2026-07-26", Size: 15, Left: 15, Through: "2026-09-04"},
	}
	got := paidFragment(bal, packs)
	want := "16\n· 1 из 13 — до 27.07\n· 15 из 15 — до 04.09"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A pack whose lessons run past the forecast horizon shows no date: none beats
// one that is too early.
func TestPaidFragmentUnknownHorizon(t *testing.T) {
	bal := perLesson(5)
	packs := []audit.Pack{{Date: "2026-07-18", Size: 8, Left: 5}}
	if got, want := paidFragment(bal, packs), "5 из 8"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// No breakdown available (debt, or a store failure) — the bare number stands in.
func TestPaidFragmentFallsBackToNumber(t *testing.T) {
	if got, want := paidFragment(perLesson(-2), nil), "-2"; got != want {
		t.Errorf("debt: got %q, want %q", got, want)
	}
	if got, want := paidFragment(perLesson(3), nil), "3"; got != want {
		t.Errorf("no packs: got %q, want %q", got, want)
	}
}

func TestBalanceStatusLineMarkers(t *testing.T) {
	pack := func(left, size int) []audit.Pack {
		return []audit.Pack{{Date: "2026-07-18", Size: size, Left: left, Through: "2026-08-04"}}
	}
	cases := []struct {
		name  string
		bal   model.Balance
		packs []audit.Pack
		want  string
	}{
		{"ok", perLesson(8), pack(8, 8), "🟢 Осталось оплаченных: 8 из 8 — до 04.08"},
		{"low", perLesson(2), pack(2, 8), "🟡 Осталось оплаченных: 2 из 8 — до 04.08"},
		{"empty", perLesson(0), nil, "🔴 Нет оплаченных занятий"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := balanceStatusLine(c.bal, c.packs); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestFormatBalanceLineDropsMonthCounter(t *testing.T) {
	bal := perLesson(2)
	bal.Person, bal.Name, bal.DoneThisMonth = "Демид", "Гимнастика", 13
	packs := []audit.Pack{{Date: "2026-07-18", Size: 8, Left: 2, Through: "2026-08-04"}}
	got := formatBalanceLine(bal, packs)
	want := "⚠️ Демид — Гимнастика: осталось 2 из 8 — до 04.08"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
