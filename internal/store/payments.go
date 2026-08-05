package store

import (
	"strconv"
	"strings"

	"familyhub/internal/model"
)

type PaymentFilter struct {
	PersonID int64
	Limit    int
	Offset   int
}

func (s *Store) ListPayments(f PaymentFilter) ([]model.Payment, error) {
	var where []string
	var args []any
	if f.PersonID != 0 {
		where = append(where, "e.person_id = ?")
		args = append(args, f.PersonID)
	}
	q := `
		SELECT pm.id, pm.enrollment_id, p.name, e.name, e.description, pm.date, pm.amount,
		       pm.lessons_paid, pm.covers_from, pm.covers_until, pm.comment
		FROM payments pm
		JOIN enrollments e ON e.id = pm.enrollment_id
		JOIN persons p     ON p.id = e.person_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY pm.date DESC, pm.id DESC"
	if f.Limit > 0 {
		q += " LIMIT " + strconv.Itoa(f.Limit)
	}
	if f.Offset > 0 {
		q += " OFFSET " + strconv.Itoa(f.Offset)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Payment
	for rows.Next() {
		var pm model.Payment
		if err := rows.Scan(&pm.ID, &pm.EnrollmentID, &pm.Person, &pm.Class, &pm.ClassDesc, &pm.Date, &pm.Amount,
			&pm.LessonsPaid, &pm.CoversFrom, &pm.CoversUntil, &pm.Comment); err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	return out, rows.Err()
}

// PaymentsForEnrollment returns the enrollment's payments oldest first — the
// order their lessons are spent in (see audit.RemainingPacks).
func (s *Store) PaymentsForEnrollment(enrollmentID int64) ([]model.Payment, error) {
	rows, err := s.db.Query(`
		SELECT id, date, amount, lessons_paid, covers_from, covers_until, comment
		FROM payments WHERE enrollment_id = ?
		ORDER BY date, id`, enrollmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Payment
	for rows.Next() {
		var pm model.Payment
		pm.EnrollmentID = enrollmentID
		if err := rows.Scan(&pm.ID, &pm.Date, &pm.Amount, &pm.LessonsPaid,
			&pm.CoversFrom, &pm.CoversUntil, &pm.Comment); err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	return out, rows.Err()
}

func (s *Store) TotalPaid(personID int64) (float64, error) {
	q := `SELECT COALESCE(SUM(pm.amount),0) FROM payments pm JOIN enrollments e ON e.id=pm.enrollment_id`
	var args []any
	if personID != 0 {
		q += " WHERE e.person_id = ?"
		args = append(args, personID)
	}
	var total float64
	err := s.db.QueryRow(q, args...).Scan(&total)
	return total, err
}

func (s *Store) GetPayment(id int64) (model.Payment, error) {
	var pm model.Payment
	err := s.db.QueryRow(`
		SELECT pm.id, pm.enrollment_id, p.name, e.name, e.description, pm.date, pm.amount,
		       pm.lessons_paid, pm.covers_from, pm.covers_until, pm.comment
		FROM payments pm
		JOIN enrollments e ON e.id = pm.enrollment_id
		JOIN persons p     ON p.id = e.person_id
		WHERE pm.id = ?`, id).Scan(
		&pm.ID, &pm.EnrollmentID, &pm.Person, &pm.Class, &pm.ClassDesc, &pm.Date, &pm.Amount,
		&pm.LessonsPaid, &pm.CoversFrom, &pm.CoversUntil, &pm.Comment)
	return pm, err
}

func (s *Store) CreatePayment(p model.Payment) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO payments (enrollment_id, date, amount, lessons_paid, covers_from, covers_until, comment)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.EnrollmentID, p.Date, p.Amount, p.LessonsPaid, p.CoversFrom, p.CoversUntil, p.Comment)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePayment(p model.Payment) error {
	_, err := s.db.Exec(`
		UPDATE payments SET enrollment_id=?, date=?, amount=?, lessons_paid=?, covers_from=?, covers_until=?, comment=?
		WHERE id=?`,
		p.EnrollmentID, p.Date, p.Amount, p.LessonsPaid, p.CoversFrom, p.CoversUntil, p.Comment, p.ID)
	return err
}

func (s *Store) DeletePayment(id int64) error {
	_, err := s.db.Exec(`DELETE FROM payments WHERE id=?`, id)
	return err
}
