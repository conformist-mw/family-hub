package store

import (
	"database/sql"
	"fmt"
	"time"

	"familyhub/internal/model"
)

const mealCols = `
	SELECT m.id, m.dish_id, d.name, d.status, m.date, m.meal, m.who, m.status,
	       m.leftover, m.created_at
	FROM meals m
	JOIN dishes d ON d.id = m.dish_id`

// Lunch before dinner, then in the order the rows were written.
const mealOrder = ` ORDER BY m.date, CASE m.meal WHEN 'lunch' THEN 0 ELSE 1 END, m.id`

// PlanMeal records the morning pick: dishID is what the family means to cook
// for this meal. A meal has one plan, so any other planned dish for the same
// (date, meal) is dropped. An eaten row is left alone — a plan tapped after a
// photo of the plate does not undo what was already eaten; if the eaten dish
// is dishID itself, that row comes back unchanged.
func (s *Store) PlanMeal(dishID int64, date, meal, who string, leftover bool) (model.MealEntry, error) {
	if err := checkMealKey(date, meal); err != nil {
		return model.MealEntry{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.MealEntry{}, err
	}
	defer tx.Rollback()

	if _, err := dishStatusTx(tx, dishID); err != nil {
		return model.MealEntry{}, err
	}
	if _, err := tx.Exec(`
		DELETE FROM meals
		WHERE date = ? AND meal = ? AND status = 'planned' AND dish_id <> ?`,
		date, meal, dishID); err != nil {
		return model.MealEntry{}, err
	}
	// Re-planning the same dish updates the row in place rather than
	// replacing it, so its id stays valid for anything already pointing at it.
	if _, err := tx.Exec(`
		INSERT INTO meals (dish_id, date, meal, who, status, leftover)
		VALUES (?, ?, ?, ?, 'planned', ?)
		ON CONFLICT(dish_id, date, meal) DO UPDATE
		SET who = excluded.who, leftover = excluded.leftover
		WHERE status = 'planned'`,
		dishID, date, meal, who, leftover); err != nil {
		return model.MealEntry{}, err
	}
	got, err := mealByKeyTx(tx, dishID, date, meal)
	if err != nil {
		return model.MealEntry{}, err
	}
	return got, tx.Commit()
}

// RecordEaten records that dishID was eaten at this meal — from the evening
// check or a photo of the plate. already is true only when that exact row was
// eaten before, and then nothing is written: the photo and the evening answer
// often report the same meal twice. A planned row for the same dish becomes
// eaten; planned rows for other dishes are dropped, because the plan did not
// happen. A proposed dish that was eaten is no longer a proposal.
func (s *Store) RecordEaten(dishID int64, date, meal, who string, leftover bool) (model.MealEntry, bool, error) {
	if err := checkMealKey(date, meal); err != nil {
		return model.MealEntry{}, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.MealEntry{}, false, err
	}
	defer tx.Rollback()

	got, already, err := recordEatenTx(tx, dishID, date, meal, who, leftover)
	if err != nil {
		return model.MealEntry{}, false, err
	}
	return got, already, tx.Commit()
}

// ConfirmMeal is RecordEaten for an existing row — "yes, we had the planned
// dish". A missing id is sql.ErrNoRows (IsNotFound).
func (s *Store) ConfirmMeal(id int64, who string) (model.MealEntry, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.MealEntry{}, false, err
	}
	defer tx.Rollback()

	row, err := scanMeal(tx.QueryRow(mealCols+` WHERE m.id = ?`, id))
	if err != nil {
		return model.MealEntry{}, false, err
	}
	got, already, err := recordEatenTx(tx, row.DishID, row.Date, row.Meal, who, row.Leftover)
	if err != nil {
		return model.MealEntry{}, false, err
	}
	return got, already, tx.Commit()
}

