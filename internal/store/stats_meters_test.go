package store_test

import (
	"testing"

	"familyhub/internal/store"
)

// Every total sums a month's readings rather than taking one, because the
// month a meter is replaced holds two — gas at 2026-05 in the fixture.
func TestMonthlyTotalsByAddressCountsAReplacementMonthWhole(t *testing.T) {
	st, _ := utilitiesFixture(t)

	rows, err := st.MonthlyTotalsByAddress("")
	if err != nil {
		t.Fatalf("MonthlyTotalsByAddress: %v", err)
	}
	var may float64
	for _, r := range rows {
		if r.Period == "2026-05" && r.AddressName == "Дім" {
			may = r.Total
		}
	}
	if may != 205 { // 160 + 45, both halves of the replacement month
		t.Fatalf("2026-05 = %v, want 205 — a replacement month was counted once", may)
	}
	// Oldest month first, so a chart reads left to right.
	if rows[0].Period != "2026-05" {
		t.Fatalf("first row is %s, want the oldest month", rows[0].Period)
	}
}

func TestMonthlyTotalsByAddressTrimsTheRange(t *testing.T) {
	st, _ := utilitiesFixture(t)

	rows, err := st.MonthlyTotalsByAddress("2026-06")
	if err != nil {
		t.Fatalf("MonthlyTotalsByAddress: %v", err)
	}
	for _, r := range rows {
		if r.Period < "2026-06" {
			t.Fatalf("%s slipped past minPeriod", r.Period)
		}
	}
}

func TestMonthlyTotalsByUtilityStaysWithinItsProperty(t *testing.T) {
	st, _ := utilitiesFixture(t)

	rows, err := st.MonthlyTotalsByUtility(1, "")
	if err != nil {
		t.Fatalf("MonthlyTotalsByUtility: %v", err)
	}
	for _, r := range rows {
		if r.UtilityName == "Охорона" {
			t.Fatal("a service from the other property is in the breakdown")
		}
	}
	if len(rows) == 0 {
		t.Fatal("no rows for a property that has readings")
	}
}

// The same utility at two properties is two rows with two ids and nothing else
// tying them together, so the comparison is matched on the name.
func TestConsumptionIsComparedByName(t *testing.T) {
	st, database := utilitiesFixture(t)
	// Give the second property a gas service of its own, with one month.
	mustExecDB(t, database, `INSERT INTO utilities (id, address_id, name, current_tariff_id, active, sort_order)
		VALUES (10, 2, 'Газ', 2, 1, 5)`)
	mustExecDB(t, database, `INSERT INTO readings (utility_id, tariff_id, period, prev1, curr1, consumed1, amount)
		VALUES (10, 2, '2026-06', 0, 7, 7, 63)`)

	rows, err := st.MonthlyConsumptionByAddress("Газ", "")
	if err != nil {
		t.Fatalf("MonthlyConsumptionByAddress: %v", err)
	}
	seen := map[string]float64{}
	for _, r := range rows {
		if r.Period == "2026-06" {
			seen[r.AddressName] = r.Consumed
		}
	}
	if seen["Дім"] != 25 || seen["Тьоща"] != 7 {
		t.Fatalf("2026-06 = %v, want Дім 25 and Тьоща 7", seen)
	}
}

// A zoned meter counts whole; a flat service has no meter and contributes
// nothing rather than being left out of the month.
func TestConsumptionSumsBothZonesAndIgnoresFlat(t *testing.T) {
	st, _ := utilitiesFixture(t)

	rows, err := st.MonthlyConsumptionByAddress("Світло", "")
	if err != nil {
		t.Fatalf("MonthlyConsumptionByAddress: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows for a zoned service")
	}
	if rows[0].Consumed != 30 {
		t.Fatalf("consumed = %v, want 30", rows[0].Consumed)
	}

	flat, err := st.MonthlyConsumptionByAddress("Охорона", "")
	if err != nil {
		t.Fatalf("MonthlyConsumptionByAddress: %v", err)
	}
	for _, r := range flat {
		if r.Consumed != 0 {
			t.Fatalf("a flat service reported %v consumed", r.Consumed)
		}
	}
}

// Only names that exist at more than one property can be compared, so those
// are the only ones offered.
func TestUtilityNamesOffersOnlySharedOnes(t *testing.T) {
	st, database := utilitiesFixture(t)

	if names, err := st.UtilityNames(); err != nil || len(names) != 0 {
		t.Fatalf("names = %v, %v; want none when nothing is shared", names, err)
	}

	mustExecDB(t, database, `INSERT INTO utilities (id, address_id, name, current_tariff_id, active, sort_order)
		VALUES (10, 2, 'Газ', 2, 1, 5)`)
	names, err := st.UtilityNames()
	if err != nil {
		t.Fatalf("UtilityNames: %v", err)
	}
	if len(names) != 1 || names[0] != "Газ" {
		t.Fatalf("names = %v, want [Газ]", names)
	}
}

func TestUnitForUtilityName(t *testing.T) {
	st, _ := utilitiesFixture(t)

	if u, err := st.UnitForUtilityName("Газ"); err != nil || u != "м3" {
		t.Fatalf("unit = %q, %v; want м3", u, err)
	}
	// A flat service has no meter to label, and that is not an error.
	if u, err := st.UnitForUtilityName("Охорона"); err != nil || u != "" {
		t.Fatalf("unit = %q, %v; want empty", u, err)
	}
	if u, err := st.UnitForUtilityName("Немає такого"); err != nil || u != "" {
		t.Fatalf("unit = %q, %v; want empty for an unknown name", u, err)
	}
}

var _ = store.MonthlyTotal{}
