package mini

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/recur"
	"familyhub/internal/reminders"
	"familyhub/internal/store"
	"familyhub/internal/valid"
)

// The reminder surface. Rules and occurrences are separate resources under a
// reminder because they answer separate questions — "how does it repeat from
// now on" versus "what happened on this date" — and collapsing them into one
// PUT would hide which of the two a request meant.

// upcomingWindow is how far ahead the list looks. Far enough to answer "what
// is coming", short enough that a daily chore does not bury the screen.
const upcomingWindow = 60 * 24 * time.Hour

// pastWindow is how far back the list reaches. It is the materialiser's own
// backfill window, not a number of its own: anything the system recorded has
// to be reachable on the screen. A shorter window would let an unclosed chore
// slide out of the list while its row stayed pending forever — a miss nobody
// was ever given the chance to close.
const pastWindow = reminders.BackfillWindow

type reminderJSON struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Person      string `json:"person"`
	DurationMin int    `json:"durationMin"`
	Active      bool   `json:"active"`
	Note        string `json:"note"`
	// Rule is the version in force now — what the form opens with. Older
	// versions stay in the database but the screen never edits them.
	Rule ruleJSON `json:"rule"`
}

type ruleJSON struct {
	ID        int64  `json:"id"`
	RRule     string `json:"rrule"`
	Date      string `json:"date"` // dtstart split for the form
	Time      string `json:"time"`
	ValidFrom string `json:"validFrom"`
}

type occurrenceJSON struct {
	ReminderID int64  `json:"reminderId"`
	Title      string `json:"title"`
	Person     string `json:"person"`
	DueAt      string `json:"dueAt"`
	Date       string `json:"date"`
	Time       string `json:"time"`
	Status     string `json:"status"`
	DoneBy     string `json:"doneBy"`
	// CanMark is decided here rather than in the browser: an occurrence that
	// has not come due cannot be closed, and the rule for that lives in the
	// service, not in two places.
	CanMark bool `json:"canMark"`
}

type reminderWriteForm struct {
	Title       string `json:"title"`
	Person      string `json:"person"`
	DurationMin int    `json:"durationMin"`
	Note        string `json:"note"`
	Active      *bool  `json:"active"`
}

type ruleWriteForm struct {
	RRule string `json:"rrule"`
	Date  string `json:"date"`
	Time  string `json:"time"`
	// ValidFrom is empty for "from now on". The form only ever offers now or
	// an amend, so a caller-chosen past instant is not part of the contract.
	ValidFrom string `json:"validFrom"`
}

type markForm struct {
	DueAt  string `json:"dueAt"`
	Status string `json:"status"`
}

type previewForm struct {
	RRule string `json:"rrule"`
	Date  string `json:"date"`
	Time  string `json:"time"`
}

func (rt *Router) handleReminders(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	list, err := rt.store.Reminders()
	if err != nil {
		rt.log.Error("mini: list reminders", "err", err)
		rt.fail(w, errInternal)
		return
	}

	now := rt.now().In(rt.loc)
	occ, err := rt.reminders.Upcoming(now.Add(-pastWindow), now.Add(upcomingWindow))
	if err != nil {
		rt.log.Error("mini: upcoming reminders", "err", err)
		rt.fail(w, errInternal)
		return
	}

	out := make([]reminderJSON, 0, len(list))
	for _, rem := range list {
		rules, err := rt.store.RulesFor(rem.ID)
		if err != nil {
			rt.log.Error("mini: rules for reminder", "reminder_id", rem.ID, "err", err)
			rt.fail(w, errInternal)
			return
		}
		out = append(out, rt.reminderJSON(rem, rules))
	}

	occOut := make([]occurrenceJSON, 0, len(occ))
	for _, o := range occ {
		occOut = append(occOut, occurrenceJSON{
			ReminderID: o.ReminderID,
			Title:      o.Title,
			Person:     o.Person,
			DueAt:      o.Due.Format(model.LocalDatetime),
			Date:       o.Due.Format("2006-01-02"),
			Time:       o.Due.Format("15:04"),
			Status:     o.Status,
			DoneBy:     o.DoneBy,
			CanMark:    !o.Due.After(now),
		})
	}
	rt.writeJSON(w, http.StatusOK, map[string]any{
		"reminders":   out,
		"occurrences": occOut,
	})
}

