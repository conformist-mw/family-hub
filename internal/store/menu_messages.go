package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"familyhub/internal/model"
)

// MenuMessage returns the morning menu posted on date; none is
// sql.ErrNoRows (IsNotFound), which is also how the morning clock tells that
// today's menu has not gone out yet.
func (s *Store) MenuMessage(date string) (model.MenuMessage, error) {
	var m model.MenuMessage
	var shown string
	err := s.db.QueryRow(`
		SELECT date, chat_id, message_id, shown FROM menu_messages WHERE date = ?`, date).
		Scan(&m.Date, &m.ChatID, &m.MessageID, &shown)
	if err != nil {
		return model.MenuMessage{}, err
	}
	if m.Shown, err = decodeShown(shown); err != nil {
		return model.MenuMessage{}, fmt.Errorf("store: menu of %s: %w", date, err)
	}
	return m, nil
}

// SaveMenuMessage records the menu just posted. A second save for the same
// date replaces the first: the clock only posts when there is no row, so a
// replace means someone re-sent by hand, and the newer message is the one
// whose buttons are live.
func (s *Store) SaveMenuMessage(m model.MenuMessage) error {
	if _, err := model.ParseDate(m.Date); err != nil {
		return fmt.Errorf("store: bad menu date %q", m.Date)
	}
	shown, err := encodeShown(m.Shown)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO menu_messages (date, chat_id, message_id, shown)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(date) DO UPDATE
		SET chat_id = excluded.chat_id, message_id = excluded.message_id, shown = excluded.shown`,
		m.Date, m.ChatID, m.MessageID, shown)
	return err
}

// AppendShown adds the dishes a shuffle just offered to the day's shown set
// for one meal. It is a read-modify-write of one JSON cell, so two people
// tapping 🔀 at once would otherwise both read the old set and the later write
// would drop the earlier one's dishes. A missing menu is sql.ErrNoRows.
func (s *Store) AppendShown(date, meal string, ids []int64) error {
	if err := checkMealKey(date, meal); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// The transaction holds the write lock from BEGIN (see db.Open), so the
	// read below sees every earlier append; the no-op write doubles as the
	// existence check.
	res, err := tx.Exec(`UPDATE menu_messages SET shown = shown WHERE date = ?`, date)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	var raw string
	if err := tx.QueryRow(`SELECT shown FROM menu_messages WHERE date = ?`, date).Scan(&raw); err != nil {
		return err
	}
	shown, err := decodeShown(raw)
	if err != nil {
		return fmt.Errorf("store: menu of %s: %w", date, err)
	}
	for _, id := range ids {
		if !slices.Contains(shown[meal], id) {
			shown[meal] = append(shown[meal], id)
		}
	}
	enc, err := encodeShown(shown)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE menu_messages SET shown = ? WHERE date = ?`, enc, date); err != nil {
		return err
	}
	return tx.Commit()
}

// LastShown maps each dish the morning menu ever offered to the last day it
// did, shuffles included. The menu ranks a dish by the later of this and
// LastSeen: a dish offered every other morning and never picked has no plan
// or meal to age it, and would otherwise stay the "longest unseen" for good.
//
// It reads the whole table on every morning build and every 🔀. That is one
// short row a day — a few thousand after a decade, milliseconds to decode —
// so it is not bounded by a date: a cutoff would make every dish last shown
// before it rank by LastSeen alone, older than it really is.
func (s *Store) LastShown() (map[int64]time.Time, error) {
	rows, err := s.db.Query(`SELECT date, shown FROM menu_messages ORDER BY date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var date, raw string
		if err := rows.Scan(&date, &raw); err != nil {
			return nil, err
		}
		d, err := model.ParseDate(date)
		if err != nil {
			return nil, fmt.Errorf("store: menu date %q: %w", date, err)
		}
		shown, err := decodeShown(raw)
		if err != nil {
			return nil, fmt.Errorf("store: menu of %s: %w", date, err)
		}
		// Rows come oldest first, so a later day simply overwrites.
		for _, ids := range shown {
			for _, id := range ids {
				out[id] = d
			}
		}
	}
	return out, rows.Err()
}

func decodeShown(raw string) (map[string][]int64, error) {
	shown := map[string][]int64{}
	if raw == "" {
		return shown, nil
	}
	if err := json.Unmarshal([]byte(raw), &shown); err != nil {
		return nil, fmt.Errorf("bad shown %q: %w", raw, err)
	}
	return shown, nil
}

func encodeShown(shown map[string][]int64) (string, error) {
	if shown == nil {
		return "{}", nil
	}
	b, err := json.Marshal(shown)
	return string(b), err
}

// EveningMessage returns the evening check posted on date; none is
// sql.ErrNoRows (IsNotFound).
func (s *Store) EveningMessage(date string) (model.EveningMessage, error) {
	var m model.EveningMessage
	var away string
	err := s.db.QueryRow(`
		SELECT date, chat_id, message_id, away FROM evening_messages WHERE date = ?`, date).
		Scan(&m.Date, &m.ChatID, &m.MessageID, &away)
	if err != nil {
		return model.EveningMessage{}, err
	}
	if away != "" {
		m.Away = strings.Split(away, ",")
	}
	return m, nil
}

// SaveEveningMessage records the check just posted. A second one for the same
// day replaces the first and starts with nobody away: it is a new question.
func (s *Store) SaveEveningMessage(m model.EveningMessage) error {
	if _, err := model.ParseDate(m.Date); err != nil {
		return fmt.Errorf("store: bad evening date %q", m.Date)
	}
	_, err := s.db.Exec(`
		INSERT INTO evening_messages (date, chat_id, message_id)
		VALUES (?, ?, ?)
		ON CONFLICT(date) DO UPDATE
		SET chat_id = excluded.chat_id, message_id = excluded.message_id, away = ''`,
		m.Date, m.ChatID, m.MessageID)
	return err
}

// MarkAway records «Не вдома» for one meal of a day's check. A day without a
// recorded check is sql.ErrNoRows: its message, if any, keeps the answer in
// its keyboard as before.
func (s *Store) MarkAway(date, meal string) error {
	if err := checkMealKey(date, meal); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var away string
	if err := tx.QueryRow(`SELECT away FROM evening_messages WHERE date = ?`, date).Scan(&away); err != nil {
		return err
	}
	meals := []string{}
	if away != "" {
		meals = strings.Split(away, ",")
	}
	if slices.Contains(meals, meal) {
		return nil
	}
	meals = append(meals, meal)
	if _, err := tx.Exec(`UPDATE evening_messages SET away = ? WHERE date = ?`, strings.Join(meals, ","), date); err != nil {
		return err
	}
	return tx.Commit()
}
