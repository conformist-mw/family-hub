package store

import (
	"database/sql"
	"errors"
	"strings"

	"lessons/internal/model"
)

var ErrDuplicateEnrollment = errors.New("такой курс уже есть")
var ErrEnrollmentHasData = errors.New("у курса есть занятия или оплаты — заархивируй вместо удаления")

func (s *Store) getOrCreateChild(name string) (int64, error) {
	return getOrCreateNamed(s.db, "children", name)
}

func (s *Store) getOrCreateActivity(name string) (int64, error) {
	return getOrCreateNamed(s.db, "activities", name)
}

func getOrCreateNamed(db *sql.DB, table, name string) (int64, error) {
	name = strings.TrimSpace(name)
	var id int64
	err := db.QueryRow("SELECT id FROM "+table+" WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := db.Exec("INSERT INTO "+table+" (name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) CreateEnrollment(childName, activityName, billingType string, price float64, lowThreshold int, notes string) (int64, error) {
	childID, err := s.getOrCreateChild(childName)
	if err != nil {
		return 0, err
	}
	activityID, err := s.getOrCreateActivity(activityName)
	if err != nil {
		return 0, err
	}
	var existing int64
	err = s.db.QueryRow(`SELECT id FROM enrollments WHERE child_id=? AND activity_id=?`, childID, activityID).Scan(&existing)
	if err == nil {
		return 0, ErrDuplicateEnrollment
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := s.db.Exec(`
		INSERT INTO enrollments (child_id, activity_id, billing_type, current_price, low_threshold, notes)
		VALUES (?, ?, ?, ?, ?, ?)`,
		childID, activityID, billingType, price, lowThreshold, notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateEnrollment(id int64, billingType string, price float64, lowThreshold int, active bool, notes string) error {
	_, err := s.db.Exec(`
		UPDATE enrollments
		SET billing_type=?, current_price=?, low_threshold=?, active=?, notes=?
		WHERE id=?`,
		billingType, price, lowThreshold, active, notes, id)
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
		SELECT id, enrollment_id, weekday, time, active
		FROM regular_slots WHERE enrollment_id=?
		ORDER BY weekday, time`, enrollmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Slot
	for rows.Next() {
		var sl model.Slot
		if err := rows.Scan(&sl.ID, &sl.EnrollmentID, &sl.Weekday, &sl.Time, &sl.Active); err != nil {
			return nil, err
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

func (s *Store) CreateSlot(enrollmentID int64, weekday int, t string) error {
	_, err := s.db.Exec(`
		INSERT INTO regular_slots (enrollment_id, weekday, time) VALUES (?, ?, ?)`,
		enrollmentID, weekday, t)
	return err
}

func (s *Store) DeleteSlot(id int64) error {
	_, err := s.db.Exec(`DELETE FROM regular_slots WHERE id=?`, id)
	return err
}
