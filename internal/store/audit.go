package store

import "familyhub/internal/model"

// AuditData is the raw material for the reconciliation page: everything the
// pure functions in internal/audit need, fetched in one place. Bounds are
// inclusive; an empty from/to means unbounded on that side.
type AuditData struct {
	Visits         []model.Visit          // ascending by date
	Payments       []model.Payment        // ascending by date
	OpeningBalance int                    // per-lesson lessons paid − done strictly before from; 0 when from is empty
	Slots          []model.Slot           // the enrollment's active weekly slots (forecast source)
	Absences       []model.TrainerAbsence // all absences of the enrollment's trainer; nil without a trainer
}

func (s *Store) AuditData(enrollmentID int64, from, to string) (AuditData, error) {
	var d AuditData

	visitQ := `
		SELECT id, date, status, comment FROM visits
		WHERE enrollment_id = ?`
	payQ := `
		SELECT id, date, amount, lessons_paid, covers_from, covers_until, comment FROM payments
		WHERE enrollment_id = ?`
	var visitArgs, payArgs []any
	visitArgs = append(visitArgs, enrollmentID)
	payArgs = append(payArgs, enrollmentID)
	if from != "" {
		visitQ += " AND date >= ?"
		payQ += " AND date >= ?"
		visitArgs = append(visitArgs, from)
		payArgs = append(payArgs, from)
	}
	if to != "" {
		visitQ += " AND date <= ?"
		payQ += " AND date <= ?"
		visitArgs = append(visitArgs, to)
		payArgs = append(payArgs, to)
	}
	visitQ += " ORDER BY date, id"
	payQ += " ORDER BY date, id"

	rows, err := s.db.Query(visitQ, visitArgs...)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var v model.Visit
		v.EnrollmentID = enrollmentID
		if err := rows.Scan(&v.ID, &v.Date, &v.Status, &v.Comment); err != nil {
			return d, err
		}
		d.Visits = append(d.Visits, v)
	}
	if err := rows.Err(); err != nil {
		return d, err
	}

	prows, err := s.db.Query(payQ, payArgs...)
	if err != nil {
		return d, err
	}
	defer prows.Close()
	for prows.Next() {
		var p model.Payment
		p.EnrollmentID = enrollmentID
		if err := prows.Scan(&p.ID, &p.Date, &p.Amount, &p.LessonsPaid, &p.CoversFrom, &p.CoversUntil, &p.Comment); err != nil {
			return d, err
		}
		d.Payments = append(d.Payments, p)
	}
	if err := prows.Err(); err != nil {
		return d, err
	}

	if from != "" {
		err = s.db.QueryRow(`
			SELECT COALESCE((SELECT SUM(lessons_paid) FROM payments
			                 WHERE enrollment_id = ? AND lessons_paid IS NOT NULL AND date < ?), 0)
			     - (SELECT COUNT(*) FROM visits
			        WHERE enrollment_id = ? AND status = 'done' AND date < ?)`,
			enrollmentID, from, enrollmentID, from).Scan(&d.OpeningBalance)
		if err != nil {
			return d, err
		}
	}

	srows, err := s.db.Query(`
		SELECT s.id, s.enrollment_id, v.weekday, v.time, v.duration_min, s.active
		FROM regular_slots s`+currentVersion+`
		WHERE s.enrollment_id = ? AND s.active = 1
		ORDER BY v.weekday, v.time`, enrollmentID)
	if err != nil {
		return d, err
	}
	defer srows.Close()
	for srows.Next() {
		var sl model.Slot
		if err := srows.Scan(&sl.ID, &sl.EnrollmentID, &sl.Weekday, &sl.Time, &sl.DurationMin, &sl.Active); err != nil {
			return d, err
		}
		d.Slots = append(d.Slots, sl)
	}
	if err := srows.Err(); err != nil {
		return d, err
	}

	d.Absences, err = s.AbsencesForEnrollment(enrollmentID)
	return d, err
}

// LastPaymentDate returns the date of the enrollment's most recent payment,
// or "" if it has none — the default audit period starts here.
func (s *Store) LastPaymentDate(enrollmentID int64) (string, error) {
	var date string
	err := s.db.QueryRow(`
		SELECT COALESCE(MAX(date), '') FROM payments WHERE enrollment_id = ?`,
		enrollmentID).Scan(&date)
	return date, err
}
