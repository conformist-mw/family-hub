package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
	"familyhub/internal/parse"
)

// cmdVisit is the explicit capture trigger — the only way to add a visit in a
// group, where free text is other people's chatter.
func (b *Bot) cmdVisit(c tele.Context) error {
	text := commandPayload(c.Text())
	if text == "" {
		return c.Send("Напиши візит після команди, наприклад:\n/visit завтра 15:00 педикюр")
	}
	return b.captureText(c, text, b.now())
}

func (b *Bot) cmdList(c tele.Context) error {
	text, markup, empty, err := b.listView(0)
	if err != nil {
		b.logger.Error("bot: list view", "err", err)
		return c.Send("Не вдалося дістати список 😕")
	}
	if empty {
		return c.Send("Майбутніх візитів немає.")
	}
	return c.Send(text, markup, tele.ModeHTML)
}

func (b *Bot) cmdWeek(c tele.Context) error {
	return c.Send(b.weekDigest(), tele.ModeHTML)
}

// onText is the capture path: parse free text, show a confirmation card.
func (b *Bot) onText(c tele.Context) error {
	text := strings.TrimSpace(c.Text())
	if text == "" || strings.HasPrefix(text, "/") {
		return nil
	}
	now := b.now()

	// If this user just tapped a field-edit button, their next message is the new
	// value for that visit (time/title/who) — handled in any chat type.
	if apptID, field, ok := b.awaiting.take(senderID(c), now); ok {
		return b.applyEdit(c, apptID, field, text, now)
	}

	// In groups the bot must not run every message through Gemini — capture is
	// explicit via /visit. Free text is a visit only in a private chat.
	if !isPrivate(c) || b.parser == nil {
		return nil
	}
	return b.captureText(c, text, now)
}

// captureText parses free text into appointments and shows a confirmation card.
func (b *Bot) captureText(c tele.Context, text string, now time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	parsed, err := b.parser.Parse(ctx, text, now)
	if err != nil {
		b.logger.Error("bot: parse", "err", err)
		return c.Send("Не вдалося розібрати текст 😕 Спробуй ще раз.")
	}
	if len(parsed) == 0 {
		return c.Send("Не знайшов візитів. Приклад: /visit 8.07 11:30 Педикюр Олежа")
	}

	// Unspecified (or self-referential) "who" defaults to the message sender —
	// the parser can't know who sent the message, so we resolve it here.
	resolvePerson(parsed, senderName(c))

	// If exactly one visit already sits at this time (single-item capture), offer
	// to update it instead of silently creating a second entry.
	var updateID int64
	if len(parsed) == 1 {
		if id, ok := b.existingAt(parsed[0].Appointment.StartsAt); ok {
			updateID = id
		}
	}

	key := b.pending.put(parsed, updateID, now)
	return c.Send(b.confirmText(parsed, updateID), b.confirmMarkup(key, len(parsed), updateID), tele.ModeHTML)
}

// existingAt returns the id of the sole active visit at startsAt, if there is
// exactly one — the candidate to update on a same-time capture. Zero/many
// matches mean "no unambiguous update", so we fall back to plain save.
func (b *Bot) existingAt(startsAt string) (int64, bool) {
	rows, err := b.store.ActiveAppointmentsAt(startsAt)
	if err != nil {
		b.logger.Warn("bot: same-time check", "err", err)
		return 0, false
	}
	if len(rows) == 1 {
		return rows[0].ID, true
	}
	return 0, false
}

func isPrivate(c tele.Context) bool {
	return c.Chat() != nil && c.Chat().Type == tele.ChatPrivate
}

// commandPayload returns everything after the leading /command token, handling
// "/add@bot rest" and multi-line "/add\nrest".
func commandPayload(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return text
	}
	i := strings.IndexAny(text, " \n\t")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(text[i+1:])
}

