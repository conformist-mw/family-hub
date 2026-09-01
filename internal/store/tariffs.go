package store

import (
	"database/sql"
	"errors"

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

// ── writes ───────────────────────────────────────────────────────────────────

// ErrTariffHasHistory refuses a change to how a tariff calculates once
// readings have been priced by it. Name, comment and the active flag stay
// editable — archiving a superseded tariff is the normal thing to do when a
// price changes, and production is full of archived tariffs that still carry
// their history. What must not move is kind, unit and the rates: the stored
// amount of every past month was computed from them, and rewriting them leaves
// those numbers unverifiable against the tariff they claim to come from.
var ErrTariffHasHistory = errors.New("тариф уже використано в показаннях — ставку і спосіб розрахунку змінити не можна")

func (s *Store) CreateTariff(t model.Tariff) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO tariffs (name, kind, unit, rate1, rate2, effective_from, effective_to, active, comment)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.Kind, orNil(t.Unit), t.Rate1, orNilF(t.Rate2),
		orNil(t.EffectiveFrom), orNil(t.EffectiveTo), boolToInt(t.Active), t.Comment)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateTariff writes the whole row when the tariff is unused, and only the
// descriptive half once it has priced something.
func (s *Store) UpdateTariff(t model.Tariff) error {
	used, err := s.TariffUsedInReadings(t.ID)
	if err != nil {
		return err
	}
	if used {
		if changed, err := s.tariffMathChanged(t); err != nil {
			return err
		} else if changed {
			return ErrTariffHasHistory
		}
	}
	_, err = s.db.Exec(`
		UPDATE tariffs
		SET name = ?, kind = ?, unit = ?, rate1 = ?, rate2 = ?,
		    effective_from = ?, effective_to = ?, comment = ?
		WHERE id = ?`,
		t.Name, t.Kind, orNil(t.Unit), t.Rate1, orNilF(t.Rate2),
		orNil(t.EffectiveFrom), orNil(t.EffectiveTo), t.Comment, t.ID)
	return err
}

// tariffMathChanged compares only what an amount was computed from. A form
// that posts the locked fields back unchanged — which is what a disabled input
// does — must save, not fail.
func (s *Store) tariffMathChanged(t model.Tariff) (bool, error) {
	old, err := s.TariffByID(t.ID)
	if err != nil {
		return false, err
	}
	if old.Kind != t.Kind || old.Rate1 != t.Rate1 {
		return true, nil
	}
	return !sameFloat(old.Rate2, t.Rate2) || !sameString(old.Unit, t.Unit), nil
}

func sameFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameString(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
