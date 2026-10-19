package bot

import (
	"fmt"
	"strings"
	"time"

	"familyhub/internal/audit"
	"familyhub/internal/model"
)

// topUpOption is one "pay this much to get this far" line of the pre-lesson
// warning.
type topUpOption struct {
	Label   string // "сьогодні", "до кінця тижня", "+2 тижні"
	Through string // date of the last lesson it pays for
	Lessons int    // lessons to pay, the existing debt included
}

// topUpOptions answers "how much do I send?" at the moment the warning asks
// for money. The last payment is no guide — a pack is sized to whatever the
// schedule needs that time, five lessons to cover Thursday and the two weeks
// after — so the amounts come from the schedule ahead and the course price
// instead: today alone, through the end of this week, and two weeks past
// that.
//
// The dates are the real upcoming lessons, trainer absences dropped, so a
// week the trainer is away costs nothing. Debt — lessons held but never paid
// for — rides on every option, since no payment can skip it. A horizon that
// adds no lesson over the one before it is left out rather than repeated.
//
// Per-lesson courses only, and only with a price: without one there is
// nothing to multiply, and a monthly pass is one fixed amount.
func topUpOptions(bal model.Balance, slots []model.Slot, absences []model.TrainerAbsence, now time.Time) []topUpOption {
	if bal.BillingType == model.BillingMonthly || bal.CurrentPrice <= 0 {
		return nil
	}
	var active []model.Slot
	for _, sl := range slots {
		if sl.Active {
			active = append(active, sl)
		}
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	weekEnd := today.AddDate(0, 0, (7-int(today.Weekday()))%7) // Sunday
	horizons := []struct {
		label string
		end   time.Time
	}{
		{"сьогодні", today},
		{"до кінця тижня", weekEnd},
		{"+2 тижні", weekEnd.AddDate(0, 0, 14)},
	}

	last := horizons[len(horizons)-1].end
	days := int(last.Sub(today).Hours()/24) + 1
	dates := audit.UpcomingDates(active, absences, today.Format("2006-01-02"), false, days)

	debt := 0
	if bal.Remaining < 0 {
		debt = -bal.Remaining
	}
	var out []topUpOption
	prev := 0
	for _, h := range horizons {
		end := h.end.Format("2006-01-02")
		n, through := 0, ""
		for _, d := range dates {
			if d > end {
				break
			}
			n++
			through = d
		}
		if n == 0 || n == prev {
			continue
		}
		prev = n
		out = append(out, topUpOption{Label: h.label, Through: through, Lessons: n + debt})
	}
	return out
}

// topUpLines renders the options under the warning's headline, one per line.
func topUpLines(bal model.Balance, opts []topUpOption) string {
	if len(opts) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\nДо оплати, по %s ₴ за заняття", formatAmount(bal.CurrentPrice))
	if bal.Remaining < 0 {
		fmt.Fprintf(&sb, " (разом із боргом %d зан.)", -bal.Remaining)
	}
	sb.WriteString(":")
	for i, o := range opts {
		fmt.Fprintf(&sb, "\n• %s", o.Label)
		// Today's date is already in the headline.
		if i > 0 {
			fmt.Fprintf(&sb, " (по %s)", formatDate(o.Through))
		}
		fmt.Fprintf(&sb, " — %d зан. = %s ₴", o.Lessons, formatAmount(float64(o.Lessons)*bal.CurrentPrice))
	}
	return sb.String()
}
