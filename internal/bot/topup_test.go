package bot

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"

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

// Each option gets its button, carrying the amount the message showed.
func TestTopUpMarkupCarriesTheShownAmount(t *testing.T) {
	bal := perLessonEmpty(450, -1)
	bal.ID = 42
	m := topUpMarkup(bal, topUpOptions(bal, tueThu, nil, at("2026-10-22")))
	if m == nil || len(m.InlineKeyboard) != 1 || len(m.InlineKeyboard[0]) != 2 {
		t.Fatalf("want one row of two buttons, got %+v", m)
	}
	btn := m.InlineKeyboard[0][1]
	if btn.Text != "✓ Оплатив 6" {
		t.Errorf("text = %q", btn.Text)
	}
	if !strings.HasSuffix(btn.Data, "42:6:2700") {
		t.Errorf("data = %q", btn.Data)
	}
	if topUpMarkup(bal, nil) != nil {
		t.Error("no options, no buttons")
	}
}

func TestParsePaidData(t *testing.T) {
	eid, lessons, amount, err := parsePaidData("42:5:2250.50")
	if err != nil || eid != 42 || lessons != 5 || amount != "2250.50" {
		t.Errorf("got %d %d %q %v", eid, lessons, amount, err)
	}
	for _, bad := range []string{"", "42:5", "x:5:100", "42:0:100", "42:5:abc"} {
		if _, _, _, err := parsePaidData(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

// paidCtx is the slice of a callback context onPaidTap reads and writes.
type paidCtx struct {
	tele.Context
	data      string
	msg       *tele.Message
	responses []string
	edited    string
}

func (c *paidCtx) Data() string           { return c.data }
func (c *paidCtx) Message() *tele.Message { return c.msg }
func (c *paidCtx) Sender() *tele.User     { return &tele.User{ID: 7, FirstName: "Аня"} }
func (c *paidCtx) Edit(what any, _ ...any) error {
	c.edited = fmt.Sprint(what)
	return nil
}
func (c *paidCtx) Respond(r ...*tele.CallbackResponse) error {
	if len(r) > 0 {
		c.responses = append(c.responses, r[0].Text)
	}
	return nil
}

// A tap records the payment and turns the warning into its answer; a second
// tap on the same message records nothing.
func TestPaidTapRecordsOnePayment(t *testing.T) {
	b := menuBot(t)
	eid, err := b.store.CreateEnrollment(model.Enrollment{
		Person: "Дитина", Name: "Курс", BillingType: model.BillingPerLesson,
		CurrentPrice: 450, AttendanceMode: model.AttendancePerSession, Active: true,
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	warning := "🔴 Дитина · Курс сьогодні у 13:35: немає оплачених занять\nДо оплати…\nРеквізити: …"
	msg := &tele.Message{ID: 10, Chat: &tele.Chat{ID: -100}, Text: warning}
	data := fmt.Sprintf("%d:5:2250", eid)

	c := &paidCtx{data: data, msg: msg}
	if err := b.onPaidTap(c); err != nil {
		t.Fatalf("tap: %v", err)
	}
	if !strings.HasPrefix(c.edited, "✅ Дитина · Курс сьогодні у 13:35: немає оплачених занять\nОплачено 5 зан. = 2250 ₴ · Аня") {
		t.Errorf("edited = %q", c.edited)
	}
	if strings.Contains(c.edited, "Реквізити") {
		t.Errorf("the answered warning should drop the details: %q", c.edited)
	}

	again := &paidCtx{data: data, msg: msg}
	if err := b.onPaidTap(again); err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if again.edited != "" || len(again.responses) != 1 || again.responses[0] != "Вже записано" {
		t.Errorf("second tap: edited %q, responses %v", again.edited, again.responses)
	}

	bal, err := b.store.BalanceFor(eid)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if bal.Paid != 5 {
		t.Errorf("paid = %d, want 5 from one payment", bal.Paid)
	}
}
