package appointments

import (
	"fmt"
	"html"
	"strings"
	"time"

	"familyhub/internal/model"
)

// How an appointment reads in Telegram. It lives here rather than in the bot
// because all three surfaces now send messages about appointments — the bot's
// own cards and digests, and the group notification a web or Mini App write
// produces — and three copies of this is how "📌 Ортодонт" starts looking
// different depending on who typed it.
//
// The markup is Telegram's HTML parse mode, so everything a person typed is
// escaped: a title like "Ремонт <крана> & фарба" is text, not markup, and
// unescaped it makes Telegram reject the whole message as unparseable entities.

var weekdaysShort = [7]string{"нд", "пн", "вт", "ср", "чт", "пт", "сб"}

// WhenLabel renders an appointment's start as "пн 8 лип, 10:30" (falls back to
// the raw stored value if it can't be parsed).
func WhenLabel(a model.Appointment, loc *time.Location) string {
	t, err := a.Start(loc)
	if err != nil {
		return html.EscapeString(a.StartsAt)
	}
	return fmt.Sprintf("%s %d %s, %02d:%02d",
		weekdaysShort[int(t.Weekday())], t.Day(), model.MonthsShort[int(t.Month())], t.Hour(), t.Minute())
}

// Format renders one appointment as a card line.
func Format(a model.Appointment, loc *time.Location) string {
	who := ""
	if a.Person != "" {
		who = " · " + html.EscapeString(a.Person)
	}
	return fmt.Sprintf("📌 <b>%s</b> — %s%s%s",
		html.EscapeString(a.Title), WhenLabel(a, loc), who, CostSuffix(a))
}

func FormatList(items []model.Appointment, loc *time.Location) string {
	lines := make([]string, 0, len(items))
	for _, a := range items {
		lines = append(lines, Format(a, loc))
	}
	return strings.Join(lines, "\n")
}

// CostSuffix renders a recorded amount, including 0 ("free" was a decision
// somebody made, so it is worth showing). Nothing when unrecorded.
func CostSuffix(a model.Appointment) string {
	if a.Cost == nil {
		return ""
	}
	return " · " + money(*a.Cost)
}

func money(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d ₴", int64(v))
	}
	return fmt.Sprintf("%.2f ₴", v)
}

// Escape makes a person's text safe to drop into one of these messages, for
// callers that assemble their own line around a shared one.
func Escape(s string) string { return html.EscapeString(s) }
