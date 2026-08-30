package bot

import (
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
	"familyhub/internal/reminders"
	"familyhub/internal/store"
)

// The evening chore nag. Deliberately the only thing the bot says about
// recurring chores: Home Assistant already announces what is due from the ICS
// feed every morning, so a message from the bot means "you forgot", not
// "here is your day". A push that always arrives stops being read; one that
// only arrives when something is open keeps its meaning.

// sendReminderNag lists what came due today and nobody closed. It stays quiet
// when everything is closed — an empty "nothing to report" would be exactly
// the background noise this avoids.
func (b *Bot) sendReminderNag(now time.Time) {
	open, err := b.cfg.Reminders.UnclosedOn(now)
	if err != nil {
		b.logger.Error("bot: reminder nag query", "err", err)
		return
	}
	if len(open) == 0 {
		return
	}
	if _, err := b.sendToGroup(reminderNagText(open), tele.ModeHTML, buildNagMarkup(open)); err != nil {
		b.logger.Error("bot: send reminder nag", "err", err)
	}
}

// buildNagMarkup puts one row per open chore: close it, or pass on it. The
// buttons are here rather than only in the Mini App because the message is
// already on screen — "read it, tap it, done" without opening anything is the
// difference between a chore that gets closed and one that gets scrolled past.
//
// Callback data is the three fields passed separately, which telebot joins
// with "|". Not the ":" the visit buttons use: due_at carries its own colon
// (2026-08-29T11:00), so a colon-separated payload could not be split back
// apart without guessing. "|" appears in none of the three fields, and
// telebot's dispatch only treats the FIRST one — the one after the unique —
// as structural. The whole payload is around 25 bytes, well inside Telegram's
// 64-byte limit.
func buildNagMarkup(open []reminders.Occurrence) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	rows := make([]tele.Row, 0, len(open))
	for _, o := range open {
		id := strconv.FormatInt(o.ReminderID, 10)
		due := o.Due.Format(model.LocalDatetime)
		rows = append(rows, m.Row(
			m.Data("✓ "+shortTitle(o.Title), "rem_chore", id, due, model.OccDone),
			m.Data("✗", "rem_chore", id, due, model.OccSkipped),
		))
	}
	m.Inline(rows...)
	return m
}

// shortTitle keeps a button readable on a phone. A label that wraps turns a
// row of two buttons into a wall.
func shortTitle(s string) string {
	const max = 18
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

// onChoreTap closes one chore from the evening message and redraws it, so the
// list always shows what is still open rather than what was open when it was
// sent.
func (b *Bot) onChoreTap(c tele.Context) error {
	id, dueAt, status, err := parseChoreData(c.Data(), b.cfg.Loc)
	if err != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: "Невірні дані"})
		return nil
	}

	// A stale keyboard tapped again with the SAME answer — someone else closed
	// it, or this message was left open from an earlier tap. Say so instead of
	// re-writing the record for no reason.
	//
	// A different answer goes through: tapping ✓ by mistake has to be fixable
	// from the group, not only from the Mini App.
	if prev, err := b.store.GetOccurrence(id, dueAt.Format(model.LocalDatetime)); err == nil &&
		prev.Status == status {
		_ = c.Respond(&tele.CallbackResponse{Text: "Вже закрито"})
		return b.redrawNag(c, dueAt)
	}

	// senderName is the same Telegram first name the Mini App stores, so both
	// entry points name a person the same way in the record.
	switch err := b.cfg.Reminders.Mark(id, dueAt, status, senderName(c)); {
	case err == nil:
		_ = c.Respond(&tele.CallbackResponse{Text: choreDoneToast(status)})
	case errors.Is(err, reminders.ErrNoSuchOccurrence), store.IsNotFound(err):
		_ = c.Respond(&tele.CallbackResponse{Text: "Не знайдено"})
		return nil
	case errors.Is(err, reminders.ErrFutureMark):
		_ = c.Respond(&tele.CallbackResponse{Text: "Ще не настало"})
		return nil
	default:
		b.logger.Error("bot: mark chore", "reminder_id", id, "due_at", dueAt, "err", err)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не вдалося"})
		return nil
	}
	return b.redrawNag(c, dueAt)
}

// redrawNag rewrites the message with whatever is still open on that date. An
// emptied list becomes a closing statement rather than a message with no
// content and a dead keyboard.
func (b *Bot) redrawNag(c tele.Context, on time.Time) error {
	open, err := b.cfg.Reminders.UnclosedOn(on)
	if err != nil {
		b.logger.Error("bot: redraw nag", "err", err)
		return nil
	}
	if len(open) == 0 {
		return c.Edit("🔔 <b>Сьогодні все закрито.</b>", tele.ModeHTML, b.appMarkup())
	}
	return c.Edit(reminderNagText(open), tele.ModeHTML, buildNagMarkup(open))
}

func choreDoneToast(status string) string {
	if status == model.OccSkipped {
		return "Пропущено"
	}
	return "Закрито"
}

// parseChoreData reads back what buildNagMarkup wrote. Callback data comes
// from Telegram and is echoed from a message that may be days old, so every
// field is checked rather than assumed.
func parseChoreData(data string, loc *time.Location) (int64, time.Time, string, error) {
	parts := strings.Split(data, "|")
	if len(parts) != 3 {
		return 0, time.Time{}, "", fmt.Errorf("bad chore data %q", data)
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, time.Time{}, "", fmt.Errorf("bad reminder id in %q", data)
	}
	if loc == nil {
		loc = time.Local
	}
	due, err := time.ParseInLocation(model.LocalDatetime, parts[1], loc)
	if err != nil {
		return 0, time.Time{}, "", fmt.Errorf("bad due_at in %q", data)
	}
	status := parts[2]
	// pending is a state the system writes, never a decision a person records,
	// so it is not accepted here even though the column allows it.
	if status != model.OccDone && status != model.OccSkipped {
		return 0, time.Time{}, "", fmt.Errorf("bad status in %q", data)
	}
	return id, due, status, nil
}

// reminderNagText is the message body. Each line carries the time the chore
// came due, because "you did not do it" is easier to act on when it says
// which one of the morning's three it means.
func reminderNagText(open []reminders.Occurrence) string {
	var b strings.Builder
	b.WriteString("🔔 <b>Сьогодні не закрито:</b>\n\n")
	for _, o := range open {
		b.WriteString("• ")
		b.WriteString(html.EscapeString(o.Title))
		if o.Person != "" {
			b.WriteString(" · ")
			b.WriteString(html.EscapeString(o.Person))
		}
		b.WriteString(" <i>(")
		b.WriteString(o.Due.Format("15:04"))
		b.WriteString(")</i>\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
