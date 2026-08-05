package store

import (
	"strconv"

	"familyhub/internal/model"
)

// Appointment methods carry the domain suffix even where the bare name would
// read fine (Create, Get, Between): one Store serves both the lessons domain
// and appointments, and CreateVisit/CreatePayment set the convention.

// CreateAppointment inserts one appointment and returns it with its id.
func (s *Store) CreateAppointment(a model.Appointment) (model.Appointment, error) {
	res, err := s.db.Exec(`
		INSERT INTO appointments (title, person, location, starts_at, ends_at, status, note, raw, cost)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Title, a.Person, a.Location, a.StartsAt, nullIfEmpty(a.EndsAt),
		orDefault(a.Status, model.ApptStatusPlanned), a.Note, a.Raw, a.Cost)
	if err != nil {
		return model.Appointment{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Appointment{}, err
	}
	return s.GetAppointment(id)
}

// CreateAppointments inserts a batch in one transaction (a parsed message may
// hold several visits) and returns the stored rows in input order.
func (s *Store) CreateAppointments(items []model.Appointment) ([]model.Appointment, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO appointments (title, person, location, starts_at, ends_at, status, note, raw, cost)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	ids := make([]int64, 0, len(items))
	for _, a := range items {
		res, err := stmt.Exec(a.Title, a.Person, a.Location, a.StartsAt, nullIfEmpty(a.EndsAt),
			orDefault(a.Status, model.ApptStatusPlanned), a.Note, a.Raw, a.Cost)
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	out := make([]model.Appointment, 0, len(ids))
	for _, id := range ids {
		a, err := s.GetAppointment(id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *Store) GetAppointment(id int64) (model.Appointment, error) {
	row := s.db.QueryRow(appointmentCols+` WHERE id = ?`, id)
	return scanAppointment(row)
}

// UpcomingAppointments returns non-cancelled, non-deleted appointments
// starting at or after `from` (LocalDatetime), soonest first.
func (s *Store) UpcomingAppointments(from string, limit int) ([]model.Appointment, error) {
	return s.queryAppointments(appointmentCols+`
		WHERE deleted_at IS NULL AND status != ? AND starts_at >= ?
		ORDER BY starts_at ASC
		LIMIT ?`, model.ApptStatusCancelled, from, limit)
}

// AppointmentsBetween returns non-cancelled, non-deleted appointments in
// [from, to) (LocalDatetime), soonest first — used for the today/week digests.
func (s *Store) AppointmentsBetween(from, to string) ([]model.Appointment, error) {
	return s.queryAppointments(appointmentCols+`
		WHERE deleted_at IS NULL AND status != ? AND starts_at >= ? AND starts_at < ?
		ORDER BY starts_at ASC`, model.ApptStatusCancelled, from, to)
}

// ActiveAppointmentsFrom returns non-cancelled, non-deleted appointments
// starting at or after `from` (LocalDatetime), soonest first — the source for
// the ICS feed.
func (s *Store) ActiveAppointmentsFrom(from string) ([]model.Appointment, error) {
	return s.queryAppointments(appointmentCols+`
		WHERE deleted_at IS NULL AND status != ? AND starts_at >= ?
		ORDER BY starts_at ASC`, model.ApptStatusCancelled, from)
}

// ActiveAppointmentsAt returns non-cancelled, non-deleted appointments with
// exactly this start (LocalDatetime) — a small set used to warn about
// duplicate captures. Title/person matching is left to the caller so it can
// fold Unicode case.
func (s *Store) ActiveAppointmentsAt(startsAt string) ([]model.Appointment, error) {
	return s.queryAppointments(appointmentCols+`
		WHERE deleted_at IS NULL AND status != ? AND starts_at = ?`,
		model.ApptStatusCancelled, startsAt)
}

// AppointmentFilter pages the web list. Now (LocalDatetime) splits upcoming
// from past — see ListAppointments.
type AppointmentFilter struct {
	Now    string
	Limit  int
	Offset int
}

// ListAppointments returns non-deleted appointments — cancelled ones included,
// they stay visible in the web list — ordered upcoming-ascending first, then
// past most-recent-first. One ORDER BY rather than two queries so the
// limit/offset pagination stays honest across the boundary.
func (s *Store) ListAppointments(f AppointmentFilter) ([]model.Appointment, error) {
	q := appointmentCols + `
		WHERE deleted_at IS NULL
		ORDER BY starts_at < ? ASC,
		         CASE WHEN starts_at >= ? THEN starts_at END ASC,
		         starts_at DESC`
	if f.Limit > 0 {
		q += " LIMIT " + strconv.Itoa(f.Limit)
	}
	if f.Offset > 0 {
		q += " OFFSET " + strconv.Itoa(f.Offset)
	}
	return s.queryAppointments(q, f.Now, f.Now)
}

// SetAppointmentStatus updates status and bumps updated_at.
func (s *Store) SetAppointmentStatus(id int64, status string) error {
	_, err := s.db.Exec(`
		UPDATE appointments SET status = ?, updated_at = `+appointmentNow+`
		WHERE id = ?`, status, id)
	return err
}

// RescheduleAppointment moves an appointment to a new start (LocalDatetime)
// and bumps updated_at; the ICS feed reflects it on HA's next poll.
func (s *Store) RescheduleAppointment(id int64, newStart string) error {
	_, err := s.db.Exec(`
		UPDATE appointments SET starts_at = ?, updated_at = `+appointmentNow+`
		WHERE id = ?`, newStart, id)
	return err
}

// UpdateAppointmentTitle renames an appointment and bumps updated_at.
func (s *Store) UpdateAppointmentTitle(id int64, title string) error {
	_, err := s.db.Exec(`
		UPDATE appointments SET title = ?, updated_at = `+appointmentNow+`
		WHERE id = ?`, title, id)
	return err
}

// UpdateAppointmentPerson changes the "who" of an appointment and bumps
// updated_at.
func (s *Store) UpdateAppointmentPerson(id int64, person string) error {
	_, err := s.db.Exec(`
		UPDATE appointments SET person = ?, updated_at = `+appointmentNow+`
		WHERE id = ?`, person, id)
	return err
}

// UpdateAppointmentDetails overwrites title and person in one shot (start time
// stays) — used when confirming an update onto an existing same-time visit.
func (s *Store) UpdateAppointmentDetails(id int64, title, person string) error {
	_, err := s.db.Exec(`
		UPDATE appointments SET title = ?, person = ?, updated_at = `+appointmentNow+`
		WHERE id = ?`, title, person, id)
	return err
}

// UpdateAppointment saves every editable field at once — the web form's save.
func (s *Store) UpdateAppointment(a model.Appointment) error {
	_, err := s.db.Exec(`
		UPDATE appointments
		SET title = ?, person = ?, location = ?, starts_at = ?, ends_at = ?,
		    status = ?, note = ?, cost = ?, updated_at = `+appointmentNow+`
		WHERE id = ?`,
		a.Title, a.Person, a.Location, a.StartsAt, nullIfEmpty(a.EndsAt),
		orDefault(a.Status, model.ApptStatusPlanned), a.Note, a.Cost, a.ID)
	return err
}

// SoftDeleteAppointment stamps deleted_at instead of removing the row: the HA
// outbox needs the record to survive long enough to issue a calendar delete,
// and every read path already filters on deleted_at IS NULL.
func (s *Store) SoftDeleteAppointment(id int64) error {
	_, err := s.db.Exec(`
		UPDATE appointments
		SET deleted_at = `+appointmentNow+`, updated_at = `+appointmentNow+`
		WHERE id = ? AND deleted_at IS NULL`, id)
	return err
}

// AppointmentsAwaitingCost returns appointments whose start is between
// `notBefore` and `until` (LocalDatetime) that still have no cost and no
// prompt sent. The lower bound matters: without it the first run after a
// deploy would prompt for every appointment ever recorded.
func (s *Store) AppointmentsAwaitingCost(notBefore, until string) ([]model.Appointment, error) {
	return s.queryAppointments(appointmentCols+`
		WHERE deleted_at IS NULL AND status != ?
		  AND cost IS NULL AND cost_prompt_msg_id IS NULL
		  AND starts_at >= ? AND starts_at <= ?
		ORDER BY starts_at ASC`, model.ApptStatusCancelled, notBefore, until)
}

// AppointmentByCostPrompt finds the appointment a cost prompt belongs to, so a
// reply to that message can be routed without any in-memory state.
func (s *Store) AppointmentByCostPrompt(msgID int64) (model.Appointment, error) {
	row := s.db.QueryRow(appointmentCols+`
		WHERE cost_prompt_msg_id = ? AND deleted_at IS NULL`, msgID)
	return scanAppointment(row)
}

// SetAppointmentCostPrompt records which message asked for the cost. It also
// marks the appointment as already-asked, so the prompt is sent exactly once.
func (s *Store) SetAppointmentCostPrompt(id, msgID int64) error {
	_, err := s.db.Exec(`
		UPDATE appointments SET cost_prompt_msg_id = ?, updated_at = `+appointmentNow+`
		WHERE id = ?`, msgID, id)
	return err
}

// SetAppointmentCost writes the amount (0 is valid — it was free).
func (s *Store) SetAppointmentCost(id int64, cost float64) error {
	_, err := s.db.Exec(`
		UPDATE appointments SET cost = ?, updated_at = `+appointmentNow+`
		WHERE id = ?`, cost, id)
	return err
}

const appointmentNow = `strftime('%Y-%m-%dT%H:%M:%S','now','localtime')`

const appointmentCols = `
	SELECT id, title, person, location, starts_at,
	       COALESCE(ends_at,''), status, note, raw, cost, cost_prompt_msg_id,
	       COALESCE(ha_uid,''), COALESCE(ha_synced_at,''),
	       created_at, updated_at, COALESCE(deleted_at,'')
	FROM appointments`

func (s *Store) queryAppointments(q string, args ...any) ([]model.Appointment, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Appointment
	for rows.Next() {
		a, err := scanAppointment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type appointmentScanner interface {
	Scan(dest ...any) error
}

func scanAppointment(sc appointmentScanner) (model.Appointment, error) {
	var a model.Appointment
	err := sc.Scan(&a.ID, &a.Title, &a.Person, &a.Location, &a.StartsAt,
		&a.EndsAt, &a.Status, &a.Note, &a.Raw, &a.Cost, &a.CostPromptMsgID,
		&a.HaUID, &a.HaSyncedAt, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt)
	return a, err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
