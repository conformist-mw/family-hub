package bot

import (
	"context"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
)

// Recording what a one-off visit cost is a two-step conversation: some time
// after the appointment started, the bot asks in the notify chat, and the
// amount arrives as a *reply* to that question.
//
// Reply — not a button-armed "your next message is the value" flow like field
// edits use. Those are private-chat only, because in a group the next message
// can be anyone's about anything. A reply names its target message explicitly,
// and Telegram delivers replies to the bot's own messages even in groups with
// privacy mode on, so the group (where the family actually talks) keeps working.
//
// The link between prompt and appointment lives in the DB
// (appointments.cost_prompt_msg_id), not in memory: deploys are frequent, and
// the prompt message stays in the chat long after the process that sent it.

// costPromptLookback bounds how far back the prompt sweep reaches. Without it
// the first tick after this feature ships would ask about every appointment in
// the history; with it, only genuinely recent visits get a question.
const costPromptLookback = 24 * time.Hour

// RunCostPrompts ticks once a minute and asks for the cost of appointments
// whose start is at least CostPromptDelayMin minutes in the past. It blocks
// until ctx is done; meant to run in its own goroutine.
func (b *Bot) RunCostPrompts(ctx context.Context) {
	if b.cfg.NotifyChat == 0 || b.cfg.CostPromptDelayMin < 0 {
		b.logger.Info("bot: cost prompts disabled",
			"notify_chat", b.cfg.NotifyChat,
			"cost_prompt_delay_min", b.cfg.CostPromptDelayMin)
		return
	}
	b.logger.Info("bot: cost prompts started",
		"notify_chat", b.cfg.NotifyChat,
		"cost_prompt_delay_min", b.cfg.CostPromptDelayMin)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.sendDueCostPrompts(b.now())
		}
	}
}

func (b *Bot) sendDueCostPrompts(now time.Time) {
	until := now.Add(-time.Duration(b.cfg.CostPromptDelayMin) * time.Minute)
	notBefore := now.Add(-costPromptLookback)

	items, err := b.store.AppointmentsAwaitingCost(
		notBefore.Format(model.LocalDatetime), until.Format(model.LocalDatetime))
	if err != nil {
		b.logger.Error("bot: cost prompt query", "err", err)
		return
	}
	for _, a := range items {
		msg, err := b.b.Send(tele.ChatID(b.cfg.NotifyChat),
			costPromptText(b.formatAppt(a)), costPromptMarkup(a.ID), tele.ModeHTML)
		if err != nil {
			b.logger.Error("bot: send cost prompt", "err", err, "id", a.ID)
			continue // no msg id stored — the next tick tries again
		}
		// Store the message id before anything else can happen to it: this is
		// also the "already asked" flag, so a failure here would mean asking
		// again next minute.
		if err := b.store.SetAppointmentCostPrompt(a.ID, int64(msg.ID)); err != nil {
			b.logger.Error("bot: store cost prompt id", "err", err, "id", a.ID)
		}
	}
}

func costPromptText(appt string) string {
	return appt + "\n\n💸 Скільки коштувало? Відповідай на це повідомлення сумою (напр. 800)."
}

func costPromptMarkup(apptID int64) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data("✗ Без суми", "appt_cost", "skip:"+strconv.FormatInt(apptID, 10))))
	return m
}

// onCostSkip closes the prompt without an amount. cost stays NULL — "nobody
// wrote it down" and "it was free" are different things, and 0 is reserved for
// the latter.
func (b *Bot) onCostSkip(c tele.Context) error {
	_, arg := splitData(c.Data())
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		_ = c.Respond()
		return nil
	}
	_ = c.Respond()
	a, err := b.store.GetAppointment(id)
	if err != nil {
		return c.Edit("Запис не знайдено.", &tele.ReplyMarkup{})
	}
	return c.Edit(b.formatAppt(a)+"\n\nБез суми.", &tele.ReplyMarkup{}, tele.ModeHTML)
}

// costReply handles a reply to a cost prompt. It reports whether the message
// was one: anything else falls through to the normal text handling.
func (b *Bot) costReply(c tele.Context) (bool, error) {
	msg := c.Message()
	if msg == nil || msg.ReplyTo == nil {
		return false, nil
	}
	a, err := b.store.AppointmentByCostPrompt(int64(msg.ReplyTo.ID))
	if err != nil {
		return false, nil // a reply to some other message of ours
	}

	amount, ok := parseAmount(msg.Text)
	if !ok {
		return true, c.Reply("Не зрозумів суму. Напиши просто число, напр. 800 (або 0, якщо безкоштовно).")
	}
	if err := b.store.SetAppointmentCost(a.ID, amount); err != nil {
		b.logger.Error("bot: set appointment cost", "err", err, "id", a.ID)
		return true, c.Reply("Не вдалося зберегти 😕")
	}

	// Fold the amount into the prompt itself and drop its button, so the
	// question visibly stops being open.
	a.Cost = &amount
	if _, err := b.b.Edit(
		tele.StoredMessage{MessageID: strconv.Itoa(msg.ReplyTo.ID), ChatID: msg.Chat.ID},
		b.formatAppt(a), &tele.ReplyMarkup{}, tele.ModeHTML,
	); err != nil {
		b.logger.Warn("bot: edit cost prompt", "err", err, "id", a.ID)
	}
	return true, c.Reply("✅ Записав " + money(amount))
}

// parseAmount reads the shapes a person actually types: "800", "800 грн",
// "1 200", "1200,50". Negative amounts are rejected; 0 is allowed and means
// free.
func parseAmount(s string) (float64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, suffix := range []string{"грн", "₴", "uah", "гривень", "гривні"} {
		s = strings.TrimSpace(strings.TrimSuffix(s, suffix))
	}
	s = strings.NewReplacer(" ", "", " ", "", ",", ".").Replace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}
