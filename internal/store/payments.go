package store

import (
	"strconv"
	"strings"

	"lessons/internal/model"
)

type PaymentFilter struct {
	ChildID int64
	Limit   int
}

func (s *Store) ListPayments(f PaymentFilter) ([]model.Payment, error) {
	var where []string
	var args []any
	if f.ChildID != 0 {
		where = append(where, "e.child_id = ?")
		args = append(args, f.ChildID)
	}
	q := `
		SELECT p.id, p.enrollment_id, c.name, a.name, p.date, p.amount,
		       p.lessons_paid, p.covers_from, p.covers_until, p.comment
		FROM payments p
		JOIN enrollments e ON e.id = p.enrollment_id
		JOIN children c    ON c.id = e.child_id
		JOIN activities a  ON a.id = e.activity_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY p.date DESC, p.id DESC"
	if f.Limit > 0 {
		q += " LIMIT " + strconv.Itoa(f.Limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Payment
	for rows.Next() {
		var p model.Payment
		if err := rows.Scan(&p.ID, &p.EnrollmentID, &p.Child, &p.Activity, &p.Date, &p.Amount,
			&p.LessonsPaid, &p.CoversFrom, &p.CoversUntil, &p.Comment); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) TotalPaid(childID int64) (float64, error) {
	q := `SELECT COALESCE(SUM(p.amount),0) FROM payments p JOIN enrollments e ON e.id=p.enrollment_id`
	var args []any
	if childID != 0 {
		q += " WHERE e.child_id = ?"
		args = append(args, childID)
	}
	var total float64
	err := s.db.QueryRow(q, args...).Scan(&total)
	return total, err
}

func (s *Store) GetPayment(id int64) (model.Payment, error) {
	var p model.Payment
	err := s.db.QueryRow(`
		SELECT p.id, p.enrollment_id, c.name, a.name, p.date, p.amount,
		       p.lessons_paid, p.covers_from, p.covers_until, p.comment
		FROM payments p
		JOIN enrollments e ON e.id = p.enrollment_id
		JOIN children c    ON c.id = e.child_id
		JOIN activities a  ON a.id = e.activity_id
		WHERE p.id = ?`, id).Scan(
		&p.ID, &p.EnrollmentID, &p.Child, &p.Activity, &p.Date, &p.Amount,
		&p.LessonsPaid, &p.CoversFrom, &p.CoversUntil, &p.Comment)
	return p, err
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
