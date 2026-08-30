package reminders

import "testing"

// These are the strings the Mini App's JavaScript produced. It was the only
// implementation, so it is the specification: the port is correct when the
// phone shows what it always showed.
func TestDescribeSaysWhatTheJavaScriptSaid(t *testing.T) {
	for _, tc := range []struct{ rule, want string }{
		{"FREQ=DAILY", "щодня"},
		{"FREQ=DAILY;INTERVAL=2", "через день"},
		{"FREQ=DAILY;INTERVAL=3", "кожні 3 дні"},
		{"FREQ=DAILY;INTERVAL=5", "кожні 5 днів"},
		{"FREQ=DAILY;INTERVAL=21", "кожні 21 день"},

		{"FREQ=WEEKLY", "щотижня"},
		{"FREQ=WEEKLY;BYDAY=TU", "щовівторка"},
		{"FREQ=WEEKLY;BYDAY=SA", "щосуботи"},
		{"FREQ=WEEKLY;BYDAY=FR", "щоп'ятниці"},
		{"FREQ=WEEKLY;BYDAY=MO,WE,FR", "щотижня: пн, ср, пт"},
		{"FREQ=WEEKLY;INTERVAL=2", "раз на 2 тижні"},
		{"FREQ=WEEKLY;INTERVAL=2;BYDAY=SA", "раз на 2 тижні, сб"},
		{"FREQ=WEEKLY;INTERVAL=3;BYDAY=MO", "кожні 3 тижні, пн"},
		{"FREQ=WEEKLY;INTERVAL=6", "кожні 6 тижнів"},

		{"FREQ=MONTHLY", "щомісяця"},
		{"FREQ=MONTHLY;BYMONTHDAY=1", "щомісяця, 1-го"},
		{"FREQ=MONTHLY;BYMONTHDAY=5", "щомісяця, 5-го"},
		{"FREQ=MONTHLY;BYMONTHDAY=-1", "останній день місяця"},
		{"FREQ=MONTHLY;INTERVAL=2;BYMONTHDAY=-1", "останній день кожні 2 місяці"},
		{"FREQ=MONTHLY;INTERVAL=2", "раз на 2 місяці"},
		{"FREQ=MONTHLY;INTERVAL=2;BYMONTHDAY=15", "раз на 2 місяці, 15-го"},
		{"FREQ=MONTHLY;INTERVAL=4", "кожні 4 місяці"},
		{"FREQ=MONTHLY;INTERVAL=7", "кожні 7 місяців"},

		{"FREQ=YEARLY", "щороку"},
		{"FREQ=YEARLY;INTERVAL=2", "кожні 2 роки"},
		{"FREQ=YEARLY;INTERVAL=5", "кожні 5 років"},

		// The prefix appears in every ICS file and every example on the web,
		// so it is what people paste into the raw-rule field.
		{"RRULE:FREQ=DAILY", "щодня"},
		{"  freq=daily  ", "щодня"},
	} {
		if got := Describe(tc.rule); got != tc.want {
			t.Errorf("Describe(%q) = %q, want %q", tc.rule, got, tc.want)
		}
	}
}

// A shape it does not understand is named for what it is. Describing it wrongly
// would be worse than admitting the rule is unusual — the raw text is still in
// the form, which is where a rule is meant to be read.
func TestDescribeRefusesToGuess(t *testing.T) {
	for _, rule := range []string{
		"FREQ=WEEKLY;BYDAY=2SU",  // the second Sunday — positional
		"FREQ=MONTHLY;BYDAY=1FR", // the first Friday
		"FREQ=DAILY;BYDAY=MO",    // daily, but only on Mondays
		"FREQ=YEARLY;BYMONTH=3",  // every March
		"FREQ=HOURLY",            // a frequency it says nothing about
		"",                       // nothing at all
		"nonsense",               // not a rule
	} {
		if got := Describe(rule); got != "за власним правилом" {
			t.Errorf("Describe(%q) = %q, want the honest fallback", rule, got)
		}
	}
}

// Ukrainian plurals are the part most likely to be got wrong in a port:
// 1 день, 2 дні, 5 днів, 11 днів, 21 день.
func TestPluralPicksTheUkrainianForm(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{1, "день"}, {2, "дні"}, {3, "дні"}, {4, "дні"},
		{5, "днів"}, {10, "днів"},
		{11, "днів"}, {12, "днів"}, {13, "днів"}, {14, "днів"},
		{21, "день"}, {22, "дні"}, {25, "днів"},
		{101, "день"}, {111, "днів"}, {112, "днів"},
	} {
		if got := plural(tc.n, "день", "дні", "днів"); got != tc.want {
			t.Errorf("plural(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
