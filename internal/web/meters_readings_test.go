package web

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// ── writing ──────────────────────────────────────────────────────────────────

// metersDB is metersRouter's fixture with the database handed back, so a write
// test can check what actually landed rather than only what was rendered.
func metersDB(t *testing.T) (http.Handler, *sql.DB) {
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
		`INSERT INTO tariffs (id, name, kind, unit, rate1, rate2, active) VALUES
			(1, 'Газ', 'meter', 'м3', 9.0, NULL, 1),
			(2, 'Світло д/н', 'meter_zoned', 'кВт', 4.0, 2.0, 1),
			(3, 'Охорона', 'flat', NULL, 500.0, NULL, 1),
			(4, 'Газ новий', 'meter', 'м3', 10.0, NULL, 1)`,
		`INSERT INTO utilities (id, address_id, name, current_tariff_id, icon, color, url, active, sort_order) VALUES
			(1, 1, 'Газ', 1, '🔥', '', '', 1, 1),
			(2, 1, 'Світло', 2, '⚡', '', '', 1, 2),
			(3, 1, 'Охорона', 3, '🛡️', '', '', 1, 3),
			(4, 1, 'Без тарифу', NULL, '', '', '', 1, 4)`,
	} {
		mustExec(t, database, q)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chores := reminders.NewService(store.New(database), time.Local, logger, time.Now)
	return NewRouter(database, logger, "", nil, nil, chores), database
}

func readingRow(t *testing.T, database *sql.DB, id int64) (amount float64, c1, c2 sql.NullFloat64, tariff int64) {
	t.Helper()
	err := database.QueryRow(`SELECT amount, consumed1, consumed2, tariff_id FROM readings WHERE id = ?`, id).
		Scan(&amount, &c1, &c2, &tariff)
	if err != nil {
		t.Fatalf("read back %d: %v", id, err)
	}
	return
}

// The three kinds price differently, and the form must not be the place that
// decides how — ComputeAmount is.
func TestCreatingAReadingPricesItByItsTariffKind(t *testing.T) {
	for _, tc := range []struct {
		name       string
		utility    string
		form       url.Values
		wantAmount float64
		wantC1     float64
		wantC2     float64
	}{
		{"meter", "1", url.Values{"prev1": {"100"}, "curr1": {"120"}}, 180, 20, 0},
		{"zoned", "2", url.Values{"prev1": {"10"}, "curr1": {"40"}, "prev2": {"5"}, "curr2": {"9"}}, 128, 30, 4},
		{"flat", "3", url.Values{}, 500, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, database := metersDB(t)
			form := url.Values{"utility_id": {tc.utility}, "period": {"2026-06"}}
			for k, v := range tc.form {
				form[k] = v
			}
			if rec := post(t, router, "/meters/readings/new", form); rec.Code != http.StatusSeeOther {
				t.Fatalf("POST = %d\n%s", rec.Code, rec.Body.String())
			}
			amount, c1, c2, _ := readingRow(t, database, 1)
			if amount != tc.wantAmount {
				t.Fatalf("amount = %v, want %v", amount, tc.wantAmount)
			}
			if tc.wantC1 != 0 && (!c1.Valid || c1.Float64 != tc.wantC1) {
				t.Fatalf("consumed1 = %v, want %v", c1, tc.wantC1)
			}
			if tc.wantC2 != 0 && (!c2.Valid || c2.Float64 != tc.wantC2) {
				t.Fatalf("consumed2 = %v, want %v", c2, tc.wantC2)
			}
			// A flat tariff has no meter, so nothing must be stored as if it did.
			if tc.name == "flat" && (c1.Valid || c2.Valid) {
				t.Fatalf("a flat reading stored meter figures: %v %v", c1, c2)
			}
		})
	}
}

// The month a meter is replaced carries two readings under two tariffs. The
// same tariff twice in one period is a double entry and must be refused.
func TestAReplacementMonthTakesTwoReadingsButNotTwoOfTheSame(t *testing.T) {
	router, database := metersDB(t)
	first := url.Values{"utility_id": {"1"}, "period": {"2026-06"}, "prev1": {"100"}, "curr1": {"120"}}

	if rec := post(t, router, "/meters/readings/new", first); rec.Code != http.StatusSeeOther {
		t.Fatalf("first reading = %d", rec.Code)
	}
	// Same period, same tariff — a duplicate, and the form has to say so
	// rather than return a 500.
	rec := post(t, router, "/meters/readings/new", first)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "уже є") {
		t.Fatalf("no duplicate message:\n%s", rec.Body.String())
	}

	// The meter is replaced: the utility moves to the new tariff, and the same
	// period now takes a second reading.
	mustExec(t, database, `UPDATE utilities SET current_tariff_id = 4 WHERE id = 1`)
	second := url.Values{"utility_id": {"1"}, "period": {"2026-06"}, "prev1": {"0"}, "curr1": {"5"}}
	if rec := post(t, router, "/meters/readings/new", second); rec.Code != http.StatusSeeOther {
		t.Fatalf("second reading = %d\n%s", rec.Code, rec.Body.String())
	}

	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM readings WHERE utility_id = 1 AND period = '2026-06'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("got %d readings in the replacement month, want 2", n)
	}
	if amount, _, _, tariff := readingRow(t, database, 2); amount != 50 || tariff != 4 {
		t.Fatalf("second reading = %v at tariff %d, want 50 at 4", amount, tariff)
	}
}

