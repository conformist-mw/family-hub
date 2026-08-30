package reminders

import (
	"context"
	"time"

	"familyhub/internal/model"
)

// BackfillWindow is how far back a catch-up pass looks. It is what makes the
// materialiser self-healing: a container that was down over an occurrence
// writes its row on the next tick, with no watermark to keep in sync and
// nothing to reset by hand.
//
// The flip side is a documented limit — a gap longer than this leaves those
// occurrences without rows for good. Thirty days is far past any outage this
// family would tolerate silently.
const BackfillWindow = 30 * 24 * time.Hour

// materialiseTick is how often a pass runs. A reminder is due to the minute,
// so anything coarser would leave a row missing for part of its own day.
const materialiseTick = time.Minute

// repairWindow is how far back a READ repairs. Two ticks: enough to cover the
// gap between an occurrence coming due and the ticker writing it, and nothing
// more. Reads are public and frequent; catching up after downtime belongs to
// the ticker, which is neither.
const repairWindow = 2 * materialiseTick

// Materialise writes a row for every occurrence that has come due and does not
// have one yet, across every active reminder.
//
// Why rows at all, when a rule can generate them: a generated occurrence
// proves nothing. A row written when the moment arrived is evidence that it
// arrived, and it survives the rule being changed afterwards. Without it, "you
// forgot the cashback in August" would be indistinguishable from "August was
// never scheduled" as soon as the schedule moved.
//
// One broken reminder does not stop the others: its error is logged and the
// pass continues, because a single unparseable rule must not freeze the
// history of every other chore.
func (s *Service) Materialise(now time.Time) error {
	return s.materialiseSince(now.Add(-BackfillWindow), now)
}

// materialiseSince is the pass over an explicit window. The floor is still
// clamped per reminder by active_since, so a wide window cannot reach back
// past the moment a chore was switched on.
func (s *Service) materialiseSince(since, now time.Time) error {
	now = now.In(s.loc)
	since = since.In(s.loc)
	reminders, err := s.store.ActiveReminders()
	if err != nil {
		return err
	}
	if len(reminders) == 0 {
		return nil
	}

	ids := make([]int64, 0, len(reminders))
	for _, r := range reminders {
		ids = append(ids, r.ID)
	}
	rulesByReminder, err := s.store.RulesForAll(ids)
	if err != nil {
		return err
	}

	var pending []model.ReminderOccurrence
	for _, r := range reminders {
		rules := rulesByReminder[r.ID]
		if len(rules) == 0 {
			continue
		}
		from := s.backfillFloor(r, now)
		if since.After(from) {
			from = since
		}
		if from.After(now) {
			continue
		}
		occ, err := s.expandVersioned(r, rules, from, now)
		if err != nil {
			s.log.Error("reminders: expand for materialise", "reminder_id", r.ID, "err", err)
			continue
		}
		for _, o := range occ {
			pending = append(pending, model.ReminderOccurrence{
				ReminderID: o.ReminderID, RuleID: o.RuleID,
				DueAt: o.Due.Format(model.LocalDatetime),
			})
		}
	}
	// One transaction for the whole pass rather than one per row: the start-up
	// catch-up can span thirty days across every chore, and an implicit
	// transaction each means one WAL write lock and one fsync per occurrence.
	return s.store.MaterialiseOccurrences(pending)
}

// backfillFloor is the earliest instant a pass may write for this reminder:
// the rolling window, but never earlier than when the chore was last switched
// on. Without the second half, resuming a reminder paused for a month
// would immediately invent a month of "you forgot" rows covering exactly the
// time it was deliberately off.
func (s *Service) backfillFloor(r model.Reminder, now time.Time) time.Time {
	floor := now.Add(-BackfillWindow)
	since, err := time.ParseInLocation(model.LocalDatetime, r.ActiveSince, s.loc)
	if err != nil {
		// An unreadable timestamp must not widen the window: fall back to the
		// rolling one, which is the conservative half.
		s.log.Warn("reminders: unreadable active_since", "reminder_id", r.ID, "value", r.ActiveSince)
		return floor
	}
	if since.After(floor) {
		return since
	}
	return floor
}

// RunMaterialiser ticks until ctx is done, writing occurrence rows as they come
// due. It blocks; meant for its own goroutine.
//
// It runs from main rather than from the bot on purpose. RunDigests bails out
// when notifications are off or no chat is configured, and hanging the history
// off that would put data integrity behind a flag for optional messages —
// switch the digests off and the record silently stops being written.
//
// A pass runs immediately, before the first tick, so a restart closes whatever
// gap the downtime opened without waiting a minute for it.
func (s *Service) RunMaterialiser(ctx context.Context) {
	s.log.Info("reminders: materialiser started",
		"tick", materialiseTick, "backfill", BackfillWindow)

	if err := s.Materialise(s.now()); err != nil {
		s.log.Error("reminders: materialise on start", "err", err)
	}

	ticker := time.NewTicker(materialiseTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Materialise(s.now()); err != nil {
				s.log.Error("reminders: materialise", "err", err)
			}
		}
	}
}
