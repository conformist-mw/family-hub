package payments

import (
	"html"
	"strconv"
	"time"

	"familyhub/internal/model"
)

// Money is the thing in this app that two people have to agree on. Until now a
// payment was written down in silence on both surfaces, so "я вже заплатила за
// футбол" and "треба заплатити за футбол" could both be true in the same
// evening. The group message is what makes a payment shared knowledge, and it
// belongs beside the write rules for the same reason the appointment one does:
// one copy, not one per surface.

// Notifier posts an HTML message to the family group. The bot implements it;
// nothing here imports telebot. A nil Notifier means the bot is off or no group
// is configured, and the writes stay silent.
type Notifier interface {
	NotifyHTML(text string) error
}

func GroupAddText(p model.Payment, by string) string {
	return "💸 Оплата" + byLine(by) + ":\n" + Format(p)
}

func GroupChangeText(p model.Payment, by string) string {
	return "🔄 Оплату змінено" + byLine(by) + ":\n" + Format(p)
}

func GroupDeleteText(p model.Payment, by string) string {
	return "🗑 Оплату видалено" + byLine(by) + ":\n" + Format(p)
}

// Format renders a payment as one line: the course, who it is for, the amount,
// and what it buys. Telegram's HTML parse mode, so anything a person typed is
// escaped — an unescaped "&" makes Telegram reject the whole message.
func Format(p model.Payment) string {
	line := "💳 <b>" + html.EscapeString(p.Class) + "</b>"
	if p.Person != "" {
		line += " · " + html.EscapeString(p.Person)
	}
	line += " — " + money(p.Amount)
	if what := covers(p); what != "" {
		line += " · " + what
	}
	return line
}

// covers says what the money bought — a pack of lessons or a named month.
// Empty when the row says neither, which the forms do not allow but an old
// imported row might.
func covers(p model.Payment) string {
	if p.LessonsPaid != nil && *p.LessonsPaid > 0 {
		return model.Plural(int(*p.LessonsPaid), "заняття", "заняття", "занять")
	}
	if p.CoversFrom == nil || *p.CoversFrom == "" {
		return ""
	}
	from, err := time.Parse("2006-01-02", *p.CoversFrom)
	if err != nil {
		return ""
	}
	// The coverage is always a whole calendar month (see monthRange), so its
	// first day names it.
	return "за " + model.MonthsNominative[int(from.Month())]
}

func money(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10) + " ₴"
	}
	return strconv.FormatFloat(v, 'f', 2, 64) + " ₴"
}

// byLine attributes a group notification to whoever made the change. Empty when
// the surface cannot name them — a message with no byline is better than one
// with a made-up author.
func byLine(by string) string {
	if by == "" {
		return ""
	}
	return " (" + html.EscapeString(by) + ")"
}
