package bot

import (
	"context"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
)

// RunDigests ticks once a minute and fires the wall-clock messages (in
// cfg.Loc): the daily and weekly appointment digests, the evening list of
// recurring chores nobody closed, tomorrow's school timetable, the Friday
// review of the school week just gone, the morning menu, the evening check of
// what was actually eaten, and the weekly suggestion of new dishes. It blocks
// until ctx is done; meant to run in its own goroutine alongside
// polling/webhook.
//
// They have separate gates on purpose. The appointment digests are off in
// prod because Home Assistant sends those summaries from the ICS feed; the
// chore nag is not something HA can send, since HA reads a calendar and knows
// nothing about what was closed, and neither is the school digest, since HA's
// calendar API drops the category that separates a lesson from after-school
// care. Gating either on the same flag would have left it permanently silent
// in the one place it matters. The menu messages are the bot's own for a
// plainer reason: their buttons are answered by the bot, not by HA.
func (b *Bot) RunDigests(ctx context.Context) {
	if b.cfg.NotifyChat == 0 {
		b.logger.Info("bot: digests disabled (no notify chat)")
		return
	}
	if !b.cfg.anyDigestEnabled() {
		b.logger.Info("bot: digests disabled (NOTIFICATIONS_ENABLED not set, no reminders)")
		return
	}
	pushOn := b.cfg.reminderPushEnabled()
	b.logger.Info("bot: digests started",
		"notify_chat", b.cfg.NotifyChat,
		"appointment_digests", b.cfg.appointmentDigestsEnabled(),
		"daily", b.cfg.DailyDigestTime,
		"weekly_dow", b.cfg.WeeklyDigestDOW,
		"weekly_time", b.cfg.WeeklyDigestTime,
		"reminder_nag", b.cfg.ReminderNagTime,
		"reminder_push", pushOn,
		"school_digest", b.cfg.SchoolDigestTime,
		"school_week_review_dow", b.cfg.SchoolWeekReviewDOW,
		"school_week_review_time", b.cfg.SchoolWeekReviewTime,
		"menu", b.cfg.menuEnabled(),
		"menu_time", b.cfg.MenuTime,
		"menu_evening", b.cfg.menuEveningEnabled(),
		"menu_evening_time", b.cfg.MenuEveningTime,
		"dish_suggest", b.cfg.dishSuggestEnabled(),
		"dish_suggest_dow", b.cfg.DishSuggestDOW,
		"dish_suggest_time", b.cfg.DishSuggestTime)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// Dates on which each digest already fired, so a minute-resolution match
	// sends exactly once. In-memory: a restart may re-send today's digest,
	// which is preferable to silently skipping it.
	var last lastFired
	// The due-time push has no wall-clock time of its own — it fires whenever
	// something comes due — so it carries an instant rather than a date. Set
	// to boot time: a restart announces nothing from before it, which is what
	// stops a catch-up backfill arriving as a message.
	lastPush := b.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := b.now()
			today := now.Format("2006-01-02")
			d := b.cfg.dueThisMinute(now, last)

			if d.daily {
				b.sendDailyDigest(now)
				last.daily = today
			}
			if d.weekly {
				b.sendWeeklyDigest()
				last.weekly = today
			}
			if d.nag {
				b.sendReminderNag(now)
				last.nag = today
			}
			if d.school {
				b.sendSchoolDigest(now)
				last.school = today
			}
			if d.review {
				b.sendSchoolWeekReview(ctx, now)
				last.review = today
			}
			if d.menu {
				b.sendMenu(now)
				last.menu = today
			}
			if d.evening {
				b.sendEveningCheck(now)
				last.evening = today
			}
			if d.suggest {
				b.sendDishSuggestions(ctx, now)
				last.suggest = today
			}
			if pushOn {
				lastPush = b.sendDueChores(now, lastPush)
			}
		}
	}
}

