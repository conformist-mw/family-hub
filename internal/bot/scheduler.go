package bot

import (
	"context"
	"fmt"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// RunScheduler ticks once a minute and sends a reminder for each of today's
// slots once the slot time plus the configured delay has passed. It blocks
// until ctx is done; meant to run in its own goroutine alongside
// polling/webhook.
func (b *Bot) RunScheduler(ctx context.Context) {
	if b.cfg.NotifyChat == 0 || (b.cfg.ReminderDelayMin < 0 && b.cfg.PreLessonLeadMin < 0) {
		b.logger.Info("bot: scheduler disabled",
			"notify_chat", b.cfg.NotifyChat,
			"reminder_delay_min", b.cfg.ReminderDelayMin,
			"pre_lesson_lead_min", b.cfg.PreLessonLeadMin)
		return
	}
	b.logger.Info("bot: scheduler started",
		"notify_chat", b.cfg.NotifyChat,
		"reminder_delay_min", b.cfg.ReminderDelayMin,
		"pre_lesson_lead_min", b.cfg.PreLessonLeadMin)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// Enrollments already reminded/warned today; reset at midnight. The state
	// is in-memory only — after a restart, unanswered reminders for slots that
	// are already due fire again, which is the catch-up we want. Pre-lesson
	// warnings don't catch up: past the slot time they are just noise.
	reminded := make(map[int64]bool)
	warned := make(map[int64]bool)
	var remindedDate string
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			today := now.Format("2006-01-02")
			if remindedDate != today {
				reminded = make(map[int64]bool)
				warned = make(map[int64]bool)
				remindedDate = today
			}
			b.sendDueReminders(now, reminded, warned)
		}
	}
}

// sendDueReminders sends one reminder per enrollment for today's slots whose
// time plus the configured delay has passed and that have no visit recorded
// yet. Multiple slots per enrollment in one day collapse into a single
// reminder after the earliest due slot — the visit model is one record per
// enrollment per date anyway.
func (b *Bot) sendDueReminders(now time.Time, reminded, warned map[int64]bool) {
	weekday := int(now.Weekday())
	today := now.Format("2006-01-02")
	slots, err := b.store.SlotsForWeekday(weekday, today)
	if err != nil {
		b.logger.Error("bot: reminders query", "err", err)
		return
	}

	for _, sl := range slots {
		if b.cfg.PreLessonLeadMin >= 0 {
			b.warnEmptyBalance(now, sl, today, warned)
		}
		if b.cfg.ReminderDelayMin >= 0 {
			b.remindSlot(now, sl, today, reminded)
		}
	}
}

func (b *Bot) remindSlot(now time.Time, sl store.SlotWithEnrollment, today string, reminded map[int64]bool) {
	eid := sl.Enrollment.ID
	if reminded[eid] {
		return
	}
	due, err := slotDue(now, sl.Slot.Time, b.cfg.ReminderDelayMin)
	if err != nil {
		b.logger.Error("bot: bad slot time", "err", err, "slot", sl.Slot.ID, "time", sl.Slot.Time)
		return
	}
	if now.Before(due) {
		return
	}

	exists, err := b.store.VisitExistsForDate(eid, today)
	if err != nil {
		b.logger.Error("bot: visit-exists check", "err", err, "eid", eid)
		return
	}
	if exists {
		reminded[eid] = true
		return
	}
	b.sendReminderFor(sl.Enrollment, sl.Slot, today)
	reminded[eid] = true
}

// warnEmptyBalance sends one informational message per enrollment per day
// when a lesson is coming up and there is nothing paid to cover it: zero or
// negative remaining lessons (per-lesson) or no pass covering today
// (monthly). It fires only inside the [slot−lead, slot) window — once the
// lesson has started, the warning is just noise — and carries no buttons:
// the post-slot reminder owns the marking flow.
func (b *Bot) warnEmptyBalance(now time.Time, sl store.SlotWithEnrollment, today string, warned map[int64]bool) {
	eid := sl.Enrollment.ID
	if warned[eid] {
		return
	}
	start, err := slotTimeToday(now, sl.Slot.Time)
	if err != nil {
		b.logger.Error("bot: bad slot time", "err", err, "slot", sl.Slot.ID, "time", sl.Slot.Time)
		return
	}
	warnFrom := start.Add(-time.Duration(b.cfg.PreLessonLeadMin) * time.Minute)
	if now.Before(warnFrom) || !now.Before(start) {
		return
	}

	// Already marked (e.g. cancelled in advance) — nothing to warn about.
	exists, err := b.store.VisitExistsForDate(eid, today)
	if err != nil {
		b.logger.Error("bot: visit-exists check", "err", err, "eid", eid)
		return
	}
	if exists {
		warned[eid] = true
		return
	}

	bal, err := b.store.BalanceFor(eid)
	if err != nil {
		b.logger.Error("bot: balance for warning", "err", err, "eid", eid)
		return
	}
	// A non-empty balance is rechecked on every tick of the window: marking
	// a backlogged visit may drain it to zero before the lesson starts.
	if bal.State() != "empty" {
		return
	}

	e := sl.Enrollment
	text := fmt.Sprintf("🔴 %s · %s сьогодні у %s: %s", e.Person, e.Name, sl.Slot.Time, emptyBalanceText(bal))
	if _, err := b.b.Send(tele.ChatID(b.cfg.NotifyChat), text); err != nil {
		b.logger.Error("bot: send balance warning", "err", err, "eid", eid)
		return
	}
	warned[eid] = true
}

func emptyBalanceText(bal model.Balance) string {
	if bal.BillingType == model.BillingMonthly {
		return "немає активного абонемента"
	}
	return "немає оплачених занять"
}

// slotDue computes when the reminder for a "HH:MM" slot becomes due on the
// day of now. A delay that would push the reminder past midnight is clamped
// to 23:59 — the scheduler only looks at today's slots, so a reminder must
// fire on the same day as its slot or not at all.
func slotDue(now time.Time, hhmm string, delayMin int) (time.Time, error) {
	start, err := slotTimeToday(now, hhmm)
	if err != nil {
		return time.Time{}, err
	}
	due := start.Add(time.Duration(delayMin) * time.Minute)
	endOfDay := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 0, 0, now.Location())
	if due.After(endOfDay) {
		due = endOfDay
	}
	return due, nil
}

// slotTimeToday places a "HH:MM" slot time on the calendar day of now.
func slotTimeToday(now time.Time, hhmm string) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location()), nil
}

func (b *Bot) sendReminderFor(e model.Enrollment, sl model.Slot, date string) {
	text := fmt.Sprintf("%s · %s у %s — було?", e.Person, e.Name, sl.Time)

	m := buildReminderMarkup(e.ID, date)
	if _, err := b.b.Send(tele.ChatID(b.cfg.NotifyChat), text, m); err != nil {
		b.logger.Error("bot: send reminder", "err", err, "eid", e.ID)
	}
}

func buildReminderMarkup(enrollmentID int64, date string) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	prefix := fmt.Sprintf("%d:%s", enrollmentID, date)
	row1 := m.Row(
		m.Data("✓ Провели", "rem_visit", prefix+":"+model.StatusDone),
		m.Data("→ Перенесли", "rem_visit", prefix+":"+model.StatusRescheduled),
	)
	row2 := m.Row(
		m.Data("✗ Скасували", "rem_visit", prefix+":"+model.StatusCancelled),
		m.Data("⤵ Пропустили", "rem_visit", prefix+":"+model.StatusSkipped),
	)
	m.Inline(row1, row2)
	return m
}
