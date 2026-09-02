package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"familyhub/internal/db"
	"familyhub/internal/store"
)

// utilitiesFixture is two properties as the real data has them: a tariff used
// by both, a flat one, a replacement tariff that took over mid-history, and an
// archived utility that most reads must not show.
//
// Seeded with SQL because the write methods arrive with the forms that need
// them; a read test must not wait on them.
func utilitiesFixture(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, q := range []string{
		`INSERT INTO addresses (id, name, currency, active, sort_order) VALUES
			(1, 'Дім',   'UAH', 1, 1),
			(2, 'Тьоща', 'UAH', 1, 2),
			(3, 'Старе', 'UAH', 0, 3)`,
		`INSERT INTO tariffs (id, name, kind, unit, rate1, rate2, active) VALUES
			(1, 'Газ 2024',     'meter',       'м3',  8.0,  NULL, 1),
			(2, 'Газ 2025',     'meter',       'м3',  9.0,  NULL, 1),
			(3, 'Світло день/ніч','meter_zoned','кВт', 4.0,  2.0,  1),
			(4, 'Охорона',      'flat',        NULL,  500.0, NULL, 1),
			(5, 'Не вживаний',  'flat',        NULL,  1.0,  NULL, 0)`,
		`INSERT INTO utilities (id, address_id, name, current_tariff_id, icon, color, url, active, sort_order) VALUES
			(1, 1, 'Газ',      2, '🔥', '#f00', 'https://gas.example', 1, 1),
			(2, 1, 'Світло',   3, '⚡', '#ff0', '',                    1, 2),
			(3, 2, 'Охорона',  4, '🛡️', '#00f', '',                   1, 1),
			(4, 1, 'Архівний', NULL, '', '',    '',                    0, 9)`,
		// Gas in 2026-05 is the meter-replacement month: two readings, two
		// tariffs, which is why the unique key has three columns.
		`INSERT INTO readings (id, utility_id, tariff_id, period, prev1, curr1, consumed1, amount, paid_date) VALUES
			(1, 1, 1, '2026-05', 100, 120, 20, 160, '2026-06-01'),
			(2, 1, 2, '2026-05', 0,   5,   5,  45,  '2026-06-01'),
			(3, 1, 2, '2026-06', 5,   30,  25, 225, NULL),
			(4, 2, 3, '2026-06', 10,  40,  30, 120, '2026-07-02'),
			(5, 3, 4, '2026-06', NULL, NULL, NULL, 500, NULL)`,
	} {
		if _, err := database.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return store.New(database), database
}

func TestAddressesActiveHidesArchivedOnes(t *testing.T) {
	st, _ := utilitiesFixture(t)

	active, err := st.AddressesActive()
	if err != nil {
		t.Fatalf("AddressesActive: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("got %d active addresses, want 2", len(active))
	}
	if active[0].Name != "Дім" {
		t.Fatalf("ordered by sort_order? got %q first", active[0].Name)
	}

	all, err := st.AddressesAll()
	if err != nil {
		t.Fatalf("AddressesAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d addresses, want 3", len(all))
	}
	if all[2].Name != "Старе" {
		t.Fatalf("the archived address is not last: %q", all[2].Name)
	}
}

// area is null for a property with no per-area tariff, and that has to stay
// distinguishable from an area of zero.
func TestAnAddressWithoutAnAreaCarriesNil(t *testing.T) {
	st, database := utilitiesFixture(t)
	if _, err := database.Exec(`UPDATE addresses SET area = 62.5 WHERE id = 2`); err != nil {
		t.Fatal(err)
	}

	withArea, err := st.AddressByID(2)
	if err != nil {
		t.Fatalf("AddressByID: %v", err)
	}
	if withArea.Area == nil || *withArea.Area != 62.5 {
		t.Fatalf("area = %v, want 62.5", withArea.Area)
	}
	without, err := st.AddressByID(1)
	if err != nil {
		t.Fatalf("AddressByID: %v", err)
	}
	if without.Area != nil {
		t.Fatalf("area = %v, want nil", *without.Area)
	}
}

func TestUtilitiesAllCarriesItsAddressAndTariffNames(t *testing.T) {
	st, _ := utilitiesFixture(t)

	all, err := st.UtilitiesAll()
	if err != nil {
		t.Fatalf("UtilitiesAll: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d utilities, want 4 (archived included)", len(all))
	}
	gas := all[0]
	if gas.Name != "Газ" || gas.AddressName != "Дім" || gas.CurrentTariffName != "Газ 2025" {
		t.Fatalf("first row = %+v", gas)
	}
	if all[3].Name != "Архівний" {
		t.Fatalf("the archived utility is not last: %q", all[3].Name)
	}
	// A utility with no tariff set yet must read as "none", not as a broken join.
	if all[3].CurrentTariffID != nil || all[3].CurrentTariffName != "" {
		t.Fatalf("archived utility carries a tariff: %+v", all[3])
	}
}

// The tariff list is shared by both properties and every service, so a reading
// form that offered all of it is how a gas reading gets priced as electricity.
func TestTariffsForUtilityOffersOnlyItsOwnHistoryAndItsNext(t *testing.T) {
	st, _ := utilitiesFixture(t)

	got, err := st.TariffsForUtility(1) // gas: billed at 1 and 2, next is 2
	if err != nil {
		t.Fatalf("TariffsForUtility: %v", err)
	}
	names := map[string]bool{}
	for _, tf := range got {
		names[tf.Name] = true
	}
	if len(got) != 2 || !names["Газ 2024"] || !names["Газ 2025"] {
		t.Fatalf("got %v, want exactly the two gas tariffs", names)
	}
}

func TestTariffsAllCountsWhatDependsOnEachTariff(t *testing.T) {
	st, _ := utilitiesFixture(t)

	all, err := st.TariffsAll()
	if err != nil {
		t.Fatalf("TariffsAll: %v", err)
	}
	by := map[string]store.TariffWithUsage{}
	for _, tu := range all {
		by[tu.Name] = tu
	}

	if got := by["Газ 2025"]; got.UtilitiesCount != 1 || got.ReadingsCount != 2 || got.LastPeriod != "2026-06" {
		t.Fatalf("Газ 2025 = %+v", got)
	}
	// A tariff nothing has been billed at yet reports no history rather than
	// an empty string where a period should be.
	if got := by["Не вживаний"]; got.ReadingsCount != 0 || got.LastPeriod != "" {
		t.Fatalf("unused tariff = %+v", got)
	}
	if all[len(all)-1].Name != "Не вживаний" {
		t.Fatalf("the archived tariff is not last: %q", all[len(all)-1].Name)
	}
}

// Editing how a tariff calculates once history depends on it would silently
// reprice every past month it covered.
func TestTariffUsedInReadings(t *testing.T) {
	st, _ := utilitiesFixture(t)
	for id, want := range map[int64]bool{1: true, 2: true, 5: false} {
		got, err := st.TariffUsedInReadings(id)
		if err != nil {
			t.Fatalf("TariffUsedInReadings(%d): %v", id, err)
		}
		if got != want {
			t.Errorf("TariffUsedInReadings(%d) = %v, want %v", id, got, want)
		}
	}
}

// What archiving would cost, which is the whole reason these counters exist.
func TestTheArchivingCounters(t *testing.T) {
	st, _ := utilitiesFixture(t)

	if n, err := st.UtilitiesCountForAddress(1); err != nil || n != 3 {
		t.Fatalf("UtilitiesCountForAddress(1) = %d, %v; want 3", n, err)
	}
	if n, err := st.ReadingsCountForUtility(1); err != nil || n != 3 {
		t.Fatalf("ReadingsCountForUtility(1) = %d, %v; want 3", n, err)
	}
	if p, err := st.UtilityLastPeriod(1); err != nil || p != "2026-06" {
		t.Fatalf("UtilityLastPeriod(1) = %q, %v; want 2026-06", p, err)
	}
	// A utility never read reports no period rather than an error.
	if p, err := st.UtilityLastPeriod(4); err != nil || p != "" {
		t.Fatalf("UtilityLastPeriod(4) = %q, %v; want empty", p, err)
	}
}

// mustExecDB seeds a row directly. The utilities write methods exist now, but
// a statistics fixture wants exact ids and exact amounts, which a form-shaped
// API is the wrong tool for.
func mustExecDB(t *testing.T, database *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := database.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}
