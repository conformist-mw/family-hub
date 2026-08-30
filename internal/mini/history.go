package mini

import (
	"net/http"
	"strconv"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/reminders"
)

// The record of what actually got done, on the phone.
//
// Occurrences are stored rather than recomputed so that "how many times did I
// forget the cashback this quarter" is answerable at all. The list tab answers
// what is open right now; this answers how a chore is going, which is the
// question you have standing in a kitchen wondering whether to give the chore
// to somebody else.
//
// Everything is formatted here. The client picks a period and renders strings,
// the same rule the audit endpoint follows.

type choreTallyDTO struct {
	Done    int `json:"done"`
	Skipped int `json:"skipped"`
	Missed  int `json:"missed"`
	Waiting int `json:"waiting"`
}

type choreHistoryRowDTO struct {
	ReminderID int64         `json:"reminderId"`
	Title      string        `json:"title"`
	Person     string        `json:"person"`
	Rule       string        `json:"rule"`
	Tally      choreTallyDTO `json:"tally"`
	// OftenMissed flags a chore worth moving, dropping, or handing to somebody
	// else. Decided here so the phone and the web agree on what "often" means.
	OftenMissed bool `json:"oftenMissed"`
}

type choreOccurrenceDTO struct {
	When   string `json:"when"`   // "08.08, 08:00"
	Mark   string `json:"mark"`   // ✓ ✗ ○ ·
	Status string `json:"status"` // done | skipped | missed | waiting — for the pill
	Label  string `json:"label"`  // "закрито", "не закрито", …
	DoneBy string `json:"doneBy"`
}

type choreHistoryDTO struct {
	Range  string `json:"range"`
	Period string `json:"period"` // "останні 30 днів"
	// Floor and Truncated say the record is short. Rows only exist for the
	// last 30 days of any downtime gap, so an empty stretch before that means
	// nothing was recorded — not that nothing came due.
	Floor     string `json:"floor"`
	Truncated bool   `json:"truncated"`
	// Chores is the overview, worst-kept first. Empty on a drill-down.
	Chores []choreHistoryRowDTO `json:"chores"`
	Totals choreTallyDTO        `json:"totals"`
	// The drill-down half.
	Title       string               `json:"title"`
	Rule        string               `json:"rule"`
	Occurrences []choreOccurrenceDTO `json:"occurrences"`
	Tally       choreTallyDTO        `json:"tally"`
	// RuleChanges explains a period with legitimately fewer rows than its rule
	// implies: an amend clears the open rows it corrects.
	RuleChanges []string `json:"ruleChanges"`
	Notice      string   `json:"notice"`
}

// historyPeriod resolves the range the client asked for. An unknown one is the
// default rather than an error: the payload is valid either way, and a phone
// showing nothing is worse than a phone showing the last 30 days.
func (rt *Router) historyPeriod(code string) (from, to time.Time, resolved, label string) {
	now := rt.now().In(rt.loc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, rt.loc)
	// To the end of today, not to this instant: a chore due at 20:00 belongs on
	// today's ledger as "ще не настало", and cutting at now would hide it.
	endOfToday := dayStart.AddDate(0, 0, 1).Add(-time.Minute)

	switch code {
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, rt.loc), endOfToday,
			"month", model.MonthsShort[int(now.Month())] + " " + strconv.Itoa(now.Year())
	case "prev":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, rt.loc)
		from = first.AddDate(0, -1, 0)
		return from, first.Add(-time.Minute), "prev",
			model.MonthsShort[int(from.Month())] + " " + strconv.Itoa(from.Year())
	}
	return dayStart.AddDate(0, 0, -29), endOfToday, "30d", "останні 30 днів"
}

func tallyDTO(t reminders.Tally) choreTallyDTO {
	return choreTallyDTO{Done: t.Done, Skipped: t.Skipped, Missed: t.Missed, Waiting: t.Waiting}
}

// oftenMissed is the same rule the web applies: half of what came due, and more
// than a couple of times. One miss is a bad week, not a habit.
func oftenMissed(t reminders.Tally) bool {
	return t.Missed >= 3 && t.MissRate() >= 0.5
}

func (rt *Router) handleChoreHistory(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	from, to, code, label := rt.historyPeriod(r.URL.Query().Get("range"))
	h, err := rt.reminders.History(from, to)
	if err != nil {
		rt.log.Error("mini: chore history", "err", err)
		rt.fail(w, errInternal)
		return
	}
	out := choreHistoryDTO{
		Range: code, Period: label,
		Floor: h.Floor.Format("02.01.2006"), Truncated: h.Truncated,
		Totals: tallyDTO(h.Totals()),
	}
	for _, c := range h.Chores {
		out.Chores = append(out.Chores, choreHistoryRowDTO{
			ReminderID: c.Reminder.ID, Title: c.Reminder.Title, Person: c.Reminder.Person,
			Rule: c.RuleText, Tally: tallyDTO(c.Tally), OftenMissed: oftenMissed(c.Tally),
		})
	}
	rt.writeJSON(w, http.StatusOK, out)
}

func (rt *Router) handleOneChoreHistory(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	from, to, code, label := rt.historyPeriod(r.URL.Query().Get("range"))
	c, err := rt.reminders.ChoreHistoryFor(id, from, to)
	if err != nil {
		rt.fail(w, errNotFound)
		return
	}
	h, err := rt.reminders.History(from, to)
	if err != nil {
		rt.log.Error("mini: chore history", "err", err)
		rt.fail(w, errInternal)
		return
	}

	now := rt.now().In(rt.loc)
	out := choreHistoryDTO{
		Range: code, Period: label,
		Floor: h.Floor.Format("02.01.2006"), Truncated: h.Truncated,
		Title: c.Reminder.Title, Rule: c.RuleText, Tally: tallyDTO(c.Tally),
	}
	for _, o := range c.Occurrences {
		status, label, mark := choreOutcome(o, now)
		out.Occurrences = append(out.Occurrences, choreOccurrenceDTO{
			When: o.Due.Format("02.01, 15:04"), Mark: mark,
			Status: status, Label: label, DoneBy: o.DoneBy,
		})
	}
	for _, ch := range c.RuleChanges {
		out.RuleChanges = append(out.RuleChanges,
			"Правило змінилось "+ch.At.Format("02.01.2006")+" — тепер «"+ch.Text+"».")
	}
	rt.writeJSON(w, http.StatusOK, out)
}

// choreOutcome names what happened to one occurrence. "Missed" and "waiting"
// are kept apart on purpose: yesterday's open chore is "you forgot", tonight's
// is "not yet", and one screen showing them alike reads as a shaming wall.
func choreOutcome(o reminders.Occurrence, now time.Time) (status, label, mark string) {
	switch o.Status {
	case model.OccDone:
		return "done", "закрито", "✓"
	case model.OccSkipped:
		return "skipped", "не треба було", "✗"
	}
	if o.Due.After(now) {
		return "waiting", "ще не настало", "·"
	}
	return "missed", "не закрито", "○"
}
