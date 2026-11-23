package store

import (
	"database/sql"
	"slices"
	"strings"

	"familyhub/internal/model"
)

// logCols reads an entry with its tags folded into one space-separated
// column; a tag never contains a space (model.ParseTags splits on them), so
// splitting it back is lossless.
const logCols = `
	SELECT e.id, e.date, e.text, e.created_at,
	       COALESCE((SELECT group_concat(t.tag, ' ') FROM log_entry_tags t WHERE t.entry_id = e.id), '')
	FROM log_entries e`

func scanLogEntry(row interface{ Scan(...any) error }) (model.LogEntry, error) {
	var e model.LogEntry
	var tags string
	if err := row.Scan(&e.ID, &e.Date, &e.Text, &e.CreatedAt, &tags); err != nil {
		return model.LogEntry{}, err
	}
	e.Tags = strings.Fields(tags)
	slices.Sort(e.Tags)
	return e, nil
}

// CreateLogEntry stores an entry and its tags, and returns it with its id.
func (s *Store) CreateLogEntry(e model.LogEntry) (model.LogEntry, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.LogEntry{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO log_entries (date, text) VALUES (?, ?)`, e.Date, e.Text)
	if err != nil {
		return model.LogEntry{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.LogEntry{}, err
	}
	if err := writeLogTags(tx, id, e.Tags); err != nil {
		return model.LogEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.LogEntry{}, err
	}
	return s.GetLogEntry(id)
}

// UpdateLogEntry replaces an entry's date, text and tags. sql.ErrNoRows means
// there is no such entry.
func (s *Store) UpdateLogEntry(e model.LogEntry) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE log_entries SET date = ?, text = ? WHERE id = ?`, e.Date, e.Text, e.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(`DELETE FROM log_entry_tags WHERE entry_id = ?`, e.ID); err != nil {
		return err
	}
	if err := writeLogTags(tx, e.ID, e.Tags); err != nil {
		return err
	}
	return tx.Commit()
}

func writeLogTags(tx *sql.Tx, id int64, tags []string) error {
	for _, t := range tags {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO log_entry_tags (entry_id, tag) VALUES (?, ?)`, id, t); err != nil {
			return err
		}
	}
	return nil
}

// DeleteLogEntry removes an entry; its tags go with it by cascade.
func (s *Store) DeleteLogEntry(id int64) error {
	res, err := s.db.Exec(`DELETE FROM log_entries WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetLogEntry(id int64) (model.LogEntry, error) {
	return scanLogEntry(s.db.QueryRow(logCols+` WHERE e.id = ?`, id))
}

// ListLogEntries returns the chronicle newest first, narrowed to one tag when
// tag is not empty. Entries of the same day keep the order they were written
// in, latest on top.
func (s *Store) ListLogEntries(tag string) ([]model.LogEntry, error) {
	q, args := logCols, []any{}
	if tag != "" {
		q += ` WHERE EXISTS (SELECT 1 FROM log_entry_tags t WHERE t.entry_id = e.id AND t.tag = ?)`
		args = append(args, tag)
	}
	rows, err := s.db.Query(q+` ORDER BY e.date DESC, e.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.LogEntry
	for rows.Next() {
		e, err := scanLogEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LogTags returns every tag in use with its entry count, most used first.
func (s *Store) LogTags() ([]model.TagCount, error) {
	rows, err := s.db.Query(`
		SELECT tag, COUNT(*) FROM log_entry_tags GROUP BY tag ORDER BY COUNT(*) DESC, tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TagCount
	for rows.Next() {
		var tc model.TagCount
		if err := rows.Scan(&tc.Tag, &tc.Count); err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}
