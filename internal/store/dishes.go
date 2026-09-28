package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"familyhub/internal/model"
)

// NameKey is the form two dish names are compared in: whitespace collapsed,
// lower case, one apostrophe. It is computed here rather than in SQL because
// SQLite's lower() folds only ASCII — "Борщ" and "борщ" would otherwise be two
// dishes — and the apostrophe comes in three spellings depending on whose
// keyboard typed it ("мʼясо", "м'ясо", "м’ясо").
func NameKey(name string) string {
	return apostrophes.Replace(strings.ToLower(cleanDishName(name)))
}

var apostrophes = strings.NewReplacer("ʼ", "'", "’", "'")

// cleanDishName is what gets stored as the display name: the same collapsed
// whitespace as the key, but the family's own capitalisation and apostrophe.
func cleanDishName(name string) string {
	return strings.Join(strings.Fields(name), " ")
}

const dishCols = `
	SELECT id, name, meal, days, status, note, created_at
	FROM dishes`

// CreateDish inserts a dish unless one with the same NameKey exists, in which
// case the existing row comes back untouched with existed=true. Callers that
// add dishes from outside — the suggestions, cmd/add-dish — rely on this
// to be idempotent: a name already in the catalogue is never a second dish,
// and never has its status overwritten by whoever arrived later.
func (s *Store) CreateDish(d model.Dish) (model.Dish, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Dish{}, false, err
	}
	defer tx.Rollback()

	got, existed, err := createDishTx(tx, d)
	if err != nil {
		return model.Dish{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.Dish{}, false, err
	}
	return got, existed, nil
}

// EnsureDish is CreateDish for the cooking log: the dish on the plate was
// eaten, so a new one is created active whatever d.Status says, and an
// existing proposed or rejected one is switched to active. A "no" to a
// suggestion does not outlive the family cooking it anyway.
func (s *Store) EnsureDish(d model.Dish) (model.Dish, bool, error) {
	d.Status = model.DishActive

	tx, err := s.db.Begin()
	if err != nil {
		return model.Dish{}, false, err
	}
	defer tx.Rollback()

	got, existed, err := createDishTx(tx, d)
	if err != nil {
		return model.Dish{}, false, err
	}
	if existed && got.Status != model.DishActive {
		if _, err := tx.Exec(`UPDATE dishes SET status = ? WHERE id = ?`, model.DishActive, got.ID); err != nil {
			return model.Dish{}, false, err
		}
		got.Status = model.DishActive
	}
	if err := tx.Commit(); err != nil {
		return model.Dish{}, false, err
	}
	return got, existed, nil
}

// createDishTx looks the key up before inserting rather than leaning on the
// unique index: the caller needs to know whether the row was already there,
// and an ON CONFLICT DO NOTHING would only say that nothing was inserted.
func createDishTx(tx *sql.Tx, d model.Dish) (model.Dish, bool, error) {
	name := cleanDishName(d.Name)
	if name == "" {
		return model.Dish{}, false, errors.New("store: dish name is empty")
	}
	status := orDefault(d.Status, model.DishActive)
	if !model.ValidDishStatus(status) {
		return model.Dish{}, false, fmt.Errorf("store: unknown dish status %q", status)
	}
	key := NameKey(name)

	existing, err := scanDish(tx.QueryRow(dishCols+` WHERE name_key = ?`, key))
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Dish{}, false, err
	}

	res, err := tx.Exec(`
		INSERT INTO dishes (name, name_key, meal, days, status, note)
		VALUES (?, ?, ?, ?, ?, ?)`,
		name, key, orDefault(d.Meal, model.DishMealAny), orDefault(d.Days, model.DishDaysAny),
		status, d.Note)
	if err != nil {
		return model.Dish{}, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Dish{}, false, err
	}
	got, err := scanDish(tx.QueryRow(dishCols+` WHERE id = ?`, id))
	return got, false, err
}

// Dish returns one dish by id; a missing one is sql.ErrNoRows (IsNotFound).
func (s *Store) Dish(id int64) (model.Dish, error) {
	return scanDish(s.db.QueryRow(dishCols+` WHERE id = ?`, id))
}

// Dishes returns the dishes in any of the given statuses, or all of them when
// none is given, in insertion order. The order carries no meaning — the menu
// shuffles, the prompts list — it only has to be stable.
func (s *Store) Dishes(statuses ...string) ([]model.Dish, error) {
	q := dishCols
	args := make([]any, 0, len(statuses))
	if len(statuses) > 0 {
		for _, st := range statuses {
			if !model.ValidDishStatus(st) {
				return nil, fmt.Errorf("store: unknown dish status %q", st)
			}
			args = append(args, st)
		}
		q += ` WHERE status IN (` + placeholders(len(statuses)) + `)`
	}
	rows, err := s.db.Query(q+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Dish
	for rows.Next() {
		d, err := scanDish(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetDishStatus records a decision on a dish — a suggestion accepted or turned
// down. A missing id is sql.ErrNoRows, so a tap on a card whose dish is gone
// can say so instead of pretending it worked.
func (s *Store) SetDishStatus(id int64, status string) error {
	if !model.ValidDishStatus(status) {
		return fmt.Errorf("store: unknown dish status %q", status)
	}
	res, err := s.db.Exec(`UPDATE dishes SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func scanDish(sc interface{ Scan(dest ...any) error }) (model.Dish, error) {
	var d model.Dish
	err := sc.Scan(&d.ID, &d.Name, &d.Meal, &d.Days, &d.Status, &d.Note, &d.CreatedAt)
	return d, err
}
