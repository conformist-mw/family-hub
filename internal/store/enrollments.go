package store

import "lessons/internal/model"

func (s *Store) ListEnrollments(activeOnly bool) ([]model.Enrollment, error) {
	q := `
		SELECT e.id, e.child_id, e.activity_id, c.name, a.name,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes
		FROM enrollments e
		JOIN children c   ON c.id = e.child_id
		JOIN activities a ON a.id = e.activity_id`
	if activeOnly {
		q += " WHERE e.active = 1"
	}
	q += " ORDER BY e.active DESC, c.name, a.name"

	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Enrollment
	for rows.Next() {
		var e model.Enrollment
		if err := rows.Scan(&e.ID, &e.ChildID, &e.ActivityID, &e.Child, &e.Activity,
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
		SELECT e.id, e.child_id, e.activity_id, c.name, a.name,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes
		FROM enrollments e
		JOIN children c   ON c.id = e.child_id
		JOIN activities a ON a.id = e.activity_id
		WHERE e.id = ?`, id).Scan(
		&e.ID, &e.ChildID, &e.ActivityID, &e.Child, &e.Activity,
		&e.BillingType, &e.CurrentPrice, &e.LowThreshold, &e.Active, &e.Notes)
	return e, err
}

func (s *Store) ListChildren() ([]model.Child, error) {
	rows, err := s.db.Query(`SELECT id, name, active FROM children ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Child
	for rows.Next() {
		var c model.Child
		if err := rows.Scan(&c.ID, &c.Name, &c.Active); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ListActivities() ([]model.Activity, error) {
	rows, err := s.db.Query(`SELECT id, name FROM activities ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Activity
	for rows.Next() {
		var a model.Activity
		if err := rows.Scan(&a.ID, &a.Name); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
