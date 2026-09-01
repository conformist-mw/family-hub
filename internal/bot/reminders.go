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
//
// The mark only moves when something was said; see markAfterPass for why an
// empty pass must leave it alone.
func (b *Bot) sendDueChores(now, since time.Time) time.Time {
	from, to := pushWindow(now, since)
	due, err := b.cfg.Reminders.Unclosed(from, to)
	if err != nil {
		b.logger.Error("bot: due chore query", "err", err)
		return since // do not advance the mark past a window nobody saw
	}
	if len(due) == 0 {
		return markAfterPass(now, since, 0)
	}
	if _, err := b.sendToGroup(choreDueText(due), tele.ModeHTML, buildNagMarkup(due)); err != nil {
		b.logger.Error("bot: send due chores", "err", err)
		return since
	}
	return markAfterPass(now, since, len(due))
}

// markAfterPass is where the push mark lands after one pass, and the whole of
// the rule that a minute is only finished once something was actually said
// about it.
//
// A pass that found nothing has not covered its window — it has only found it
// empty *so far*. The push and the materialiser are separate tickers over the
// same minute, and nothing orders them: the materialiser builds its ticker
// after an opening backfill pass, so its phase drifts from the push's by
// however long that took. When the push queries first, the row for 08:00 does
// not exist yet.
//
// Advancing the mark there loses the chore permanently, because the next
// window starts at 08:01 and due_at never moves. So the mark stays put and the
// window grows until something is found — bounded by maxPushLookback, which is
// what keeps a stale mark from turning into a flood.
//
// Once a pass has sent, the mark moves to now and every minute up to it is
// closed for good, which is what stops an unclosed chore being re-announced
// on each of the following ticks.
func markAfterPass(now, since time.Time, found int) time.Time {
	if found == 0 {
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
	for i, o := range open {
		if o.Closed() {
			// Answered: its line already shows the ✓ or the ✗, and a button
			// that only ever answers "вже закрито" is a button that lies about
			// being live. Changing a wrong answer moves to the Mini App and
			// the web, which is where the rest of the correcting happens.
			continue
		}
		id := strconv.FormatInt(o.ReminderID, 10)
		due := o.Due.Format(model.LocalDatetime)
		// i and len(open) are the chore's place in the LIST, not among the
		// buttons still standing: once one is answered its neighbours must
		// keep the numbers their lines show.
		done, skip := choreButtonLabels(i, len(open))
		rows = append(rows, m.Row(
			m.Data(done, choreCallbackUnique, id, due, model.OccDone),
			m.Data(skip, choreCallbackUnique, id, due, model.OccSkipped),
		))
	}
	m.Inline(rows...)
	return m
}

// choreButtonLabels keeps a button's meaning whole. The label used to carry the
// chore's title, truncated to fit — which on a real title ("Проверить
// начисления коммунальных в банке") left an ellipsis where the meaning was.
//
// A lone message needs no title on the button: there is one chore, and the
// text above says which. Several need telling apart, so the line is numbered
// and the button carries the number. Either way the label is short, fixed and
// never cut.
func choreButtonLabels(i, total int) (done, skip string) {
	if total == 1 {
		return "✓ Зроблено", "✗ Не треба"
	}
	n := strconv.Itoa(i + 1)
	return n + " ✓", n + " ✗"
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
		return b.redrawNag(c)
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
	return b.redrawNag(c)
}

// redrawNag re-renders the message that was tapped, with the same chores it
// always listed and each line now showing its answer.
//
// It used to rebuild the list from the nag window instead, which meant the
// text changed into something else entirely: tap a button on the 11:00 "Пора"
// message and it became the evening list, or "Сьогодні все закрито" when
// nothing else was open. The reader lost the one thing they had opened the
// message to see.
//
// The chores are recovered from the message's own keyboard rather than from a
// window or from parsing the prose. Every button already carries its chore's
// id and due_at, so the message knows exactly what it is about — including a
// message days old, whose window has long since moved on.
func (b *Bot) redrawNag(c tele.Context) error {
	msg := c.Message()
	if msg == nil {
		return nil
	}
	refs := choreRefsFromMarkup(msg.ReplyMarkup)
	if len(refs) == 0 {
		return nil // nothing to redraw against; the toast already answered
	}
	items := make([]reminders.Occurrence, 0, len(refs))
	for _, ref := range refs {
		row, err := b.store.GetOccurrence(ref.id, ref.dueAt)
		if err != nil {
			b.logger.Error("bot: redraw chore", "reminder_id", ref.id, "due_at", ref.dueAt, "err", err)
			return nil // a partial redraw would silently drop a line
		}
		due, err := row.Due(b.cfg.Loc)
		if err != nil {
			b.logger.Error("bot: redraw chore due_at", "value", row.DueAt, "err", err)
			return nil
		}
		items = append(items, reminders.Occurrence{
			ReminderID: row.ReminderID, Title: row.Title, Person: row.Person,
			Due: due, Status: row.Status, Stored: true,
		})
	}
	// withAppButton so a redraw keeps the row the message was sent with; it is
	// the only thing left on the keyboard once every chore has been answered.
	opts := b.withAppButton([]any{tele.ModeHTML, buildNagMarkup(items)})
	return c.Edit(choreHeaderOf(msg.Text)+choreLines(items), opts...)
}

// choreRef is one chore as a message's keyboard remembers it.
type choreRef struct {
	id    int64
	dueAt string
}

// choreRefsFromMarkup reads back the chores a message lists, in the order it
// lists them, from the callback data its buttons carry.
//
// Telegram echoes callback_data verbatim, so the buttons still hold what
// buildNagMarkup wrote: "\frem_chore|<id>|<due_at>|<status>". telebot only
// splits that apart for the callback being handled, not for the markup hanging
// off the message, so the prefix is stripped here.
//
// Each chore owns a row of two buttons; taking the first of each keeps one
// entry per chore without deduplicating.
func choreRefsFromMarkup(m *tele.ReplyMarkup) []choreRef {
	if m == nil {
		return nil
	}
	var out []choreRef
	for _, row := range m.InlineKeyboard {
		for _, btn := range row {
			data, ok := choreCallbackPayload(btn)
			if !ok {
				continue // not one of ours: the "open the app" button
			}
			parts := strings.Split(data, "|")
			if len(parts) != 3 {
				continue
			}
			id, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				continue
			}
			out = append(out, choreRef{id: id, dueAt: parts[1]})
			break // one entry per row, not one per button
		}
	}
	return out
}

