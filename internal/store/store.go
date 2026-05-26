package store

import (
	"database/sql"
	"time"

	"lessons/internal/model"
)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Balances() ([]model.Balance, error) {
	rows, err := s.db.Query(`
		SELECT e.id, e.child_id, e.activity_id, c.name, a.name,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes,
		       COALESCE((SELECT SUM(lessons_paid) FROM payments p
		                 WHERE p.enrollment_id=e.id AND p.lessons_paid IS NOT NULL),0) AS paid,
		       (SELECT COUNT(*) FROM visits v
		        WHERE v.enrollment_id=e.id AND v.status='done') AS done,
		       COALESCE((SELECT MAX(covers_until) FROM payments p
		                 WHERE p.enrollment_id=e.id AND p.covers_until IS NOT NULL),'') AS covers_until
		FROM enrollments e
		JOIN children c   ON c.id = e.child_id
		JOIN activities a ON a.id = e.activity_id
		ORDER BY e.active DESC, c.name, a.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	today := time.Now()
	var out []model.Balance
	for rows.Next() {
		var b model.Balance
		if err := rows.Scan(&b.ID, &b.ChildID, &b.ActivityID, &b.Child, &b.Activity,
			&b.BillingType, &b.CurrentPrice, &b.LowThreshold, &b.Active, &b.Notes,
			&b.Paid, &b.Done, &b.CoversUntil); err != nil {
			return nil, err
		}
		b.Remaining = b.Paid - b.Done
		if b.CoversUntil != "" {
			if until, err := model.ParseDate(b.CoversUntil); err == nil {
				b.DaysLeft = int(until.Sub(truncateDay(today)).Hours() / 24)
			}
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func truncateDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
