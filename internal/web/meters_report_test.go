package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/reminders"
	"familyhub/internal/store"
)

// fakeNotifier stands in for the bot: the report is a message, and what
// matters is what it says.
type fakeNotifier struct {
	html []string
	err  error
}

func (f *fakeNotifier) NotifyText(text string) error { return f.NotifyHTML(text) }
func (f *fakeNotifier) NotifyHTML(text string) error {
	if f.err != nil {
		return f.err
	}
	f.html = append(f.html, text)
	return nil
}

// reportRouter is the meters fixture wired to a notifier we can read back.
func reportRouter(t *testing.T) (http.Handler, *fakeNotifier) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO addresses (id, name, currency, active, sort_order) VALUES (1, 'Дім', 'UAH', 1, 1)`,
		`INSERT INTO tariffs (id, name, kind, unit, rate1, active) VALUES
			(1, 'Газ', 'meter', 'м3', 9.0, 1),
			(2, 'Охорона', 'flat', NULL, 500.0, 1)`,
		`INSERT INTO utilities (id, address_id, name, current_tariff_id, icon, color, url, active, sort_order) VALUES
			(1, 1, 'Газ', 1, '🔥', '', '', 1, 1),
			(2, 1, 'Охорона', 2, '🛡️', '', '', 1, 2),
			(3, 1, 'Вода', 1, '💧', '', '', 1, 3)`,
		`INSERT INTO readings (id, utility_id, tariff_id, period, prev1, curr1, consumed1, amount, paid_date) VALUES
			(1, 1, 1, '2026-06', 100, 120, 20, 180, '2026-07-01'),
			(2, 2, 2, '2026-06', NULL, NULL, NULL, 500, NULL)`,
	} {
		mustExec(t, database, q)
	}
	notifier := &fakeNotifier{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chores := reminders.NewService(store.New(database), time.Local, logger, time.Now)
	return NewRouter(database, logger, "", nil, notifier, chores), notifier
}

func TestTheReportNamesTheMonthAndWhatItCameTo(t *testing.T) {
	router, notifier := reportRouter(t)

	rec := post(t, router, "/meters/report", url.Values{"address_id": {"1"}, "period": {"2026-06"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("send = %d\n%s", rec.Code, rec.Body.String())
	}
	if len(notifier.html) != 1 {
		t.Fatalf("sent %d messages, want 1", len(notifier.html))
	}
	got := notifier.html[0]

	for _, want := range []string{
		"Дім", "червень 2026",
		"🔥 Газ — 180 ₴",
		"🛡️ Охорона — 500 ₴",
		"Разом: 680 ₴",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// The button sends whatever month is open, so a half-entered one has to report
// itself honestly: a total that quietly left out the unread services would be
// worse than saying which they are.
func TestAHalfEnteredMonthSaysWhatIsMissingAndUnpaid(t *testing.T) {
	router, notifier := reportRouter(t)
	post(t, router, "/meters/report", url.Values{"address_id": {"1"}, "period": {"2026-06"}})
	got := notifier.html[0]

	if !strings.Contains(got, "Не сплачено: <b>500 ₴</b>") {
		t.Errorf("the unpaid figure is missing:\n%s", got)
	}
	if !strings.Contains(got, "Ще не внесено: Вода") {
		t.Errorf("the unread service is not named:\n%s", got)
	}
	if !strings.Contains(got, "не сплачено</i>") {
		t.Errorf("the unpaid line is not marked:\n%s", got)
	}
}

// A month where everything is in and paid — the case the button exists for —
// carries neither warning.
func TestASettledMonthCarriesNoWarnings(t *testing.T) {
	router, notifier := reportRouter(t)
	// Fill in the missing service and pay everything.
	post(t, router, "/meters/readings/new",
		url.Values{"utility_id": {"3"}, "period": {"2026-06"}, "prev1": {"0"}, "curr1": {"10"}, "paid": {"on"}})
	post(t, router, "/meters/readings/2/paid", nil)

	post(t, router, "/meters/report", url.Values{"address_id": {"1"}, "period": {"2026-06"}})
	got := notifier.html[len(notifier.html)-1]

	if strings.Contains(got, "Не сплачено") || strings.Contains(got, "Ще не внесено") {
		t.Fatalf("a settled month still warns:\n%s", got)
	}
	if !strings.Contains(got, "Разом: 770 ₴") { // 180 + 500 + 90
		t.Fatalf("total wrong:\n%s", got)
	}
}

// Nothing is recorded about what was sent, so pressing twice sends twice —
// the decision is the press.
func TestSendingTwiceSendsTwice(t *testing.T) {
	router, notifier := reportRouter(t)
	post(t, router, "/meters/report", url.Values{"address_id": {"1"}, "period": {"2026-06"}})
	post(t, router, "/meters/report", url.Values{"address_id": {"1"}, "period": {"2026-06"}})
	if len(notifier.html) != 2 {
		t.Fatalf("sent %d messages, want 2", len(notifier.html))
	}
}

// The month view says it went, and the button is not offered at all when there
// is no bot to send it.
func TestTheMonthViewOffersTheButtonAndConfirmsTheSend(t *testing.T) {
	router, _ := reportRouter(t)

	body := metersBody(t, router, "/meters?address_id=1&period=2026-06")
	if !strings.Contains(body, `action="/meters/report"`) {
		t.Errorf("no send button:\n%s", body)
	}
	if strings.Contains(body, "Звіт надіслано") {
		t.Error("the confirmation shows without a send")
	}
	if sent := metersBody(t, router, "/meters?address_id=1&period=2026-06&sent=1"); !strings.Contains(sent, "Звіт надіслано") {
		t.Error("no confirmation after a send")
	}

	// The fixture without a notifier is the bot-off shape.
	if body := metersBody(t, metersRouter(t), "/meters?address_id=1&period=2026-06"); strings.Contains(body, `action="/meters/report"`) {
		t.Error("the button is offered with the bot off")
	}
}

func TestSendingWithTheBotOffIsRefused(t *testing.T) {
	rec := post(t, metersRouter(t), "/meters/report", url.Values{"address_id": {"1"}, "period": {"2026-06"}})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("send with no bot = %d, want 503", rec.Code)
	}
}

// An address that is not there is a 404, not a report about nothing.
func TestSendingForAnUnknownAddress(t *testing.T) {
	router, notifier := reportRouter(t)
	if rec := post(t, router, "/meters/report", url.Values{"address_id": {"99"}, "period": {"2026-06"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown address = %d, want 404", rec.Code)
	}
	if len(notifier.html) != 0 {
		t.Fatal("a message went out for an address that does not exist")
	}
}

// The report is HTML, so a service named with angle brackets must not break
// the message or inject into it.
func TestServiceNamesAreEscaped(t *testing.T) {
	router, notifier := reportRouter(t)
	post(t, router, "/meters/utilities/1", url.Values{
		"name": {"Газ <b>2</b>"}, "address_id": {"1"}, "current_tariff_id": {"1"},
	})
	post(t, router, "/meters/report", url.Values{"address_id": {"1"}, "period": {"2026-06"}})

	got := notifier.html[0]
	if strings.Contains(got, "<b>2</b>") {
		t.Fatalf("markup from a name survived:\n%s", got)
	}
	if !strings.Contains(got, "&lt;b&gt;2&lt;/b&gt;") {
		t.Fatalf("name not escaped:\n%s", got)
	}
}
