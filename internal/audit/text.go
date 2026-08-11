package audit

import (
	"fmt"
	"strings"

	"familyhub/internal/model"
)

// statusGlyphs mirror the bot's reminder buttons.
var statusGlyphs = map[string]string{
	model.StatusDone:        "✓",
	model.StatusRescheduled: "→",
	model.StatusCancelled:   "✗",
	model.StatusSkipped:     "⤵",
}

// View is everything the text rendering needs; the web handler fills it from
// the same data the page shows, so copy/send never diverge from the screen.
type View struct {
	Title       string // "<хто> · Гимнастика"
	PeriodLabel string // "з останньої оплати (12.06) по 18.07"
	BillingType string
	Rows        []Row
	Summary     Summary
	Forecast    Forecast
}

// RenderText renders the audit as plain text — no markdown, so it survives
// pasting into Telegram and Viber alike.
func RenderText(v View) string {
	var b strings.Builder
	b.WriteString(v.Title + "\n")
	b.WriteString("Період: " + v.PeriodLabel + "\n\n")

	perLesson := v.BillingType != model.BillingMonthly
	for _, r := range v.Rows {
		b.WriteString(dateShort(r.Date) + "  " + rowText(r, perLesson) + "\n")
	}
	for _, r := range v.Forecast.Rows {
		b.WriteString(dateShort(r.Date) + "  " + rowText(r, perLesson) + "\n")
	}

	b.WriteString("\n")
	var parts []string
	for _, st := range []string{model.StatusDone, model.StatusRescheduled, model.StatusCancelled, model.StatusSkipped} {
		if n := v.Summary.ByStatus[st]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d", capitalize(model.StatusLabels[st]), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString(strings.Join(parts, " · ") + "\n")
	}
	if v.Summary.PaidAmount > 0 || v.Summary.PaidLessons > 0 {
		if perLesson {
			b.WriteString(fmt.Sprintf("Оплачено за період: %d занять (%s)\n", v.Summary.PaidLessons, money(v.Summary.PaidAmount)))
		} else {
			b.WriteString(fmt.Sprintf("Оплачено за період: %s\n", money(v.Summary.PaidAmount)))
		}
	}
	if perLesson {
		b.WriteString(fmt.Sprintf("Залишок: %d (на початок періоду: %d)\n", v.Summary.Closing, v.Summary.Opening))
	}

	if len(v.Forecast.Rows) > 0 {
		f := v.Forecast
		b.WriteString("\n")
		if f.PaidThrough != "" {
			b.WriteString("Оплаченого вистачить до " + dateShort(f.PaidThrough) + "\n")
		}
		if f.TopUpCount > 0 {
			unit := "занять"
			if !perLesson {
				unit = "міс."
			}
			line := fmt.Sprintf("Доплатити: %d %s × %s = %s", f.TopUpCount, unit, moneyInt(f.TopUpAmount/float64(f.TopUpCount)), money(f.TopUpAmount))
			if f.Debt > 0 {
				line += fmt.Sprintf(" (борг %d + наперед %d)", f.Debt, f.Uncovered)
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func rowText(r Row, perLesson bool) string {
	switch r.Kind {
	case KindPayment:
		var s string
		switch {
		case r.Lessons > 0:
			s = fmt.Sprintf("оплата +%d занять (%s)", r.Lessons, money(r.Amount))
		case r.Covers != "":
			s = fmt.Sprintf("абонемент до %s (%s)", dateShort(r.Covers), money(r.Amount))
		default:
			s = fmt.Sprintf("оплата (%s)", money(r.Amount))
		}
		if r.Comment != "" {
			s += " — " + r.Comment
		}
		return s
	case KindFuture:
		if r.Covered {
			return "— за розкладом"
		}
		return "— за розкладом (не оплачено)"
	default:
		s := statusGlyphs[r.Status] + " " + model.StatusLabels[r.Status]
		if r.Comment != "" {
			s += " — " + r.Comment
		}
		return s
	}
}

// SplitMessage splits text into chunks of at most limit bytes, breaking on
// line boundaries — Telegram rejects messages over 4096 chars. A single
// overlong line (not realistic here) becomes its own chunk.
func SplitMessage(text string, limit int) []string {
	text = strings.TrimRight(text, "\n")
	if len(text) <= limit {
		return []string{text}
	}
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if cur.Len() > 0 && cur.Len()+len(line)+1 > limit {
			out = append(out, strings.TrimRight(cur.String(), "\n"))
			cur.Reset()
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	if cur.Len() > 0 {
		out = append(out, strings.TrimRight(cur.String(), "\n"))
	}
	return out
}

func dateShort(s string) string {
	t, err := model.ParseDate(s)
	if err != nil {
		return s
	}
	return t.Format("02.01")
}

func money(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%.0f ₴", v)
	}
	return fmt.Sprintf("%.2f ₴", v)
}

func moneyInt(v float64) string {
	return strings.TrimSuffix(money(v), " ₴")
}

func capitalize(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
