package payments

import (
	"errors"
	"strings"
	"testing"

	"familyhub/internal/model"
	"familyhub/internal/valid"
)

func TestMonthRangeSpansTheWholeCalendarMonth(t *testing.T) {
	cases := []struct {
		in          string
		from, until string
	}{
		{"2026-09", "2026-09-01", "2026-09-30"},
		{"2026-01", "2026-01-01", "2026-01-31"},
		{"2027-02", "2027-02-01", "2027-02-28"},
		{"2028-02", "2028-02-01", "2028-02-29"}, // leap year
		{"2026-12", "2026-12-01", "2026-12-31"},
	}
	for _, c := range cases {
		from, until, err := monthRange(c.in)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.in, err)
			continue
		}
		if from != c.from || until != c.until {
			t.Errorf("%s: got %s..%s, want %s..%s", c.in, from, until, c.from, c.until)
		}
	}
}

func TestMonthRangeRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "2026", "2026-13", "вересень", "2026-09-01"} {
		if _, _, err := monthRange(in); err == nil {
			t.Errorf("%q should not parse as a month", in)
		}
	}
}

func TestParsePerLesson(t *testing.T) {
	p, err := Form{Date: "2026-08-16", Amount: "5000", Lessons: "10", Comment: " готівкою "}.
		Parse(model.BillingPerLesson)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Amount != 5000 || p.LessonsPaid == nil || *p.LessonsPaid != 10 {
		t.Errorf("payment = %+v", p)
	}
	if p.Comment != "готівкою" {
		t.Errorf("comment = %q, want it trimmed", p.Comment)
	}
	// A pack of lessons covers no period — the balance counts lessons, and a
	// stray range would put the course on the monthly side of every query.
	if p.CoversFrom != nil || p.CoversUntil != nil {
		t.Errorf("coverage = %v..%v, want none", p.CoversFrom, p.CoversUntil)
	}
}

func TestParseMonthly(t *testing.T) {
	// The month is the point of the monthly form: a payment made on 28 August
	// can be for September, and it is the coverage that the balance reads.
	p, err := Form{Date: "2026-08-28", Amount: "3200", CoversMonth: "2026-09"}.
		Parse(model.BillingMonthly)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.CoversFrom == nil || *p.CoversFrom != "2026-09-01" || p.CoversUntil == nil || *p.CoversUntil != "2026-09-30" {
		t.Errorf("coverage = %v..%v", p.CoversFrom, p.CoversUntil)
	}
	if p.LessonsPaid != nil {
		t.Errorf("lessons = %v, want none on a monthly course", *p.LessonsPaid)
	}
}

// A monthly course ignores a lesson count and a per-lesson one ignores a
// month, so the enrollment decides which field is required.
func TestParseRequiresTheFieldTheBillingNeeds(t *testing.T) {
	cases := []struct {
		name    string
		billing string
		form    Form
		field   string
	}{
		{"bad date", model.BillingPerLesson, Form{Date: "16.08.2026", Amount: "100", Lessons: "1"}, "date"},
		{"no amount", model.BillingPerLesson, Form{Date: "2026-08-16", Lessons: "1"}, "amount"},
		{"negative amount", model.BillingPerLesson, Form{Date: "2026-08-16", Amount: "-5", Lessons: "1"}, "amount"},
		{"no lessons", model.BillingPerLesson, Form{Date: "2026-08-16", Amount: "100"}, "lessons"},
		{"zero lessons", model.BillingPerLesson, Form{Date: "2026-08-16", Amount: "100", Lessons: "0"}, "lessons"},
		{"month on a per-lesson course", model.BillingPerLesson, Form{Date: "2026-08-16", Amount: "100", CoversMonth: "2026-09"}, "lessons"},
		{"no month", model.BillingMonthly, Form{Date: "2026-08-16", Amount: "100"}, "month"},
		{"lessons on a monthly course", model.BillingMonthly, Form{Date: "2026-08-16", Amount: "100", Lessons: "8"}, "month"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.form.Parse(tc.billing)
			var invalid valid.FieldError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %v, want a field error", err)
			}
			if invalid.Field != tc.field {
				t.Errorf("field = %q, want %q", invalid.Field, tc.field)
			}
		})
	}
}

// An extra carries a label instead of a lesson count or a month, and the
// billing type says nothing about it — the same form has to work on both kinds
// of course.
func TestParseExtraIgnoresTheBilling(t *testing.T) {
	for _, billing := range []string{model.BillingPerLesson, model.BillingMonthly} {
		t.Run(billing, func(t *testing.T) {
			p, err := Form{
				Kind: model.PaymentKindExtra, Date: "2026-09-05",
				Amount: "1500", Label: "  Кімоно  ", Comment: "розмір 140",
			}.Parse(billing)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !p.IsExtra() {
				t.Errorf("kind = %q, want %q", p.Kind, model.PaymentKindExtra)
			}
			if p.Label != "Кімоно" {
				t.Errorf("label = %q, want it trimmed", p.Label)
			}
			// The whole reason for the kind: neither half of a course payment
			// is filled, so no balance query can see this row.
			if p.LessonsPaid != nil || p.CoversFrom != nil || p.CoversUntil != nil {
				t.Errorf("extra carries lessons/coverage: %v %v %v", p.LessonsPaid, p.CoversFrom, p.CoversUntil)
			}
		})
	}
}

