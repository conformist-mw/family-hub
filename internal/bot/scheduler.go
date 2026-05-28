package bot

import (
	"context"
	"fmt"
	"time"

	tele "gopkg.in/telebot.v3"

	"lessons/internal/model"
)

// RunScheduler ticks once a minute and fires the evening reminder when its
// configured hour rolls around for the day. It blocks until ctx is done; meant
// to run in its own goroutine alongside polling/webhook.
func (b *Bot) RunScheduler(ctx context.Context) {
	if b.cfg.NotifyChat == 0 || b.cfg.ReminderHour < 0 || b.cfg.ReminderHour > 23 {
		b.logger.Info("bot: scheduler disabled",
			"notify_chat", b.cfg.NotifyChat,
			"reminder_hour", b.cfg.ReminderHour)
		return
	}
	b.logger.Info("bot: scheduler started",
		"notify_chat", b.cfg.NotifyChat,
		"reminder_hour", b.cfg.ReminderHour)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	var lastEveningSent string // YYYY-MM-DD of the last day reminders were sent
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			today := now.Format("2006-01-02")
			if lastEveningSent != today && now.Hour() >= b.cfg.ReminderHour {
				b.sendEveningReminders(now)
				lastEveningSent = today
			}
		}
	}
}

func (b *Bot) sendEveningReminders(now time.Time) {
	weekday := int(now.Weekday())
	today := now.Format("2006-01-02")
	slots, err := b.store.SlotsForWeekday(weekday)
	if err != nil {
		b.logger.Error("bot: reminders query", "err", err)
		return
	}
	if len(slots) == 0 {
		b.logger.Info("bot: no slots for today", "weekday", weekday)
		return
	}

	// Dedupe by enrollment — if multiple slots in one day for the same
	// enrollment, send one reminder.
	seen := make(map[int64]bool)
	for _, sl := range slots {
		eid := sl.Enrollment.ID
		if seen[eid] {
			continue
		}
		seen[eid] = true

		exists, err := b.store.VisitExistsForDate(eid, today)
		if err != nil {
			b.logger.Error("bot: visit-exists check", "err", err, "eid", eid)
			continue
		}
		if exists {
			b.logger.Info("bot: reminder skipped (already recorded)", "eid", eid, "date", today)
			continue
		}
		b.sendReminderFor(sl.Enrollment, sl.Slot, today)
	}
}

func (b *Bot) sendReminderFor(e model.Enrollment, sl model.Slot, date string) {
	label := e.Name
	if e.Description != "" {
		label = e.Name + " (" + e.Description + ")"
	}
	text := fmt.Sprintf("%s · %s в %s — было?", e.Person, label, sl.Time)

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
		m.Data("✗ Отменили", "rem_visit", prefix+":"+model.StatusCancelled),
		m.Data("⤵ Пропустили", "rem_visit", prefix+":"+model.StatusSkipped),
	)
	m.Inline(row1, row2)
	return m
}
