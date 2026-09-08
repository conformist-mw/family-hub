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
		SELECT pm.id, pm.enrollment_id, p.name, e.name, e.description, e.billing_type, pm.date, pm.amount,
		       pm.lessons_paid, pm.covers_from, pm.covers_until, pm.comment, pm.kind, pm.label
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
		if err := rows.Scan(&pm.ID, &pm.EnrollmentID, &pm.Person, &pm.Class, &pm.ClassDesc, &pm.Billing, &pm.Date, &pm.Amount,
			&pm.LessonsPaid, &pm.CoversFrom, &pm.CoversUntil, &pm.Comment, &pm.Kind, &pm.Label); err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	return out, rows.Err()
}

// PaymentsForEnrollment returns the enrollment's course payments oldest first
// — the order their lessons are spent in (see audit.RemainingPacks).
//
// Extras are filtered out at the source rather than skipped downstream. This
// function is about packs of lessons, and RemainingPacks ignoring a row with
// no lesson count is a property of that function, not a promise of this one.
func (s *Store) PaymentsForEnrollment(enrollmentID int64) ([]model.Payment, error) {
	rows, err := s.db.Query(`
		SELECT id, date, amount, lessons_paid, covers_from, covers_until, comment, kind, label
		FROM payments WHERE enrollment_id = ? AND kind = 'course'
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
			&pm.CoversFrom, &pm.CoversUntil, &pm.Comment, &pm.Kind, &pm.Label); err != nil {
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
		SELECT pm.id, pm.enrollment_id, p.name, e.name, e.description, e.billing_type, pm.date, pm.amount,
		       pm.lessons_paid, pm.covers_from, pm.covers_until, pm.comment, pm.kind, pm.label
		FROM payments pm
		JOIN enrollments e ON e.id = pm.enrollment_id
		JOIN persons p     ON p.id = e.person_id
		WHERE pm.id = ?`, id).Scan(
		&pm.ID, &pm.EnrollmentID, &pm.Person, &pm.Class, &pm.ClassDesc, &pm.Billing, &pm.Date, &pm.Amount,
		&pm.LessonsPaid, &pm.CoversFrom, &pm.CoversUntil, &pm.Comment, &pm.Kind, &pm.Label)
	return pm, err
}

// courseByDefault fills in an unset Kind.
//
// The column has DEFAULT 'course', but a default only applies to an INSERT
// that does not name the column — and both writes below do, because an extra
// has to be able to say so. That leaves an empty Kind reaching the table
// verbatim as kind=”, which the `kind = 'course'` filters on
// PaymentsForEnrollment and LastPaymentDate would then drop: the bot's /packs
// goes empty and the default audit period resets to all time, with nothing
// reporting an error.
//
// payments.Form.Parse sets Kind on both of its paths, so the surfaces never
// rely on this. It exists so that the store is total: no caller can write a
// row whose kind is a value the rest of the code does not handle.
func courseByDefault(p model.Payment) model.Payment {
	if p.Kind == "" {
		p.Kind = model.PaymentKindCourse
	}
	return p
}

func (s *Store) CreatePayment(p model.Payment) (int64, error) {
	p = courseByDefault(p)
	res, err := s.db.Exec(`
		INSERT INTO payments (enrollment_id, date, amount, lessons_paid, covers_from, covers_until, comment, kind, label)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.EnrollmentID, p.Date, p.Amount, p.LessonsPaid, p.CoversFrom, p.CoversUntil, p.Comment, p.Kind, p.Label)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePayment(p model.Payment) error {
	p = courseByDefault(p)
	_, err := s.db.Exec(`
		UPDATE payments SET enrollment_id=?, date=?, amount=?, lessons_paid=?, covers_from=?, covers_until=?,
		       comment=?, kind=?, label=?
		WHERE id=?`,
		p.EnrollmentID, p.Date, p.Amount, p.LessonsPaid, p.CoversFrom, p.CoversUntil, p.Comment, p.Kind, p.Label, p.ID)
	return err
}

func (s *Store) DeletePayment(id int64) error {
	_, err := s.db.Exec(`DELETE FROM payments WHERE id=?`, id)
	return err
}
