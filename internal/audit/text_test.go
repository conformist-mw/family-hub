package audit

import (
	"strings"
	"testing"

	"familyhub/internal/model"
)

// The text version is what gets pasted into Telegram or Viber, so an extra has
// to name itself there too — "оплата (1500 ₴)" against a karate course reads
// as lessons nobody bought.
func TestRenderTextNamesAnExtra(t *testing.T) {
	out := RenderText(View{
		Title:       "Демид · Карате",
		PeriodLabel: "з останньої оплати (01.09) по 30.09",
		BillingType: model.BillingPerLesson,
		Rows: []Row{
			{Date: "2026-09-01", Kind: KindPayment, Amount: 3200, Lessons: 8, Balance: 8},
			{Date: "2026-09-05", Kind: KindExtra, Amount: 1500, What: "Кімоно", Balance: 8},
		},
		Summary: Summary{
			ByStatus: map[string]int{}, PaidLessons: 8, PaidAmount: 3200,
			ExtrasAmount: 1500, Opening: 0, Closing: 8,
		},
	})

	if !strings.Contains(out, "додатково: Кімоно (1500 ₴)") {
		t.Errorf("the extra row is not named:\n%s", out)
	}
	// Its own summary line, so the paid-for-lessons pair stays divisible.
	if !strings.Contains(out, "Оплачено за період: 8 занять (3200 ₴)") {
		t.Errorf("the paid line changed:\n%s", out)
	}
	if !strings.Contains(out, "Додатково за період: 1500 ₴") {
		t.Errorf("the extras total is missing:\n%s", out)
	}
}

// A comment on an extra is a note about the purchase ("розмір 140") and is
// worth carrying, the same as on a payment.
func TestRenderTextKeepsAnExtrasComment(t *testing.T) {
	out := RenderText(View{
		BillingType: model.BillingPerLesson,
		Rows: []Row{
			{Date: "2026-09-05", Kind: KindExtra, Amount: 1500, What: "Кімоно", Comment: "розмір 140"},
		},
		Summary: Summary{ByStatus: map[string]int{}, ExtrasAmount: 1500},
	})

	if !strings.Contains(out, "додатково: Кімоно (1500 ₴) — розмір 140") {
		t.Errorf("comment lost:\n%s", out)
	}
}

// Nothing extra was bought, so no line about it — an always-present "0 ₴"
// would be noise on the majority of courses.
func TestRenderTextOmitsTheExtrasLineWhenThereAreNone(t *testing.T) {
	out := RenderText(View{
		BillingType: model.BillingPerLesson,
		Rows:        []Row{{Date: "2026-09-01", Kind: KindPayment, Amount: 3200, Lessons: 8, Balance: 8}},
		Summary:     Summary{ByStatus: map[string]int{}, PaidLessons: 8, PaidAmount: 3200, Closing: 8},
	})

	if strings.Contains(out, "Додатково") {
		t.Errorf("an extras line appeared with no extras:\n%s", out)
	}
}
