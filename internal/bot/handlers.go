package bot

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
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
		"Привіт! Це трекер занять і сімейних записів.\nChat id: %d\n\nКоманди: /balance — баланс по курсах, /stats — витрати, /visit — записати візит, /list — записи по тижнях, /help — довідка.",
		chat.ID,
	)
	return c.Send(msg)
}

func (b *Bot) cmdHelp(c tele.Context) error {
	return c.Send(strings.Join([]string{
		"Заняття:",
		"/add — відмітити заняття кнопками (курс → дата → статус)",
		"/balance — залишок занять і абонементів по кожному курсу",
		"/stats — витрати за місяць / рік / весь час",
		"",
		"Записи (лікарі, майстри, разові візити):",
		"/visit — записати: /visit завтра 15:00 педикюр (можна кілька рядків)",
		"/week — що на найближчий тиждень",
		"/list — записи по тижнях: перенести, виправити, скасувати",
		"",
		"/start — привітання, показує chat id",
		"",
		"У приватці зі мною запис можна писати й без /visit — просто текстом.",
	}, "\n"))
}

func (b *Bot) cmdBalance(c tele.Context) error {
	balances, err := b.store.Balances()
	if err != nil {
		b.logger.Error("bot: balances", "err", err)
		return c.Send("Не вдалося отримати баланс.")
	}
	if len(balances) == 0 {
		return c.Send("Активних курсів немає.")
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
	if bal.BillingType == model.BillingMonthly {
		switch {
		case !bal.CoveredNow:
			return fmt.Sprintf("%s %s — %s: немає абонемента", mark, bal.Person, label)
		default:
			return fmt.Sprintf("%s %s — %s: %d дн.", mark, bal.Person, label, bal.DaysLeft)
		}
	}
	return fmt.Sprintf("%s %s — %s: залишилось %d (цього місяця %d)",
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
			return mark + " Немає активного абонемента"
		}
		return fmt.Sprintf("%s Абонемент до %s, залишилось %d дн.", mark, dateShort(bal.CoversUntil), bal.DaysLeft)
	}
	// An empty balance reads as the pre-lesson warning's wording rather than
	// a literal "0 из N", which looked odd. emptyBalanceText keeps both
	// surfaces in sync; capitalised here as it starts the line.
	if bal.State() == "empty" {
		return mark + " " + capitalizeFirst(emptyBalanceText(bal))
	}
	// "из N" is the size of the most recent pack — "how much of the last
	// payment is left". Dropped when no payment has been recorded yet.
	if bal.LastPack == 0 {
		return fmt.Sprintf("%s Залишилось оплачених: %d", mark, bal.Remaining)
	}
	return fmt.Sprintf("%s Залишилось оплачених: %d з %d", mark, bal.Remaining, bal.LastPack)
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
		return c.Send("Не вдалося отримати статистику.")
	}
	var lines []string
	lines = append(lines,
		"*Витрати*",
		fmt.Sprintf("цього місяця: %s", money(st.TotalMonth)),
		fmt.Sprintf("цього року:   %s", money(st.TotalYear)),
		fmt.Sprintf("за весь час:  %s", money(st.TotalAll)),
	)
	if len(st.ByCourse) > 0 {
		lines = append(lines, "", "*Топ курсів (усього):*")
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
