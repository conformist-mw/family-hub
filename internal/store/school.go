package store

import "familyhub/internal/model"

// ReplaceSchoolLessons swaps the mirror for one pupil over the half-open window
// [from, to) in a single transaction: everything currently stored in that
// window is deleted and the freshly fetched set is written in its place.
//
// A full-window replace rather than a per-row upsert because the portal is the
// only writer and this table keeps no history: a lesson that vanished from the
// portal (cancelled, rescheduled out of the window) must vanish here too, and a
// diff that only ever inserts would leave the cancelled one behind forever. The
// window is bounded by what the sync actually fetched, so a fetch that returned
// nothing for a future week correctly empties that week — but a login failure,
// which raises an error before this is called, never reaches here and so never
// wipes the cache.
//
// from and to are LocalDatetime bounds; to is exclusive so adjacent windows
// tile without a one-minute overlap deleting each other's edge lesson.
func (s *Store) ReplaceSchoolLessons(pupilID int64, from, to string, lessons []model.SchoolLesson) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		DELETE FROM school_lessons
		WHERE pupil_id = ? AND starts_at >= ? AND starts_at < ?`,
		pupilID, from, to); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`
		INSERT INTO school_lessons
			(event_id, pupil_id, subject, starts_at, ends_at, topic, has_marks, theme_color)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, l := range lessons {
		if _, err := stmt.Exec(l.EventID, pupilID, l.Subject, l.StartsAt, l.EndsAt,
			l.Topic, boolToInt(l.HasMarks), l.ThemeColor); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SchoolLessons returns every mirrored lesson starting in the half-open window
// [from, to), across all pupils, ordered by start. The ICS handler classifies
// and filters; the store stays a plain read.
func (s *Store) SchoolLessons(from, to string) ([]model.SchoolLesson, error) {
	rows, err := s.db.Query(`
		SELECT id, event_id, pupil_id, subject, starts_at, ends_at, topic, has_marks, theme_color
		FROM school_lessons
		WHERE starts_at >= ? AND starts_at < ?
		ORDER BY starts_at, subject`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.SchoolLesson
	for rows.Next() {
		var l model.SchoolLesson
		var hasMarks int
		if err := rows.Scan(&l.ID, &l.EventID, &l.PupilID, &l.Subject, &l.StartsAt,
			&l.EndsAt, &l.Topic, &hasMarks, &l.ThemeColor); err != nil {
			return nil, err
		}
		l.HasMarks = hasMarks != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
