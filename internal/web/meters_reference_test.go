package web

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A tariff is shared between properties and services, so the page that offers
// to change one has to show what changing it would reprice.
func TestTheTariffListShowsWhatDependsOnEachTariff(t *testing.T) {
	body := metersBody(t, metersRouter(t), "/meters/tariffs")

	for _, want := range []string{
		"Газ 2026", "лічильник", "м3",
		"Світло д/н", "двозонний",
		"Охорона", "фіксований",
		"червень 2026", // the last period a tariff was billed at
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// Archiving hides a row from the month view; the page whose job is to manage
// rows must still show what there is to bring back, marked.
func TestTheReferenceListsShowArchivedRowsMarked(t *testing.T) {
	router := metersRouter(t)

	utilities := metersBody(t, router, "/meters/utilities")
	if !strings.Contains(utilities, "Архівний") || !strings.Contains(utilities, "в архіві") {
		t.Errorf("an archived utility is hidden from its own management page:\n%s", utilities)
	}
	addresses := metersBody(t, router, "/meters/addresses")
	if !strings.Contains(addresses, "Старе") || !strings.Contains(addresses, "в архіві") {
		t.Errorf("an archived address is hidden from its own management page:\n%s", addresses)
	}
}

// The column is the tariff the NEXT reading will use; a utility that has none
// set says so rather than showing a blank cell.
func TestAUtilityWithoutATariffSaysSo(t *testing.T) {
	body := metersBody(t, metersRouter(t), "/meters/utilities")
	if !strings.Contains(body, "не заданий") {
		t.Errorf("a utility with no tariff renders as an empty cell:\n%s", body)
	}
	if !strings.Contains(body, "Газ 2026") || !strings.Contains(body, "Дім") {
		t.Error("a utility does not carry its tariff and address")
	}
}

func TestTheAddressListCountsWhatArchivingWouldTake(t *testing.T) {
	body := metersBody(t, metersRouter(t), "/meters/addresses")

	for _, want := range []string{"Дім", "вул. Прикладна, 1", "120.5 м²", "UAH"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// A property with no area recorded shows a dash, not "0 м²".
	if !strings.Contains(body, "<td data-label=\"Площа\">—</td>") {
		t.Errorf("a property without an area renders as zero:\n%s", body)
	}
}

// ── writing ──────────────────────────────────────────────────────────────────

func refDB(t *testing.T) (http.Handler, *sql.DB) { return metersDB(t) }

func countRow(t *testing.T, database *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := database.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestCreatingEachReferenceEntity(t *testing.T) {
	router, database := refDB(t)

	if rec := post(t, router, "/meters/addresses/new", url.Values{
		"name": {"Дача"}, "comment": {"с. Приклад"}, "area": {"48,5"}, "currency": {"UAH"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("address = %d\n%s", rec.Code, rec.Body.String())
	}
	var area sql.NullFloat64
	if err := database.QueryRow(`SELECT area FROM addresses WHERE name = 'Дача'`).Scan(&area); err != nil {
		t.Fatal(err)
	}
	// A comma is what a phone keyboard offers for a decimal point.
	if !area.Valid || area.Float64 != 48.5 {
		t.Fatalf("area = %v, want 48.5", area)
	}

	if rec := post(t, router, "/meters/tariffs/new", url.Values{
		"name": {"Вода 2027"}, "kind": {"meter"}, "rate1": {"31.36"}, "unit": {"м3"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("tariff = %d\n%s", rec.Code, rec.Body.String())
	}

	if rec := post(t, router, "/meters/utilities/new", url.Values{
		"name": {"Вода"}, "address_id": {"1"}, "icon": {"💧"}, "color": {"#0af"},
		"current_tariff_id": {"1"}, "url": {"https://vodokanal.example"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("utility = %d\n%s", rec.Code, rec.Body.String())
	}
	var icon, color string
	if err := database.QueryRow(`SELECT icon, color FROM utilities WHERE name = 'Вода'`).Scan(&icon, &color); err != nil {
		t.Fatal(err)
	}
	if icon != "💧" || color != "#0af" {
		t.Fatalf("icon/color = %q/%q", icon, color)
	}
}

func TestEditingEachReferenceEntity(t *testing.T) {
	router, database := refDB(t)

	if rec := post(t, router, "/meters/addresses/1", url.Values{
		"name": {"Домівка"}, "currency": {"UAH"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("address = %d", rec.Code)
	}
	if n := countRow(t, database, `SELECT COUNT(*) FROM addresses WHERE id = 1 AND name = 'Домівка'`); n != 1 {
		t.Error("the address was not renamed")
	}

	if rec := post(t, router, "/meters/utilities/1", url.Values{
		"name": {"Газ"}, "address_id": {"1"}, "current_tariff_id": {"4"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("utility = %d", rec.Code)
	}
	if n := countRow(t, database, `SELECT COUNT(*) FROM utilities WHERE id = 1 AND current_tariff_id = 4`); n != 1 {
		t.Error("the utility did not move to the new tariff")
	}
}

// A name is not optional, and a form that loses what was typed is worse than
// one that refuses.
func TestASavedEntityKeepsWhatWasTypedWhenItIsRejected(t *testing.T) {
	router, _ := refDB(t)

	rec := post(t, router, "/meters/utilities/new", url.Values{
		"name": {""}, "address_id": {"1"}, "icon": {"🚿"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty name = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "🚿") {
		t.Errorf("the rejected form lost what was typed:\n%s", rec.Body.String())
	}
}

// Archiving a superseded tariff is the normal thing to do when a price
// changes; production is full of archived tariffs that still carry history. It
// is the arithmetic that must not move, not the flag.
func TestATariffWithHistoryCanStillBeArchived(t *testing.T) {
	router, database := refDB(t)
	post(t, router, "/meters/readings/new",
		url.Values{"utility_id": {"1"}, "period": {"2026-06"}, "prev1": {"100"}, "curr1": {"120"}})

	if rec := post(t, router, "/meters/tariffs/1/toggle", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("toggle = %d", rec.Code)
	}
	if n := countRow(t, database, `SELECT COUNT(*) FROM tariffs WHERE id = 1 AND active = 0`); n != 1 {
		t.Fatal("a used tariff could not be archived")
	}
	// And back again.
	post(t, router, "/meters/tariffs/1/toggle", nil)
	if n := countRow(t, database, `SELECT COUNT(*) FROM tariffs WHERE id = 1 AND active = 1`); n != 1 {
		t.Fatal("the tariff could not be restored")
	}
}

// The amount of every past month was computed from these fields. Rewriting
// them leaves those numbers unverifiable against the tariff they claim to come
// from — a new price is a new tariff, not an edit of the old one.
func TestTheArithmeticOfAUsedTariffIsLocked(t *testing.T) {
	router, database := refDB(t)
	post(t, router, "/meters/readings/new",
		url.Values{"utility_id": {"1"}, "period": {"2026-06"}, "prev1": {"100"}, "curr1": {"120"}})

	// The edit form says so, and does not offer the fields.
	body := metersBody(t, router, "/meters/tariffs/1")
	if !strings.Contains(body, "заблоковані") {
		t.Errorf("the form does not say the tariff is locked:\n%s", body)
	}
	if !strings.Contains(body, `name="rate1" value="9" disabled`) {
		t.Errorf("the rate is editable on a used tariff:\n%s", body)
	}

	// And a hand-made POST is refused rather than politely ignored.
	rec := post(t, router, "/meters/tariffs/1", url.Values{
		"name": {"Газ"}, "kind": {"meter"}, "rate1": {"99"}, "unit": {"м3"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("repricing a used tariff = %d, want 422", rec.Code)
	}
	var rate float64
	database.QueryRow(`SELECT rate1 FROM tariffs WHERE id = 1`).Scan(&rate)
	if rate != 9 {
		t.Fatalf("rate1 = %v — a used tariff was repriced", rate)
	}
}

// Renaming a locked tariff has to work: the disabled inputs are not submitted,
// so the fields it may not change are read back from the stored row rather
// than from the request.
func TestALockedTariffCanStillBeRenamed(t *testing.T) {
	router, database := refDB(t)
	post(t, router, "/meters/readings/new",
		url.Values{"utility_id": {"1"}, "period": {"2026-06"}, "prev1": {"100"}, "curr1": {"120"}})

	rec := post(t, router, "/meters/tariffs/1", url.Values{
		"name": {"Газ (стара ціна)"}, "comment": {"до вересня 2026"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename = %d\n%s", rec.Code, rec.Body.String())
	}
	var name string
	var rate float64
	var kind string
	database.QueryRow(`SELECT name, rate1, kind FROM tariffs WHERE id = 1`).Scan(&name, &rate, &kind)
	if name != "Газ (стара ціна)" {
		t.Fatalf("name = %q", name)
	}
	if rate != 9 || kind != "meter" {
		t.Fatalf("the rename blanked the arithmetic: rate=%v kind=%q", rate, kind)
	}
}

// An unused tariff is still fully editable — the lock is about history, not
// about age.
func TestAnUnusedTariffIsFullyEditable(t *testing.T) {
	router, database := refDB(t)

	rec := post(t, router, "/meters/tariffs/4", url.Values{
		"name": {"Газ новий"}, "kind": {"meter"}, "rate1": {"11.5"}, "unit": {"м3"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit = %d\n%s", rec.Code, rec.Body.String())
	}
	var rate float64
	database.QueryRow(`SELECT rate1 FROM tariffs WHERE id = 4`).Scan(&rate)
	if rate != 11.5 {
		t.Fatalf("rate1 = %v, want 11.5", rate)
	}
}

func TestArchivingAUtilityTakesItOutOfTheMonth(t *testing.T) {
	router, _ := refDB(t)

	before := metersBody(t, router, "/meters?address_id=1&period=2026-06")
	if !strings.Contains(before, "Охорона") {
		t.Fatal("the fixture is wrong: Охорона is not in the month")
	}
	if rec := post(t, router, "/meters/utilities/3/toggle", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("toggle = %d", rec.Code)
	}
	after := metersBody(t, router, "/meters?address_id=1&period=2026-06")
	if strings.Contains(after, "Охорона") {
		t.Error("an archived utility is still listed as a gap to fill in")
	}
	// But it is still on its own management page, marked.
	if !strings.Contains(metersBody(t, router, "/meters/utilities"), "Охорона") {
		t.Error("an archived utility vanished from the page that manages it")
	}
}

// The table a toggle writes to comes from the route, so a request cannot name
// one; an id that does not exist is a 404 rather than a silent success.
func TestTogglingSomethingThatIsNotThere(t *testing.T) {
	router, _ := refDB(t)
	if rec := post(t, router, "/meters/tariffs/999/toggle", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("toggle of a missing tariff = %d, want 404", rec.Code)
	}
}
