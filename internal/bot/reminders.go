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

// The bot says two things about recurring chores, and they are different
// sentences. At the moment a chore comes due it says "пора" — that is what a
// reminder is for, and without it a chore you have forgotten is one nothing
// ever raises. In the evening it says "не закрито", which is about what the
// day did not answer.
//
// This used to be the nag alone, on the theory that Home Assistant announces
// what is due from the ICS feed. It does not say it here, so a chore recorded
// itself, waited, and told nobody until twelve hours later — by which time
// knowing is no use.

// maxPushLookback bounds how far back a due-time push will reach. The mark is
// set to boot time and moves with each tick, so this only matters when a tick
// is late — and it has to stay small: the materialiser backfills up to
// BackfillWindow after downtime, and announcing that would empty a month of
// history into the family chat in one message.
//
// Anything older is not lost, only unannounced: the evening nag still reports
// what the day left open.
const maxPushLookback = 10 * time.Minute

// sendDueChores announces what has come due since the mark and is still open,
// returning the new mark. One message for the whole minute rather than one per
// chore: three chores at 08:00 is one notification, not three.
//
// Already-closed chores are absent by construction — Unclosed reads pending
// rows — so closing one in the Mini App a minute early means no push at all,
// which is the right outcome.
func (b *Bot) sendDueChores(now, since time.Time) time.Time {
	from, to := pushWindow(now, since)
	due, err := b.cfg.Reminders.Unclosed(from, to)
	if err != nil {
		b.logger.Error("bot: due chore query", "err", err)
		return since // do not advance the mark past a window nobody saw
	}
	if len(due) == 0 {
		return now
	}
	if _, err := b.sendToGroup(choreDueText(due), tele.ModeHTML, buildNagMarkup(due)); err != nil {
		b.logger.Error("bot: send due chores", "err", err)
		return since
	}
	return now
}

// pushWindow is the stretch one push announces: everything since the previous
// tick, clamped to maxPushLookback.
//
// The lower bound is exclusive — the previous tick already announced whatever
// fell exactly on the mark — and due_at has minute resolution, so a minute is
// the step that makes it exclusive.
//
// The clamp is the flood guard, and it is the reason this is a function rather
// than two lines inside the sender: after downtime the materialiser writes up
// to BackfillWindow of past occurrences, and a push that trusted its mark
// would deliver a month of them as one message.
func pushWindow(now, since time.Time) (from, to time.Time) {
	if floor := now.Add(-maxPushLookback); since.Before(floor) {
		since = floor
	}
	return since.Add(time.Minute), now
}

// sendReminderNag lists what came due since the previous nag and nobody
// closed. It stays quiet when everything is closed — an empty "nothing to
// report" would be exactly the background noise this avoids.
func (b *Bot) sendReminderNag(now time.Time) {
	from, to := b.nagWindow(now)
	open, err := b.cfg.Reminders.Unclosed(from, to)
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

// nagWindow is the stretch one nag reports: the twenty-four hours ending at the
// most recent nag instant.
//
// Anchored on the nag time rather than on `now` so that the message and any
// later redraw of it agree about which chores belong to it. Anchored on a
// window rather than on a calendar day because a day cannot report an evening:
// the nag fires at 20:00, a chore due at 21:00 has no row yet, and tomorrow's
// nag asks about tomorrow. Every occurrence now falls in exactly one window.
func (b *Bot) nagWindow(now time.Time) (from, to time.Time) {
	to = now
	if t, err := time.ParseInLocation("15:04", b.cfg.ReminderNagTime, b.cfg.Loc); err == nil {
		to = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, b.cfg.Loc)
		if to.After(now) {
			to = to.AddDate(0, 0, -1) // today's nag has not run yet
		}
	}
	return to.AddDate(0, 0, -1).Add(time.Minute), to
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
	from, to := b.nagWindow(b.now())
	open, err := b.cfg.Reminders.Unclosed(from, to)
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

// choreDueText is the message at the moment a chore comes due. "Пора" rather
// than the nag's "не закрито": nothing has been missed yet, and a message that
// opens by telling you off for a chore you are about to do is a message you
// learn to dismiss.
func choreDueText(due []reminders.Occurrence) string {
	return "⏰ <b>Пора:</b>\n\n" + choreLines(due)
}

// reminderNagText is the evening message body. Each line carries the time the
// chore came due, because "you did not do it" is easier to act on when it says
// which one of the morning's three it means.
func reminderNagText(open []reminders.Occurrence) string {
	return "🔔 <b>Не закрито:</b>\n\n" + choreLines(open)
}

// choreLines is the shared body of both messages: same chores, same shape, so
// the two read as one feature rather than two.
func choreLines(items []reminders.Occurrence) string {
	var b strings.Builder
	for _, o := range items {
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
