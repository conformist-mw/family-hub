package store

import "familyhub/internal/model"

// SaveLessonDetails writes what the weekly review collected, one transaction
// for the whole batch. Rows are addressed by the portal's event_id: a lesson
// already recorded is updated in place, so re-running a Friday collect (a
// restart re-arms it) does not duplicate anything.
//
// A blank field never overwrites a filled one. The portal occasionally serves
// a lesson detail with an empty topic — a teacher mid-edit, a page that half
// rendered — and a second collect that hour would otherwise erase a note that
// was correctly captured an hour earlier. Marks and files are the exception:
// they are deleted and rewritten wholesale, because for those "it is gone now"
// is a real state the record should follow.
func (s *Store) SaveLessonDetails(details []model.SchoolLessonDetail) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// COALESCE would not do here: the columns are NOT NULL, so the blank case
	// is "" rather than NULL and has to be tested for explicitly.
	upsert, err := tx.Prepare(`
		INSERT INTO school_lesson_details
			(event_id, pupil_id, starts_at, subject, teacher, topic, notes, homework, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%S','now','localtime'))
		ON CONFLICT (event_id) DO UPDATE SET
			pupil_id   = excluded.pupil_id,
			starts_at  = excluded.starts_at,
			subject    = excluded.subject,
			teacher    = CASE WHEN excluded.teacher  <> '' THEN excluded.teacher  ELSE school_lesson_details.teacher  END,
			topic      = CASE WHEN excluded.topic    <> '' THEN excluded.topic    ELSE school_lesson_details.topic    END,
			notes      = CASE WHEN excluded.notes    <> '' THEN excluded.notes    ELSE school_lesson_details.notes    END,
			homework   = CASE WHEN excluded.homework <> '' THEN excluded.homework ELSE school_lesson_details.homework END,
			fetched_at = excluded.fetched_at`)
	if err != nil {
		return err
	}
	defer upsert.Close()

	delMarks, err := tx.Prepare(`DELETE FROM school_lesson_marks WHERE event_id = ?`)
	if err != nil {
		return err
	}
	defer delMarks.Close()
	insMark, err := tx.Prepare(
		`INSERT INTO school_lesson_marks (event_id, kind, value) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insMark.Close()

	delFiles, err := tx.Prepare(`DELETE FROM school_lesson_files WHERE event_id = ?`)
	if err != nil {
		return err
	}
	defer delFiles.Close()
	insFile, err := tx.Prepare(
		`INSERT INTO school_lesson_files (event_id, kind, url, title) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insFile.Close()

	for _, d := range details {
		if _, err := upsert.Exec(d.EventID, d.PupilID, d.StartsAt, d.Subject,
			d.Teacher, d.Topic, d.Notes, d.Homework); err != nil {
			return err
		}
		if _, err := delMarks.Exec(d.EventID); err != nil {
			return err
		}
		for _, m := range d.Marks {
			if _, err := insMark.Exec(d.EventID, m.Kind, m.Value); err != nil {
				return err
			}
		}
		if _, err := delFiles.Exec(d.EventID); err != nil {
			return err
		}
		for _, f := range d.Files {
			if _, err := insFile.Exec(d.EventID, f.Kind, f.URL, f.Title); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// LessonDetails returns every collected lesson starting in the half-open
// window [from, to), across all pupils, ordered by start then subject — the
// same ordering SchoolLessons uses, so the review reads chronologically before
// the renderer regroups it.
//
// Marks and files are fetched in two follow-up queries rather than joined:
// a join would multiply the detail rows by marks × files and leave the caller
// de-duplicating what it just asked for.
func (s *Store) LessonDetails(from, to string) ([]model.SchoolLessonDetail, error) {
	rows, err := s.db.Query(`
		SELECT event_id, pupil_id, starts_at, subject, teacher, topic, notes, homework
		FROM school_lesson_details
		WHERE starts_at >= ? AND starts_at < ?
		ORDER BY starts_at, subject`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.SchoolLessonDetail
	byID := map[int64]int{}
	for rows.Next() {
		var d model.SchoolLessonDetail
		if err := rows.Scan(&d.EventID, &d.PupilID, &d.StartsAt, &d.Subject,
			&d.Teacher, &d.Topic, &d.Notes, &d.Homework); err != nil {
			return nil, err
		}
		byID[d.EventID] = len(out)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}

	if err := s.attachMarks(out, byID, from, to); err != nil {
		return nil, err
	}
	return out, s.attachFiles(out, byID, from, to)
}

// attachMarks and attachFiles hang the child rows off the details already
// read. Both re-state the window as a subquery instead of taking an id list:
// the id list would be a variadic IN clause built by hand, and the window is
// the same one the caller just asked for.
func (s *Store) attachMarks(out []model.SchoolLessonDetail, byID map[int64]int, from, to string) error {
	rows, err := s.db.Query(`
		SELECT m.event_id, m.kind, m.value
		FROM school_lesson_marks m
		JOIN school_lesson_details d ON d.event_id = m.event_id
		WHERE d.starts_at >= ? AND d.starts_at < ?
		ORDER BY m.id`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var eventID int64
		var m model.SchoolMark
		if err := rows.Scan(&eventID, &m.Kind, &m.Value); err != nil {
			return err
		}
		if i, ok := byID[eventID]; ok {
			out[i].Marks = append(out[i].Marks, m)
		}
	}
	return rows.Err()
}

func (s *Store) attachFiles(out []model.SchoolLessonDetail, byID map[int64]int, from, to string) error {
	rows, err := s.db.Query(`
		SELECT f.event_id, f.kind, f.url, f.title
		FROM school_lesson_files f
		JOIN school_lesson_details d ON d.event_id = f.event_id
		WHERE d.starts_at >= ? AND d.starts_at < ?
		ORDER BY f.id`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var eventID int64
		var f model.SchoolFile
		if err := rows.Scan(&eventID, &f.Kind, &f.URL, &f.Title); err != nil {
			return err
		}
		if i, ok := byID[eventID]; ok {
			out[i].Files = append(out[i].Files, f)
		}
	}
	return rows.Err()
}
