package store

import (
	"database/sql"
	"math"
	"sort"
	"time"

	"familyhub/internal/model"
)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Balances() ([]model.Balance, error) {
	return s.balances("e.active = 1")
}

// BalanceFor returns the balance rollup for a single enrollment. Unlike
// Balances it does not filter on active — it is used to annotate a visit
// that was just recorded, and that visit exists regardless.
func (s *Store) BalanceFor(enrollmentID int64) (model.Balance, error) {
	out, err := s.balances("e.id = ?", enrollmentID)
	if err != nil {
		return model.Balance{}, err
	}
	if len(out) == 0 {
		return model.Balance{}, sql.ErrNoRows
	}
	return out[0], nil
}

func (s *Store) balances(where string, args ...any) ([]model.Balance, error) {
	rows, err := s.db.Query(`
		SELECT e.id, e.person_id, p.name, e.name, e.description,
		       e.billing_type, e.current_price, e.low_threshold, e.active, e.notes,
		       e.attendance_mode, e.payment_instructions, e.payment_notice_min,
		       COALESCE((SELECT SUM(lessons_paid) FROM payments pm
		                 WHERE pm.enrollment_id=e.id AND pm.lessons_paid IS NOT NULL),0) AS paid,
		       (SELECT COUNT(*) FROM visits v
		        WHERE v.enrollment_id=e.id AND v.status='done') AS done,
		       (SELECT COUNT(*) FROM visits v
		        WHERE v.enrollment_id=e.id AND v.status='done'
		          AND v.date >= date('now','localtime','start of month')) AS done_this_month
		FROM enrollments e
		JOIN persons p ON p.id = e.person_id
		WHERE `+where+`
		ORDER BY p.name, e.name
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	today := truncateDay(time.Now())
	var out []model.Balance
	for rows.Next() {
		var b model.Balance
		if err := rows.Scan(&b.ID, &b.PersonID, &b.Person, &b.Name, &b.Description,
			&b.BillingType, &b.CurrentPrice, &b.LowThreshold, &b.Active, &b.Notes,
			&b.AttendanceMode, &b.PaymentInstructions, &b.PaymentNoticeMin,
			&b.Paid, &b.Done, &b.DoneThisMonth); err != nil {
			return nil, err
		}
		b.Remaining = b.Paid - b.Done
		if b.BillingType == model.BillingMonthly {
			periods, err := s.coveragePeriods(b.ID)
			if err != nil {
				return nil, err
			}
			b.CoveredNow, b.CoversUntil, b.DaysLeft = coverageFromToday(periods, today)
			if !b.CoveredNow {
				b.PrepaidFrom, b.CoversUntil = upcomingCoverage(periods, today)
			}
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

type period struct {
	from time.Time
	to   time.Time
}

func (s *Store) coveragePeriods(enrollmentID int64) ([]period, error) {
	rows, err := s.db.Query(`
		SELECT covers_from, covers_until FROM payments
		WHERE enrollment_id=? AND covers_from IS NOT NULL AND covers_until IS NOT NULL`, enrollmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ps []period
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		f, err1 := model.ParseDate(from)
		t, err2 := model.ParseDate(to)
		if err1 != nil || err2 != nil {
			continue
		}
		ps = append(ps, period{from: f, to: t})
	}
	return ps, rows.Err()
}

// coverageFromToday reports whether today is covered by some paid period and,
// if so, the end of the contiguous run starting at today (merging adjacent or
// overlapping periods). A gap between periods correctly ends the run.
func coverageFromToday(periods []period, today time.Time) (bool, string, int) {
	if len(periods) == 0 {
		return false, "", 0
	}
	sort.Slice(periods, func(i, j int) bool { return periods[i].from.Before(periods[j].from) })

	var cover *time.Time
	for _, p := range periods {
		if !today.Before(p.from) && !today.After(p.to) {
			end := p.to
			cover = &end
			break
		}
	}
	if cover == nil {
		return false, "", 0
	}
	for _, p := range periods {
		// extend if the next period begins no later than the day after current end
		if !p.from.After(cover.AddDate(0, 0, 1)) && p.to.After(*cover) {
			end := p.to
			cover = &end
		}
	}
	// Round, not truncate: both bounds are local midnights, but a DST
	// transition inside the span makes one "day" 23 or 25 hours long.
	days := int(math.Round(cover.Sub(today).Hours() / 24))
	return true, cover.Format("2006-01-02"), days
}

// upcomingCoverage reports a paid period that has not started yet: its first
// day and the end of the contiguous run from there. It answers the case the
// school scenario is built on — September paid on 28 August. Coverage-from-
// today alone reads that as "nothing is paid", which would put a red badge on
// a course whose next month is already settled.
//
// Only the nearest future block counts; a gap between blocks ends the run for
// the same reason it does in coverageFromToday.
func upcomingCoverage(periods []period, today time.Time) (string, string) {
	sort.Slice(periods, func(i, j int) bool { return periods[i].from.Before(periods[j].from) })

	var start, end *time.Time
	for _, p := range periods {
		if !p.from.After(today) {
			continue
		}
		if start == nil {
			f, t := p.from, p.to
			start, end = &f, &t
			continue
		}
		if p.from.After(end.AddDate(0, 0, 1)) {
			break
		}
		if p.to.After(*end) {
			t := p.to
			end = &t
		}
	}
	if start == nil {
		return "", ""
	}
	return start.Format("2006-01-02"), end.Format("2006-01-02")
}

func truncateDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
