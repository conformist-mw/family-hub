package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"lessons/internal/model"
)

func (s *Store) ListTrainers() ([]model.Trainer, error) {
	rows, err := s.db.Query(`
		SELECT id, name, notes, active FROM trainers
		ORDER BY active DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Trainer
	for rows.Next() {
		var t model.Trainer
		if err := rows.Scan(&t.ID, &t.Name, &t.Notes, &t.Active); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// FindOrCreateTrainer mirrors getOrCreatePerson: TrimSpace, exact-match
// lookup, insert if missing. Backs the free-text datalist on the enrollment
// form.
func (s *Store) FindOrCreateTrainer(name string) (int64, error) {
	name = strings.TrimSpace(name)
	var id int64
	err := s.db.QueryRow("SELECT id FROM trainers WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := s.db.Exec("INSERT INTO trainers (name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListAllAbsences returns every absence with its trainer's name, newest
// period first — the /trainers page groups them client-side.
func (s *Store) ListAllAbsences() ([]model.TrainerAbsence, error) {
	rows, err := s.db.Query(`
		SELECT a.id, a.trainer_id, t.name, a.date_from, a.date_to, a.kind, a.comment
		FROM trainer_absences a
		JOIN trainers t ON t.id = a.trainer_id
		ORDER BY a.date_from DESC, a.id DESC`)
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

func (s *Store) CreateAbsence(trainerID int64, from, to, kind, comment string) error {
	if from > to {
		return fmt.Errorf("период отсутствия: начало позже конца")
	}
	if _, ok := model.AbsenceKindLabels[kind]; !ok {
		return fmt.Errorf("неизвестный тип отсутствия %q", kind)
	}
	_, err := s.db.Exec(`
		INSERT INTO trainer_absences (trainer_id, date_from, date_to, kind, comment)
		VALUES (?, ?, ?, ?, ?)`, trainerID, from, to, kind, comment)
	return err
}

func (s *Store) DeleteAbsence(id int64) error {
	_, err := s.db.Exec(`DELETE FROM trainer_absences WHERE id=?`, id)
	return err
}

// ActiveAbsenceByEnrollment maps enrollment id → the absence covering the
// given date, for active enrollments whose trainer is currently away. Drives
// the dashboard badge. Overlapping absences pick the one ending last.
// Values are pointers so a template `index` miss yields nil (falsy for
// {{with}}) — a zero struct would render an empty badge on every card.
func (s *Store) ActiveAbsenceByEnrollment(date string) (map[int64]*model.TrainerAbsence, error) {
	rows, err := s.db.Query(`
		SELECT e.id, a.id, a.trainer_id, t.name, a.date_from, a.date_to, a.kind, a.comment
		FROM enrollments e
		JOIN trainers t         ON t.id = e.trainer_id
		JOIN trainer_absences a ON a.trainer_id = t.id
		WHERE e.active = 1 AND ? BETWEEN a.date_from AND a.date_to
		ORDER BY a.date_to`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]*model.TrainerAbsence)
	for rows.Next() {
		var eid int64
		var a model.TrainerAbsence
		if err := rows.Scan(&eid, &a.ID, &a.TrainerID, &a.Trainer, &a.DateFrom, &a.DateTo, &a.Kind, &a.Comment); err != nil {
			return nil, err
		}
		out[eid] = &a // ascending date_to: the last write wins
	}
	return out, rows.Err()
}
