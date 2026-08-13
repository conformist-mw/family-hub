package bot

import (
	"strings"
	"testing"

	"familyhub/internal/model"
)

func monthlyBalance(daysLeft, threshold int, covered bool) model.Balance {
	return model.Balance{
		Enrollment: model.Enrollment{
			Person: "Маша", Name: "Школа",
			BillingType: model.BillingMonthly, CurrentPrice: 12500,
			LowThreshold: threshold,
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
		want bool
	}{
		{"the day before the period ends", monthlyBalance(1, 1, true), true},
		{"the last day itself", monthlyBalance(0, 1, true), true},
		{"two days out, threshold is one", monthlyBalance(2, 1, true), false},
		{"five days out, threshold is five", monthlyBalance(5, 5, true), true},
		{"six days out, threshold is five", monthlyBalance(6, 5, true), false},
		{"a day the app was down, still inside the window", monthlyBalance(3, 5, true), true},
		{"threshold 0 means do not warn", monthlyBalance(0, 0, true), false},
		{"summer: nothing is covered", monthlyBalance(0, 5, false), false},
		{"per-lesson course", func() model.Balance {
			b := monthlyBalance(1, 1, true)
			b.BillingType = model.BillingPerLesson
			return b
		}(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dueForBillingReminder(c.bal); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// The badge and the message must not disagree about when it is time to pay.
func TestBillingReminderFiresWhenTheBadgeGoesYellow(t *testing.T) {
	for days := 0; days <= 7; days++ {
		bal := monthlyBalance(days, 5, true)
		low := bal.State() == "low"
		if got := dueForBillingReminder(bal); got != low {
			t.Errorf("%d days left: reminder %v, badge low %v", days, got, low)
		}
	}
}

func TestBillingReminderTextCarriesAmountAndDetails(t *testing.T) {
	bal := monthlyBalance(1, 5, true)
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
	text := billingReminderText(monthlyBalance(0, 5, true))
	if strings.Contains(text, "Реквізити") {
		t.Errorf("no details were set, got:\n%s", text)
	}
	if !strings.Contains(text, "останній день") {
		t.Errorf("the last day should be said plainly, got:\n%s", text)
	}
}
