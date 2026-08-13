package bot

import (
	"strings"
	"testing"

	"familyhub/internal/model"
)

func monthlyBalance(daysLeft int, covered bool) model.Balance {
	return model.Balance{
		Enrollment: model.Enrollment{
			Person: "Маша", Name: "Школа",
			BillingType: model.BillingMonthly, CurrentPrice: 12500,
		},
		CoveredNow:  covered,
		CoversUntil: "2026-09-30",
		DaysLeft:    daysLeft,
	}
}

func TestBillingReminderWindow(t *testing.T) {
	cases := []struct {
		name string
		bal  model.Balance
		lead int
		want bool
	}{
		{"the day before the period ends", monthlyBalance(1, true), 1, true},
		{"the last day itself", monthlyBalance(0, true), 1, true},
		{"a day the app was down, still inside the window", monthlyBalance(0, true), 1, true},
		{"two days out, lead is one", monthlyBalance(2, true), 1, false},
		{"two days out, lead is three", monthlyBalance(2, true), 3, true},
		{"summer: nothing is covered", monthlyBalance(0, false), 1, false},
		{"per-lesson course", func() model.Balance {
			b := monthlyBalance(1, true)
			b.BillingType = model.BillingPerLesson
			return b
		}(), 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dueForBillingReminder(c.bal, c.lead); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestBillingReminderTextCarriesAmountAndDetails(t *testing.T) {
	bal := monthlyBalance(1, true)
	bal.PaymentInstructions = "ФОП Іваненко, UA12 3456"

	text := billingReminderText(bal)
	for _, want := range []string{"Маша", "Школа", "30.09", "завтра", "12500", "UA12 3456"} {
		if !strings.Contains(text, want) {
			t.Errorf("reminder should mention %q, got:\n%s", want, text)
		}
	}
}

// Without details there is simply no line for them — not an empty one.
func TestBillingReminderTextOmitsMissingDetails(t *testing.T) {
	text := billingReminderText(monthlyBalance(0, true))
	if strings.Contains(text, "Реквізити") {
		t.Errorf("no details were set, got:\n%s", text)
	}
	if !strings.Contains(text, "останній день") {
		t.Errorf("the last day should be said plainly, got:\n%s", text)
	}
}
