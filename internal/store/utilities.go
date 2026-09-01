package store

import (
	"database/sql"

	"familyhub/internal/model"
)

// Reads over the utilities tables. Writes arrive with the forms that need
// them; what is here is what the read screens and the report ask for, plus the
// four counters the management pages use to say what archiving would cost.

// ptrString and ptrFloat turn a nullable column into the pointer the model
// carries. A utilities column is null where the value is genuinely absent —
// a flat tariff has no unit, an unpaid reading has no date — so the
// distinction has to survive into Go rather than collapsing to a zero.
func ptrString(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

func ptrFloat(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// ── addresses ────────────────────────────────────────────────────────────────

// AddressesActive returns the properties still in use, in display order.
func (s *Store) AddressesActive() ([]model.Address, error) {
	return s.queryAddresses(`WHERE active = 1 ORDER BY sort_order, id`)
}

// AddressesAll returns every property, archived ones last — the management
// page has to show what it would be un-archiving.
func (s *Store) AddressesAll() ([]model.Address, error) {
	return s.queryAddresses(`ORDER BY active DESC, sort_order, id`)
}

func (s *Store) queryAddresses(suffix string) ([]model.Address, error) {
	rows, err := s.db.Query(`
		SELECT id, name, comment, area, currency, active, sort_order
		FROM addresses ` + suffix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Address
	for rows.Next() {
		a, err := scanAddress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) AddressByID(id int64) (model.Address, error) {
	return scanAddress(s.db.QueryRow(`
		SELECT id, name, comment, area, currency, active, sort_order
		FROM addresses WHERE id = ?`, id))
}

func scanAddress(sc rowScanner) (model.Address, error) {
	var a model.Address
	var area sql.NullFloat64
	var active int
	if err := sc.Scan(&a.ID, &a.Name, &a.Comment, &area, &a.Currency, &active, &a.SortOrder); err != nil {
		return a, err
	}
	a.Active = active == 1
	a.Area = ptrFloat(area)
	return a, nil
}

// UtilitiesCountForAddress is what an address costs to archive: the services
// that would go with it.
func (s *Store) UtilitiesCountForAddress(id int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM utilities WHERE address_id = ?`, id).Scan(&n)
	return n, err
}

// ── utilities ────────────────────────────────────────────────────────────────

// UtilityWithAddress carries the two names a utility is never shown without:
// the property it belongs to, and the tariff it will next be billed at.
type UtilityWithAddress struct {
	model.Utility
	AddressName       string
	CurrentTariffName string
}

const utilitySelect = `
SELECT u.id, u.address_id, u.name, u.current_tariff_id, u.icon, u.color, u.url,
       u.active, u.sort_order, u.comment`

// UtilitiesAll returns every utility with its address, archived ones last.
func (s *Store) UtilitiesAll() ([]UtilityWithAddress, error) {
	rows, err := s.db.Query(utilitySelect + `,
		       a.name, COALESCE(t.name, '')
		FROM utilities u
		JOIN addresses a ON a.id = u.address_id
		LEFT JOIN tariffs t ON t.id = u.current_tariff_id
		ORDER BY u.active DESC, a.sort_order, a.id, u.sort_order, u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UtilityWithAddress
	for rows.Next() {
		var u UtilityWithAddress
		var currentTariff sql.NullInt64
		var active int
		if err := rows.Scan(&u.ID, &u.AddressID, &u.Name, &currentTariff, &u.Icon, &u.Color,
			&u.URL, &active, &u.SortOrder, &u.Comment,
			&u.AddressName, &u.CurrentTariffName); err != nil {
			return nil, err
		}
		u.Active = active == 1
		if currentTariff.Valid {
			u.CurrentTariffID = &currentTariff.Int64
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UtilityByID(id int64) (model.Utility, error) {
	var u model.Utility
	var currentTariff sql.NullInt64
	var active int
	err := s.db.QueryRow(utilitySelect+`
		FROM utilities u WHERE u.id = ?`, id).
		Scan(&u.ID, &u.AddressID, &u.Name, &currentTariff, &u.Icon, &u.Color,
			&u.URL, &active, &u.SortOrder, &u.Comment)
	if err != nil {
		return u, err
	}
	u.Active = active == 1
	if currentTariff.Valid {
		u.CurrentTariffID = &currentTariff.Int64
	}
	return u, nil
}

// ReadingsCountForUtility is what archiving a utility would hide.
func (s *Store) ReadingsCountForUtility(id int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM readings WHERE utility_id = ?`, id).Scan(&n)
	return n, err
}

// UtilityLastPeriod is the most recent month this utility has a reading for,
// or "" when it has none — what the new-reading form offers as the next one.
func (s *Store) UtilityLastPeriod(id int64) (string, error) {
	var p sql.NullString
	if err := s.db.QueryRow(`SELECT MAX(period) FROM readings WHERE utility_id = ?`, id).Scan(&p); err != nil {
		return "", err
	}
	return p.String, nil
}
