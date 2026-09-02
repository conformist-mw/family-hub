package web

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// thisMonth and lastMonth put the fixture inside the twelve-month window the
// page shows, so the test does not go stale with the calendar.
func thisMonth() string { return time.Now().Format("2006-01") }
func lastMonth() string { return time.Now().AddDate(0, -1, 0).Format("2006-01") }

// statsRouter has both worlds spending money in the same months, which is the
// only case the overview exists to show.
func statsRouter(t *testing.T) http.Handler {
	t.Helper()
	router, database := metersDB(t)
	mustExec(t, database, `INSERT INTO readings (utility_id, tariff_id, period, prev1, curr1, consumed1, amount, paid_date)
		VALUES (1, 1, '`+lastMonth()+`', 100, 120, 20, 180, '2026-07-01')`)
	mustExec(t, database, `INSERT INTO readings (utility_id, tariff_id, period, amount, paid_date)
		VALUES (3, 3, '`+thisMonth()+`', 500, NULL)`)
	// A lessons payment in the same month, so the row carries both halves.
	mustExec(t, database, `INSERT INTO persons (id, name) VALUES (1, 'Дьома')`)
	mustExec(t, database, `INSERT INTO enrollments (id, person_id, name, billing_type, current_price)
		VALUES (1, 1, 'Футбол', 'per_lesson', 300)`)
	mustExec(t, database, `INSERT INTO payments (enrollment_id, date, amount, lessons_paid)
		VALUES (1, '`+thisMonth()+`-05', 2400, 8)`)
	return router
}

// The point of the page: what a month cost altogether, and where it went.
func TestTheOverviewPutsBothWorldsInOneMonth(t *testing.T) {
	body := metersBody(t, statsRouter(t), "/stats")

	// Totals, not individual rows: lessons 2400, utilities 180 + 500, and the
	// two together.
	for _, want := range []string{"Заняття", "Комуналка", "За 12 місяців",
		"2400 ₴", "680 ₴", "3080 ₴"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// One split bar per month rather than two charts to compare by eye.
	if !strings.Contains(body, "seg-lessons") || !strings.Contains(body, "seg-meters") {
		t.Errorf("the bar is not split by world:\n%s", body)
	}
	if !strings.Contains(body, "/stats/lessons") || !strings.Contains(body, "/stats/meters") {
		t.Error("the overview does not lead into either world")
	}
}

// A month with one world's spend and not the other still has to appear: the
// two sources cover ranges that need not match.
func TestAMonthWithOnlyOneWorldStillAppears(t *testing.T) {
	body := metersBody(t, statsRouter(t), "/stats")
	if !strings.Contains(body, periodLabelOf(lastMonth())) {
		t.Errorf("a utilities-only month is missing:\n%s", body)
	}
	if !strings.Contains(body, periodLabelOf(thisMonth())) {
		t.Error("the current month is missing")
	}
}

func TestTheUtilitiesStatsShowMonthsAndComparison(t *testing.T) {
	body := metersBody(t, statsRouter(t), "/stats/meters")

	if !strings.Contains(body, "По місяцях") {
		t.Errorf("no monthly breakdown:\n%s", body)
	}
	if !strings.Contains(body, "Дім") {
		t.Error("the property is not named in the legend")
	}
	// Nothing is shared between properties in this fixture, so the comparison
	// section is absent rather than empty — a chooser with nothing to choose
	// is worse than no chooser.
	if strings.Contains(body, "Споживання") {
		t.Error("the comparison is offered with no shared service")
	}
}

// The comparison appears once the same service exists at two properties, and
// defaults to one rather than making you pick before seeing anything.
func TestTheComparisonAppearsWhenAServiceIsShared(t *testing.T) {
	router, database := metersDB(t)
	mustExec(t, database, `INSERT INTO addresses (id, name, currency, active, sort_order) VALUES (2, 'Тьоща', 'UAH', 1, 2)`)
	mustExec(t, database, `INSERT INTO utilities (id, address_id, name, current_tariff_id, active, sort_order)
		VALUES (10, 2, 'Газ', 1, 1, 1)`)
	for _, q := range []string{
		`INSERT INTO readings (utility_id, tariff_id, period, prev1, curr1, consumed1, amount) VALUES (1, 1, '` + thisMonth() + `', 0, 25, 25, 225)`,
		`INSERT INTO readings (utility_id, tariff_id, period, prev1, curr1, consumed1, amount) VALUES (10, 1, '` + thisMonth() + `', 0, 7, 7, 63)`,
	} {
		mustExec(t, database, q)
	}

	body := metersBody(t, router, "/stats/meters")
	if !strings.Contains(body, "Споживання") {
		t.Fatalf("no comparison for a shared service:\n%s", body)
	}
	if !strings.Contains(body, `value="Газ" selected`) {
		t.Error("the comparison does not default to a service")
	}
	if !strings.Contains(body, "м3") {
		t.Error("the unit is not labelled")
	}
	for _, want := range []string{"Дім", "Тьоща"} {
		if !strings.Contains(body, want) {
			t.Errorf("%q is missing from the comparison", want)
		}
	}
}

// An unknown service in the query must not widen the comparison to everything.
func TestAnUnknownServiceFallsBackToTheFirstShared(t *testing.T) {
	router, database := metersDB(t)
	mustExec(t, database, `INSERT INTO addresses (id, name, currency, active, sort_order) VALUES (2, 'Тьоща', 'UAH', 1, 2)`)
	mustExec(t, database, `INSERT INTO utilities (id, address_id, name, current_tariff_id, active, sort_order)
		VALUES (10, 2, 'Газ', 1, 1, 1)`)
	mustExec(t, database, `INSERT INTO readings (utility_id, tariff_id, period, prev1, curr1, consumed1, amount) VALUES (1, 1, '`+thisMonth()+`', 0, 25, 25, 225)`)

	body := metersBody(t, router, "/stats/meters?utility=Немає")
	if !strings.Contains(body, `value="Газ" selected`) {
		t.Errorf("an unknown service did not fall back:\n%s", body)
	}
}

// Empty data is a page that says so.
func TestTheStatisticsPagesOnAnEmptyDatabase(t *testing.T) {
	router := smokeRouter(t)
	if body := metersBody(t, router, "/stats/meters"); !strings.Contains(body, "Показань немає") {
		t.Errorf("/stats/meters does not say it is empty:\n%s", body)
	}
	// The overview still renders: lessons exist in the smoke fixture.
	metersBody(t, router, "/stats")
}

// The statistics world's navigation gains its third entry here, and it is the
// page's own tab that highlights.
func TestTheStatisticsWorldHasThreeTabs(t *testing.T) {
	body := metersBody(t, statsRouter(t), "/stats/meters")
	for _, want := range []string{`href="/stats"`, `href="/stats/lessons"`, `href="/stats/meters"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing tab %s", want)
		}
	}
	if !strings.Contains(body, `href="/stats/meters" class="active"`) {
		t.Errorf("the open tab is not the highlighted one:\n%s", body)
	}
}
