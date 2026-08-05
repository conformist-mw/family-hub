package bot

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
)

// The /list UI is a single self-editing message. It shows one calendar week of
// visits at a time (Mon–Sun; the current week starts at today) and pages
// forward/back by week. Tapping a numbered visit morphs the same message into
// its card → edit sub-menu / cancel confirmation, and "← До списку" morphs it
// back. All navigation state lives in the callback data, so there's no
// server-side session to track.

// listView renders the week at `offset` (0 = current week). It returns the
// message text, its inline markup, and whether there are simply no future
// visits at all (offset 0 and nothing ahead) — in which case the caller shows a
// plain "no visits" line instead.
func (b *Bot) listView(offset int) (string, *tele.ReplyMarkup, bool, error) {
	if offset < 0 {
		offset = 0
	}
	now := b.now()
	winStart, winEnd := weekWindow(now, offset)
	from := winStart
	if offset == 0 {
		from = now // current week starts "now": hide days/visits already past
	}

	items, err := b.store.AppointmentsBetween(from.Format(model.LocalDatetime), winEnd.Format(model.LocalDatetime))
	if err != nil {
		return "", nil, false, err
	}
	// Is there anything past this window? Gates the "next week" button so paging
	// stops at the last week that actually holds a visit.
	later, err := b.store.UpcomingAppointments(winEnd.Format(model.LocalDatetime), 1)
	if err != nil {
		return "", nil, false, err
	}
	hasNext := len(later) > 0

	if offset == 0 && len(items) == 0 && !hasNext {
		return "", nil, true, nil // no future visits anywhere
	}

	var sb strings.Builder
	sb.WriteString("📋 Візити · ")
	sb.WriteString(weekLabel(from, winEnd))
	sb.WriteString("\n\n")
	if len(items) == 0 {
		sb.WriteString("На цьому тижні візитів немає.")
	} else {
		for i, a := range items {
			sb.WriteString(b.formatListLine(i+1, a))
			sb.WriteByte('\n')
		}
	}

	m := &tele.ReplyMarkup{}
	var numBtns []tele.Btn
	off := strconv.Itoa(offset)
	for i, a := range items {
		id := strconv.FormatInt(a.ID, 10)
		numBtns = append(numBtns, m.Data(strconv.Itoa(i+1), "lst_nav", "card:"+id+":"+off))
	}
	rows := chunkButtons(m, numBtns, 5)

	var navRow []tele.Btn
	if offset > 0 {
		navRow = append(navRow, m.Data("◀ Попер. тиждень", "lst_nav", "week:"+strconv.Itoa(offset-1)))
	}
	if hasNext {
		navRow = append(navRow, m.Data("Наст. тиждень ▶", "lst_nav", "week:"+strconv.Itoa(offset+1)))
	}
	navRow = append(navRow, m.Data("✕ Закрити", "lst_nav", "close:"+off))
	rows = append(rows, m.Row(navRow...))
	m.Inline(rows...)
	return sb.String(), m, false, nil
}

// onNav switches the single list message between views by editing it in place.
// Data is "week:<offset>", "close:<offset>", or "<view>:<id>:<offset>"
// (view ∈ card/edit/cancel).
func (b *Bot) onNav(c tele.Context) error {
	view, rest := splitData(c.Data())
	_ = c.Respond()
	switch view {
	case "week":
		offset, _ := strconv.Atoi(rest)
		return b.editToList(c, offset)
	case "close":
		offset, _ := strconv.Atoi(rest)
		return b.closeList(c, offset)
	}
	id, offset := parseIDOffset(rest)
	a, err := b.store.GetAppointment(id)
	if err != nil {
		return b.editToList(c, offset) // visit vanished — fall back to the list
	}
	private := isPrivate(c)
	switch view {
	case "edit":
		if !private { // field editing is private-only; fall back to the card
			return c.Edit(b.formatAppt(a), b.cardMarkup(a.ID, offset, false), tele.ModeHTML)
		}
		return c.Edit(b.formatAppt(a), b.editMarkup(a.ID, offset), tele.ModeHTML)
	case "cancel":
		return c.Edit("Скасувати візит?\n"+b.formatAppt(a), b.cancelMarkup(a.ID, offset), tele.ModeHTML)
	default: // card
		return c.Edit(b.formatAppt(a), b.cardMarkup(a.ID, offset, private), tele.ModeHTML)
	}
}

// onArm handles the "change one field" taps: it remembers which field of which
// visit the sender is editing and prompts them for the new value. Their next
// text message is applied in onText. Data is "<field>:<id>" (field ∈ time/title/who).
func (b *Bot) onArm(c tele.Context) error {
	// Field edits take the sender's next text message as the new value. In a
	// group any member (or bot) can post at any moment and would be captured, so
	// arming is restricted to private chats.
	if !isPrivate(c) {
		return c.Respond(&tele.CallbackResponse{Text: "Правки візитів — у приватці зі мною 🙏", ShowAlert: true})
	}
	field, arg := splitData(c.Data())
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		_ = c.Respond()
		return nil
	}
	a, err := b.store.GetAppointment(id)
	if err != nil {
		_ = c.Respond()
		return b.editToList(c, 0)
	}
	b.awaiting.set(senderID(c), id, field, b.now())
	_ = c.Respond()
	return c.Send(armPrompt(field, a))
}

// onDel soft-cancels the visit behind a confirmed "Так, скасувати" tap.
func (b *Bot) onDel(c tele.Context) error {
	id, err := strconv.ParseInt(c.Data(), 10, 64)
	if err != nil {
		_ = c.Respond()
		return nil
	}
	a, err := b.store.GetAppointment(id)
	if err != nil {
		_ = c.Respond()
		return b.editToList(c, 0)
	}
	if err := b.store.SetAppointmentStatus(id, model.ApptStatusCancelled); err != nil {
		b.logger.Error("bot: cancel appointment", "err", err, "id", id)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не вдалося скасувати 😕", ShowAlert: true})
		return nil
	}
	_ = c.Respond()
	b.mirrorToGroup(c, b.groupCancelText(c, a))
	return c.Edit("✗ Скасовано: "+b.formatAppt(a), tele.ModeHTML)
}