func (b *Bot) confirmText(parsed []parse.Parsed, updateID int64) string {
	if updateID > 0 && len(parsed) == 1 {
		return b.confirmUpdateText(parsed[0], updateID)
	}
	var sb strings.Builder
	sb.WriteString("Знайшов, зберегти?\n\n")
	for _, p := range parsed {
		line := b.formatAppt(p.Appointment)
		if p.Confidence == "low" {
			line += " ⚠️"
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// confirmUpdateText frames a same-time collision: the existing visit vs. the
// newly parsed one, with a choice to update or add as a second entry.
func (b *Bot) confirmUpdateText(p parse.Parsed, updateID int64) string {
	var sb strings.Builder
	sb.WriteString("⚠️ На цей час уже є візит:\n")
	if ex, err := b.store.GetAppointment(updateID); err == nil {
		sb.WriteString(b.formatAppt(ex))
		sb.WriteByte('\n')
	}
	sb.WriteString("\nНове:\n")
	line := b.formatAppt(p.Appointment)
	if p.Confidence == "low" {
		line += " ⚠️"
	}
	sb.WriteString(line)
	sb.WriteString("\n\nОновити наявний чи зберегти як новий?")
	return sb.String()
}

func (b *Bot) confirmMarkup(key string, n int, updateID int64) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	if updateID > 0 {
		m.Inline(
			m.Row(
				m.Data("🔄 Оновити", "appt_update", key),
				m.Data("✅ Зберегти як новий", "appt_save", key),
			),
			m.Row(m.Data("✗ Скасувати", "appt_cancel", key)),
		)
		return m
	}
	m.Inline(m.Row(
		m.Data(fmt.Sprintf("✅ Зберегти (%d)", n), "appt_save", key),
		m.Data("✗ Скасувати", "appt_cancel", key),
	))
	return m
}

// ── formatting ───────────────────────────────────────────────────────────────

var monthsShort = [...]string{
	"", "січ", "лют", "бер", "кві", "тра", "чер",
	"лип", "сер", "вер", "жов", "лис", "гру",
}

var weekdaysShort = [7]string{"нд", "пн", "вт", "ср", "чт", "пт", "сб"}

// whenLabel renders an appointment's start as "пн 8 лип, 10:30" (falls back to
// the raw stored value if it can't be parsed).
func (b *Bot) whenLabel(a model.Appointment) string {
	t, err := a.Start(b.cfg.Loc)
	if err != nil {
		return a.StartsAt
	}
	return fmt.Sprintf("%s %d %s, %02d:%02d",
		weekdaysShort[int(t.Weekday())], t.Day(), monthsShort[int(t.Month())], t.Hour(), t.Minute())
}

func (b *Bot) formatAppt(a model.Appointment) string {
	who := ""
	if a.Person != "" {
		who = " · " + a.Person
	}
	return fmt.Sprintf("📌 <b>%s</b> — %s%s", a.Title, b.whenLabel(a), who)
}

func (b *Bot) formatList(items []model.Appointment) string {
	lines := make([]string, 0, len(items))
	for _, a := range items {
		lines = append(lines, b.formatAppt(a))
	}
	return strings.Join(lines, "\n")
}

func (b *Bot) now() time.Time { return time.Now().In(b.cfg.Loc) }

// ── group mirror ─────────────────────────────────────────────────────────────

// mirrorToGroup echoes a private-chat add/update/cancel into the family group,
// so the group stays the shared source of truth even when someone captures a
// visit in a 1:1 chat with the bot. It's a no-op in the group itself (the
// action's confirmation card is already visible there, so mirroring would
// double-post) and when no notify chat is configured.
func (b *Bot) mirrorToGroup(c tele.Context, text string) {
	if !isPrivate(c) || b.cfg.NotifyChat == 0 {
		return
	}
	if _, err := b.b.Send(tele.ChatID(b.cfg.NotifyChat), text, tele.ModeHTML); err != nil {
		b.logger.Error("bot: mirror to group", "err", err)
	}
}

func (b *Bot) groupAddText(c tele.Context, items []model.Appointment) string {
	head := "🆕 Новий візит"
	if len(items) > 1 {
		head = "🆕 Нові візити"
	}
	return head + byLine(c) + ":\n\n" + b.formatList(items)
}

func (b *Bot) groupChangeText(c tele.Context, a model.Appointment, verb string) string {
	return "🔄 Візит " + verb + byLine(c) + ":\n" + b.formatAppt(a)
}

func (b *Bot) groupCancelText(c tele.Context, a model.Appointment) string {
	return "✗ Візит скасовано" + byLine(c) + ":\n" + b.formatAppt(a)
}

// byLine attributes a group notification to whoever made the change.
func byLine(c tele.Context) string {
	if who := senderName(c); who != "" {
		return " (" + who + ")"
	}
	return ""
}

// applyEdit routes a follow-up text message to the field the user chose to edit.
func (b *Bot) applyEdit(c tele.Context, apptID int64, field, text string, now time.Time) error {
	switch field {
	case "title":
		return b.applyFieldEdit(c, apptID, field, text, b.store.UpdateAppointmentTitle)
	case "who":
		return b.applyFieldEdit(c, apptID, field, text, b.store.UpdateAppointmentPerson)
	default: // time
		return b.applyReschedule(c, apptID, text, now)
	}
}

// applyReschedule parses a datetime from text and moves the appointment.
func (b *Bot) applyReschedule(c tele.Context, apptID int64, text string, now time.Time) error {
	if b.parser == nil { // no LLM configured: dates can only be edited on the web
		return c.Send("Перенесення через бота недоступне — зміни час на сторінці «Записи».")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	when, _, err := b.parser.ParseWhen(ctx, text, now)
	if err != nil {
		b.logger.Error("bot: parse when", "err", err)
		b.awaiting.set(senderID(c), apptID, "time", now) // keep it, let the user retry
		return c.Send("Не зрозумів дату. Напиши, наприклад: у п’ятницю 17:00")
	}
	if err := b.store.RescheduleAppointment(apptID, when.Format(model.LocalDatetime)); err != nil {
		b.logger.Error("bot: reschedule", "err", err, "id", apptID)
		return c.Send("Не вдалося перенести 😕")
	}
	a, err := b.store.GetAppointment(apptID)
	if err != nil {
		return c.Send("Переніс, але не зміг показати 🤔")
	}
	b.mirrorToGroup(c, b.groupChangeText(c, a, "перенесено"))
	return c.Send("✅ Перенесено:\n"+b.formatAppt(a), tele.ModeHTML)
}

// applyFieldEdit writes a free-text field (title/person) and echoes the result.
func (b *Bot) applyFieldEdit(c tele.Context, apptID int64, field, value string, update func(int64, string) error) error {
	if err := update(apptID, value); err != nil {
		b.logger.Error("bot: edit field", "err", err, "id", apptID, "field", field)
		return c.Send("Не вдалося змінити 😕")
	}
	a, err := b.store.GetAppointment(apptID)
	if err != nil {
		return c.Send("Змінив, але не зміг показати 🤔")
	}
	b.mirrorToGroup(c, b.groupChangeText(c, a, "змінено"))
	return c.Send("✅ Змінено:\n"+b.formatAppt(a), tele.ModeHTML)
}

// senderID is the message author's Telegram id (0 if unknown).
func senderID(c tele.Context) int64 {
	if u := c.Sender(); u != nil {
		return u.ID
	}
	return 0
}

// senderName is the best display name for the message author, used as the
// default "who".
func senderName(c tele.Context) string {
	u := c.Sender()
	if u == nil {
		return ""
	}
	if u.FirstName != "" {
		return u.FirstName
	}
	return u.Username
}

// resolvePerson fills an empty or self-referential person with self (the
// sender). Named people ("Олежа", "обоє") are left untouched.
func resolvePerson(parsed []parse.Parsed, self string) {
	if self == "" {
		return
	}
	for i := range parsed {
		switch strings.ToLower(strings.TrimSpace(parsed[i].Appointment.Person)) {
		case "", "я", "мене", "мне", "себе", "собі":
			parsed[i].Appointment.Person = self
		}
	}
}

// onSave persists the pending parsed items behind the tapped card.
func (b *Bot) onSave(c tele.Context) error {
	key := c.Data()
	entry, ok := b.pending.take(key)
	if !ok {
		_ = c.Respond(&tele.CallbackResponse{Text: "Картка застаріла, надішли текст ще раз", ShowAlert: true})
		return nil
	}

	items := make([]model.Appointment, 0, len(entry.parsed))
	for _, p := range entry.parsed {
		items = append(items, p.Appointment)
	}
	saved, err := b.store.CreateAppointments(items)
	if err != nil {
		b.logger.Error("bot: save appointments", "err", err, "n", len(items))
		_ = c.Respond(&tele.CallbackResponse{Text: "Не вдалося зберегти 😕", ShowAlert: true})
		return nil
	}

	var sb strings.Builder
	sb.WriteString("✅ Збережено:\n\n")
	sb.WriteString(b.formatList(saved))
	_ = c.Respond()
	b.mirrorToGroup(c, b.groupAddText(c, saved))
	return c.Edit(sb.String(), tele.ModeHTML)
}

// onUpdate applies a same-time capture onto the existing visit (title/person)
// instead of creating a second entry.
func (b *Bot) onUpdate(c tele.Context) error {
	entry, ok := b.pending.take(c.Data())
	if !ok {
		_ = c.Respond(&tele.CallbackResponse{Text: "Картка застаріла, надішли текст ще раз", ShowAlert: true})
		return nil
	}
	if entry.updateID == 0 || len(entry.parsed) == 0 {
		_ = c.Respond(&tele.CallbackResponse{Text: "Нема чого оновлювати", ShowAlert: true})
		return nil
	}
	n := entry.parsed[0].Appointment
	if err := b.store.UpdateAppointmentDetails(entry.updateID, n.Title, n.Person); err != nil {
		b.logger.Error("bot: update details", "err", err, "id", entry.updateID)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не вдалося оновити 😕", ShowAlert: true})
		return nil
	}
	a, err := b.store.GetAppointment(entry.updateID)
	if err != nil {
		_ = c.Respond()
		return c.Edit("Оновив, але не зміг показати 🤔")
	}
	_ = c.Respond()
	b.mirrorToGroup(c, b.groupChangeText(c, a, "оновлено"))
	return c.Edit("✅ Оновлено:\n"+b.formatAppt(a), tele.ModeHTML)
}

// onCancel drops the pending items and closes the card.
func (b *Bot) onCancel(c tele.Context) error {
	b.pending.take(c.Data())
	_ = c.Respond()
	return c.Edit("Скасовано.")
}
