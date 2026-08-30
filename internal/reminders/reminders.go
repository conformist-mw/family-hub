// Package reminders is the write and read rules for recurring chores, shared
// by the Mini App, the bot and the ICS feed so no two surfaces can disagree
// about what a reminder means.
//
// The model in one paragraph: a reminder says WHAT the chore is; a list of
// rule versions says HOW it repeated and FROM WHEN; occurrence rows say what
// actually came due. The boundary between stored and computed is the instant
// `now` — everything at or before it is read from rows, everything after is
// expanded from the rules, and nothing is ever both.
package reminders

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/recur"
	"familyhub/internal/store"
)

// ErrFutureMark refuses a mark on something that has not happened. A stored
// row in the future would be invisible to the calendar and the list, which
// both project that half from the rules, and it would be orphaned the moment
// the rule changed. The chore can be closed when it comes due.
var ErrFutureMark = errors.New("reminders: cannot mark an occurrence that has not come due")

// ErrNoSuchOccurrence rejects a mark for an instant the rules never scheduled,
// so hand-crafted callback data or a stale screen cannot invent history.
var ErrNoSuchOccurrence = errors.New("reminders: no occurrence at that instant")

// Service holds the rules shared across surfaces. now is injectable because
// every interesting behaviour here is a statement about the current instant.
type Service struct {
	store *store.Store
	loc   *time.Location
	log   *slog.Logger
	now   func() time.Time
}

func NewService(st *store.Store, loc *time.Location, logger *slog.Logger, now func() time.Time) *Service {
	if loc == nil {
		loc = time.Local
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: st, loc: loc, log: logger, now: func() time.Time { return now().In(loc) }}
}

// Upcoming returns every occurrence in [from, to] as one timeline: instants at
// or before now come from stored rows, later ones are projected from the rules.
//
// It repairs before it reads. The materialiser ticks once a minute, so an
// occurrence that came due seconds ago has no row yet — without this it would
// fall through the gap between "past comes from rows" and "future comes from
// rules" and simply vanish from the calendar and the list for up to a minute.
// The pass is idempotent and bounded, so paying for it on a read is cheaper
// than explaining a chore that flickers.
//
// A failed repair is logged, not returned: a read must not fail because a
// write did. The worst case is the same minute-long gap, which the next tick
// closes.
func (s *Service) Upcoming(from, to time.Time) ([]Occurrence, error) {
	now := s.now()
	if err := s.Materialise(now); err != nil {
		s.log.Error("reminders: repair before read", "err", err)
	}

	from, to = from.In(s.loc), to.In(s.loc)
	if to.Before(from) {
		return nil, nil
	}

	var out []Occurrence
	if !from.After(now) {
		storedTo := to
		if now.Before(storedTo) {
			storedTo = now
		}
		rows, err := s.store.OccurrencesIn(
			from.Format(model.LocalDatetime), storedTo.Format(model.LocalDatetime))
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			due, err := r.Due(s.loc)
			if err != nil {
				s.log.Warn("reminders: unreadable due_at", "occurrence_id", r.ID, "value", r.DueAt)
				continue
			}
			out = append(out, Occurrence{
				ReminderID: r.ReminderID, RuleID: r.RuleID,
				Title: r.Title, Person: r.Person, Due: due,
				DurationMin: r.DurationMin, Status: r.Status, DoneBy: r.DoneBy,
				Stored: true,
			})
		}
	}

	if to.After(now) {
		projected, err := s.project(now, to)
		if err != nil {
			return nil, err
		}
		out = append(out, projected...)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Due.Before(out[j].Due) })
	return out, nil
}

// project expands the rules over (after, to]. Strictly after: an instant at
// `after` itself already has a row, and returning it twice would double every
// chore on the boundary minute.
func (s *Service) project(after, to time.Time) ([]Occurrence, error) {
	reminders, err := s.store.ActiveReminders()
	if err != nil {
		return nil, err
	}
	if len(reminders) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(reminders))
	for _, r := range reminders {
		ids = append(ids, r.ID)
	}
	rulesByReminder, err := s.store.RulesForAll(ids)
	if err != nil {
		return nil, err
	}

	var out []Occurrence
	for _, r := range reminders {
		rules := rulesByReminder[r.ID]
		if len(rules) == 0 {
			continue
		}
		occ, err := s.expandVersioned(r, rules, after, to)
		if err != nil {
			// Same reasoning as the materialiser: one unparseable rule must not
			// blank the calendar for every other chore.
			s.log.Error("reminders: project", "reminder_id", r.ID, "err", err)
			continue
		}
		for _, o := range occ {
			if o.Due.After(after) {
				out = append(out, o)
			}
		}
	}
	return out, nil
}