// closeList ends the interaction: the week stays readable as a plain message,
// the keyboard goes. Without this every /list ever sent keeps offering buttons
// that act on whatever their callback data points at.
func (b *Bot) closeList(c tele.Context, offset int) error {
	text, _, empty, err := b.listView(offset)
	if err != nil || empty {
		return c.Edit("Список закрито.", &tele.ReplyMarkup{})
	}
	return c.Edit(text, &tele.ReplyMarkup{}, tele.ModeHTML)
}

func (b *Bot) editToList(c tele.Context, offset int) error {
	text, markup, empty, err := b.listView(offset)
	if err != nil {
		b.logger.Error("bot: list view", "err", err)
		return c.Edit("Не вдалося дістати список 😕")
	}
	if empty {
		return c.Edit("Майбутніх візитів немає.")
	}
	return c.Edit(text, markup, tele.ModeHTML)
}

// ── markup ────────────────────────────────────────────────────────────────────

func (b *Bot) cardMarkup(id int64, offset int, private bool) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	ids := strconv.FormatInt(id, 10)
	ref := ids + ":" + strconv.Itoa(offset)
	back := m.Row(
		m.Data("← До списку", "lst_nav", "week:"+strconv.Itoa(offset)),
		m.Data("✕ Закрити", "lst_nav", "close:"+strconv.Itoa(offset)),
	)
	if !private {
		// In groups, the text-driven edits (Перенести/Изменить) are disabled —
		// only the button-only cancel is safe from stray messages being captured.
		m.Inline(
			m.Row(m.Data("✗ Скасувати", "lst_nav", "cancel:"+ref)),
			back,
		)
		return m
	}
	m.Inline(
		m.Row(
			m.Data("→ Перенести", "lst_arm", "time:"+ids),
			m.Data("✎ Змінити", "lst_nav", "edit:"+ref),
			m.Data("✗ Скасувати", "lst_nav", "cancel:"+ref),
		),
		back,
	)
	return m
}

func (b *Bot) editMarkup(id int64, offset int) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	ids := strconv.FormatInt(id, 10)
	m.Inline(
		m.Row(
			m.Data("🕑 Час", "lst_arm", "time:"+ids),
			m.Data("✏️ Назва", "lst_arm", "title:"+ids),
			m.Data("👤 Хто", "lst_arm", "who:"+ids),
		),
		m.Row(m.Data("← Назад", "lst_nav", "card:"+ids+":"+strconv.Itoa(offset))),
	)
	return m
}

func (b *Bot) cancelMarkup(id int64, offset int) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	ids := strconv.FormatInt(id, 10)
	m.Inline(m.Row(
		m.Data("✗ Так, скасувати", "lst_del", ids),
		m.Data("← Назад", "lst_nav", "card:"+ids+":"+strconv.Itoa(offset)),
	))
	return m
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (b *Bot) formatListLine(n int, a model.Appointment) string {
	who := ""
	if a.Person != "" {
		who = " · " + a.Person
	}
	return fmt.Sprintf("%d. %s — <b>%s</b>%s%s", n, b.whenLabel(a), a.Title, who, costSuffix(a))
}

func armPrompt(field string, a model.Appointment) string {
	switch field {
	case "title":
		return fmt.Sprintf("Змінити назву «%s». Напиши нову назву.", a.Title)
	case "who":
		cur := a.Person
		if cur == "" {
			cur = "не вказано"
		}
		return fmt.Sprintf("Для кого «%s» (зараз: %s)? Напиши ім’я.", a.Title, cur)
	default: // time
		return fmt.Sprintf("Перенесення «%s». Напиши нову дату й час — наприклад: у п’ятницю 17:00", a.Title)
	}
}

// weekWindow returns [start, end) for the calendar week (Mon–Mon) at `offset`
// weeks from the week containing `now`.
func weekWindow(now time.Time, offset int) (time.Time, time.Time) {
	daysSinceMon := (int(now.Weekday()) + 6) % 7 // Go: Sun=0..Sat=6 → Mon=0
	monday := startOfDay(now).AddDate(0, 0, -daysSinceMon)
	start := monday.AddDate(0, 0, 7*offset)
	return start, start.AddDate(0, 0, 7)
}

// weekLabel is a compact "8–13 лип" / "28 лип – 3 сер" range for the header.
func weekLabel(from, winEnd time.Time) string {
	last := winEnd.AddDate(0, 0, -1)
	if from.Month() == last.Month() {
		return fmt.Sprintf("%d–%d %s", from.Day(), last.Day(), monthsShort[int(last.Month())])
	}
	return fmt.Sprintf("%d %s – %d %s",
		from.Day(), monthsShort[int(from.Month())], last.Day(), monthsShort[int(last.Month())])
}

func chunkButtons(m *tele.ReplyMarkup, btns []tele.Btn, per int) []tele.Row {
	var rows []tele.Row
	for i := 0; i < len(btns); i += per {
		end := i + per
		if end > len(btns) {
			end = len(btns)
		}
		rows = append(rows, m.Row(btns[i:end]...))
	}
	return rows
}

func splitData(s string) (string, string) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func parseIDOffset(rest string) (int64, int) {
	idStr, offStr := splitData(rest)
	id, _ := strconv.ParseInt(idStr, 10, 64)
	offset, _ := strconv.Atoi(offStr)
	return id, offset
}
