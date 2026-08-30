package store

import "familyhub/internal/model"

// currentVersion joins a regular_slots row to the version in force right now.
//
// Most readers — the dashboard line, the enrollment editor, the evening
// reminder, the forecast — want today's schedule and nothing else, so they
// keep working against model.Slot as though it were still one row. Only the
// calendar feed needs the whole history, and it asks for it by name
// (SlotHistories).
//
// Ordered by valid_from_at then id so two versions stamped in the same minute
// resolve to the later write rather than to an arbitrary one.
const currentVersion = `
	JOIN slot_versions v ON v.id = (
	    SELECT id FROM slot_versions
	    WHERE slot_id = s.id AND valid_from_at <= strftime('%Y-%m-%dT%H:%M','now','localtime')
	    ORDER BY valid_from_at DESC, id DESC LIMIT 1
	)`

// SlotsForWeekday returns active slots for the given weekday with the
// enrollment they belong to attached. The slot list is what the evening
// reminder iterates over.
type SlotWithEnrollment struct {
	Slot       model.Slot
	Enrollment model.Enrollment
}

// SlotsForWeekday filters out enrollments whose trainer is absent on the
// given date — that one condition silences both the post-slot reminder and
// the pre-slot balance warning. Enrollments without a trainer never match
// the subquery and are never muted.
//
// exceptions_only enrollments are filtered out for the same reason: their
// slots exist for the calendar and the forecast, not to be confirmed one
// evening at a time. Their slots still reach the ICS feed via SlotHistories.
//
// The weekday and time come from the version in force now, which is the right
// one to ask about this evening.
func (s *Store) SlotsForWeekday(weekday int, date string) ([]SlotWithEnrollment, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.enrollment_id, v.weekday, v.time, s.active,
		       e.id, e.person_id, p.name, e.name, e.description,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes,
		       e.payment_notice_min
		FROM regular_slots s`+currentVersion+`
		JOIN enrollments e ON e.id = s.enrollment_id
		JOIN persons p     ON p.id = e.person_id
		WHERE s.active = 1 AND e.active = 1 AND v.weekday = ?
		  AND e.attendance_mode = 'per_session'
		  AND NOT EXISTS (
		      SELECT 1 FROM trainer_absences a
		      WHERE a.trainer_id = e.trainer_id
		        AND ? BETWEEN a.date_from AND a.date_to
		  )
		ORDER BY v.time, p.name, e.name`, weekday, date)
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
			&x.Enrollment.PaymentNoticeMin,
		); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// AllActiveSlots returns every active slot (across all weekdays) as it stands
// today, with its enrollment attached. The dashboard line and the Mini App's
// schedule read it — surfaces that show the current timetable, not its
// history. The calendar feed wants SlotHistories instead.
func (s *Store) AllActiveSlots() ([]SlotWithEnrollment, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.enrollment_id, v.weekday, v.time, v.duration_min, s.active,
		       e.id, e.person_id, p.name, e.name, e.description,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes,
		       e.trainer_id
		FROM regular_slots s` + currentVersion + `
		JOIN enrollments e ON e.id = s.enrollment_id
		JOIN persons p     ON p.id = e.person_id
		WHERE s.active = 1 AND e.active = 1
		ORDER BY v.weekday, v.time, p.name, e.name`)
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
			&x.Enrollment.TrainerID,
		); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// UpcomingAbsences returns absences of active trainers that have not fully
// passed as of the given date — the ICS feed renders them as all-day events,
// and the expansion drops the lessons that fall inside them.
func (s *Store) UpcomingAbsences(date string) ([]model.TrainerAbsence, error) {
	rows, err := s.db.Query(`
		SELECT a.id, a.trainer_id, t.name, a.date_from, a.date_to, a.kind, a.comment
		FROM trainer_absences a
		JOIN trainers t ON t.id = a.trainer_id
		WHERE t.active = 1 AND a.date_to >= ?
		ORDER BY a.date_from, a.id`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TrainerAbsence
	for rows.Next() {
		var a model.TrainerAbsence
		if err := rows.Scan(&a.ID, &a.TrainerID, &a.Trainer, &a.DateFrom, &a.DateTo, &a.Kind, &a.Comment); err != nil {
			return nil, err
		}
		out = append(out, a)
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

// SlotHistory is one weekly slot with every version of it, oldest first — what
// the calendar feed expands, so that a window is rendered with the schedule
// that was actually in force over it rather than with today's.
type SlotHistory struct {
	SlotID     int64
	Enrollment model.Enrollment
	Versions   []model.SlotVersion
}

// SlotHistories returns every active slot with its full version list. The
// enrollment carries its trainer id so the expansion can match lessons against
// trainer absences.
//
// Two queries rather than one join: a join repeats the enrollment once per
// version, and assembling it back costs more than the second round trip on a
// table this size.
func (s *Store) SlotHistories() ([]SlotHistory, error) {
	rows, err := s.db.Query(`
		SELECT s.id, e.id, e.person_id, p.name, e.name, e.description,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes,
		       e.trainer_id
		FROM regular_slots s
		JOIN enrollments e ON e.id = s.enrollment_id
		JOIN persons p     ON p.id = e.person_id
		WHERE s.active = 1 AND e.active = 1
		ORDER BY p.name, e.name, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SlotHistory
	byID := make(map[int64]int)
	for rows.Next() {
		var h SlotHistory
		if err := rows.Scan(
			&h.SlotID,
			&h.Enrollment.ID, &h.Enrollment.PersonID, &h.Enrollment.Person,
			&h.Enrollment.Name, &h.Enrollment.Description,
			&h.Enrollment.BillingType, &h.Enrollment.CurrentPrice,
			&h.Enrollment.LowThreshold, &h.Enrollment.Active, &h.Enrollment.Notes,
			&h.Enrollment.TrainerID,
		); err != nil {
			return nil, err
		}
		byID[h.SlotID] = len(out)
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}

	vrows, err := s.db.Query(`
		SELECT v.id, v.slot_id, v.valid_from_at, v.weekday, v.time, v.duration_min
		FROM slot_versions v
		JOIN regular_slots s ON s.id = v.slot_id
		JOIN enrollments e   ON e.id = s.enrollment_id
		WHERE s.active = 1 AND e.active = 1
		ORDER BY v.slot_id, v.valid_from_at, v.id`)
	if err != nil {
		return nil, err
	}
	defer vrows.Close()
	for vrows.Next() {
		var v model.SlotVersion
		if err := vrows.Scan(&v.ID, &v.SlotID, &v.ValidFromAt, &v.Weekday, &v.Time, &v.DurationMin); err != nil {
			return nil, err
		}
		if i, ok := byID[v.SlotID]; ok {
			out[i].Versions = append(out[i].Versions, v)
		}
	}
	return out, vrows.Err()
}

// VersionsFor returns one slot's versions, oldest first.
func (s *Store) VersionsFor(slotID int64) ([]model.SlotVersion, error) {
	rows, err := s.db.Query(`
		SELECT id, slot_id, valid_from_at, weekday, time, duration_min
		FROM slot_versions WHERE slot_id = ?
		ORDER BY valid_from_at, id`, slotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SlotVersion
	for rows.Next() {
		var v model.SlotVersion
		if err := rows.Scan(&v.ID, &v.SlotID, &v.ValidFromAt, &v.Weekday, &v.Time, &v.DurationMin); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
