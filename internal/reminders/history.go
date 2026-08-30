package reminders

import (
	"sort"
	"time"

	"familyhub/internal/model"
)

// The record of what actually got done.
//
// Occurrences are stored rather than recomputed precisely so this is
// answerable: a pending row in the past is evidence that the moment came and
// nobody answered it. Until now that record existed and there was nowhere to
// look at it — the calendar shows individual events, which answers "what
// happened on the 5th", not "how is this chore going".
//
// Three things make a naive count of these rows lie, and each is carried on
// the result rather than left for a surface to rediscover:
//
//   - The backfill floor. Rows only exist for the last BackfillWindow of any
//     downtime gap, so a period reaching further back has a hole that is not
//     the family's fault. Floor and Truncated say so.
//   - Age makes "pending" ambiguous. Yesterday's open chore is "you forgot";
//     tonight's is "not yet". Missed and Waiting are separate counts, because
//     one screen showing them together reads as a shaming wall.
//   - An amend clears the open rows it corrects, so a corrected period
//     legitimately holds fewer rows than its rule implies. RuleChanges names
//     the versions that began inside the period so the drop is explained
//     rather than mysterious.

// Tally is what happened to one chore over a period.
type Tally struct {
	Done    int // somebody said they did it
	Skipped int // somebody said it was not needed this time
	Missed  int // came due, still unanswered
	Waiting int // has not come due yet
}

// Total is every occurrence counted, answered or not.
func (t Tally) Total() int { return t.Done + t.Skipped + t.Missed + t.Waiting }

// Answered is the ones somebody made a decision about. Waiting is excluded on
// purpose: it is not a failure to have not yet done tonight's chore.
func (t Tally) Answered() int { return t.Done + t.Skipped }

// Settled is what the miss rate is measured against — everything that came due,
// whether or not it was answered.
func (t Tally) Settled() int { return t.Answered() + t.Missed }

// MissRate is the share of what came due that nobody answered, 0..1. Zero when
// nothing came due, so a brand-new chore does not rank as perfectly kept.
func (t Tally) MissRate() float64 {
	if t.Settled() == 0 {
		return 0
	}
	return float64(t.Missed) / float64(t.Settled())
}

// RuleChange is a rule version that began inside the period, so a screen can
// say the schedule moved underneath the numbers.
type RuleChange struct {
	At   time.Time
	Text string // the new rule, in words
}

// ChoreHistory is one chore's record over the period.
type ChoreHistory struct {
	Reminder model.Reminder
	// RuleText is the rule in force at the end of the period — what the chore
	// looks like now, which is what a list wants to show.
	RuleText    string
	Tally       Tally
	Occurrences []Occurrence // every one in the period, oldest first
	RuleChanges []RuleChange
}

// History is the whole screen: every chore with a record in the period.
type History struct {
	From, To time.Time
	Chores   []ChoreHistory
	// Floor is the earliest instant rows can exist for. Before it the absence
	// of a row means nothing was recorded, not that nothing came due.
	Floor time.Time
	// Truncated reports that the asked-for period reaches before Floor, so the
	// screen has to say the history is short rather than show a silent hole.
	Truncated bool
}

// Totals sums every chore, for a header line.
func (h History) Totals() Tally {
	var out Tally
	for _, c := range h.Chores {
		out.Done += c.Tally.Done
		out.Skipped += c.Tally.Skipped
		out.Missed += c.Tally.Missed
		out.Waiting += c.Tally.Waiting
	}
	return out
}

