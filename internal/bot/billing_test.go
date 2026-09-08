package bot

import (
	"strings"
	"testing"

	"familyhub/internal/model"
)

// noticeDays is the form's "N днів" as the column stores it.
func noticeDays(n int) int { return n * model.MinutesPerDay }

func monthlyBalance(daysLeft, noticeMin int, covered bool) model.Balance {
	return model.Balance{
		Enrollment: model.Enrollment{
			Person: "Маша", Name: "Школа",
			BillingType: model.BillingMonthly, CurrentPrice: 12500,
			PaymentNoticeMin: noticeMin,
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
		{"the day before the period ends", monthlyBalance(1, noticeDays(1), true), true},
		{"the last day itself", monthlyBalance(0, noticeDays(1), true), true},
		{"two days out, notice is one day", monthlyBalance(2, noticeDays(1), true), false},
		{"five days out, notice is five days", monthlyBalance(5, noticeDays(5), true), true},
		{"six days out, notice is five days", monthlyBalance(6, noticeDays(5), true), false},
		{"a day the app was down, still inside the window", monthlyBalance(3, noticeDays(5), true), true},
		// A notice shorter than a day cannot resolve finer than "the last day":
		// coverage is measured in whole days.
		{"two hours, on the last day", monthlyBalance(0, 120, true), true},
		{"two hours, a day before", monthlyBalance(1, 120, true), false},
		{"notice 0 means do not warn", monthlyBalance(0, 0, true), false},
		{"summer: nothing is covered", monthlyBalance(0, noticeDays(5), false), false},
		{"per-lesson course", func() model.Balance {
			b := monthlyBalance(1, noticeDays(1), true)
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
		bal := monthlyBalance(days, noticeDays(5), true)
		low := bal.State() == "low"
		if got := dueForBillingReminder(bal); got != low {
			t.Errorf("%d days left: reminder %v, badge low %v", days, got, low)
		}
	}
}

func TestBillingReminderTextCarriesAmountAndDetails(t *testing.T) {
	bal := monthlyBalance(1, noticeDays(5), true)
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
	text := billingReminderText(monthlyBalance(0, noticeDays(5), true))
	if strings.Contains(text, "Реквізити") {
		t.Errorf("no details were set, got:\n%s", text)
	}
	if !strings.Contains(text, "останній день") {
		t.Errorf("the last day should be said plainly, got:\n%s", text)
	}
}

// A per-lesson course that has run out gets its warning from the scheduler,
// not from the billing reminder — and that half used to drop the payment
// details, so the message said money was due without saying where to send it.
func TestBalanceWarningCarriesPaymentDetails(t *testing.T) {
	bal := model.Balance{
		Enrollment: model.Enrollment{
			Person: "Демид", Name: "Логопед", BillingType: model.BillingPerLesson,
			PaymentInstructions: "ФОП Іваненко, UA12 3456",
		},
		Paid: 4, Done: 4, Remaining: 0,
	}

	text := balanceWarningText(bal, "17:00")

	for _, want := range []string{"Демид", "Логопед", "17:00", "немає оплачених занять", "UA12 3456"} {
		if !strings.Contains(text, want) {
			t.Errorf("warning should mention %q, got:\n%s", want, text)
		}
	}
	// The details belong on their own line: the first line is what a phone
	// notification shows, and an IBAN pushed into it hides the course name.
	lines := strings.Split(text, "\n")
	if len(lines) != 2 {
		t.Fatalf("want two lines, got %d:\n%s", len(lines), text)
	}
	if !strings.HasPrefix(lines[1], "Реквізити: ") {
		t.Errorf("second line = %q", lines[1])
	}
}

// A monthly course with no active pass gets the same warning shape, with the
// wording that fits a pass rather than a pack of lessons.
func TestBalanceWarningWordsAMonthlyCourseDifferently(t *testing.T) {
	bal := model.Balance{
		Enrollment: model.Enrollment{
			Person: "Демид", Name: "Карате", BillingType: model.BillingMonthly,
		},
	}
	text := balanceWarningText(bal, "18:30")

	if !strings.Contains(text, "немає активного абонемента") {
		t.Errorf("got:\n%s", text)
	}
	// Nothing was set, so no details line at all — not an empty one.
	if strings.Contains(text, "Реквізити") {
		t.Errorf("no details were set, got:\n%s", text)
	}
	if strings.Contains(text, "\n") {
		t.Errorf("want a single line without details, got:\n%s", text)
	}
}

// The two messages that say "time to pay" must word the details line the same;
// they render it through one helper for that reason.
func TestBothPaymentMessagesShareTheDetailsWording(t *testing.T) {
	const details = "ФОП Іваненко, UA12 3456"

	monthly := monthlyBalance(1, noticeDays(5), true)
	monthly.PaymentInstructions = details

	perLesson := model.Balance{
		Enrollment: model.Enrollment{
			Person: "Демид", Name: "Логопед", BillingType: model.BillingPerLesson,
			PaymentInstructions: details,
		},
	}

	line := "Реквізити: " + details
	if !strings.Contains(billingReminderText(monthly), line) {
		t.Errorf("monthly reminder lost the shared wording:\n%s", billingReminderText(monthly))
	}
	if !strings.Contains(balanceWarningText(perLesson, "17:00"), line) {
		t.Errorf("pre-lesson warning lost the shared wording:\n%s", balanceWarningText(perLesson, "17:00"))
	}
}
