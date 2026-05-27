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
	ByPerson   []PersonSpend
	ByCourse   []CourseSpend
	MaxMonth   float64
	MaxPerson  float64
	MaxCourse  float64
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

	monthRows, err := s.db.Query(`
		SELECT strftime('%Y-%m', date) AS ym, SUM(amount)
		FROM payments
		GROUP BY ym
		ORDER BY ym DESC
		LIMIT 12`)
	if err != nil {
		return st, err
	}
	defer monthRows.Close()
	for monthRows.Next() {
		var m MonthSpend
		if err := monthRows.Scan(&m.Month, &m.Amount); err != nil {
			return st, err
		}
		if m.Amount > st.MaxMonth {
			st.MaxMonth = m.Amount
		}
		st.ByMonth = append(st.ByMonth, m)
	}
	if err := monthRows.Err(); err != nil {
		return st, err
	}

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
