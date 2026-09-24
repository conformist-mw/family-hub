package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

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

	// Write first, read second. A deferred transaction that reads and then
	// writes can find, at the write, that another writer committed in
	// between; SQLite then fails it with SQLITE_BUSY instead of waiting,
	// because its snapshot is stale. A write as the first statement takes the
	// write lock up front, where busy_timeout does make it wait its turn.
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
