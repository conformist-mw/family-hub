package store_test

import (
	"database/sql"
	"errors"
	"testing"

	"familyhub/internal/store"
)

func TestReadingsListFilters(t *testing.T) {
	st, _ := utilitiesFixture(t)

	for _, tc := range []struct {
		name   string
		filter store.ReadingFilter
		want   int
	}{
		{"everything", store.ReadingFilter{}, 5},
		{"one property", store.ReadingFilter{AddressID: 1}, 4},
		{"one month", store.ReadingFilter{Period: "2026-06"}, 3},
		{"one year", store.ReadingFilter{Year: "2026"}, 5},
		{"unpaid only", store.ReadingFilter{Unpaid: true}, 2},
		{"unpaid at one property", store.ReadingFilter{AddressID: 1, Unpaid: true}, 1},
		// An exact period wins over a year, so a form that sends both does not
		// quietly widen the answer.
		{"period beats year", store.ReadingFilter{Period: "2026-05", Year: "2025"}, 2},
		{"a year with nothing in it", store.ReadingFilter{Year: "2020"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.ReadingsList(tc.filter)
			if err != nil {
				t.Fatalf("ReadingsList: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("got %d readings, want %d", len(got), tc.want)
			}
		})
	}
}

// Newest month first: the list is opened to answer "what about this month",
// not to read history from the start.
func TestReadingsListPutsTheNewestMonthFirst(t *testing.T) {
	st, _ := utilitiesFixture(t)

	got, err := st.ReadingsList(store.ReadingFilter{})
	if err != nil {
		t.Fatalf("ReadingsList: %v", err)
	}
	if got[0].Period != "2026-06" || got[len(got)-1].Period != "2026-05" {
		t.Fatalf("order = %q … %q", got[0].Period, got[len(got)-1].Period)
	}
}

// A reading on its own is a number and two dates; the list has to say which
// service at which property, priced how.
func TestAReadingCarriesWhatItIsShownBeside(t *testing.T) {
	st, _ := utilitiesFixture(t)

	rv, err := st.ReadingByID(3)
	if err != nil {
		t.Fatalf("ReadingByID: %v", err)
	}
	if rv.UtilityName != "Газ" || rv.AddressName != "Дім" || rv.TariffName != "Газ 2025" {
		t.Fatalf("names = %q / %q / %q", rv.UtilityName, rv.AddressName, rv.TariffName)
	}
	if rv.TariffUnit == nil || *rv.TariffUnit != "м3" {
		t.Fatalf("unit = %v", rv.TariffUnit)
	}
	if rv.PaidDate != nil {
		t.Fatalf("paid_date = %v, want nil for an unpaid month", *rv.PaidDate)
	}
	// A flat service has no unit, and that must stay distinguishable from "".
	flat, err := st.ReadingByID(5)
	if err != nil {
		t.Fatalf("ReadingByID: %v", err)
	}
	if flat.TariffUnit != nil {
		t.Fatalf("unit = %q, want nil for a flat tariff", *flat.TariffUnit)
	}
}

// This month's "previous" is last month's "current", so the form pre-fills
// from the latest reading — which in a meter-replacement month is the one on
// the new meter, not the higher id's period-mate on the old.
func TestLatestReadingForUtility(t *testing.T) {
	st, _ := utilitiesFixture(t)

	rv, err := st.LatestReadingForUtility(1)
	if err != nil {
		t.Fatalf("LatestReadingForUtility: %v", err)
	}
	if rv.Period != "2026-06" || rv.ID != 3 {
		t.Fatalf("got reading %d for %s, want 3 for 2026-06", rv.ID, rv.Period)
	}
	// A utility with no history says so rather than returning a zero reading
	// that the form would pre-fill from.
	if _, err := st.LatestReadingForUtility(4); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestCurrentMonthStatusAnswersWhatIsMissingAndWhatIsUnpaid(t *testing.T) {
	st, _ := utilitiesFixture(t)

	rows, err := st.CurrentMonthStatus("2026-06", 0)
	if err != nil {
		t.Fatalf("CurrentMonthStatus: %v", err)
	}
	// Active utilities at active addresses only: the archived one is not a
	// gap somebody still has to fill in.
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	by := map[string]store.UtilityMonthlyStatus{}
	for _, r := range rows {
		by[r.UtilityName] = r
	}

	if got := by["Газ"]; !got.HasReading || got.Amount != 225 || got.Paid {
		t.Fatalf("Газ = %+v, want read, 225, unpaid", got)
	}
	if got := by["Світло"]; !got.Paid || got.Amount != 120 {
		t.Fatalf("Світло = %+v, want paid, 120", got)
	}
	if got := by["Газ"]; got.Unit != "м3" || got.Consumed1 == nil || *got.Consumed1 != 25 {
		t.Fatalf("Газ detail = %+v", got)
	}
	// A flat service has no meter, so its detail stays empty while its amount
	// does not.
	if got := by["Охорона"]; got.Amount != 500 || got.Consumed1 != nil || got.Unit != "" {
		t.Fatalf("Охорона = %+v", got)
	}
}

// A month with nothing entered is the state the screen exists to make visible.
func TestAnUnreadMonthStillListsEveryUtility(t *testing.T) {
	st, _ := utilitiesFixture(t)

	rows, err := st.CurrentMonthStatus("2026-07", 0)
	if err != nil {
		t.Fatalf("CurrentMonthStatus: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 — an unread month must not be an empty list", len(rows))
	}
	for _, r := range rows {
		if r.HasReading || r.Paid || r.Amount != 0 || r.ReadingCount != 0 {
			t.Fatalf("%s reads as entered: %+v", r.UtilityName, r)
		}
	}
}

// The month a meter is replaced carries two readings. They are one line to a
// person: one total, and paid only when both halves are.
func TestAMeterReplacementMonthIsOneRow(t *testing.T) {
	st, database := utilitiesFixture(t)

	rows, err := st.CurrentMonthStatus("2026-05", 1)
	if err != nil {
		t.Fatalf("CurrentMonthStatus: %v", err)
	}
	var gas store.UtilityMonthlyStatus
	for _, r := range rows {
		if r.UtilityName == "Газ" {
			gas = r
		}
	}
	if gas.ReadingCount != 2 {
		t.Fatalf("reading count = %d, want 2", gas.ReadingCount)
	}
	if gas.Amount != 205 { // 160 + 45
		t.Fatalf("amount = %v, want 205 — both readings of the month", gas.Amount)
	}
	if !gas.Paid {
		t.Fatal("both readings are paid, so the month is")
	}
	// FirstReading identifies the row to open, and only when there is one to
	// open — two readings need the list, not a guess at which.
	if gas.FirstReading == 0 {
		t.Fatal("FirstReading = 0")
	}

	// Leave one half unpaid and the month must stop reading as paid.
	if _, err := database.Exec(`UPDATE readings SET paid_date = NULL WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	rows, err = st.CurrentMonthStatus("2026-05", 1)
	if err != nil {
		t.Fatalf("CurrentMonthStatus: %v", err)
	}
	for _, r := range rows {
		if r.UtilityName == "Газ" && r.Paid {
			t.Fatal("half the month is unpaid and it still reads as paid")
		}
	}
}

func TestCurrentMonthStatusScopesToOneProperty(t *testing.T) {
	st, _ := utilitiesFixture(t)

	rows, err := st.CurrentMonthStatus("2026-06", 2)
	if err != nil {
		t.Fatalf("CurrentMonthStatus: %v", err)
	}
	if len(rows) != 1 || rows[0].AddressName != "Тьоща" {
		t.Fatalf("got %d rows for one property: %+v", len(rows), rows)
	}
}
