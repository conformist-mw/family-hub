package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"familyhub/internal/model"
)

// Reminder methods carry the domain suffix like the appointment ones: one
// Store serves lessons, appointments and reminders.

const nowLocal = `strftime('%Y-%m-%dT%H:%M:%S','now','localtime')`

const reminderCols = `
	SELECT id, title, person, duration_min, active, active_since, note,
	       created_at, updated_at, COALESCE(deleted_at,'')
	FROM reminders`

const reminderRuleCols = `
	SELECT id, reminder_id, valid_from_at, dtstart, rrule, created_at
	FROM reminder_rules`

// Occurrences are always read with their reminder's display fields joined in:
// every caller — the calendar, the nag, the Mini App list — needs the title,
// and none of them has a reason to make a second round trip for it.
const reminderOccCols = `
	SELECT o.id, o.reminder_id, o.rule_id, o.due_at, o.status, o.done_at, o.done_by,
	       r.title, r.person, r.duration_min
	FROM reminder_occurrences o
	JOIN reminders r ON r.id = o.reminder_id`

// CreateReminder inserts a reminder together with its first rule version, in
// one transaction. The two are not separable: a reminder with no rule cannot
// be expanded, so committing one without the other would store a chore that
// never happens.
// activeSince is stamped by the caller rather than by SQL's own now(). The
// clock has to be the one the service reasons with: the materialiser compares
// active_since against its injected time, and letting the database pick a
// second, independent now leaves the two able to disagree.
func (s *Store) CreateReminder(r model.Reminder, first model.ReminderRule) (model.Reminder, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Reminder{}, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO reminders (title, person, duration_min, active, active_since, note)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.Title, r.Person, orDefaultInt(r.DurationMin, 15), r.Active,
		orDefault(r.ActiveSince, time.Now().Format(model.LocalDatetime)), r.Note)
	if err != nil {
		return model.Reminder{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Reminder{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO reminder_rules (reminder_id, valid_from_at, dtstart, rrule)
		VALUES (?, ?, ?, ?)`,
		id, first.ValidFromAt, first.DTStart, first.RRule); err != nil {
		return model.Reminder{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Reminder{}, err
	}
	return s.GetReminder(id)
}

// GetReminder skips soft-deleted rows, so a second delete and an edit of a
// removed chore both come back as not-found rather than quietly succeeding.
func (s *Store) GetReminder(id int64) (model.Reminder, error) {
	row := s.db.QueryRow(reminderCols+` WHERE id = ? AND deleted_at IS NULL`, id)
	return scanReminder(row)
}

// Reminders returns every reminder that has not been deleted, active or not —
// the Mini App list shows paused ones so they can be switched back on.
func (s *Store) Reminders() ([]model.Reminder, error) {
	return s.queryReminders(reminderCols + `
		WHERE deleted_at IS NULL
		ORDER BY active DESC, title ASC`)
}

// ActiveReminders is what the materialiser walks. Paused and deleted chores
// produce no occurrences, which is the whole point of pausing one.
func (s *Store) ActiveReminders() ([]model.Reminder, error) {
	return s.queryReminders(reminderCols + `
		WHERE deleted_at IS NULL AND active = 1
		ORDER BY title ASC`)
}

// UpdateReminder rewrites the fields describing the chore itself. It
// deliberately does not touch `active` or `active_since` — see
// SetReminderActive, where the bookkeeping matters.
func (s *Store) UpdateReminder(r model.Reminder) error {
	_, err := s.db.Exec(`
		UPDATE reminders
		SET title = ?, person = ?, duration_min = ?, note = ?, updated_at = `+nowLocal+`
		WHERE id = ? AND deleted_at IS NULL`,
		r.Title, r.Person, orDefaultInt(r.DurationMin, 15), r.Note, r.ID)
	return err
}

// SetReminderActive flips the switch and, when switching ON, moves
// active_since to now. That timestamp is the floor for the catch-up backfill:
// without it, resuming a chore paused for a month would immediately invent a
// month of "you forgot" rows for the time it was deliberately off.
//
// Switching OFF leaves active_since alone. It is only ever read for reminders
// that are currently on.
func (s *Store) SetReminderActive(id int64, active bool, at string) error {
	if active {
		_, err := s.db.Exec(`
			UPDATE reminders
			SET active = 1, active_since = ?, updated_at = `+nowLocal+`
			WHERE id = ? AND deleted_at IS NULL`, at, id)
		return err
	}
	_, err := s.db.Exec(`
		UPDATE reminders SET active = 0, updated_at = `+nowLocal+`
		WHERE id = ? AND deleted_at IS NULL`, id)
	return err
}

// SoftDeleteReminder hides the chore while keeping its occurrence history
// readable, the same way appointments are removed.
func (s *Store) SoftDeleteReminder(id int64) error {
	_, err := s.db.Exec(`
		UPDATE reminders SET deleted_at = `+nowLocal+`, updated_at = `+nowLocal+`
		WHERE id = ? AND deleted_at IS NULL`, id)
	return err
}

// RulesFor returns a reminder's rule versions oldest first. Order is the
// contract: the expander walks them in sequence to cut a window into
// per-version segments, and an unordered read would silently mis-assign
// occurrences to the wrong rule.
func (s *Store) RulesFor(reminderID int64) ([]model.ReminderRule, error) {
	return s.queryReminderRules(reminderRuleCols+`
		WHERE reminder_id = ?
		ORDER BY valid_from_at ASC`, reminderID)
}

// RulesForAll is the batched form, so expanding N reminders is two queries
// rather than N+1. The map is keyed by reminder id; each slice keeps the same
// oldest-first ordering RulesFor guarantees.
func (s *Store) RulesForAll(reminderIDs []int64) (map[int64][]model.ReminderRule, error) {
	out := make(map[int64][]model.ReminderRule, len(reminderIDs))
	if len(reminderIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(reminderIDs))
	for i, id := range reminderIDs {
		args[i] = id
	}
	q := reminderRuleCols + `
		WHERE reminder_id IN (` + placeholders(len(args)) + `)
		ORDER BY reminder_id ASC, valid_from_at ASC`
	rules, err := s.queryReminderRules(q, args...)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		out[r.ReminderID] = append(out[r.ReminderID], r)
	}
	return out, nil
}

// AddRule appends a new version. Existing occurrences are left untouched by
// design: they are the frozen record of what actually came due, and rewriting
// them under a new rule is exactly the history-rewriting this table exists to
// prevent.
func (s *Store) AddRule(r model.ReminderRule) (model.ReminderRule, error) {
	res, err := s.db.Exec(`
		INSERT INTO reminder_rules (reminder_id, valid_from_at, dtstart, rrule)
		VALUES (?, ?, ?, ?)`,
		r.ReminderID, r.ValidFromAt, r.DTStart, r.RRule)
	if err != nil {
		return model.ReminderRule{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.ReminderRule{}, err
	}
	return s.GetRule(id)
}

// AmendRule corrects a version in place — the "I mistyped it, it was always
// the 5th" case, as opposed to "from September we do the 5th", which is
// AddRule. It does not rebuild already-materialised occurrences; that
// divergence is a documented compromise, not an oversight.
func (s *Store) AmendRule(r model.ReminderRule) error {
	_, err := s.db.Exec(`
		UPDATE reminder_rules SET valid_from_at = ?, dtstart = ?, rrule = ?
		WHERE id = ?`,
		r.ValidFromAt, r.DTStart, r.RRule, r.ID)
	return err
}

// AmendRuleAndDropOpen rewrites a version and clears the still-open
// occurrences it produced, in one transaction.
//
// Both halves are needed together. The backfill cannot tell an amend from a
// gap, so on its next pass it materialises the amended text across the whole
// catch-up window — leaving the old rows in place and the record showing a
// chore that came due twice a day for a month. Dropping the `pending` rows of
// that version lets the pass regenerate them cleanly.
//
// Closed rows survive: they carry a person's decision, which no schedule edit
// is allowed to erase.
func (s *Store) AmendRuleAndDropOpen(r model.ReminderRule) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		UPDATE reminder_rules SET valid_from_at = ?, dtstart = ?, rrule = ?
		WHERE id = ?`, r.ValidFromAt, r.DTStart, r.RRule, r.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		DELETE FROM reminder_occurrences
		WHERE rule_id = ? AND status = ?`, r.ID, model.OccPending); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetRule(id int64) (model.ReminderRule, error) {
	row := s.db.QueryRow(reminderRuleCols+` WHERE id = ?`, id)
	return scanReminderRule(row)
}

// OccurrencesIn returns stored occurrences with due_at in [from, to],
// soonest first. Only the past half of any window is served from here; later
// instants are expanded from the rules instead.
func (s *Store) OccurrencesIn(from, to string) ([]model.ReminderOccurrence, error) {
	return s.queryReminderOccurrences(reminderOccCols+`
		WHERE o.due_at >= ? AND o.due_at <= ? AND r.deleted_at IS NULL
		ORDER BY o.due_at ASC`, from, to)
}

// PendingOccurrencesIn is the evening nag's query: what came due in the window
// and nobody closed. A row is what makes this answerable — an occurrence
// recomputed from today's rule could not prove it ever came due.
func (s *Store) PendingOccurrencesIn(from, to string) ([]model.ReminderOccurrence, error) {
	return s.queryReminderOccurrences(reminderOccCols+`
		WHERE o.due_at >= ? AND o.due_at <= ? AND o.status = ?
		  AND r.deleted_at IS NULL
		ORDER BY o.due_at ASC`, from, to, model.OccPending)
}

// GetOccurrence looks one up by its natural key.
func (s *Store) GetOccurrence(reminderID int64, dueAt string) (model.ReminderOccurrence, error) {
	row := s.db.QueryRow(reminderOccCols+`
		WHERE o.reminder_id = ? AND o.due_at = ?`, reminderID, dueAt)
	return scanReminderOccurrence(row)
}

// MaterialiseOccurrences writes a whole pass in one transaction. The
// start-up catch-up can cover thirty days across every chore, and one implicit
// transaction per row means one WAL write lock and one fsync each.
//
// Conflict behaviour is the single-row one: DO NOTHING, so a pass never
// reopens something already closed.
func (s *Store) MaterialiseOccurrences(rows []model.ReminderOccurrence) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO reminder_occurrences (reminder_id, rule_id, due_at, status)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (reminder_id, due_at) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, o := range rows {
		if _, err := stmt.Exec(o.ReminderID, o.RuleID, o.DueAt, model.OccPending); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkOccurrence records a person's decision, and unlike the materialiser it
// DOES overwrite: a second tap changing done to skipped must take effect.
//
// It also inserts when the row is missing, which closes the race between a
// user marking an occurrence the moment it comes due and the minute-ticker
// that has not written it yet.
func (s *Store) MarkOccurrence(reminderID, ruleID int64, dueAt, status, by string) error {
	if !model.ValidOccStatus(status) {
		return fmt.Errorf("store: unknown occurrence status %q", status)
	}
	_, err := s.db.Exec(`
		INSERT INTO reminder_occurrences (reminder_id, rule_id, due_at, status, done_at, done_by)
		VALUES (?, ?, ?, ?, `+nowLocal+`, ?)
		ON CONFLICT (reminder_id, due_at) DO UPDATE
		SET status = excluded.status, done_at = excluded.done_at, done_by = excluded.done_by`,
		reminderID, ruleID, dueAt, status, by)
	return err
}

// --- scanning ---

type reminderScanner interface{ Scan(dest ...any) error }

func (s *Store) queryReminders(q string, args ...any) ([]model.Reminder, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Reminder
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanReminder(sc reminderScanner) (model.Reminder, error) {
	var r model.Reminder
	err := sc.Scan(&r.ID, &r.Title, &r.Person, &r.DurationMin, &r.Active, &r.ActiveSince,
		&r.Note, &r.CreatedAt, &r.UpdatedAt, &r.DeletedAt)
	return r, err
}

func (s *Store) queryReminderRules(q string, args ...any) ([]model.ReminderRule, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ReminderRule
	for rows.Next() {
		r, err := scanReminderRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanReminderRule(sc reminderScanner) (model.ReminderRule, error) {
	var r model.ReminderRule
	err := sc.Scan(&r.ID, &r.ReminderID, &r.ValidFromAt, &r.DTStart, &r.RRule, &r.CreatedAt)
	return r, err
}

func (s *Store) queryReminderOccurrences(q string, args ...any) ([]model.ReminderOccurrence, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ReminderOccurrence
	for rows.Next() {
		o, err := scanReminderOccurrence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func scanReminderOccurrence(sc reminderScanner) (model.ReminderOccurrence, error) {
	var o model.ReminderOccurrence
	err := sc.Scan(&o.ID, &o.ReminderID, &o.RuleID, &o.DueAt, &o.Status,
		&o.DoneAt, &o.DoneBy, &o.Title, &o.Person, &o.DurationMin)
	return o, err
}

// IsNotFound reports a missing row, so callers can turn it into a 404 without
// importing database/sql themselves.
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func orDefaultInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
