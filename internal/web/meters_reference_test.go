package web

import (
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
