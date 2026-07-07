package store

import "lessons/internal/model"

// SlotsForWeekday returns active slots for the given weekday with the
// enrollment they belong to attached. The slot list is what the evening
// reminder iterates over.
type SlotWithEnrollment struct {
	Slot       model.Slot
	Enrollment model.Enrollment
}

func (s *Store) SlotsForWeekday(weekday int) ([]SlotWithEnrollment, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.enrollment_id, s.weekday, s.time, s.active,
		       e.id, e.person_id, p.name, e.name, e.description,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes
		FROM regular_slots s
		JOIN enrollments e ON e.id = s.enrollment_id
		JOIN persons p     ON p.id = e.person_id
		WHERE s.active = 1 AND e.active = 1 AND s.weekday = ?
		ORDER BY s.time, p.name, e.name`, weekday)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SlotWithEnrollment
	for rows.Next() {
		var x SlotWithEnrollment
		if err := rows.Scan(
			&x.Slot.ID, &x.Slot.EnrollmentID, &x.Slot.Weekday, &x.Slot.Time, &x.Slot.Active,
			&x.Enrollment.ID, &x.Enrollment.PersonID, &x.Enrollment.Person,
			&x.Enrollment.Name, &x.Enrollment.Description,
			&x.Enrollment.BillingType, &x.Enrollment.CurrentPrice,
			&x.Enrollment.LowThreshold, &x.Enrollment.Active, &x.Enrollment.Notes,
		); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// AllActiveSlots returns every active slot (across all weekdays) with its
// enrollment attached — the source for the ICS calendar feed.
func (s *Store) AllActiveSlots() ([]SlotWithEnrollment, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.enrollment_id, s.weekday, s.time, s.duration_min, s.active,
		       e.id, e.person_id, p.name, e.name, e.description,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes
		FROM regular_slots s
		JOIN enrollments e ON e.id = s.enrollment_id
		JOIN persons p     ON p.id = e.person_id
		WHERE s.active = 1 AND e.active = 1
		ORDER BY s.weekday, s.time, p.name, e.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SlotWithEnrollment
	for rows.Next() {
		var x SlotWithEnrollment
		if err := rows.Scan(
			&x.Slot.ID, &x.Slot.EnrollmentID, &x.Slot.Weekday, &x.Slot.Time, &x.Slot.DurationMin, &x.Slot.Active,
			&x.Enrollment.ID, &x.Enrollment.PersonID, &x.Enrollment.Person,
			&x.Enrollment.Name, &x.Enrollment.Description,
			&x.Enrollment.BillingType, &x.Enrollment.CurrentPrice,
			&x.Enrollment.LowThreshold, &x.Enrollment.Active, &x.Enrollment.Notes,
		); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// VisitExistsForDate reports whether a visit already exists for the given
// enrollment on the given date. Used to avoid duplicate inserts when a user
// taps a reminder button after manually entering the visit.
func (s *Store) VisitExistsForDate(enrollmentID int64, date string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM visits WHERE enrollment_id = ? AND date = ?`,
		enrollmentID, date).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
