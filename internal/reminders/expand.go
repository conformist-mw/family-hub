package reminders

import (
	"fmt"
	"sort"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/recur"
)

// Occurrence is one instance of a chore, whether it was read from a row or
// computed from a rule. Callers that need to tell the two apart look at
// Stored: only a stored occurrence carries a status a person set.
type Occurrence struct {
	ReminderID  int64
	RuleID      int64
	Title       string
	Person      string
	Due         time.Time
	DurationMin int
	Status      string
	DoneBy      string
	// Stored distinguishes a materialised row from a projection. A computed
	// occurrence is always OccPending and can never be marked — it has not
	// happened yet.
	Stored bool
}

// expandVersioned returns every occurrence of one reminder in [from, to],
// using for each stretch of the window whichever rule version was in force
// over it.
//
// This is what keeps the past honest. With a single mutable rule, moving the
// cashback from the 1st to the 5th would retroactively claim it had always
// been the 5th, so the record of what was scheduled — and of what was missed —
// would change under you. Cutting the window at each version boundary means a
// August is expanded with August's rule no matter what today's says.
//
// rules must be ordered oldest first; store.RulesFor guarantees it.
func (s *Service) expandVersioned(r model.Reminder, rules []model.ReminderRule, from, to time.Time) ([]Occurrence, error) {
	if len(rules) == 0 || to.Before(from) {
		return nil, nil
	}

	var out []Occurrence
	for i, rule := range rules {
		// This version governs [validFrom, nextValidFrom), intersected with
		// the caller's window. The last version has no successor and runs on.
		validFrom, err := rule.ValidFrom(s.loc)
		if err != nil {
			return nil, fmt.Errorf("reminders: rule %d valid_from_at: %w", rule.ID, err)
		}
		segFrom := from
		if validFrom.After(segFrom) {
			segFrom = validFrom
		}
		segTo := to
		if i+1 < len(rules) {
			nextFrom, err := rules[i+1].ValidFrom(s.loc)
			if err != nil {
				return nil, fmt.Errorf("reminders: rule %d valid_from_at: %w", rules[i+1].ID, err)
			}
			// Exclusive upper bound: an occurrence landing exactly on the next
			// version's starting instant belongs to that version, not this one.
			// A nanosecond back rather than a minute, so the boundary holds
			// whatever precision an occurrence turns out to have.
			if end := nextFrom.Add(-time.Nanosecond); end.Before(segTo) {
				segTo = end
			}
		}
		if segTo.Before(segFrom) {
			continue // this version does not overlap the window at all
		}

		anchor, err := rule.Anchor(s.loc)
		if err != nil {
			return nil, fmt.Errorf("reminders: rule %d dtstart: %w", rule.ID, err)
		}
		times, err := recur.Expand(anchor, rule.RRule, segFrom, segTo)
		if err != nil {
			return nil, fmt.Errorf("reminders: rule %d: %w", rule.ID, err)
		}
		for _, t := range times {
			out = append(out, Occurrence{
				ReminderID:  r.ID,
				RuleID:      rule.ID,
				Title:       r.Title,
				Person:      r.Person,
				Due:         t,
				DurationMin: r.DurationMin,
				Status:      model.OccPending,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Due.Before(out[j].Due) })
	return out, nil
}

// ruleAt returns the version in force at t, or false when the reminder had no
// rule yet. Used when a single instant needs attributing to a version —
// materialising a row, or validating a mark.
func (s *Service) ruleAt(rules []model.ReminderRule, t time.Time) (model.ReminderRule, bool, error) {
	var found model.ReminderRule
	var ok bool
	for _, rule := range rules {
		validFrom, err := rule.ValidFrom(s.loc)
		if err != nil {
			return model.ReminderRule{}, false, fmt.Errorf("reminders: rule %d valid_from_at: %w", rule.ID, err)
		}
		if validFrom.After(t) {
			break // ordered oldest first, so nothing later can apply either
		}
		found, ok = rule, true
	}
	return found, ok, nil
}

// occursAt reports whether the reminder really has an occurrence at t under
// the version in force then. Marks are checked against this so a hand-crafted
// request cannot invent an occurrence that was never scheduled.
func (s *Service) occursAt(r model.Reminder, rules []model.ReminderRule, t time.Time) (model.ReminderRule, bool, error) {
	rule, ok, err := s.ruleAt(rules, t)
	if err != nil || !ok {
		return model.ReminderRule{}, false, err
	}
	// A one-minute window: due_at is stored to the minute, so an exact match
	// is the only thing that counts.
	got, err := s.expandVersioned(r, []model.ReminderRule{rule}, t, t)
	if err != nil {
		return model.ReminderRule{}, false, err
	}
	for _, o := range got {
		if o.Due.Equal(t) {
			return rule, true, nil
		}
	}
	return model.ReminderRule{}, false, nil
}
