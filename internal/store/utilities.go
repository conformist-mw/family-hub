package store

import (
	"database/sql"
	"fmt"

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

// ── writes ───────────────────────────────────────────────────────────────────

func (s *Store) CreateAddress(a model.Address) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO addresses (name, comment, area, currency, active, sort_order)
		VALUES (?, ?, ?, ?, ?, ?)`,
		a.Name, a.Comment, orNilF(a.Area), a.Currency, boolToInt(a.Active), a.SortOrder)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateAddress(a model.Address) error {
	_, err := s.db.Exec(`
		UPDATE addresses
		SET name = ?, comment = ?, area = ?, currency = ?, sort_order = ?
		WHERE id = ?`,
		a.Name, a.Comment, orNilF(a.Area), a.Currency, a.SortOrder, a.ID)
	return err
}

func (s *Store) CreateUtility(u model.Utility) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO utilities (address_id, name, current_tariff_id, icon, color, url, active, sort_order, comment)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.AddressID, u.Name, orNilI(u.CurrentTariffID), u.Icon, u.Color, u.URL,
		boolToInt(u.Active), u.SortOrder, u.Comment)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateUtility(u model.Utility) error {
	_, err := s.db.Exec(`
		UPDATE utilities
		SET address_id = ?, name = ?, current_tariff_id = ?, icon = ?, color = ?,
		    url = ?, sort_order = ?, comment = ?
		WHERE id = ?`,
		u.AddressID, u.Name, orNilI(u.CurrentTariffID), u.Icon, u.Color, u.URL,
		u.SortOrder, u.Comment, u.ID)
	return err
}

// ToggleActive flips the active flag on one row of one of the utilities
// tables. Archiving is a toggle rather than a field on the edit form because
// it is the one change made from the list, on a row you are not otherwise
// editing.
//
// table is not caller-supplied text: only the three constants below reach it.
func (s *Store) ToggleActive(table string, id int64) error {
	switch table {
	case TableAddresses, TableUtilities, TableTariffs:
	default:
		return fmt.Errorf("store: cannot toggle %q", table)
	}
	res, err := s.db.Exec(`UPDATE `+table+` SET active = 1 - active WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// The tables ToggleActive accepts, so the name is never a string from a
// request.
const (
	TableAddresses = "addresses"
	TableUtilities = "utilities"
	TableTariffs   = "tariffs"
)

func orNilI(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