// UnclosedOn is the evening nag's question: what came due on this date and
// nobody closed. Answerable only because rows exist — an occurrence recomputed
// from today's rule could never prove it had come due at all.
func (s *Service) UnclosedOn(date time.Time) ([]Occurrence, error) {
	date = date.In(s.loc)
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, s.loc)
	dayEnd := dayStart.AddDate(0, 0, 1).Add(-time.Minute)

	rows, err := s.store.PendingOccurrencesIn(
		dayStart.Format(model.LocalDatetime), dayEnd.Format(model.LocalDatetime))
	if err != nil {
		return nil, err
	}
	out := make([]Occurrence, 0, len(rows))
	for _, r := range rows {
		due, err := r.Due(s.loc)
		if err != nil {
			s.log.Warn("reminders: unreadable due_at", "occurrence_id", r.ID, "value", r.DueAt)
			continue
		}
		out = append(out, Occurrence{
			ReminderID: r.ReminderID, RuleID: r.RuleID,
			Title: r.Title, Person: r.Person, Due: due,
			DurationMin: r.DurationMin, Status: r.Status, Stored: true,
		})
	}
	return out, nil
}

// Mark records a person's decision about one occurrence.
//
// Two guards, both about not inventing history: the instant must already have
// come due, and the rules must actually have scheduled it.
func (s *Service) Mark(reminderID int64, dueAt time.Time, status, by string) error {
	if !model.ValidOccStatus(status) || status == model.OccPending {
		return fmt.Errorf("reminders: %q is not a decision a person can record", status)
	}
	now := s.now()
	dueAt = dueAt.In(s.loc)
	if dueAt.After(now) {
		return ErrFutureMark
	}

	r, err := s.store.GetReminder(reminderID)
	if err != nil {
		return err
	}
	rules, err := s.store.RulesFor(reminderID)
	if err != nil {
		return err
	}
	rule, ok, err := s.occursAt(r, rules, dueAt)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoSuchOccurrence
	}
	return s.store.MarkOccurrence(reminderID, rule.ID, dueAt.Format(model.LocalDatetime), status, by)
}

// Create stores a new chore with its first rule version. The rule is validated
// by the same library that will later expand it, so anything accepted here
// cannot fail to expand afterwards.
func (s *Service) Create(r model.Reminder, rrule string, dtstart time.Time) (model.Reminder, error) {
	if err := recur.Validate(rrule); err != nil {
		return model.Reminder{}, err
	}
	r.Active = true
	// Stamped from the service clock, not SQL's: the materialiser compares
	// this against the same clock when deciding how far back to catch up.
	r.ActiveSince = s.now().Format(model.LocalDatetime)
	// The first version reaches back indefinitely: before it there was no
	// chore at all, so there is nothing for an earlier boundary to protect.
	first := model.ReminderRule{
		ValidFromAt: dtstart.In(s.loc).Format(model.LocalDatetime),
		DTStart:     dtstart.In(s.loc).Format(model.LocalDatetime),
		RRule:       rrule,
	}
	return s.store.CreateReminder(r, first)
}

// AddRule changes how a chore repeats from a moment onwards, leaving
// everything before it exactly as it was recorded. This is the ordinary edit:
// "from September we do it on the 5th".
func (s *Service) AddRule(reminderID int64, rrule string, dtstart, validFrom time.Time) (model.ReminderRule, error) {
	if err := recur.Validate(rrule); err != nil {
		return model.ReminderRule{}, err
	}
	return s.store.AddRule(model.ReminderRule{
		ReminderID:  reminderID,
		ValidFromAt: validFrom.In(s.loc).Format(model.LocalDatetime),
		DTStart:     dtstart.In(s.loc).Format(model.LocalDatetime),
		RRule:       rrule,
	})
}

// AmendRule corrects a version in place — "I mistyped it, it was always the
// 5th" — as opposed to AddRule's "from now on". It deliberately does not
// rebuild occurrences already written under the old text: those rows are the
// record of what actually came due, and rewriting them is the history-editing
// the whole design refuses. The divergence is documented, not silent.
// SetActive switches a chore on or off, stamping the backfill floor from the
// service clock when switching on.
func (s *Service) SetActive(id int64, active bool) error {
	return s.store.SetReminderActive(id, active, s.now().Format(model.LocalDatetime))
}

func (s *Service) AmendRule(rule model.ReminderRule) error {
	if err := recur.Validate(rule.RRule); err != nil {
		return err
	}
	return s.store.AmendRule(rule)
}

// Preview is what the form shows before anything is saved: the next few
// instants a rule would produce, computed by the library that will expand it
// for real.
func (s *Service) Preview(rrule string, dtstart time.Time, n int) ([]time.Time, error) {
	if err := recur.Validate(rrule); err != nil {
		return nil, err
	}
	return recur.Next(dtstart.In(s.loc), rrule, s.now(), n)
}
