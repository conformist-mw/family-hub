package bot

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tele "gopkg.in/telebot.v3"

	"lessons/internal/audit"
	"lessons/internal/model"
)

// capitalizeFirst upper-cases the first rune of s, leaving the rest intact. It
// is rune-aware so Cyrillic (multi-byte) leads are handled correctly.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

func (b *Bot) cmdStart(c tele.Context) error {
	chat := c.Chat()
	user := c.Sender()
	b.logger.Info("bot: /start",
		"chat_id", chat.ID,
		"chat_type", chat.Type,
		"user", user.Username,
	)
	msg := fmt.Sprintf(
		"Привет! Это трекер занятий.\nChat id: %d\n\nКоманды: /balance — баланс по курсам, /stats — потрачено, /help — справка.",
		chat.ID,
	)
	return c.Send(msg)
}

func (b *Bot) cmdHelp(c tele.Context) error {
	return c.Send(strings.Join([]string{
		"/add — отметить занятие через кнопки (курс → дата → статус)",
		"/balance — остаток занятий и абонементов по каждому курсу",
		"/stats — потрачено в этом месяце / году / за всё время",
		"/start — приветствие, показывает chat id",
	}, "\n"))
}

func (b *Bot) cmdBalance(c tele.Context) error {
	balances, err := b.store.Balances()
	if err != nil {
		b.logger.Error("bot: balances", "err", err)
		return c.Send("Не удалось получить баланс.")
	}
	if len(balances) == 0 {
		return c.Send("Активных курсов нет.")
	}
	var lines []string
	lines = append(lines, "*Баланс*")
	for _, bal := range balances {
		lines = append(lines, formatBalanceLine(bal, b.loadPaidOutlook(bal)))
	}
	return c.Send(strings.Join(lines, "\n"), tele.ModeMarkdown)
}

func formatBalanceLine(bal model.Balance, out paidOutlook) string {
	mark := "·"
	switch bal.State() {
	case "low":
		mark = "⚠️"
	case "empty":
		mark = "❗"
	default:
		mark = "✓"
	}
	label := bal.Name
	if bal.BillingType == model.BillingMonthly {
		switch {
		case !bal.CoveredNow:
			return fmt.Sprintf("%s %s — %s: нет абонемента", mark, bal.Person, label)
		default:
			return fmt.Sprintf("%s %s — %s: %d дн.", mark, bal.Person, label, bal.DaysLeft)
		}
	}
	return fmt.Sprintf("%s %s — %s: осталось %s (в этом месяце %d)",
		mark, bal.Person, label, paidFragment(bal, out), bal.DoneThisMonth)
}

// paidOutlook is the schedule-aware half of the per-lesson balance: how far
// the paid lessons stretch, and — when a top-up landed while the previous
// pack still had lessons in it — when the newest payment starts being spent.
// Lessons are consumed in payment order, so the newest pack is always the
// last one to be used.
type paidOutlook struct {
	CarriedOver  int    // lessons left from payments older than the last one
	Through      string // date the last paid lesson falls on; "" if past the horizon
	LastPackFrom string // date the last payment starts being spent; "" if it already is
}

// newPaidOutlook is the pure part: dates are the upcoming lesson dates, at
// most Remaining of them. A short list means the schedule ran past the
// forecast horizon, and then "through" stays unknown rather than wrong.
func newPaidOutlook(bal model.Balance, dates []string) paidOutlook {
	var o paidOutlook
	if bal.BillingType == model.BillingMonthly || bal.Remaining <= 0 {
		return o
	}
	if o.CarriedOver = bal.Remaining - bal.LastPack; o.CarriedOver < 0 {
		o.CarriedOver = 0
	}
	if len(dates) == bal.Remaining {
		o.Through = dates[len(dates)-1]
	}
	if o.CarriedOver > 0 && o.CarriedOver < len(dates) {
		o.LastPackFrom = dates[o.CarriedOver]
	}
	return o
}

// paidFragment renders the remaining lessons for both the digest and the
// post-marking line, so the two can't drift apart again.
//
// "из N" appears only when everything left fits inside the last payment —
// paying ahead while lessons remain is normal, and then the remainder spans
// several packs and "3 из 8" would be a lie. That case shows the newest
// pack separately instead, with the date it kicks in.
func paidFragment(bal model.Balance, out paidOutlook) string {
	if bal.Remaining <= 0 {
		return strconv.Itoa(bal.Remaining)
	}
	s := strconv.Itoa(bal.Remaining)
	if out.CarriedOver == 0 && bal.LastPack > 0 {
		s += fmt.Sprintf(" из %d", bal.LastPack)
	}
	if out.Through != "" {
		s += " — до " + dateRuShort(out.Through)
	}
	if out.CarriedOver > 0 && bal.LastPack > 0 && out.LastPackFrom != "" {
		s += fmt.Sprintf(" · последняя оплата %d с %s", bal.LastPack, dateRuShort(out.LastPackFrom))
	}
	return s
}

