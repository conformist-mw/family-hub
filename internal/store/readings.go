package store

import (
	"database/sql"
	"strings"

	"familyhub/internal/model"
)

// rowScanner is what *sql.Row and *sql.Rows have in common, so one scan
// function serves both the single-row and the list query.
type rowScanner interface {
	Scan(dest ...any) error
}

// ReadingView is a reading with everything it is displayed beside: a reading
// alone is a number and two dates, and the list needs to say which service at
// which property, priced how.
type ReadingView struct {
	model.Reading
	UtilityName  string
	UtilityIcon  string
	UtilityColor string
	AddressName  string
	AddressID    int64
	TariffName   string
	TariffKind   string
	TariffUnit   *string
}

const readingSelect = `
SELECT r.id, r.utility_id, r.tariff_id, r.period, r.reading_date,
       r.prev1, r.curr1, r.prev2, r.curr2, r.consumed1, r.consumed2,
       r.amount, r.paid_date, r.comment,
       u.name, u.icon, u.color, a.name, a.id,
       t.name, t.kind, t.unit
FROM readings r
JOIN utilities u ON u.id = r.utility_id
JOIN addresses a ON a.id = u.address_id
JOIN tariffs   t ON t.id = r.tariff_id`

func scanReadingView(sc rowScanner) (ReadingView, error) {
	var rv ReadingView
	var readingDate, paidDate, unit sql.NullString
	var prev1, curr1, prev2, curr2, consumed1, consumed2 sql.NullFloat64
	if err := sc.Scan(
		&rv.ID, &rv.UtilityID, &rv.TariffID, &rv.Period, &readingDate,
		&prev1, &curr1, &prev2, &curr2, &consumed1, &consumed2,
		&rv.Amount, &paidDate, &rv.Comment,
		&rv.UtilityName, &rv.UtilityIcon, &rv.UtilityColor, &rv.AddressName, &rv.AddressID,
		&rv.TariffName, &rv.TariffKind, &unit,
	); err != nil {
		return rv, err
	}
	rv.ReadingDate = ptrString(readingDate)
	rv.PaidDate = ptrString(paidDate)
	rv.Prev1 = ptrFloat(prev1)
	rv.Curr1 = ptrFloat(curr1)
	rv.Prev2 = ptrFloat(prev2)
	rv.Curr2 = ptrFloat(curr2)
	rv.Consumed1 = ptrFloat(consumed1)
	rv.Consumed2 = ptrFloat(consumed2)
	rv.TariffUnit = ptrString(unit)
	return rv, nil
}

// ReadingFilter narrows the readings list. Zero values mean "everything".
type ReadingFilter struct {
	AddressID int64
	Year      string // "YYYY"
	Period    string // exact "YYYY-MM"; wins over Year when both are set
	Unpaid    bool
}

func (s *Store) ReadingsList(f ReadingFilter) ([]ReadingView, error) {
	q := readingSelect
	var conds []string
	var args []any
	if f.AddressID != 0 {
		conds = append(conds, "a.id = ?")
		args = append(args, f.AddressID)
	}
	if f.Period != "" {
		conds = append(conds, "r.period = ?")
		args = append(args, f.Period)
	} else if f.Year != "" {
		conds = append(conds, "r.period LIKE ?")
		args = append(args, f.Year+"-%")
	}
	if f.Unpaid {
		conds = append(conds, "r.paid_date IS NULL")
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY r.period DESC, a.sort_order, a.id, u.sort_order, u.id, r.id"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ReadingView
	for rows.Next() {
		rv, err := scanReadingView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rv)
	}
	return out, rows.Err()
}

func (s *Store) ReadingByID(id int64) (ReadingView, error) {
	return scanReadingView(s.db.QueryRow(readingSelect+" WHERE r.id = ?", id))
}

// LatestReadingForUtility is what the new-reading form pre-fills prev1/prev2
// from: this month's "previous" is last month's "current". Returns
// sql.ErrNoRows when the utility has no readings yet.
func (s *Store) LatestReadingForUtility(utilityID int64) (ReadingView, error) {
	return scanReadingView(s.db.QueryRow(readingSelect+`
		WHERE r.utility_id = ?
		ORDER BY r.period DESC, r.id DESC
		LIMIT 1`, utilityID))
}

// UtilityMonthlyStatus is one active utility's month: whether it has been read,
// what it came to, and whether it has been paid.
type UtilityMonthlyStatus struct {
	UtilityID    int64
	UtilityName  string
	UtilityIcon  string
	UtilityColor string
	UtilityURL   string
	AddressID    int64
	AddressName  string
	HasReading   bool
	// Amount sums the period's readings rather than taking one, because the
	// month a meter is replaced has two: the last on the old meter and the
	// first on the new.
	Amount float64
	// Paid is true only when every reading in the month is paid, for the same
	// reason.
	Paid         bool
	Currency     string
	ReadingCount int
	FirstReading int64 // the reading's id when there is exactly one, else 0
	// The latest detail, for the annotation under the row. Meaningful when
	// ReadingCount is 1; nil for a flat tariff or an unread month.
	Curr1     *float64
	Curr2     *float64
	Consumed1 *float64
	Consumed2 *float64
	Unit      string
}

// CurrentMonthStatus returns one row per active utility for `period` — the
// question the readings screen exists to answer: what is still not entered,
// and what is entered but not paid. addressID of 0 means every property.
func (s *Store) CurrentMonthStatus(period string, addressID int64) ([]UtilityMonthlyStatus, error) {
	q := `
		SELECT u.id, u.name, u.icon, u.color, u.url,
		       a.id, a.name, a.currency,
		       COUNT(r.id),
		       COALESCE(SUM(r.amount), 0),
		       SUM(CASE WHEN r.id IS NOT NULL AND r.paid_date IS NULL THEN 1 ELSE 0 END),
		       COALESCE(MIN(r.id), 0),
		       MAX(r.curr1), MAX(r.curr2), SUM(r.consumed1), SUM(r.consumed2),
		       COALESCE(MAX(t.unit), '')
		FROM utilities u
		JOIN addresses a ON a.id = u.address_id
		LEFT JOIN readings r ON r.utility_id = u.id AND r.period = ?
		LEFT JOIN tariffs  t ON t.id = r.tariff_id
		WHERE u.active = 1 AND a.active = 1`
	args := []any{period}
	if addressID != 0 {
		q += ` AND a.id = ?`
		args = append(args, addressID)
	}
	q += ` GROUP BY u.id ORDER BY a.sort_order, a.id, u.sort_order, u.id`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UtilityMonthlyStatus
	for rows.Next() {
		var st UtilityMonthlyStatus
		var unpaid int
		var curr1, curr2, consumed1, consumed2 sql.NullFloat64
		if err := rows.Scan(&st.UtilityID, &st.UtilityName, &st.UtilityIcon, &st.UtilityColor, &st.UtilityURL,
			&st.AddressID, &st.AddressName, &st.Currency,
			&st.ReadingCount, &st.Amount, &unpaid, &st.FirstReading,
			&curr1, &curr2, &consumed1, &consumed2, &st.Unit); err != nil {
			return nil, err
		}
		st.Curr1, st.Curr2 = ptrFloat(curr1), ptrFloat(curr2)
		st.Consumed1, st.Consumed2 = ptrFloat(consumed1), ptrFloat(consumed2)
		st.HasReading = st.ReadingCount > 0
		st.Paid = st.ReadingCount > 0 && unpaid == 0
		out = append(out, st)
	}
	return out, rows.Err()
}
