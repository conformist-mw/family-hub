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

// ListSlots returns an enrollment's slots as they stand today — the editor's
// view. The version history is VersionsFor.
func (s *Store) ListSlots(enrollmentID int64) ([]model.Slot, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.enrollment_id, v.weekday, v.time, v.duration_min, s.active
		FROM regular_slots s`+currentVersion+`
		WHERE s.enrollment_id=?
		ORDER BY v.weekday, v.time`, enrollmentID)
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

// CreateSlot inserts the slot and its first version in one transaction. A slot
// with no version is invisible to every read, so the two must not be able to
// come apart.
//
// validFrom is when the schedule starts applying — for a brand-new slot, now.
// It deliberately does not reach back: a course entered in October did not
// silently happen all September.
func (s *Store) CreateSlot(enrollmentID int64, weekday int, t string, durationMin int, validFrom string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO regular_slots (enrollment_id) VALUES (?)`, enrollmentID)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO slot_versions (slot_id, valid_from_at, weekday, time, duration_min)
		VALUES (?, ?, ?, ?, ?)`, id, validFrom, weekday, t, durationMin); err != nil {
		return err
	}
	return tx.Commit()
}

// AddSlotVersion changes when a lesson happens from a moment onwards, leaving
// everything before it exactly as it was recorded. This is the ordinary edit:
// "from September Логопед moved to Thursday".
//
// The slot id survives, which is what makes the history one story rather than
// an old slot and an unrelated new one.
//
// Saving twice inside the same minute collides on (slot_id, valid_from_at) —
// that second save is the person correcting the first, so it amends the
// version instead of failing at them.
func (s *Store) AddSlotVersion(slotID int64, validFrom string, weekday int, t string, durationMin int) error {
	_, err := s.db.Exec(`
		INSERT INTO slot_versions (slot_id, valid_from_at, weekday, time, duration_min)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (slot_id, valid_from_at) DO UPDATE SET
		    weekday = excluded.weekday,
		    time = excluded.time,
		    duration_min = excluded.duration_min`,
		slotID, validFrom, weekday, t, durationMin)
	return err
}

// AmendSlotVersion corrects a version in place — "I mistyped it, it was always
// 13:35" — as opposed to AddSlotVersion's "from now on". It rewrites the past
// on purpose, because the past as recorded was wrong.
func (s *Store) AmendSlotVersion(versionID int64, weekday int, t string, durationMin int) error {
	_, err := s.db.Exec(`
		UPDATE slot_versions SET weekday=?, time=?, duration_min=? WHERE id=?`,
		weekday, t, durationMin, versionID)
	return err
}

// GetSlot returns the slot as it stands today.
func (s *Store) GetSlot(id int64) (model.Slot, error) {
	var sl model.Slot
	err := s.db.QueryRow(`
		SELECT s.id, s.enrollment_id, v.weekday, v.time, v.duration_min, s.active
		FROM regular_slots s`+currentVersion+`
		WHERE s.id=?`, id).
		Scan(&sl.ID, &sl.EnrollmentID, &sl.Weekday, &sl.Time, &sl.DurationMin, &sl.Active)
	return sl, err
}

// DeleteSlot removes the slot and, by cascade, its versions. Deleting is
// still a delete rather than an end-dating: a slot removed by hand was
// entered by mistake, and "this course stopped happening" is what active = 0
// is for.
func (s *Store) DeleteSlot(id int64) error {
	_, err := s.db.Exec(`DELETE FROM regular_slots WHERE id=?`, id)
	return err
}