// Change the price next year and this month must keep the number it was
// actually billed at.
func TestEditingAReadingKeepsTheTariffItWasBilledAt(t *testing.T) {
	router, database := metersDB(t)
	post(t, router, "/meters/readings/new",
		url.Values{"utility_id": {"1"}, "period": {"2026-06"}, "prev1": {"100"}, "curr1": {"120"}})

	// The utility moves to a dearer tariff, as it would next year.
	mustExec(t, database, `UPDATE utilities SET current_tariff_id = 4 WHERE id = 1`)

	rec := post(t, router, "/meters/readings/1",
		url.Values{"period": {"2026-06"}, "prev1": {"100"}, "curr1": {"130"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update = %d\n%s", rec.Code, rec.Body.String())
	}
	amount, _, _, tariff := readingRow(t, database, 1)
	if tariff != 1 {
		t.Fatalf("tariff moved to %d — a past month was repriced", tariff)
	}
	if amount != 270 { // 30 × 9, the old rate, not 30 × 10
		t.Fatalf("amount = %v, want 270 at the tariff it was billed at", amount)
	}
}

// A blank meter box means "not read yet", which is a different fact from a
// reading of zero — and it decides whether the month has an amount at all.
func TestABlankMeterIsNotAZeroReading(t *testing.T) {
	router, database := metersDB(t)
	post(t, router, "/meters/readings/new",
		url.Values{"utility_id": {"1"}, "period": {"2026-06"}, "prev1": {"100"}, "curr1": {""}})

	amount, c1, _, _ := readingRow(t, database, 1)
	if c1.Valid {
		t.Fatalf("consumed1 = %v, want NULL for an unread meter", c1)
	}
	if amount != 0 {
		t.Fatalf("amount = %v, want 0", amount)
	}
}

// This month's "previous" is last month's "current", so only the new numbers
// have to be typed.
func TestTheFormCarriesLastMonthsNumbersForward(t *testing.T) {
	router, _ := metersDB(t)
	post(t, router, "/meters/readings/new",
		url.Values{"utility_id": {"2"}, "period": {"2026-06"},
			"prev1": {"10"}, "curr1": {"40"}, "prev2": {"5"}, "curr2": {"9"}})

	body := metersBody(t, router, "/meters/readings/new?utility_id=2&period=2026-07")
	if !strings.Contains(body, `name="prev1" value="40"`) {
		t.Errorf("day zone did not carry forward:\n%s", body)
	}
	if !strings.Contains(body, `name="prev2" value="9"`) {
		t.Error("night zone did not carry forward")
	}
}

// A nil pointer is an unread meter. Rendered straight, Go prints it as the
// literal "<nil>", which lands in the input and is then submitted as the
// reading — caught by opening the form against real data, not by a handler
// test that only checked the status code.
func TestAnEmptyMeterFieldRendersEmpty(t *testing.T) {
	router, _ := metersDB(t)
	body := metersBody(t, router, "/meters/readings/new?utility_id=2&period=2026-06")

	if strings.Contains(body, "nil") {
		t.Fatalf("a nil meter figure reached the form:\n%s", body)
	}
	if !strings.Contains(body, `name="curr1" value=""`) {
		t.Errorf("an unread meter is not an empty field:\n%s", body)
	}
	if !strings.Contains(body, `name="reading_date" value=""`) {
		t.Error("an absent reading date is not an empty field")
	}
}

// A utility nobody has priced has no amount to compute, so the form refuses
// rather than writing a silent 0 ₴ into the month.
func TestAUtilityWithoutATariffCannotBeRead(t *testing.T) {
	router, _ := metersDB(t)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/meters/readings/new?utility_id=4", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET = %d, want 404", rec.Code)
	}
}

func TestThePaidToggleMarksAndUnmarks(t *testing.T) {
	router, database := metersDB(t)
	post(t, router, "/meters/readings/new",
		url.Values{"utility_id": {"3"}, "period": {"2026-06"}})

	paidDate := func() sql.NullString {
		var d sql.NullString
		if err := database.QueryRow(`SELECT paid_date FROM readings WHERE id = 1`).Scan(&d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	if paidDate().Valid {
		t.Fatal("a new reading starts paid")
	}
	if rec := post(t, router, "/meters/readings/1/paid", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("toggle = %d", rec.Code)
	}
	if !paidDate().Valid {
		t.Fatal("the toggle did not mark it paid")
	}
	// The mistake the toggle has to undo is its own.
	post(t, router, "/meters/readings/1/paid", nil)
	if paidDate().Valid {
		t.Fatal("the toggle did not unmark it")
	}
}

// Every write returns to the month it belongs to, not to whichever month is
// current.
func TestAWriteReturnsToItsOwnMonth(t *testing.T) {
	router, _ := metersDB(t)
	rec := post(t, router, "/meters/readings/new",
		url.Values{"utility_id": {"3"}, "period": {"2024-02"}})
	if got, want := rec.Header().Get("Location"), "/meters?address_id=1&period=2024-02"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestDeletingAReading(t *testing.T) {
	router, database := metersDB(t)
	post(t, router, "/meters/readings/new",
		url.Values{"utility_id": {"3"}, "period": {"2026-06"}})

	if rec := post(t, router, "/meters/readings/1/delete", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d", rec.Code)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM readings`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d readings left", n)
	}
	// A second delete of the same id is not a 500.
	if rec := post(t, router, "/meters/readings/1/delete", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("deleting a gone reading = %d, want 404", rec.Code)
	}
}
