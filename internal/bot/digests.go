package bot

import (
	"context"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
)

// RunDigests ticks once a minute and fires the daily and weekly digests at
// their configured wall-clock times (in cfg.Loc). It blocks until ctx is
// done; meant to run in its own goroutine alongside polling/webhook.
func (b *Bot) RunDigests(ctx context.Context) {
	if !b.cfg.NotificationsEnabled {
		b.logger.Info("bot: digests disabled (NOTIFICATIONS_ENABLED not set)")
		return
	}
	if b.cfg.NotifyChat == 0 {
		b.logger.Info("bot: digests disabled (no notify chat)")
		return
	}
	b.logger.Info("bot: digests started",
		"notify_chat", b.cfg.NotifyChat,
		"daily", b.cfg.DailyDigestTime,
		"weekly_dow", b.cfg.WeeklyDigestDOW,
		"weekly_time", b.cfg.WeeklyDigestTime)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// Dates on which each digest already fired, so a minute-resolution match
	// sends exactly once. In-memory: a restart may re-send today's digest,
	// which is preferable to silently skipping it.
	var lastDaily, lastWeekly string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := b.now()
			hm := now.Format("15:04")
			today := now.Format("2006-01-02")

			if b.cfg.DailyDigestTime != "" && hm == b.cfg.DailyDigestTime && lastDaily != today {
				b.sendDailyDigest(now)
				lastDaily = today
			}
			if b.cfg.WeeklyDigestDOW >= 0 && int(now.Weekday()) == b.cfg.WeeklyDigestDOW &&
				hm == b.cfg.WeeklyDigestTime && lastWeekly != today {
				b.sendWeeklyDigest()
				lastWeekly = today
			}
		}
	}
}

func (b *Bot) sendDailyDigest(now time.Time) {
	from := startOfDay(now)
	items, err := b.store.AppointmentsBetween(from.Format(model.LocalDatetime), from.AddDate(0, 0, 1).Format(model.LocalDatetime))
	if err != nil {
		b.logger.Error("bot: daily digest query", "err", err)
		return
	}
	if len(items) == 0 {
		return // no visits today — stay quiet rather than spam
	}
	text := "☀️ Сьогодні:\n\n" + b.formatList(items)
	if _, err := b.sendToGroup(text, tele.ModeHTML); err != nil {
		b.logger.Error("bot: send daily digest", "err", err)
	}
}

func (b *Bot) sendWeeklyDigest() {
	items, empty := b.weekItems()
	if empty {
		return // quiet week — no message
	}
	if _, err := b.sendToGroup(b.weekText(items), tele.ModeHTML); err != nil {
		b.logger.Error("bot: send weekly digest", "err", err)
	}
}

// weekItems returns upcoming visits over the next 7 days. The lower bound is
// now (not the start of today), so visits already past earlier today drop off.
func (b *Bot) weekItems() ([]model.Appointment, bool) {
	now := b.now()
	items, err := b.store.AppointmentsBetween(
		now.Format(model.LocalDatetime),
		startOfDay(now).AddDate(0, 0, 7).Format(model.LocalDatetime))
	if err != nil {
		b.logger.Error("bot: week query", "err", err)
		return nil, true
	}
	return items, len(items) == 0
}

func (b *Bot) weekText(items []model.Appointment) string {
	return "🗓 На найближчий тиждень:\n\n" + b.formatList(items)
}

// weekDigest is the text for the /week command; unlike the scheduler it always
// produces a message, even for an empty week.
func (b *Bot) weekDigest() string {
	items, empty := b.weekItems()
	if empty {
		return "На найближчий тиждень візитів немає."
	}
	return b.weekText(items)
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
