package store

import (
	"database/sql"
	"errors"
	"strings"

	"familyhub/internal/model"
)

var ErrEnrollmentHasData = errors.New("у курсу є заняття або оплати — заархівуй замість видалення")

func (s *Store) getOrCreatePerson(name string) (int64, error) {
	name = strings.TrimSpace(name)
	var id int64
	err := s.db.QueryRow("SELECT id FROM persons WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := s.db.Exec("INSERT INTO persons (name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CreateEnrollment takes the whole struct rather than a positional list: the
// argument count had already outgrown the form, and the two school fields
// would have pushed it to eleven. e.Person is the person's name, resolved (or
// created) here; e.ID is ignored.
func (s *Store) CreateEnrollment(e model.Enrollment) (int64, error) {
	personID, err := s.getOrCreatePerson(e.Person)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`
		INSERT INTO enrollments (person_id, name, description, billing_type, current_price,
		                         low_threshold, notes, trainer_id, attendance_mode, payment_instructions,
		                         payment_notice_min)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		personID, strings.TrimSpace(e.Name), e.Description, e.BillingType, e.CurrentPrice,
		e.LowThreshold, e.Notes, e.TrainerID, e.AttendanceMode, e.PaymentInstructions,
		e.PaymentNoticeMin)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateEnrollment writes every editable field of e, keyed by e.ID. The person
// is not among them — a course does not change who attends it.
func (s *Store) UpdateEnrollment(e model.Enrollment) error {
	_, err := s.db.Exec(`
		UPDATE enrollments
		SET name=?, description=?, billing_type=?, current_price=?, low_threshold=?,
		    active=?, notes=?, trainer_id=?, attendance_mode=?, payment_instructions=?,
		    payment_notice_min=?
		WHERE id=?`,
		strings.TrimSpace(e.Name), e.Description, e.BillingType, e.CurrentPrice, e.LowThreshold,
		e.Active, e.Notes, e.TrainerID, e.AttendanceMode, e.PaymentInstructions,
		e.PaymentNoticeMin, e.ID)
	return err
}

func (s *Store) DeleteEnrollment(id int64) error {
	var n int
	err := s.db.QueryRow(`
		SELECT (SELECT COUNT(*) FROM visits WHERE enrollment_id=?) +
		       (SELECT COUNT(*) FROM payments WHERE enrollment_id=?)`, id, id).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrEnrollmentHasData
	}
	_, err = s.db.Exec(`DELETE FROM enrollments WHERE id=?`, id)
	return err
}

func (s *Store) ListSlots(enrollmentID int64) ([]model.Slot, error) {
	rows, err := s.db.Query(`
		SELECT id, enrollment_id, weekday, time, duration_min, active
		FROM regular_slots WHERE enrollment_id=?
		ORDER BY weekday, time`, enrollmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Slot
	for rows.Next() {
		var sl model.Slot
		if err := rows.Scan(&sl.ID, &sl.EnrollmentID, &sl.Weekday, &sl.Time, &sl.DurationMin, &sl.Active); err != nil {
			return nil, err
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

func (s *Store) CreateSlot(enrollmentID int64, weekday int, t string, durationMin int) error {
	_, err := s.db.Exec(`
		INSERT INTO regular_slots (enrollment_id, weekday, time, duration_min) VALUES (?, ?, ?, ?)`,
		enrollmentID, weekday, t, durationMin)
	return err
}

// UpdateSlot moves an existing weekly slot. Editing beats delete-and-recreate
// because the row id is what the reminder scheduler and the ICS feed key on:
// recreating a slot would hand Home Assistant a new uid and duplicate the
// event in the family calendar.
func (s *Store) UpdateSlot(id int64, weekday int, t string, durationMin int) error {
	_, err := s.db.Exec(`
		UPDATE regular_slots SET weekday=?, time=?, duration_min=? WHERE id=?`,
		weekday, t, durationMin, id)
	return err
}

func (s *Store) GetSlot(id int64) (model.Slot, error) {
	var sl model.Slot
	err := s.db.QueryRow(`
		SELECT id, enrollment_id, weekday, time, duration_min, active
		FROM regular_slots WHERE id=?`, id).
		Scan(&sl.ID, &sl.EnrollmentID, &sl.Weekday, &sl.Time, &sl.DurationMin, &sl.Active)
	return sl, err
}

func (s *Store) DeleteSlot(id int64) error {
	_, err := s.db.Exec(`DELETE FROM regular_slots WHERE id=?`, id)
	return err
}
