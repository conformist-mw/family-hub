package store

import "database/sql"

// Statistics over the utilities tables.
//
// Every total SUMs the readings of a month rather than taking one, because the
// month a meter is replaced holds two — the last on the old meter and the
// first on the new — and a query that picked one would quietly halve that
// month.

// MonthlyTotal is one month of spend at one property.
type MonthlyTotal struct {
	Period      string
	AddressID   int64
	AddressName string
	Total       float64
}

// MonthlyTotalsByAddress returns spend per month per property, oldest month
// first. minPeriod ("YYYY-MM") trims the range; "" is all history.
func (s *Store) MonthlyTotalsByAddress(minPeriod string) ([]MonthlyTotal, error) {
	q := `
		SELECT r.period, a.id, a.name, SUM(r.amount)
		FROM readings r
		JOIN utilities u ON u.id = r.utility_id
		JOIN addresses a ON a.id = u.address_id`
	var args []any
	if minPeriod != "" {
		q += ` WHERE r.period >= ?`
		args = append(args, minPeriod)
	}
	q += ` GROUP BY r.period, a.id ORDER BY r.period, a.sort_order, a.id`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MonthlyTotal
	for rows.Next() {
		var row MonthlyTotal
		if err := rows.Scan(&row.Period, &row.AddressID, &row.AddressName, &row.Total); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// MonthlyUtilityTotal is one month of spend on one service.
type MonthlyUtilityTotal struct {
	Period       string
	UtilityID    int64
	UtilityName  string
	UtilityColor string
	Total        float64
}

// MonthlyTotalsByUtility breaks one property's months down by service.
func (s *Store) MonthlyTotalsByUtility(addressID int64, minPeriod string) ([]MonthlyUtilityTotal, error) {
	q := `
		SELECT r.period, u.id, u.name, u.color, SUM(r.amount)
		FROM readings r
		JOIN utilities u ON u.id = r.utility_id
		WHERE u.address_id = ?`
	args := []any{addressID}
	if minPeriod != "" {
		q += ` AND r.period >= ?`
		args = append(args, minPeriod)
	}
	q += ` GROUP BY r.period, u.id ORDER BY r.period, u.sort_order, u.id`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MonthlyUtilityTotal
	for rows.Next() {
		var row MonthlyUtilityTotal
		if err := rows.Scan(&row.Period, &row.UtilityID, &row.UtilityName, &row.UtilityColor, &row.Total); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// MonthlyConsumption is one month of metered use at one property.
type MonthlyConsumption struct {
	Period      string
	AddressID   int64
	AddressName string
	Consumed    float64
}

// MonthlyConsumptionByAddress compares how much of one thing each property
// used — "Газ" at Дом against "Газ" at Тьоща. Matched on the service *name*,
// because the same utility at two properties is two rows with two ids and
// nothing else ties them together.
//
// Both zones are summed, so a zoned meter counts whole; a flat service has no
// meter and contributes nothing.
func (s *Store) MonthlyConsumptionByAddress(utilityName, minPeriod string) ([]MonthlyConsumption, error) {
	q := `
		SELECT r.period, a.id, a.name,
		       SUM(COALESCE(r.consumed1, 0) + COALESCE(r.consumed2, 0))
		FROM readings r
		JOIN utilities u ON u.id = r.utility_id
		JOIN addresses a ON a.id = u.address_id
		WHERE u.name = ?`
	args := []any{utilityName}
	if minPeriod != "" {
		q += ` AND r.period >= ?`
		args = append(args, minPeriod)
	}
	q += ` GROUP BY r.period, a.id ORDER BY r.period, a.sort_order, a.id`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MonthlyConsumption
	for rows.Next() {
		var row MonthlyConsumption
		if err := rows.Scan(&row.Period, &row.AddressID, &row.AddressName, &row.Consumed); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// UtilityNames lists the service names that exist at more than one property —
// the only ones a cross-property comparison can be drawn for. Ordered by how
// many properties share the name, then alphabetically.
func (s *Store) UtilityNames() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT u.name
		FROM utilities u
		GROUP BY u.name
		HAVING COUNT(DISTINCT u.address_id) > 1
		ORDER BY COUNT(DISTINCT u.address_id) DESC, u.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnitForUtilityName is the unit a comparison of that service is measured in,
// taken from the most recent tariff that actually priced one. "" when the
// service is flat and has no meter to label.
func (s *Store) UnitForUtilityName(name string) (string, error) {
	var unit sql.NullString
	err := s.db.QueryRow(`
		SELECT t.unit
		FROM readings r
		JOIN utilities u ON u.id = r.utility_id
		JOIN tariffs   t ON t.id = r.tariff_id
		WHERE u.name = ? AND t.unit IS NOT NULL AND t.unit <> ''
		ORDER BY r.period DESC, r.id DESC
		LIMIT 1`, name).Scan(&unit)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return unit.String, nil
}
