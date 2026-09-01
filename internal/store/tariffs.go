package store

import (
	"database/sql"

	"familyhub/internal/model"
)

const tariffSelect = `SELECT id, name, kind, unit, rate1, rate2, active, comment FROM tariffs`

func scanTariff(sc rowScanner) (model.Tariff, error) {
	var t model.Tariff
	var unit sql.NullString
	var rate2 sql.NullFloat64
	var active int
	if err := sc.Scan(&t.ID, &t.Name, &t.Kind, &unit, &t.Rate1, &rate2, &active, &t.Comment); err != nil {
		return t, err
	}
	t.Active = active == 1
	t.Unit = ptrString(unit)
	t.Rate2 = ptrFloat(rate2)
	return t, nil
}

func (s *Store) queryTariffs(suffix string, args ...any) ([]model.Tariff, error) {
	rows, err := s.db.Query(tariffSelect+" "+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Tariff
	for rows.Next() {
		t, err := scanTariff(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) TariffByID(id int64) (model.Tariff, error) {
	return scanTariff(s.db.QueryRow(tariffSelect+" WHERE id = ?", id))
}

// TariffsActive fills the tariff dropdown on a reading form.
func (s *Store) TariffsActive() ([]model.Tariff, error) {
	return s.queryTariffs(`WHERE active = 1 ORDER BY name`)
}

// TariffsForUtility narrows that dropdown to the tariffs this utility has
// actually been billed at, plus the one it is set to bill at next. The full
// list is shared across both properties and every service, so offering all of
// it is how a gas reading gets saved against the electricity price.
func (s *Store) TariffsForUtility(utilityID int64) ([]model.Tariff, error) {
	return s.queryTariffs(`
		WHERE id IN (
			SELECT DISTINCT tariff_id FROM readings WHERE utility_id = ?
			UNION
			SELECT current_tariff_id FROM utilities WHERE id = ? AND current_tariff_id IS NOT NULL
		)
		ORDER BY active DESC, name`, utilityID, utilityID)
}

// TariffWithUsage is a tariff plus what depends on it, for the page that
// offers to change one.
type TariffWithUsage struct {
	model.Tariff
	UtilitiesCount int
	ReadingsCount  int
	LastPeriod     string
}

func (s *Store) TariffsAll() ([]TariffWithUsage, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.name, t.kind, t.unit, t.rate1, t.rate2, t.active, t.comment,
		       (SELECT COUNT(*) FROM utilities u WHERE u.current_tariff_id = t.id),
		       (SELECT COUNT(*) FROM readings r WHERE r.tariff_id = t.id),
		       COALESCE((SELECT MAX(period) FROM readings r WHERE r.tariff_id = t.id), '')
		FROM tariffs t
		ORDER BY t.active DESC, t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TariffWithUsage
	for rows.Next() {
		var tu TariffWithUsage
		var unit sql.NullString
		var rate2 sql.NullFloat64
		var active int
		if err := rows.Scan(&tu.ID, &tu.Name, &tu.Kind, &unit, &tu.Rate1, &rate2, &active, &tu.Comment,
			&tu.UtilitiesCount, &tu.ReadingsCount, &tu.LastPeriod); err != nil {
			return nil, err
		}
		tu.Active = active == 1
		tu.Unit = ptrString(unit)
		tu.Rate2 = ptrFloat(rate2)
		out = append(out, tu)
	}
	return out, rows.Err()
}

// TariffUsedInReadings reports whether history depends on this tariff. Once it
// does, how it calculates (kind, rates, unit) must stop being editable, or
// every past month it priced silently changes to a number it was never billed.
func (s *Store) TariffUsedInReadings(id int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM readings WHERE tariff_id = ?`, id).Scan(&n)
	return n > 0, err
}