func recordEatenTx(tx *sql.Tx, dishID int64, date, meal, who string, leftover bool) (model.MealEntry, bool, error) {
	status, err := dishStatusTx(tx, dishID)
	if err != nil {
		return model.MealEntry{}, false, err
	}
	existing, err := mealByKeyTx(tx, dishID, date, meal)
	switch {
	case err == nil && existing.Status == model.MealEaten:
		return existing, true, nil
	case err != nil && !IsNotFound(err):
		return model.MealEntry{}, false, err
	}

	// Confirming a plan keeps the planner's name unless someone is named now,
	// and keeps its leftover mark: the photo of a finished pot does not know
	// it was yesterday's.
	if _, err := tx.Exec(`
		INSERT INTO meals (dish_id, date, meal, who, status, leftover)
		VALUES (?, ?, ?, ?, 'eaten', ?)
		ON CONFLICT(dish_id, date, meal) DO UPDATE
		SET status = 'eaten',
		    who = CASE WHEN excluded.who <> '' THEN excluded.who ELSE who END,
		    leftover = MAX(leftover, excluded.leftover)
		WHERE status = 'planned'`,
		dishID, date, meal, who, leftover); err != nil {
		return model.MealEntry{}, false, err
	}
	if _, err := tx.Exec(`
		DELETE FROM meals
		WHERE date = ? AND meal = ? AND status = 'planned' AND dish_id <> ?`,
		date, meal, dishID); err != nil {
		return model.MealEntry{}, false, err
	}
	if status == model.DishProposed {
		if _, err := tx.Exec(`UPDATE dishes SET status = ? WHERE id = ?`, model.DishActive, dishID); err != nil {
			return model.MealEntry{}, false, err
		}
	}
	got, err := mealByKeyTx(tx, dishID, date, meal)
	return got, false, err
}

// DeleteMeal removes one row — "we did not eat at home", or a plan for a dish
// the family turned down. A missing id is sql.ErrNoRows.
func (s *Store) DeleteMeal(id int64) error {
	res, err := s.db.Exec(`DELETE FROM meals WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MealsOn returns every row of one day, planned and eaten, lunch first.
func (s *Store) MealsOn(date string) ([]model.MealEntry, error) {
	return s.meals(mealCols+` WHERE m.date = ?`+mealOrder, date)
}

// EatenOn returns only what was actually eaten on one day — the leftovers of
// the next morning and the dishes the evening check offers when nothing was
// planned.
func (s *Store) EatenOn(date string) ([]model.MealEntry, error) {
	return s.meals(mealCols+` WHERE m.date = ? AND m.status = 'eaten'`+mealOrder, date)
}

// LastSeen maps each dish that ever appeared in the journal to the last day it
// did, planned or eaten. Plans count so that an unanswered evening check does
// not make a dish look forgotten; dishes never planned or eaten are absent.
func (s *Store) LastSeen() (map[int64]time.Time, error) {
	rows, err := s.db.Query(`SELECT dish_id, MAX(date) FROM meals GROUP BY dish_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var date string
		if err := rows.Scan(&id, &date); err != nil {
			return nil, err
		}
		d, err := model.ParseDate(date)
		if err != nil {
			return nil, fmt.Errorf("store: meal date %q of dish %d: %w", date, id, err)
		}
		out[id] = d
	}
	return out, rows.Err()
}

func (s *Store) meals(q string, args ...any) ([]model.MealEntry, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.MealEntry
	for rows.Next() {
		m, err := scanMeal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// checkMealKey validates what arrives in callback data before it reaches the
// table: the CHECK constraint covers the meal, but nothing would stop a
// malformed date from being stored and never matching any day.
func checkMealKey(date, meal string) error {
	if !model.ValidMeal(meal) {
		return fmt.Errorf("store: unknown meal %q", meal)
	}
	if _, err := model.ParseDate(date); err != nil {
		return fmt.Errorf("store: bad meal date %q", date)
	}
	return nil
}

// dishStatusTx doubles as the existence check: a tap on a button whose dish is
// gone gets sql.ErrNoRows rather than a foreign-key error.
func dishStatusTx(tx *sql.Tx, dishID int64) (string, error) {
	var status string
	err := tx.QueryRow(`SELECT status FROM dishes WHERE id = ?`, dishID).Scan(&status)
	return status, err
}

func mealByKeyTx(tx *sql.Tx, dishID int64, date, meal string) (model.MealEntry, error) {
	return scanMeal(tx.QueryRow(mealCols+` WHERE m.dish_id = ? AND m.date = ? AND m.meal = ?`,
		dishID, date, meal))
}

func scanMeal(sc interface{ Scan(dest ...any) error }) (model.MealEntry, error) {
	var m model.MealEntry
	err := sc.Scan(&m.ID, &m.DishID, &m.Dish, &m.DishStatus, &m.Date, &m.Meal, &m.Who,
		&m.Status, &m.Leftover, &m.CreatedAt)
	return m, err
}