// History assembles the record over [from, to].
//
// Chores come back worst-first: most missed, then by miss rate, then by name.
// That order is the point of the screen — the ones habitually forgotten are the
// ones worth moving, dropping, or handing to somebody else, and they are
// invisible in a list sorted by name.
//
// A chore with nothing in the period is left out entirely. A screen padded with
// chores that had nothing to do is a screen nobody reads twice.
func (s *Service) History(from, to time.Time) (History, error) {
	now := s.now()
	from, to = from.In(s.loc), to.In(s.loc)
	out := History{From: from, To: to, Floor: now.Add(-BackfillWindow)}
	if to.Before(from) {
		return out, nil
	}
	out.Truncated = from.Before(out.Floor)

	occurrences, err := s.Upcoming(from, to)
	if err != nil {
		return out, err
	}

	all, err := s.store.Reminders()
	if err != nil {
		return out, err
	}
	byID := make(map[int64]model.Reminder, len(all))
	ids := make([]int64, 0, len(all))
	for _, r := range all {
		byID[r.ID] = r
		ids = append(ids, r.ID)
	}
	rules, err := s.store.RulesForAll(ids)
	if err != nil {
		return out, err
	}

	grouped := map[int64]*ChoreHistory{}
	var order []int64
	for _, o := range occurrences {
		c, ok := grouped[o.ReminderID]
		if !ok {
			// A soft-deleted chore keeps its occurrences — the record of what
			// came due outlives the chore — so fall back to the occurrence's
			// own title rather than dropping the rows.
			rem, known := byID[o.ReminderID]
			if !known {
				rem = model.Reminder{ID: o.ReminderID, Title: o.Title, Person: o.Person}
			}
			c = &ChoreHistory{Reminder: rem}
			grouped[o.ReminderID] = c
			order = append(order, o.ReminderID)
		}
		c.Occurrences = append(c.Occurrences, o)
		switch {
		case o.Status == model.OccDone:
			c.Tally.Done++
		case o.Status == model.OccSkipped:
			c.Tally.Skipped++
		case o.Due.After(now):
			c.Tally.Waiting++
		default:
			c.Tally.Missed++
		}
	}

	for _, id := range order {
		c := grouped[id]
		versions := rules[id]
		c.RuleText = ruleTextAt(versions, s.loc, to)
		c.RuleChanges = changesIn(versions, s.loc, from, to)
		out.Chores = append(out.Chores, *c)
	}
	sort.SliceStable(out.Chores, func(i, j int) bool {
		a, b := out.Chores[i].Tally, out.Chores[j].Tally
		if a.Missed != b.Missed {
			return a.Missed > b.Missed
		}
		if a.MissRate() != b.MissRate() {
			return a.MissRate() > b.MissRate()
		}
		return out.Chores[i].Reminder.Title < out.Chores[j].Reminder.Title
	})
	return out, nil
}

// ChoreHistoryFor is the drill-down: one chore over the period. Separate from
// History because opening one chore should not assemble every other.
func (s *Service) ChoreHistoryFor(reminderID int64, from, to time.Time) (ChoreHistory, error) {
	full, err := s.History(from, to)
	if err != nil {
		return ChoreHistory{}, err
	}
	for _, c := range full.Chores {
		if c.Reminder.ID == reminderID {
			return c, nil
		}
	}
	// No rows in the period is not an error — the chore may simply have had
	// nothing to do. Return it named, so the screen says "nothing here" about
	// a chore rather than about nothing.
	rem, err := s.store.GetReminder(reminderID)
	if err != nil {
		return ChoreHistory{}, err
	}
	versions, err := s.store.RulesFor(reminderID)
	if err != nil {
		return ChoreHistory{}, err
	}
	return ChoreHistory{
		Reminder: rem,
		RuleText: ruleTextAt(versions, s.loc, to),
	}, nil
}

// ruleTextAt describes the version in force at t, falling back to the newest
// when every version starts later.
func ruleTextAt(versions []model.ReminderRule, loc *time.Location, t time.Time) string {
	if len(versions) == 0 {
		return ""
	}
	current := versions[len(versions)-1]
	for _, v := range versions {
		if from, err := v.ValidFrom(loc); err == nil && !from.After(t) {
			current = v
		}
	}
	return Describe(current.RRule)
}

// changesIn returns the versions that began inside the period. The first
// version of a chore is not a change — there was no schedule before it to
// change from — so it is only reported when something preceded it.
func changesIn(versions []model.ReminderRule, loc *time.Location, from, to time.Time) []RuleChange {
	var out []RuleChange
	for i, v := range versions {
		if i == 0 {
			continue
		}
		at, err := v.ValidFrom(loc)
		if err != nil || at.Before(from) || at.After(to) {
			continue
		}
		out = append(out, RuleChange{At: at, Text: Describe(v.RRule)})
	}
	return out
}