// An unnamed extra is a number with no answer to "за що" — unreadable a month
// later, and the one field the person alone can supply.
func TestParseExtraRequiresALabel(t *testing.T) {
	for _, label := range []string{"", "   "} {
		_, err := Form{Kind: model.PaymentKindExtra, Date: "2026-09-05", Amount: "1500", Label: label}.
			Parse(model.BillingPerLesson)
		var invalid valid.FieldError
		if !errors.As(err, &invalid) {
			t.Fatalf("label %q: err = %v, want a field error", label, err)
		}
		if invalid.Field != "label" {
			t.Errorf("label %q: field = %q, want %q", label, invalid.Field, "label")
		}
	}
}

// The store names `kind` in its INSERT and UPDATE, which is precisely when the
// column default stops applying. An empty Kind on the form therefore has to
// come out of Parse as "course" — written as an empty string it would be
// dropped by the `kind = 'course'` filters on PaymentsForEnrollment and
// LastPaymentDate, and nothing would report an error: /packs would go empty.
func TestParseDefaultsToACoursePayment(t *testing.T) {
	cases := []struct {
		name    string
		billing string
		form    Form
	}{
		{"per-lesson", model.BillingPerLesson, Form{Date: "2026-08-16", Amount: "5000", Lessons: "10"}},
		{"monthly", model.BillingMonthly, Form{Date: "2026-08-28", Amount: "3200", CoversMonth: "2026-09"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.form.Parse(tc.billing)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if p.Kind != model.PaymentKindCourse {
				t.Errorf("kind = %q, want %q", p.Kind, model.PaymentKindCourse)
			}
			if p.Label != "" {
				t.Errorf("label = %q, want empty on a course payment", p.Label)
			}
		})
	}
}

// A form rejected for something else is re-rendered from what Parse returned,
// so the kind and the label have to survive the failure — otherwise an extra
// with a typo in the amount redraws as a course payment with its label gone.
func TestParseKeepsTheExtraKindThroughAValidationError(t *testing.T) {
	p, err := Form{Kind: model.PaymentKindExtra, Date: "2026-09-05", Amount: "-5", Label: "Кімоно"}.
		Parse(model.BillingPerLesson)
	if err == nil {
		t.Fatal("a negative amount was accepted")
	}
	if !p.IsExtra() || p.Label != "Кімоно" {
		t.Errorf("payment = %+v, want the extra kind and label kept", p)
	}
}

// A free lesson is still worth recording as paid for.
func TestParseAcceptsZeroAmount(t *testing.T) {
	p, err := Form{Date: "2026-08-16", Amount: "0", Lessons: "1"}.Parse(model.BillingPerLesson)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Amount != 0 {
		t.Errorf("amount = %v", p.Amount)
	}
}

func TestGroupTextNamesWhatTheMoneyBought(t *testing.T) {
	lessons := int64(10)
	from, until := "2026-09-01", "2026-09-30"
	cases := []struct {
		name string
		p    model.Payment
		want string
	}{
		{
			"a pack of lessons",
			model.Payment{Class: "Логопед", Person: "Демид", Amount: 5000, LessonsPaid: &lessons},
			"💸 Оплата (Олег):\n💳 <b>Логопед</b> · Демид — 5000 ₴ · 10 занять",
		},
		{
			"a month",
			model.Payment{Class: "Футбол", Person: "Єгор", Amount: 3200, CoversFrom: &from, CoversUntil: &until},
			"💸 Оплата (Олег):\n💳 <b>Футбол</b> · Єгор — 3200 ₴ · за вересень",
		},
		{
			// The label is what makes an extra legible in the group: without
			// it the message reads as an unexplained sum against the course.
			"an extra",
			model.Payment{Class: "Карате", Person: "Демид", Amount: 1500,
				Kind: model.PaymentKindExtra, Label: "Кімоно"},
			"💸 Оплата (Олег):\n💳 <b>Карате</b> · Демид — 1500 ₴ · Кімоно",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GroupAddText(tc.p, "Олег"); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// Telegram parses these as HTML, so a course named with an ampersand must not
// be able to break the message — or, worse, inject markup.
func TestGroupTextEscapes(t *testing.T) {
	p := model.Payment{Class: "Танці & спорт", Person: "<b>Демид</b>", Amount: 100}
	got := GroupDeleteText(p, "Оля & Олег")
	if strings.Contains(got, "<b>Демид</b>") || !strings.Contains(got, "Танці &amp; спорт") {
		t.Errorf("unescaped: %q", got)
	}
	if !strings.Contains(got, "(Оля &amp; Олег)") {
		t.Errorf("byline not escaped: %q", got)
	}
}

// No byline is better than a made-up one, and the surfaces that cannot name
// the author send an empty string.
func TestGroupTextWithoutAuthor(t *testing.T) {
	got := GroupChangeText(model.Payment{Class: "Логопед", Amount: 500}, "")
	if !strings.HasPrefix(got, "🔄 Оплату змінено:\n") {
		t.Errorf("got %q", got)
	}
}

// Format appends what covers() returns without escaping it, which was safe
// while that was only ever a numeral or a month name. A label is typed by a
// person, and an unescaped "&" makes Telegram reject the whole message — after
// the row is already saved, so the group would hear nothing at all.
func TestGroupTextEscapesTheExtraLabel(t *testing.T) {
	p := model.Payment{
		Class: "Карате", Person: "Демид", Amount: 1500,
		Kind: model.PaymentKindExtra, Label: "Кімоно & пояс <9>",
	}
	got := GroupAddText(p, "Олег")
	if strings.Contains(got, "Кімоно & пояс <9>") {
		t.Errorf("the label went in raw: %q", got)
	}
	if !strings.Contains(got, "Кімоно &amp; пояс &lt;9&gt;") {
		t.Errorf("got %q, want the label escaped", got)
	}
}