// choreCallbackPayload returns a button's "<id>|<due_at>|<status>", and
// whether the button is one of ours at all.
//
// Two shapes, because a keyboard is read in two directions. One we built
// carries Unique separately and Data bare — telebot only fuses them into
// "\f<unique>|<data>" as it sends. One that came back from Telegram has only
// callback_data, so it arrives fused with Unique empty.
func choreCallbackPayload(btn tele.InlineButton) (string, bool) {
	if btn.Unique == choreCallbackUnique {
		return btn.Data, true
	}
	const prefix = "\f" + choreCallbackUnique + "|"
	if rest, found := strings.CutPrefix(btn.Data, prefix); found {
		return rest, true
	}
	return "", false
}

// choreHeaderOf keeps the message's own opening line. "Пора" and "не закрито"
// are different sentences about different moments, and a redraw has no
// business turning one into the other.
func choreHeaderOf(text string) string {
	if strings.HasPrefix(text, dueHeaderPlain) {
		return dueHeader
	}
	return nagHeader
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
	return dueHeader + choreLines(due)
}

// reminderNagText is the evening message body. Each line carries the time the
// chore came due, because "you did not do it" is easier to act on when it says
// which one of the morning's three it means.
func reminderNagText(open []reminders.Occurrence) string {
	return nagHeader + choreLines(open)
}

const (
	dueHeader = "⏰ <b>Пора:</b>\n\n"
	nagHeader = "🔔 <b>Не закрито:</b>\n\n"
	// dueHeaderPlain is how the due header reaches us back: Message.Text is
	// the rendered text, with the HTML markup gone.
	dueHeaderPlain = "⏰ Пора:"
	// choreCallbackUnique is the endpoint name buildNagMarkup writes and
	// choreRefsFromMarkup reads back.
	choreCallbackUnique = "rem_chore"
)

// choreLines is the shared body of both messages: same chores, same shape, so
// the two read as one feature rather than two.
//
// The bullet is the answer. A closed chore keeps its line and gains a ✓ or a ✗
// where the dot was, so the message still says what it was about after it has
// been answered — the whole text used to be replaced by a summary, which threw
// away the one thing the reader had come back to check.
//
// Numbering appears only when there is more than one chore, because that is
// the only time a button needs to say which line it belongs to.
func choreLines(items []reminders.Occurrence) string {
	var b strings.Builder
	for i, o := range items {
		if len(items) > 1 {
			b.WriteString(strconv.Itoa(i + 1))
			b.WriteString(" ")
		}
		b.WriteString(choreMark(o.Status))
		b.WriteString(" ")
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

// choreMark is the same glyph set the web calendar uses, so a chore reads the
// same wherever it is seen.
func choreMark(status string) string {
	switch status {
	case model.OccDone:
		return "✓"
	case model.OccSkipped:
		return "✗"
	}
	return "•"
}
