package audit

import (
	"testing"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

func pack(date string, amount float64, lessons int64) model.Payment {
	return model.Payment{Kind: model.PaymentKindCourse, Date: date, Amount: amount, LessonsPaid: &lessons}
}

func extra(date, label string, amount float64) model.Payment {
	return model.Payment{Kind: model.PaymentKindExtra, Date: date, Amount: amount, Label: label}
}

func done(date string) model.Visit {
	return model.Visit{Date: date, Status: model.StatusDone}
}

// The point of the kind: an extra belongs on the timeline, so the reader
// reconciling a month sees the kimono, but it bought no lessons and so must
// leave the running balance exactly where it found it.
func TestAnExtraSitsInTheTimelineWithoutMovingTheBalance(t *testing.T) {
	rows, _ := BuildLedger(store.AuditData{
		Payments: []model.Payment{pack("2026-09-01", 3200, 8), extra("2026-09-05", "Кімоно", 1500)},
		Visits:   []model.Visit{done("2026-09-03"), done("2026-09-10")},
	})

	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}

	// Ascending by date, with the extra in its own place rather than appended.
	wantKinds := []string{KindPayment, KindVisit, KindExtra, KindVisit}
	for i, want := range wantKinds {
		if rows[i].Kind != want {
			t.Errorf("row %d kind = %q, want %q", i, rows[i].Kind, want)
		}
	}

	kimono := rows[2]
	if kimono.What != "Кімоно" {
		t.Errorf("what = %q, want Кімоно", kimono.What)
	}
	if kimono.Amount != 1500 {
		t.Errorf("amount = %v, want 1500", kimono.Amount)
	}
	if kimono.Lessons != 0 || kimono.Covers != "" {
		t.Errorf("extra carries lessons/coverage: %+v", kimono)
	}

	// 8 bought, one spent before the extra, one after — the extra changes
	// nothing on either side of itself.
	if rows[1].Balance != 7 {
		t.Fatalf("balance before the extra = %d, want 7", rows[1].Balance)
	}
	if kimono.Balance != 7 {
		t.Errorf("balance on the extra = %d, want it unchanged at 7", kimono.Balance)
	}
	if rows[3].Balance != 6 {
		t.Errorf("balance after the extra = %d, want 6", rows[3].Balance)
	}
}

// PaidAmount is the other half of "оплачено за період: N занять (M ₴)". Folding
// an extra into it would leave a total that does not divide by the count.
func TestTheSummaryKeepsExtrasApartFromThePaidTotal(t *testing.T) {
	_, sum := BuildLedger(store.AuditData{
		Payments: []model.Payment{
			pack("2026-09-01", 3200, 8),
			extra("2026-09-05", "Кімоно", 1500),
			extra("2026-09-20", "Поїздка", 2000),
		},
	})

	if sum.PaidLessons != 8 {
		t.Errorf("paid lessons = %d, want 8", sum.PaidLessons)
	}
	if sum.PaidAmount != 3200 {
		t.Errorf("paid amount = %v, want 3200 — the extras leaked in", sum.PaidAmount)
	}
	if sum.ExtrasAmount != 3500 {
		t.Errorf("extras amount = %v, want 3500", sum.ExtrasAmount)
	}
}

// Money arrives before the lesson it pays for, and an extra is money — sorting
// it after the visit would show a course going empty on a day it did not.
func TestAnExtraOnAVisitDateSortsFirst(t *testing.T) {
	rows, _ := BuildLedger(store.AuditData{
		Payments: []model.Payment{extra("2026-09-03", "Кімоно", 1500)},
		Visits:   []model.Visit{done("2026-09-03")},
	})

	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Kind != KindExtra || rows[1].Kind != KindVisit {
		t.Errorf("order = %q, %q", rows[0].Kind, rows[1].Kind)
	}
}

// A course whose only money is an extra has no lessons to report, and the
// opening balance has to survive it untouched — the "з оплати" period can open
// on a month like that.
func TestAPeriodWithOnlyAnExtraLeavesTheBalanceAlone(t *testing.T) {
	rows, sum := BuildLedger(store.AuditData{
		OpeningBalance: 3,
		Payments:       []model.Payment{extra("2026-09-05", "Форма", 900)},
	})

	if len(rows) != 1 || rows[0].Balance != 3 {
		t.Fatalf("rows = %+v, want the balance left at 3", rows)
	}
	if sum.Opening != 3 || sum.Closing != 3 {
		t.Errorf("opening/closing = %d/%d, want 3/3", sum.Opening, sum.Closing)
	}
	if sum.PaidAmount != 0 || sum.PaidLessons != 0 {
		t.Errorf("summary claims lessons were paid for: %+v", sum)
	}
}

// Kind is empty on a row built by code that predates it. An empty kind means a
// course payment, so such a row must still add its lessons to the balance.
func TestAPaymentWithNoKindStillCountsAsACoursePayment(t *testing.T) {
	lessons := int64(4)
	rows, sum := BuildLedger(store.AuditData{
		Payments: []model.Payment{{Date: "2026-09-01", Amount: 1600, LessonsPaid: &lessons}},
	})

	if len(rows) != 1 || rows[0].Kind != KindPayment {
		t.Fatalf("rows = %+v, want one payment row", rows)
	}
	if rows[0].Balance != 4 || sum.PaidAmount != 1600 {
		t.Errorf("row = %+v, summary = %+v", rows[0], sum)
	}
}
