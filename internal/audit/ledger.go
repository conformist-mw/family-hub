// Package audit builds the reconciliation view for one enrollment: a
// chronological ledger of visits and payments with a running per-lesson
// balance, a forecast of upcoming lessons with paid/unpaid marking, and a
// plain-text rendering shareable via Telegram/Viber. Everything here is
// pure — data in, data out; the store and handlers stay thin.
package audit

import (
	"familyhub/internal/model"
	"familyhub/internal/store"
)

const (
	KindVisit   = "visit"
	KindPayment = "payment"
	KindFuture  = "future"
	// KindExtra is money the course asked for that bought no lessons and no
	// coverage. It stands in the timeline because the reader reconciling a
	// month wants to see it, and it is a kind of its own because it must not
	// touch the running balance.
	KindExtra = "extra"
)

// Row is one ledger line. Balance is the running per-lesson balance after
// the row (meaningless for monthly enrollments — the template hides it).
// Covered is set on future rows only.
type Row struct {
	Date    string
	Kind    string
	Status  string  // visit rows
	Amount  float64 // payment and extra rows
	Lessons int     // payment rows: lessons_paid (0 for monthly passes)
	Covers  string  // payment rows, monthly: covers_until
	// What names an extra ("Кімоно"). Not called Label: one layer up the
	// rendered line is already called that (mini's auditRowDTO.Label), and two
	// fields with the same name meaning different things across one boundary
	// is how a renderer starts printing the wrong one.
	What    string
	Comment string
	Balance int
	Covered bool
}

// Summary aggregates the period for the page header and the text version.
type Summary struct {
	ByStatus    map[string]int
	PaidLessons int
	PaidAmount  float64
	// ExtrasAmount is kept apart from PaidAmount rather than folded into it.
	// PaidAmount is the other half of a pair — "оплачено за період: 8 занять
	// (3200 ₴)" — and a total that no longer divides by the count is a figure
	// the reader can check and find wrong.
	ExtrasAmount float64
	Opening      int
	Closing      int
}

// BuildLedger merges the period's visits and payments into one ascending
// timeline. On equal dates the payment sorts first — money arrives before
// the lesson it pays for. The running balance starts at OpeningBalance;
// a payment adds its lessons_paid, a done visit subtracts one, other
// statuses leave it unchanged.
//
// An extra takes its place in the timeline by date and carries its amount, but
// it neither moves the balance nor joins PaidAmount: it bought no lessons, so
// counting it in either would make the column and the total disagree with the
// lessons they are supposed to explain.
func BuildLedger(d store.AuditData) ([]Row, Summary) {
	sum := Summary{ByStatus: map[string]int{}, Opening: d.OpeningBalance}
	bal := d.OpeningBalance
	rows := make([]Row, 0, len(d.Visits)+len(d.Payments))

	vi, pi := 0, 0
	for vi < len(d.Visits) || pi < len(d.Payments) {
		takePayment := pi < len(d.Payments) &&
			(vi >= len(d.Visits) || d.Payments[pi].Date <= d.Visits[vi].Date)
		if takePayment {
			p := d.Payments[pi]
			pi++
			if p.IsExtra() {
				rows = append(rows, Row{
					Date: p.Date, Kind: KindExtra, Amount: p.Amount,
					What: p.Label, Comment: p.Comment, Balance: bal,
				})
				sum.ExtrasAmount += p.Amount
				continue
			}
			r := Row{Date: p.Date, Kind: KindPayment, Amount: p.Amount, Comment: p.Comment}
			if p.LessonsPaid != nil {
				r.Lessons = int(*p.LessonsPaid)
				bal += r.Lessons
			}
			if p.CoversUntil != nil {
				r.Covers = *p.CoversUntil
			}
			r.Balance = bal
			rows = append(rows, r)
			sum.PaidLessons += r.Lessons
			sum.PaidAmount += p.Amount
			continue
		}
		v := d.Visits[vi]
		vi++
		if v.Status == model.StatusDone {
			bal--
		}
		rows = append(rows, Row{
			Date: v.Date, Kind: KindVisit, Status: v.Status,
			Comment: v.Comment, Balance: bal,
		})
		sum.ByStatus[v.Status]++
	}
	sum.Closing = bal
	return rows, sum
}