// anyDigestEnabled is RunDigests' early return, split out so a test can reach
// it. Every new clock has to be listed here as well as in dueThisMinute:
// forgetting it passes every dueThisMinute test and leaves a deploy that turns
// on only that clock with a ticker that never starts.
func (c Config) anyDigestEnabled() bool {
	return c.appointmentDigestsEnabled() || c.reminderNagEnabled() ||
		c.reminderPushEnabled() || c.schoolDigestEnabled() ||
		c.schoolWeekReviewEnabled() || c.menuEnabled() ||
		c.menuEveningEnabled() || c.dishSuggestEnabled()
}

// appointmentDigestsEnabled and reminderNagEnabled are separate because the
// two answer to different owners. Home Assistant sends the appointment
// summaries in prod, which is why NOTIFICATIONS_ENABLED is off there; it
// cannot send the chore nag, because it reads a calendar and knows nothing
// about what was closed. One shared flag would have left the nag permanently
// silent in the only place it matters.
func (c Config) appointmentDigestsEnabled() bool {
	return c.NotificationsEnabled
}

func (c Config) reminderNagEnabled() bool {
	return c.ReminderNagTime != "" && c.Reminders != nil
}

// reminderPushEnabled has no flag of its own. A chore that does not tell you
// when it comes due is not a reminder, so the push exists wherever chores and
// a chat both do — the same condition under which the record itself is kept.
// The nag stays configurable because it is an editorial choice about the
// evening; this one is the feature working at all.
func (c Config) reminderPushEnabled() bool {
	return c.Reminders != nil
}

// due says which wall-clock messages fire on a tick, and lastFired the date
// each one last went out. Structs rather than positional results: with eight
// clocks, a positional signature meant every new one touched every call in the
// tests, and two adjacent strings swapped by mistake still compiled.
type due struct {
	daily, weekly, nag, school, review, menu, evening, suggest bool
}

type lastFired struct {
	daily, weekly, nag, school, review, menu, evening, suggest string
}

// dueThisMinute decides which of the wall-clock messages fire on this tick,
// given what already went out today.
//
// Split out of the loop deliberately. The rule this feature exists to protect
// — that the chore nag does not answer to NOTIFICATIONS_ENABLED — lived inside
// RunDigests, where a test could not reach it: a review found the old early
// return could be restored and the whole suite would still pass.
func (c Config) dueThisMinute(now time.Time, last lastFired) due {
	hm := now.Format("15:04")
	today := now.Format("2006-01-02")
	digestsOn := c.appointmentDigestsEnabled()
	dow := int(now.Weekday())

	return due{
		daily: digestsOn && c.DailyDigestTime != "" &&
			hm == c.DailyDigestTime && last.daily != today,
		weekly: digestsOn && c.WeeklyDigestDOW >= 0 &&
			dow == c.WeeklyDigestDOW &&
			hm == c.WeeklyDigestTime && last.weekly != today,
		nag:    c.reminderNagEnabled() && hm == c.ReminderNagTime && last.nag != today,
		school: c.schoolDigestEnabled() && hm == c.SchoolDigestTime && last.school != today,
		review: c.schoolWeekReviewEnabled() &&
			dow == c.SchoolWeekReviewDOW &&
			hm == c.SchoolWeekReviewTime && last.review != today,
		menu:    c.menuEnabled() && hm == c.MenuTime && last.menu != today,
		evening: c.menuEveningEnabled() && hm == c.MenuEveningTime && last.evening != today,
		suggest: c.dishSuggestEnabled() &&
			dow == c.DishSuggestDOW &&
			hm == c.DishSuggestTime && last.suggest != today,
	}
}

// sendMenu, sendEveningCheck and sendDishSuggestions are placeholders until
// the menu itself lands: the clocks are wired first so their gating can be
// tested on its own, and a deploy that sets the times early only gets a log
// line instead of a half-built message in the group.
func (b *Bot) sendMenu(now time.Time) {
	b.logger.Info("bot: menu due (not implemented yet)", "date", now.Format("2006-01-02"))
}

func (b *Bot) sendEveningCheck(now time.Time) {
	b.logger.Info("bot: evening meal check due (not implemented yet)", "date", now.Format("2006-01-02"))
}

func (b *Bot) sendDishSuggestions(_ context.Context, now time.Time) {
	b.logger.Info("bot: dish suggestions due (not implemented yet)", "date", now.Format("2006-01-02"))
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
