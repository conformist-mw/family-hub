package bot

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"familyhub/internal/model"
)

// Tuesday and Thursday, the shape of a twice-a-week course.
var tueThu = []model.Slot{
	{ID: 1, Weekday: int(time.Tuesday), Time: "13:35", Active: true},
	{ID: 2, Weekday: int(time.Thursday), Time: "13:35", Active: true},
}

func perLessonEmpty(price float64, remaining int) model.Balance {
	return model.Balance{
		Enrollment: model.Enrollment{
			Person: "Дитина", Name: "Курс", BillingType: model.BillingPerLesson,
			CurrentPrice: price,
		},
		Remaining: remaining,
	}
}

func at(date string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", date+" 12:00", time.Local)
	return t
}

// On a Thursday the week has no lesson left after today, so "end of week" is
// the same as "today" and is dropped; two weeks on is today plus four — the
// five-lesson pack that used to be worked out by hand.
func TestTopUpOptionsFromThursday(t *testing.T) {
	got := topUpOptions(perLessonEmpty(450, 0), tueThu, nil, at("2026-10-22"))
	want := []topUpOption{
		{Label: "сьогодні", Through: "2026-10-22", Lessons: 1},
		{Label: "+2 тижні", Through: "2026-11-05", Lessons: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestTopUpOptionsFromTuesday(t *testing.T) {
	got := topUpOptions(perLessonEmpty(450, 0), tueThu, nil, at("2026-10-20"))
	want := []topUpOption{
		{Label: "сьогодні", Through: "2026-10-20", Lessons: 1},
		{Label: "до кінця тижня", Through: "2026-10-22", Lessons: 2},
		{Label: "+2 тижні", Through: "2026-11-05", Lessons: 6},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// A lesson held but never paid for is owed whatever horizon is picked.
func TestTopUpOptionsCarryDebt(t *testing.T) {
	got := topUpOptions(perLessonEmpty(450, -1), tueThu, nil, at("2026-10-22"))
	if len(got) != 2 || got[0].Lessons != 2 || got[1].Lessons != 6 {
		t.Errorf("debt should ride on every option, got %+v", got)
	}
}

// A week the trainer is away costs nothing; an inactive slot is not a lesson.
func TestTopUpOptionsSkipAbsencesAndInactiveSlots(t *testing.T) {
	slots := append([]model.Slot{{ID: 3, Weekday: int(time.Monday), Time: "10:00"}}, tueThu...)
	absences := []model.TrainerAbsence{{DateFrom: "2026-10-26", DateTo: "2026-11-01"}}
	got := topUpOptions(perLessonEmpty(450, 0), slots, absences, at("2026-10-22"))
	if len(got) != 2 || got[1].Lessons != 3 || got[1].Through != "2026-11-05" {
		t.Errorf("got %+v", got)
	}
}

func TestTopUpOptionsNeedAPerLessonPrice(t *testing.T) {
	if got := topUpOptions(perLessonEmpty(0, 0), tueThu, nil, at("2026-10-22")); got != nil {
		t.Errorf("no price, no amounts: got %+v", got)
	}
	monthly := perLessonEmpty(450, 0)
	monthly.BillingType = model.BillingMonthly
	if got := topUpOptions(monthly, tueThu, nil, at("2026-10-22")); got != nil {
		t.Errorf("a monthly pass has no per-lesson options: got %+v", got)
	}
}

func TestBalanceWarningShowsTopUpAmounts(t *testing.T) {
	bal := perLessonEmpty(450, -1)
	bal.PaymentInstructions = "ФОП Іваненко, UA12 3456"
	opts := topUpOptions(bal, tueThu, nil, at("2026-10-20"))

	text := balanceWarningText(bal, "13:35", opts)

	want := strings.Join([]string{
		"🔴 Дитина · Курс сьогодні у 13:35: немає оплачених занять",
		"До оплати, по 450 ₴ за заняття (разом із боргом 1 зан.):",
		"• сьогодні — 2 зан. = 900 ₴",
		"• до кінця тижня (по 22.10) — 3 зан. = 1350 ₴",
		"• +2 тижні (по 05.11) — 7 зан. = 3150 ₴",
		"Реквізити: ФОП Іваненко, UA12 3456",
	}, "\n")
	if text != want {
		t.Errorf("got:\n%s\nwant:\n%s", text, want)
	}
}

func TestBalanceWarningShowsMonthlyPrice(t *testing.T) {
	bal := perLessonEmpty(1200, 0)
	bal.BillingType = model.BillingMonthly
	text := balanceWarningText(bal, "18:30", nil)
	if !strings.Contains(text, "\nДо оплати 1200 ₴ за місяць.") {
		t.Errorf("got:\n%s", text)
	}
}
