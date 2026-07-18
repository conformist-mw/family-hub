package store

import (
	"errors"
	"strconv"
	"strings"

	"lessons/internal/model"
)

// ErrVisitExists reports an insert/update that would put a second visit on
// the same enrollment+date. The message is user-facing (shown in the web
// form), matching the ErrEnrollmentHasData convention.
var ErrVisitExists = errors.New("на эту дату уже есть запись по этому курсу")

type VisitFilter struct {
	PersonID int64
	Status   string
	Limit    int
	Offset   int
}

func (s *Store) ListVisits(f VisitFilter) ([]model.Visit, error) {
	var where []string
	var args []any
	if f.PersonID != 0 {
		where = append(where, "e.person_id = ?")
		args = append(args, f.PersonID)
	}
	if f.Status != "" {
		where = append(where, "v.status = ?")
		args = append(args, f.Status)
	}
	q := `
		SELECT v.id, v.enrollment_id, p.name, e.name, e.description, v.date, v.status, v.comment, v.created_at
		FROM visits v
		JOIN enrollments e ON e.id = v.enrollment_id
		JOIN persons p     ON p.id = e.person_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY v.date DESC, v.id DESC"
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
	var out []model.Visit
	for rows.Next() {
		var v model.Visit
		if err := rows.Scan(&v.ID, &v.EnrollmentID, &v.Person, &v.Class, &v.ClassDesc, &v.Date, &v.Status, &v.Comment, &v.Created); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetVisit(id int64) (model.Visit, error) {
	var v model.Visit
	err := s.db.QueryRow(`
		SELECT v.id, v.enrollment_id, p.name, e.name, e.description, v.date, v.status, v.comment
		FROM visits v
		JOIN enrollments e ON e.id = v.enrollment_id
		JOIN persons p     ON p.id = e.person_id
		WHERE v.id = ?`, id).Scan(
		&v.ID, &v.EnrollmentID, &v.Person, &v.Class, &v.ClassDesc, &v.Date, &v.Status, &v.Comment)
	return v, err
}

// CreateVisit inserts a visit; a concurrent or repeated insert for the same
// enrollment+date loses to the UNIQUE index and returns ErrVisitExists. The
// conflict is resolved by the database, not a prior existence check — two
// double-tap webhook requests can both pass such a check.
func (s *Store) CreateVisit(enrollmentID int64, date, status, comment string) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO visits (enrollment_id, date, status, comment)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(enrollment_id, date) DO NOTHING`, enrollmentID, date, status, comment)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrVisitExists
	}
	return res.LastInsertId()
}

func (s *Store) UpdateVisit(id, enrollmentID int64, date, status, comment string) error {
	_, err := s.db.Exec(`
		UPDATE visits SET enrollment_id=?, date=?, status=?, comment=?
		WHERE id=?`, enrollmentID, date, status, comment, id)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: visits.") {
		return ErrVisitExists
	}
	return err
}

// SetVisitComment updates only the comment. Used by the Telegram reason
// step, which runs after the visit row already exists.
func (s *Store) SetVisitComment(id int64, comment string) error {
	_, err := s.db.Exec(`UPDATE visits SET comment=? WHERE id=?`, comment, id)
	return err
}

func (s *Store) DeleteVisit(id int64) error {
	_, err := s.db.Exec(`DELETE FROM visits WHERE id=?`, id)
	return err
}