// reminderJSON picks the version in force now — the last one whose validFrom
// has passed, falling back to the newest when a version starts in the future.
func (rt *Router) reminderJSON(rem model.Reminder, rules []model.ReminderRule) reminderJSON {
	out := reminderJSON{
		ID: rem.ID, Title: rem.Title, Person: rem.Person,
		DurationMin: rem.DurationMin, Active: rem.Active, Note: rem.Note,
	}
	if len(rules) == 0 {
		return out
	}
	now := rt.now().In(rt.loc)
	current := rules[len(rules)-1]
	for _, r := range rules {
		if from, err := r.ValidFrom(rt.loc); err == nil && !from.After(now) {
			current = r
		}
	}
	date, clock := splitLocalDatetime(current.DTStart)
	out.Rule = ruleJSON{
		ID: current.ID, RRule: current.RRule,
		Date: date, Time: clock, ValidFrom: current.ValidFromAt,
	}
	return out
}

func (rt *Router) handleReminderCreate(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	var body struct {
		reminderWriteForm
		ruleWriteForm
	}
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	dtstart, err := parseDateTime(body.Date, body.Time, rt.loc)
	if err != nil {
		rt.writeError(w, err, "create reminder")
		return
	}
	if body.Title == "" {
		rt.writeError(w, valid.FieldError{Field: "title", Message: "вкажіть назву"}, "create reminder")
		return
	}
	rem, err := rt.reminders.Create(model.Reminder{
		Title: body.Title, Person: body.Person,
		DurationMin: body.DurationMin, Note: body.Note,
	}, body.RRule, dtstart)
	if err != nil {
		rt.writeError(w, ruleFieldError(err), "create reminder")
		return
	}
	rt.writeJSON(w, http.StatusCreated, map[string]int64{"id": rem.ID})
}

