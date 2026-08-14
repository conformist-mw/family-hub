package appointments

import (
	"time"

	"familyhub/internal/model"
)

// The family group is the shared log of what was booked. Whoever wrote a visit
// down — the bot in a private chat, the web form, the Mini App on a phone —
// everyone else finds out the same way, from a message in the group. So the
// notification is part of what saving an appointment means, and it lives here
// next to the write rules rather than in each surface, where three copies would
// drift and one of them (both, as it happened) would simply be missing.

// Notifier posts an HTML message to the family group. The bot implements it;
// nothing here imports telebot. A nil Notifier means the bot is off or no group
// is configured, and the writes stay silent.
type Notifier interface {
	NotifyHTML(text string) error
}

func GroupAddText(items []model.Appointment, by string, loc *time.Location) string {
	head := "🆕 Новий візит"
	if len(items) > 1 {
		head = "🆕 Нові візити"
	}
	return head + byLine(by) + ":\n\n" + FormatList(items, loc)
}

func GroupChangeText(a model.Appointment, verb, by string, loc *time.Location) string {
	return "🔄 Візит " + verb + byLine(by) + ":\n" + Format(a, loc)
}

func GroupCancelText(a model.Appointment, by string, loc *time.Location) string {
	return "✗ Візит скасовано" + byLine(by) + ":\n" + Format(a, loc)
}

func GroupDeleteText(a model.Appointment, by string, loc *time.Location) string {
	return "🗑 Візит видалено" + byLine(by) + ":\n" + Format(a, loc)
}

// byLine attributes a group notification to whoever made the change. Empty when
// the surface cannot name them — a message with no byline is better than one
// with a made-up author.
func byLine(by string) string {
	if by == "" {
		return ""
	}
	return " (" + Escape(by) + ")"
}
