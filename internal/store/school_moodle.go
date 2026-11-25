package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"familyhub/internal/model"
)

// ErrEmptySchoolSync is ApplySchoolSync refusing a sync with no courses while
// the store holds some. An enrolment list that comes back empty is far more
// likely a hiccup on the school's side than the child leaving every subject,
// and taking it at its word would delete every grade and re-seed them all
// silently on the next good sync, losing whatever was graded in between. A new
// school year needs no empty sync: its first enrolment list replaces the old.
var ErrEmptySchoolSync = errors.New("school sync has no courses; keeping the stored ones")

// ApplySchoolSync writes one complete Moodle sync — every enrolled course and
// every graded item of those courses — and returns the grades that are news:
// an item not seen before, or one whose grade changed.
//
// Only courses that were already known count. A course seen for the first
// time is seeded silently, because its grades are history rather than news;
// that one rule covers the first sync ever, a first sync that came back empty,
// a course that failed the first time round, and a course added mid-year.
//
// The sync is the whole truth, so what it no longer contains goes: a course
// missing from it is deleted with its grades (a new school year starts clean),
// and so is a grade missing from a course it did contain (a teacher removed
// it). The caller must therefore only call this with a sync where every course
// succeeded — a course dropped by a timeout would otherwise lose its grades
// and come back next time as a silent seed.
//
// A grade with an empty GradedAt is one Moodle gave no date for. It keeps the
// stored date while its value stands and is dated now otherwise: re-dating it
// on every sync would move it into the current week forever, so the weekly
// review would list the same old grade every Friday.
//
// All of it happens in one transaction: a failure leaves the previous sync in
// place. Fresh grades come back in course order, then by when they were graded.
func (s *Store) ApplySchoolSync(courses []model.SchoolCourse, grades []model.SchoolGrade, now time.Time) ([]model.SchoolGrade, error) {
	byID := make(map[int64]model.SchoolCourse, len(courses))
	for _, c := range courses {
		byID[c.ID] = c
	}
	for _, g := range grades {
		if _, ok := byID[g.CourseID]; !ok {
			return nil, fmt.Errorf("school grade %d belongs to course %d, which is not in the sync", g.ItemID, g.CourseID)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	known, err := schoolCourseIDs(tx)
	if err != nil {
		return nil, err
	}
	if len(courses) == 0 && len(known) > 0 {
		return nil, ErrEmptySchoolSync
	}
	stored, err := schoolGradeValues(tx)
	if err != nil {
		return nil, err
	}

	for id := range known {
		if _, ok := byID[id]; ok {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM school_grades WHERE course_id = ?`, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM school_courses WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}

	syncedAt := now.Format(model.LocalDatetime)
	for _, c := range courses {
		if _, err := tx.Exec(`
			INSERT INTO school_courses (id, name, position, average, average_text, synced_at)
			VALUES (?, ?, ?, ?, NULLIF(?, ''), ?)
			ON CONFLICT (id) DO UPDATE SET
				name = excluded.name, position = excluded.position,
				average = excluded.average, average_text = excluded.average_text,
				synced_at = excluded.synced_at`,
			c.ID, c.Name, c.Position, c.Average, c.AverageText, syncedAt); err != nil {
			return nil, err
		}
	}

	seen := make(map[int64]bool, len(grades))
	var fresh []model.SchoolGrade
	for _, g := range grades {
		seen[g.ItemID] = true
		old, had := stored[g.ItemID]
		unchanged := had && old.raw == g.GradeRaw
		if g.GradedAt == "" {
			g.GradedAt = syncedAt
			if unchanged {
				g.GradedAt = old.gradedAt
			}
		}
		if _, err := tx.Exec(`
			INSERT INTO school_grades
				(item_id, course_id, item_module, item_instance, name, grade_raw, grade_text, graded_at)
			VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)
			ON CONFLICT (item_id) DO UPDATE SET
				course_id = excluded.course_id, item_module = excluded.item_module,
				item_instance = excluded.item_instance, name = excluded.name,
				grade_raw = excluded.grade_raw, grade_text = excluded.grade_text,
				graded_at = excluded.graded_at`,
			g.ItemID, g.CourseID, g.ItemModule, g.ItemInstance, g.Name,
			g.GradeRaw, g.GradeText, g.GradedAt); err != nil {
			return nil, err
		}
		if !known[g.CourseID] || unchanged {
			continue
		}
		c := byID[g.CourseID]
		g.CourseName, g.CourseAverage = c.Name, c.AverageText
		fresh = append(fresh, g)
	}

	for id := range stored {
		if seen[id] {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM school_grades WHERE item_id = ?`, id); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sort.SliceStable(fresh, func(i, j int) bool {
		a, b := fresh[i], fresh[j]
		if pa, pb := byID[a.CourseID].Position, byID[b.CourseID].Position; pa != pb {
			return pa < pb
		}
		if a.GradedAt != b.GradedAt {
			return a.GradedAt < b.GradedAt
		}
		return a.ItemID < b.ItemID
	})
	return fresh, nil
}

func schoolCourseIDs(tx *sql.Tx) (map[int64]bool, error) {
	rows, err := tx.Query(`SELECT id FROM school_courses`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// storedGrade is what the store holds for one grade item before a sync.
type storedGrade struct {
	raw      float64
	gradedAt string
}

func schoolGradeValues(tx *sql.Tx) (map[int64]storedGrade, error) {
	rows, err := tx.Query(`SELECT item_id, grade_raw, graded_at FROM school_grades`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]storedGrade{}
	for rows.Next() {
		var id int64
		var g storedGrade
		if err := rows.Scan(&id, &g.raw, &g.gradedAt); err != nil {
			return nil, err
		}
		out[id] = g
	}
	return out, rows.Err()
}

// SchoolSummary returns every course in Moodle's order, each with its grades
// in the order they were given.
func (s *Store) SchoolSummary() ([]model.SchoolCourse, error) {
	rows, err := s.db.Query(`
		SELECT id, name, position, average, COALESCE(average_text, ''), synced_at
		FROM school_courses ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SchoolCourse
	index := map[int64]int{}
	for rows.Next() {
		var c model.SchoolCourse
		var avg sql.NullFloat64
		if err := rows.Scan(&c.ID, &c.Name, &c.Position, &avg, &c.AverageText, &c.SyncedAt); err != nil {
			return nil, err
		}
		if avg.Valid {
			c.Average = &avg.Float64
		}
		index[c.ID] = len(out)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	grades, err := s.schoolGrades(``)
	if err != nil {
		return nil, err
	}
	for _, g := range grades {
		if i, ok := index[g.CourseID]; ok {
			out[i].Grades = append(out[i].Grades, g)
		}
	}
	return out, nil
}

// SchoolGradesBetween returns the grades given in the half-open window
// [from, to) of LocalDatetime bounds, in course order and then by time — the
// week of the Friday review.
func (s *Store) SchoolGradesBetween(from, to string) ([]model.SchoolGrade, error) {
	return s.schoolGrades(`WHERE g.graded_at >= ? AND g.graded_at < ?`, from, to)
}

func (s *Store) schoolGrades(where string, args ...any) ([]model.SchoolGrade, error) {
	rows, err := s.db.Query(`
		SELECT g.item_id, g.course_id, COALESCE(g.item_module, ''), COALESCE(g.item_instance, 0),
		       g.name, g.grade_raw, g.grade_text, g.graded_at,
		       c.name, COALESCE(c.average_text, '')
		FROM school_grades g
		JOIN school_courses c ON c.id = g.course_id
		`+where+`
		ORDER BY c.position, c.id, g.graded_at, g.item_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SchoolGrade
	for rows.Next() {
		var g model.SchoolGrade
		if err := rows.Scan(&g.ItemID, &g.CourseID, &g.ItemModule, &g.ItemInstance,
			&g.Name, &g.GradeRaw, &g.GradeText, &g.GradedAt,
			&g.CourseName, &g.CourseAverage); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