func (rt *Router) handleReminderUpdate(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	var body reminderWriteForm
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	rem, err := rt.store.GetReminder(id)
	if err != nil {
		if store.IsNotFound(err) {
			rt.fail(w, errNotFound)
			return
		}
		rt.log.Error("mini: get reminder", "err", err)
		rt.fail(w, errInternal)
		return
	}
	if body.Title == "" {
		rt.writeError(w, valid.FieldError{Field: "title", Message: "вкажіть назву"}, "update reminder")
		return
	}
	rem.Title, rem.Person, rem.Note = body.Title, body.Person, body.Note
	rem.DurationMin = body.DurationMin
	if err := rt.store.UpdateReminder(rem); err != nil {
		rt.log.Error("mini: update reminder", "err", err)
		rt.fail(w, errInternal)
		return
	}
	// The switch moves through its own call, which also stamps the backfill
	// floor — a plain field write would resume a chore without one.
	if body.Active != nil && *body.Active != rem.Active {
		if err := rt.reminders.SetActive(id, *body.Active); err != nil {
			rt.log.Error("mini: set reminder active", "err", err)
			rt.fail(w, errInternal)
			return
		}
	}
	rt.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (rt *Router) handleReminderDelete(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	if _, err := rt.store.GetReminder(id); err != nil {
		if store.IsNotFound(err) {
			rt.fail(w, errNotFound)
			return
		}
		rt.log.Error("mini: get reminder", "err", err)
		rt.fail(w, errInternal)
		return
	}
	if err := rt.store.SoftDeleteReminder(id); err != nil {
		rt.log.Error("mini: delete reminder", "err", err)
		rt.fail(w, errInternal)
		return
	}
	rt.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRuleCreate changes how a chore repeats from a moment onwards. It is a
// POST rather than a PUT because it appends a version: everything before that
// moment keeps the schedule it actually ran on.
func (rt *Router) handleRuleCreate(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	var body ruleWriteForm
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	if _, err := rt.store.GetReminder(id); err != nil {
		if store.IsNotFound(err) {
			rt.fail(w, errNotFound)
			return
		}
		rt.log.Error("mini: get reminder", "err", err)
		rt.fail(w, errInternal)
		return
	}
	dtstart, err := parseDateTime(body.Date, body.Time, rt.loc)
	if err != nil {
		rt.writeError(w, err, "add rule")
		return
	}
	validFrom := rt.now().In(rt.loc)
	if body.ValidFrom != "" {
		parsed, err := time.ParseInLocation(model.LocalDatetime, body.ValidFrom, rt.loc)
		if err != nil {
			rt.writeError(w, valid.FieldError{Field: "validFrom", Message: "невірний момент"}, "add rule")
			return
		}
		validFrom = parsed
	}
	if _, err := rt.reminders.AddRule(id, body.RRule, dtstart, validFrom); err != nil {
		rt.writeError(w, ruleFieldError(err), "add rule")
		return
	}
	rt.writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

// handleRuleAmend corrects a version in place — "I mistyped it, it was always
// the 5th". It does not rebuild occurrences already recorded under the old
// text; those rows are what actually came due.
func (rt *Router) handleRuleAmend(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	ruleID, err := strconv.ParseInt(r.PathValue("ruleId"), 10, 64)
	if err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	var body ruleWriteForm
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	rules, err := rt.store.RulesFor(id)
	if err != nil {
		rt.log.Error("mini: rules for reminder", "err", err)
		rt.fail(w, errInternal)
		return
	}
	var target model.ReminderRule
	for _, rule := range rules {
		if rule.ID == ruleID {
			target = rule
			break
		}
	}
	if target.ID == 0 {
		rt.fail(w, errNotFound)
		return
	}
	dtstart, err := parseDateTime(body.Date, body.Time, rt.loc)
	if err != nil {
		rt.writeError(w, err, "amend rule")
		return
	}
	target.RRule = body.RRule
	target.DTStart = dtstart.Format(model.LocalDatetime)
	if body.ValidFrom != "" {
		target.ValidFromAt = body.ValidFrom
	}
	if err := rt.reminders.AmendRule(target); err != nil {
		rt.writeError(w, ruleFieldError(err), "amend rule")
		return
	}
	rt.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (rt *Router) handleOccurrenceMark(w http.ResponseWriter, r *http.Request) {
	who, bad := rt.v.authenticate(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	var body markForm
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	dueAt, err := time.ParseInLocation(model.LocalDatetime, body.DueAt, rt.loc)
	if err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	switch err := rt.reminders.Mark(id, dueAt, body.Status, who.Name); {
	case err == nil:
		rt.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, reminders.ErrFutureMark):
		rt.writeError(w, valid.FieldError{
			Field: "dueAt", Message: "це ще не настало",
		}, "mark occurrence")
	case errors.Is(err, reminders.ErrNoSuchOccurrence):
		rt.fail(w, errNotFound)
	case store.IsNotFound(err):
		rt.fail(w, errNotFound)
	default:
		rt.log.Error("mini: mark occurrence", "err", err)
		rt.fail(w, errInternal)
	}
}

// handlePreview answers "what would this rule do" before anything is stored,
// using the same library that will expand it for real — so what the form shows
// and what the calendar later holds cannot disagree.
func (rt *Router) handlePreview(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	var body previewForm
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	dtstart, err := parseDateTime(body.Date, body.Time, rt.loc)
	if err != nil {
		rt.writeError(w, err, "preview rule")
		return
	}
	next, err := rt.reminders.Preview(body.RRule, dtstart, 5)
	if err != nil {
		rt.writeError(w, ruleFieldError(err), "preview rule")
		return
	}
	out := make([]map[string]string, 0, len(next))
	for _, t := range next {
		out = append(out, map[string]string{
			"dueAt": t.Format(model.LocalDatetime),
			"date":  t.Format("2006-01-02"),
			"time":  t.Format("15:04"),
		})
	}
	rt.writeJSON(w, http.StatusOK, map[string]any{"next": out})
}

// ruleFieldError points an unexpandable rule at the field that holds it. The
// library's message is English and about RFC 5545, so it is logged rather than
// shown; the person gets something they can act on.
//
// Only recur's own rejections are mapped. Everything else — a locked database,
// a constraint, a full disk — falls through to writeError's logged 500. It
// used to catch those too, so a store failure was reported to the person as
// "your recurrence rule is not understood" and left nothing in the log to
// contradict them.
func ruleFieldError(err error) error {
	if err == nil {
		return nil
	}
	var invalid valid.FieldError
	if errors.As(err, &invalid) {
		return err
	}
	if errors.Is(err, recur.ErrBadRule) || errors.Is(err, recur.ErrEmptyRule) {
		return valid.FieldError{Field: "rrule", Message: "правило повторення незрозуміле"}
	}
	return err
}

func parseDateTime(date, clock string, loc *time.Location) (time.Time, error) {
	if date == "" {
		return time.Time{}, valid.FieldError{Field: "date", Message: "вкажіть дату"}
	}
	if clock == "" {
		return time.Time{}, valid.FieldError{Field: "time", Message: "вкажіть час"}
	}
	t, err := time.ParseInLocation(model.LocalDatetime, date+"T"+clock, loc)
	if err != nil {
		return time.Time{}, valid.FieldError{Field: "time", Message: "невірні дата або час"}
	}
	return t, nil
}

func splitLocalDatetime(s string) (date, clock string) {
	if len(s) != len(model.LocalDatetime) {
		return "", ""
	}
	return s[:10], s[11:]
}
