package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/reminders"
	"familyhub/internal/store"
)

// metersRouter is the smoke fixture plus utilities data: two properties, a
// month that is fully entered and paid, one that is entered and unpaid, and a
// service nobody has read.
func metersRouter(t *testing.T) http.Handler {
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
		`INSERT INTO addresses (id, name, comment, area, currency, active, sort_order) VALUES
			(1, 'Дім', 'вул. Прикладна, 1', 120.5, 'UAH', 1, 1),
			(2, 'Тьоща', '', NULL, 'UAH', 1, 2),
			(3, 'Старе', '', NULL, 'UAH', 0, 3)`,
		`INSERT INTO tariffs (id, name, kind, unit, rate1, rate2, active) VALUES
			(1, 'Газ 2026', 'meter', 'м3', 9.0, NULL, 1),
			(2, 'Світло д/н', 'meter_zoned', 'кВт', 4.0, 2.0, 1),
			(3, 'Охорона', 'flat', NULL, 500.0, NULL, 1)`,
		`INSERT INTO utilities (id, address_id, name, current_tariff_id, icon, color, url, active, sort_order) VALUES
			(1, 1, 'Газ', 1, '🔥', '', 'https://gas.example', 1, 1),
			(2, 1, 'Світло', 2, '⚡', '', '', 1, 2),
			(3, 1, 'Нечитаний', 1, '💧', '', '', 1, 3),
			(4, 2, 'Охорона', 3, '🛡️', '', '', 1, 1),
			(5, 1, 'Архівний', NULL, '', '', '', 0, 9)`,
		`INSERT INTO readings (id, utility_id, tariff_id, period, prev1, curr1, consumed1, prev2, curr2, consumed2, amount, paid_date) VALUES
			(1, 1, 1, '2026-06', 100, 120, 20, NULL, NULL, NULL, 180, '2026-07-01'),
			(2, 2, 2, '2026-06', 10, 40, 30, 5, 9, 4, 128, NULL),
			(3, 4, 3, '2026-06', NULL, NULL, NULL, NULL, NULL, NULL, 500, NULL)`,
	} {
		mustExec(t, database, q)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chores := reminders.NewService(store.New(database), time.Local, logger, time.Now)
	return NewRouter(database, logger, "", nil, nil, chores)
}

func metersBody(t *testing.T, router http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d\n%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// The month view answers two questions: what has not been entered, and what
// has not been paid.
func TestTheMonthViewNamesWhatIsMissingAndUnpaid(t *testing.T) {
	body := metersBody(t, metersRouter(t), "/meters?address_id=1&period=2026-06")

	if !strings.Contains(body, "червень 2026") {
		t.Fatalf("no month heading:\n%s", body)
	}
	for _, want := range []string{"Газ", "Світло", "Нечитаний", "сплачено", "не сплачено", "немає"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// The archived utility is not a gap anybody has to fill in.
	if strings.Contains(body, "Архівний") {
		t.Error("an archived utility is listed as missing")
	}
	// Another property's service does not leak into this one's month.
	if strings.Contains(body, "Охорона") {
		t.Error("a service from the other property is listed")
	}
	// 180 paid + 128 unpaid; the unpaid figure is the one being chased.
	if !strings.Contains(body, "308 ₴") || !strings.Contains(body, "128 ₴") {
		t.Errorf("totals wrong:\n%s", body)
	}
	if !strings.Contains(body, "не внесено <b>1</b>") {
		t.Error("the count of unentered services is not shown")
	}
}

// A month nobody has touched is the state the screen exists to make visible,
// not an error and not an empty page.
func TestAnUnenteredMonthListsEveryServiceAsMissing(t *testing.T) {
	body := metersBody(t, metersRouter(t), "/meters?address_id=1&period=2026-07")

	if strings.Count(body, "badge-empty") != 3 {
		t.Fatalf("want three services marked missing:\n%s", body)
	}
	if !strings.Contains(body, "не внесено <b>3</b>") {
		t.Error("the count is wrong for a month with nothing in it")
	}
}

// Walking back is always possible; walking forward past the current month is
// not, because there is nothing there to enter.
func TestTheMonthWalksBackButNotIntoTheFuture(t *testing.T) {
	router := metersRouter(t)

	past := metersBody(t, router, "/meters?address_id=1&period=2026-06")
	if !strings.Contains(past, "period=2026-05") || !strings.Contains(past, "period=2026-07") {
		t.Errorf("a past month offers both directions:\n%s", past)
	}

	now := time.Now().Format("2006-01")
	body := metersBody(t, router, "/meters?address_id=1&period="+now)
	next := time.Now().AddDate(0, 1, 0).Format("2006-01")
	if strings.Contains(body, "period="+next) {
		t.Errorf("the current month offers a step into the future (%s)", next)
	}
}

// A typo in a hand-edited URL must not render as "nothing was entered".
func TestARubbishPeriodFallsBackToThisMonth(t *testing.T) {
	body := metersBody(t, metersRouter(t), "/meters?address_id=1&period=nonsense")
	if !strings.Contains(body, periodLabelFor(time.Now())) {
		t.Fatalf("did not fall back to the current month:\n%s", body)
	}
}

func periodLabelFor(t time.Time) string {
	return monthNames[t.Month()-1] + " " + t.Format("2006")
}

// An address id that is not one of the active ones must not silently widen the
// view to another property.
func TestAnUnknownAddressFallsBackToTheFirst(t *testing.T) {
	body := metersBody(t, metersRouter(t), "/meters?address_id=999&period=2026-06")
	if strings.Contains(body, "Охорона") {
		t.Error("an unknown address showed another property's services")
	}
	if !strings.Contains(body, "Газ") {
		t.Error("an unknown address showed nothing at all")
	}
}
