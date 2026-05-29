package store

import "lessons/internal/model"

func (s *Store) ListEnrollments(activeOnly bool) ([]model.Enrollment, error) {
	q := `
		SELECT e.id, e.person_id, p.name, e.name, e.description,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes,
		       (SELECT COUNT(*) FROM regular_slots s
		        WHERE s.enrollment_id = e.id AND s.active = 1) AS slot_count
		FROM enrollments e
		JOIN persons p ON p.id = e.person_id`
	if activeOnly {
		q += " WHERE e.active = 1"
	}
	q += " ORDER BY e.active DESC, p.name, e.name"

	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Enrollment
	for rows.Next() {
		var e model.Enrollment
		if err := rows.Scan(&e.ID, &e.PersonID, &e.Person, &e.Name, &e.Description,
			&e.BillingType, &e.CurrentPrice, &e.LowThreshold, &e.Active, &e.Notes,
			&e.SlotCount); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FrequentActiveEnrollments returns active enrollments ordered by how many
// visits they have, for quick-pick chips on the visit form.
func (s *Store) FrequentActiveEnrollments(limit int) ([]model.Enrollment, error) {
	rows, err := s.db.Query(`
		SELECT e.id, e.person_id, p.name, e.name, e.description,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes
		FROM enrollments e
		JOIN persons p ON p.id = e.person_id
		WHERE e.active = 1
		ORDER BY (SELECT COUNT(*) FROM visits v WHERE v.enrollment_id = e.id) DESC, p.name, e.name
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Enrollment
	for rows.Next() {
		var e model.Enrollment
		if err := rows.Scan(&e.ID, &e.PersonID, &e.Person, &e.Name, &e.Description,
			&e.BillingType, &e.CurrentPrice, &e.LowThreshold, &e.Active, &e.Notes); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) GetEnrollment(id int64) (model.Enrollment, error) {
	var e model.Enrollment
	err := s.db.QueryRow(`
		SELECT e.id, e.person_id, p.name, e.name, e.description,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes
		FROM enrollments e
		JOIN persons p ON p.id = e.person_id
		WHERE e.id = ?`, id).Scan(
		&e.ID, &e.PersonID, &e.Person, &e.Name, &e.Description,
		&e.BillingType, &e.CurrentPrice, &e.LowThreshold, &e.Active, &e.Notes)
	return e, err
}

func (s *Store) ListPersons() ([]model.Person, error) {
	rows, err := s.db.Query(`SELECT id, name, kind, active, notes FROM persons ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Person
	for rows.Next() {
		var p model.Person
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Active, &p.Notes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FrequentComments returns the most-used short visit comments, for quick-pick
// reason chips (e.g. "заболел").
func (s *Store) FrequentComments(limit int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT comment FROM visits
		WHERE comment <> '' AND length(comment) <= 40
		GROUP BY comment
		ORDER BY COUNT(*) DESC, comment
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DistinctClassNames returns class names already used, for input suggestions.
func (s *Store) DistinctClassNames() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT name FROM enrollments ORDER BY name`)
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
