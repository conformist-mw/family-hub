package bot

import (
	"fmt"
	"strings"

	tele "gopkg.in/telebot.v3"

	"lessons/internal/model"
)

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
		lines = append(lines, formatBalanceLine(bal))
	}
	return c.Send(strings.Join(lines, "\n"), tele.ModeMarkdown)
}

func formatBalanceLine(bal model.Balance) string {
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
	if bal.Description != "" {
		label = bal.Name + " (" + bal.Description + ")"
	}
	if bal.BillingType == model.BillingMonthly {
		switch {
		case !bal.CoveredNow:
			return fmt.Sprintf("%s %s — %s: нет абонемента", mark, bal.Person, label)
		default:
			return fmt.Sprintf("%s %s — %s: %d дн.", mark, bal.Person, label, bal.DaysLeft)
		}
	}
	return fmt.Sprintf("%s %s — %s: осталось %d (в этом месяце %d)",
		mark, bal.Person, label, bal.Remaining, bal.DoneThisMonth)
}

// balanceStatusLine renders the one-line paid-balance summary appended to a
// Telegram message right after a lesson is marked. The traffic-light marker
// follows Balance.State so it agrees with the dashboard badge.
func balanceStatusLine(bal model.Balance) string {
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
	// "из N" is the size of the most recent pack — "how much of the last
	// payment is left". Dropped when no payment has been recorded yet.
	if bal.LastPack == 0 {
		return fmt.Sprintf("%s Осталось оплаченных: %d", mark, bal.Remaining)
	}
	return fmt.Sprintf("%s Осталось оплаченных: %d из %d", mark, bal.Remaining, bal.LastPack)
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
	return balanceStatusLine(bal)
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
			name := c.Class
			if c.ClassDesc != "" {
				name = c.Class + " (" + c.ClassDesc + ")"
			}
			lines = append(lines, fmt.Sprintf("• %s — %s: %s", c.Person, name, money(c.Amount)))
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
