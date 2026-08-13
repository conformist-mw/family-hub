package store

type MonthSpend struct {
	Month  string
	Amount float64
}

type PersonSpend struct {
	Person string
	Amount float64
}

type CourseSpend struct {
	Person    string
	Class     string
	ClassDesc string
	Amount    float64
	Attended  int
}

type Stats struct {
	TotalAll   float64
	TotalYear  float64
	TotalMonth float64
	ByMonth    []MonthSpend
	ByPeriod   []MonthSpend
	ByPerson   []PersonSpend
	ByCourse   []CourseSpend
	MaxMonth   float64
	MaxPeriod  float64
	MaxPerson  float64
	MaxCourse  float64
}

// spendByMonth sums payments into the last 12 buckets produced by groupExpr,
// which must be a SQL expression over one payments row yielding "YYYY-MM".
// It is interpolated, not bound: a bucket expression is code, and both call
// sites are literals in this file.
func (s *Store) spendByMonth(groupExpr string) ([]MonthSpend, float64, error) {
	rows, err := s.db.Query(`
		SELECT ` + groupExpr + ` AS ym, SUM(amount)
		FROM payments
		GROUP BY ym
		ORDER BY ym DESC
		LIMIT 12`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []MonthSpend
	var max float64
	for rows.Next() {
		var m MonthSpend
		if err := rows.Scan(&m.Month, &m.Amount); err != nil {
			return nil, 0, err
		}
		if m.Amount > max {
			max = m.Amount
		}
		out = append(out, m)
	}
	return out, max, rows.Err()
}

func (s *Store) Stats() (Stats, error) {
	var st Stats

	if err := s.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payments`).Scan(&st.TotalAll); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payments WHERE date >= date('now','localtime','start of year')`).Scan(&st.TotalYear); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM payments WHERE date >= date('now','localtime','start of month')`).Scan(&st.TotalMonth); err != nil {
		return st, err
	}

	// Two month-by-month views of the same rows, deliberately not one query.
	// ByMonth groups on the transfer date — when money left the account.
	// ByPeriod groups on what was bought: September's fee paid on 28 August
	// belongs to August in the first and to September in the second. The
	// second is also the only place a skipped month shows up, since
	// coverageFromToday merges adjacent periods and hides the gap.
	byMonth, maxMonth, err := s.spendByMonth(`strftime('%Y-%m', date)`)
	if err != nil {
		return st, err
	}
	st.ByMonth, st.MaxMonth = byMonth, maxMonth

	// Per-lesson payments have no coverage range; for them the money and what
	// it bought coincide, so they fall back to the payment date.
	byPeriod, maxPeriod, err := s.spendByMonth(
		`COALESCE(strftime('%Y-%m', covers_from), strftime('%Y-%m', date))`)
	if err != nil {
		return st, err
	}
	st.ByPeriod, st.MaxPeriod = byPeriod, maxPeriod

	personRows, err := s.db.Query(`
		SELECT p.name, SUM(pm.amount)
		FROM payments pm
		JOIN enrollments e ON e.id = pm.enrollment_id
		JOIN persons p     ON p.id = e.person_id
		GROUP BY p.id
		ORDER BY SUM(pm.amount) DESC`)
	if err != nil {
		return st, err
	}
	defer personRows.Close()
	for personRows.Next() {
		var p PersonSpend
		if err := personRows.Scan(&p.Person, &p.Amount); err != nil {
			return st, err
		}
		if p.Amount > st.MaxPerson {
			st.MaxPerson = p.Amount
		}
		st.ByPerson = append(st.ByPerson, p)
	}
	if err := personRows.Err(); err != nil {
		return st, err
	}

	courseRows, err := s.db.Query(`
		SELECT p.name, e.name, e.description, SUM(pm.amount) AS amt,
		       (SELECT COUNT(*) FROM visits v WHERE v.enrollment_id = e.id AND v.status='done') AS done
		FROM payments pm
		JOIN enrollments e ON e.id = pm.enrollment_id
		JOIN persons p     ON p.id = e.person_id
		GROUP BY e.id
		ORDER BY amt DESC`)
	if err != nil {
		return st, err
	}
	defer courseRows.Close()
	for courseRows.Next() {
		var c CourseSpend
		if err := courseRows.Scan(&c.Person, &c.Class, &c.ClassDesc, &c.Amount, &c.Attended); err != nil {
			return st, err
		}
		if c.Amount > st.MaxCourse {
			st.MaxCourse = c.Amount
		}
		st.ByCourse = append(st.ByCourse, c)
	}
	return st, courseRows.Err()
}