// balanceStatusLine renders the one-line paid-balance summary appended to a
// Telegram message right after a lesson is marked. The traffic-light marker
// follows Balance.State so it agrees with the dashboard badge.
func balanceStatusLine(bal model.Balance, out paidOutlook) string {
	mark := "🟢"
	switch bal.State() {
	case "low":
		mark = "🟡"
	case "empty":
		mark = "🔴"
	}
	if bal.BillingType == model.BillingMonthly {
		if !bal.CoveredNow {
			return mark + " Нет активного абонемента"
		}
		return fmt.Sprintf("%s Абонемент до %s, осталось %d дн.", mark, dateRu(bal.CoversUntil), bal.DaysLeft)
	}
	// An empty balance reads as the pre-lesson warning's wording rather than
	// a literal "0 из N", which looked odd. emptyBalanceText keeps both
	// surfaces in sync; capitalised here as it starts the line.
	if bal.State() == "empty" {
		return mark + " " + capitalizeFirst(emptyBalanceText(bal))
	}
	return fmt.Sprintf("%s Осталось оплаченных: %s", mark, paidFragment(bal, out))
}

// balanceLineFor loads and formats the balance line for an enrollment. It
// returns "" on a lookup failure — the visit is already recorded at that
// point, and a missing balance line must not turn that into an error.
func (b *Bot) balanceLineFor(eid int64) string {
	bal, err := b.store.BalanceFor(eid)
	if err != nil {
		b.logger.Error("bot: balance line", "err", err, "eid", eid)
		return ""
	}
	return balanceStatusLine(bal, b.loadPaidOutlook(bal))
}

// loadPaidOutlook walks the enrollment's schedule far enough to place every
// remaining paid lesson on a date. Any failure degrades to a zero outlook —
// the line then reads like it did before dates existed, which beats losing it.
func (b *Bot) loadPaidOutlook(bal model.Balance) paidOutlook {
	if bal.BillingType == model.BillingMonthly || bal.Remaining <= 0 {
		return paidOutlook{}
	}
	slots, err := b.store.ListSlots(bal.ID)
	if err != nil {
		b.logger.Error("bot: outlook slots", "err", err, "eid", bal.ID)
		return paidOutlook{}
	}
	absences, err := b.store.AbsencesForEnrollment(bal.ID)
	if err != nil {
		b.logger.Error("bot: outlook absences", "err", err, "eid", bal.ID)
		return paidOutlook{}
	}
	today := time.Now().Format("2006-01-02")
	hasToday, err := b.store.VisitExistsForDate(bal.ID, today)
	if err != nil {
		b.logger.Error("bot: outlook visit-exists", "err", err, "eid", bal.ID)
		return paidOutlook{}
	}
	return newPaidOutlook(bal, audit.UpcomingDates(slots, absences, today, hasToday, bal.Remaining))
}

func (b *Bot) cmdStats(c tele.Context) error {
	st, err := b.store.Stats()
	if err != nil {
		b.logger.Error("bot: stats", "err", err)
		return c.Send("Не удалось получить статистику.")
	}
	var lines []string
	lines = append(lines,
		"*Потрачено*",
		fmt.Sprintf("в этом месяце: %s", money(st.TotalMonth)),
		fmt.Sprintf("в этом году:   %s", money(st.TotalYear)),
		fmt.Sprintf("за всё время:  %s", money(st.TotalAll)),
	)
	if len(st.ByCourse) > 0 {
		lines = append(lines, "", "*Топ курсов (всего):*")
		top := st.ByCourse
		if len(top) > 5 {
			top = top[:5]
		}
		for _, c := range top {
			lines = append(lines, fmt.Sprintf("• %s — %s: %s", c.Person, c.Class, money(c.Amount)))
		}
	}
	return c.Send(strings.Join(lines, "\n"), tele.ModeMarkdown)
}

func money(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d ₴", int64(v))
	}
	return fmt.Sprintf("%.2f ₴", v)
}
