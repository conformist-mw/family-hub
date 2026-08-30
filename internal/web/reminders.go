package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/recur"
	"familyhub/internal/reminders"
	"familyhub/internal/valid"
)

// Recurring chores from the desk. They shipped Mini-App-only, which was the
// right call for the daily gesture — closing a checkbox is a phone thing — but
// it left the web, which is meant to be the whole data model, without three of
// its tables. Creating a chore with a real keyboard, and seeing what is still
// open at midday, are desk jobs.
//
// Every write goes through reminders.Service, never the store: it owns what a
// valid rule is, what a rule change does to the past, and what may be closed.
// A handler with its own SQL would be a second answer to all three.

// choreListLimit caps the "what happened lately" list. The page is about what
// is open now; the full record is #55's screen, not this one.
const choreListLimit = 20

type reminderRow struct {
	Reminder model.Reminder
	RuleText string // the rule in Ukrainian, from reminders.Describe
	Time     string // HH:MM of the version in force
	RuleID   int64
}

type remindersListData struct {
	Reminders []reminderRow
	Open      []reminders.Occurrence // came due, still unanswered
	Recent    []reminders.Occurrence // already answered, newest first
	Error     string
}

// requireReminders answers 404 when the feature is off. nil means "no chores
// service wired", which is a deployment state, not an error to log.
func (a *App) requireReminders(w http.ResponseWriter, r *http.Request) bool {
	if a.Reminders == nil {
		http.NotFound(w, r)
		return false
	}
	return true
}

func (a *App) handleReminders(w http.ResponseWriter, r *http.Request) {
	if !a.requireReminders(w, r) {
		return
	}
	a.renderReminders(w, "")
}

func (a *App) renderReminders(w http.ResponseWriter, errMsg string) {
	rows, err := a.reminderRows()
	if err != nil {
		a.serverError(w, err)
		return
	}
	now := time.Now()
	// Back over the window the materialiser records, so the list cannot show a
	// hole the rows do not have. Forward not at all: this page is about what
	// came due, and what is coming is the calendar's job.
	occurrences, err := a.Reminders.Upcoming(now.Add(-reminders.BackfillWindow), now)
	if err != nil {
		a.serverError(w, err)
		return
	}
	data := remindersListData{Reminders: rows, Error: errMsg}
	for i := len(occurrences) - 1; i >= 0; i-- {
		o := occurrences[i]
		switch {
		case o.Status == model.OccPending:
			data.Open = append(data.Open, o)
		case len(data.Recent) < choreListLimit:
			data.Recent = append(data.Recent, o)
		}
	}
	// Open items read oldest-first: the one forgotten longest is the one to
	// answer, and burying it under this morning's would defeat the list.
	sort.Slice(data.Open, func(i, j int) bool { return data.Open[i].Due.Before(data.Open[j].Due) })
	a.render(w, "reminders.html", "Справи", "reminders", data)
}

// reminderRows pairs each chore with the version of its rule in force now —
// what the list shows and what the form opens with.
func (a *App) reminderRows() ([]reminderRow, error) {
	all, err := a.Store.Reminders()
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(all))
	for _, r := range all {
		ids = append(ids, r.ID)
	}
	rules, err := a.Store.RulesForAll(ids)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]reminderRow, 0, len(all))
	for _, r := range all {
		row := reminderRow{Reminder: r}
		if versions := rules[r.ID]; len(versions) > 0 {
			cur := currentRule(versions, now)
			row.RuleID = cur.ID
			row.RuleText = reminders.Describe(cur.RRule)
			_, row.Time = splitLocalDatetime(cur.DTStart)
		}
		out = append(out, row)
	}
	return out, nil
}

// currentRule is the last version whose validFrom has passed, falling back to
// the newest when every version starts in the future. Same choice the Mini App
// makes, for the same reason: the form edits what is in force, not history.
func currentRule(versions []model.ReminderRule, now time.Time) model.ReminderRule {
	current := versions[len(versions)-1]
	for _, v := range versions {
		if from, err := v.ValidFrom(time.Local); err == nil && !from.After(now) {
			current = v
		}
	}
	return current
}

type reminderFormData struct {
	Reminder model.Reminder
	RRule    string
	Date     string
	Time     string
	RuleID   int64
	Presets  []rulePreset
	Preview  []string // the next few occurrences, so a rule can be checked
	RuleText string
	IsEdit   bool
	Today    string
	Error    string
}

type rulePreset struct {
	Label string
	RRule string
}

// presets are the rules worth a single click, the same six the Mini App
// offers. Anything else goes in the raw field beside them: the store keeps a
// full RRULE, so an odd schedule is a typing job, not a missing feature.
var presets = []rulePreset{
	{"щодня", "FREQ=DAILY"},
	{"через день", "FREQ=DAILY;INTERVAL=2"},
	{"щотижня", "FREQ=WEEKLY"},
	{"раз на 2 тижні", "FREQ=WEEKLY;INTERVAL=2"},
	{"щомісяця", "FREQ=MONTHLY"},
	{"останній день місяця", "FREQ=MONTHLY;BYMONTHDAY=-1"},
}

func (a *App) handleReminderNew(w http.ResponseWriter, r *http.Request) {
	if !a.requireReminders(w, r) {
		return
	}
	a.render(w, "reminder_form.html", "Нова справа", "reminders", reminderFormData{
		Reminder: model.Reminder{DurationMin: 15, Active: true},
		RRule:    "FREQ=DAILY",
		Date:     today(),
		Time:     "08:00",
		Presets:  presets,
		Today:    today(),
	})
}

func (a *App) handleReminderEdit(w http.ResponseWriter, r *http.Request) {
	if !a.requireReminders(w, r) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rem, err := a.Store.GetReminder(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	versions, err := a.Store.RulesFor(id)
	if err != nil {
		a.serverError(w, err)
		return
	}
	data := reminderFormData{
		Reminder: rem, Presets: presets, IsEdit: true, Today: today(),
	}
	if len(versions) > 0 {
		cur := currentRule(versions, time.Now())
		data.RRule, data.RuleID = cur.RRule, cur.ID
		data.Date, data.Time = splitLocalDatetime(cur.DTStart)
		data.RuleText = reminders.Describe(cur.RRule)
		data.Preview = a.rulePreview(cur.RRule, cur.DTStart)
	}
	a.render(w, "reminder_form.html", "Справа", "reminders", data)
}

// rulePreview renders the next few occurrences so a rule can be checked before
// it is trusted. Best-effort: a rule that will not expand is reported by the
// save, not by a blank preview.
func (a *App) rulePreview(rrule, dtstart string) []string {
	start, err := time.ParseInLocation(model.LocalDatetime, dtstart, time.Local)
	if err != nil {
		return nil
	}
	next, err := a.Reminders.Preview(rrule, start, 5)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(next))
	for _, t := range next {
		out = append(out, t.Format("02.01.2006, 15:04"))
	}
	return out
}

// reminderForm reads the shared fields. The rule fields are read separately
// because create and edit do different things with them.
func reminderForm(r *http.Request) model.Reminder {
	duration, err := strconv.Atoi(strings.TrimSpace(r.FormValue("durationMin")))
	if err != nil || duration <= 0 {
		duration = 15
	}
	return model.Reminder{
		Title:       strings.TrimSpace(r.FormValue("title")),
		Person:      strings.TrimSpace(r.FormValue("person")),
		DurationMin: duration,
		Note:        strings.TrimSpace(r.FormValue("note")),
		Active:      r.FormValue("active") != "",
	}
}

func (a *App) handleReminderCreate(w http.ResponseWriter, r *http.Request) {
	if !a.requireReminders(w, r) {
		return
	}
	rem := reminderForm(r)
	rrule := strings.TrimSpace(r.FormValue("rrule"))
	date, clock := r.FormValue("date"), r.FormValue("time")

	dtstart, err := parseLocalDatetime(date, clock)
	if err != nil {
		a.renderReminderFormError(w, rem, rrule, date, clock, 0, false, "вкажи дату і час початку")
		return
	}
	if rem.Title == "" {
		a.renderReminderFormError(w, rem, rrule, date, clock, 0, false, "вкажи назву")
		return
	}
	if _, err := a.Reminders.Create(rem, rrule, dtstart, actorName(r)); err != nil {
		a.renderReminderFormError(w, rem, rrule, date, clock, 0, false, ruleMessage(err))
		return
	}
	http.Redirect(w, r, "/reminders", http.StatusSeeOther)
}

// handleReminderUpdate saves the chore's own fields, and — when the rule
// changed — either starts a new version or corrects the current one.
//
// The choice is the person's and has to be asked, because the two mean
// opposite things about the past. "Відсьогодні" leaves what was recorded
// alone; "Виправити" rewrites it, which is right only when the old rule was
// wrong rather than merely old. The Mini App asks the same question, so Save
// cannot mean different things on the two surfaces.
func (a *App) handleReminderUpdate(w http.ResponseWriter, r *http.Request) {
	if !a.requireReminders(w, r) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	existing, err := a.Store.GetReminder(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rem := reminderForm(r)
	rem.ID = id
	rem.ActiveSince = existing.ActiveSince
	rrule := strings.TrimSpace(r.FormValue("rrule"))
	date, clock := r.FormValue("date"), r.FormValue("time")
	ruleID, _ := strconv.ParseInt(r.FormValue("ruleId"), 10, 64)
	amend := r.FormValue("ruleChange") == "amend"

	fail := func(msg string) {
		a.renderReminderFormError(w, rem, rrule, date, clock, ruleID, true, msg)
	}
	if rem.Title == "" {
		fail("вкажи назву")
		return
	}
	dtstart, err := parseLocalDatetime(date, clock)
	if err != nil {
		fail("вкажи дату і час початку")
		return
	}
	if err := a.Reminders.Update(rem, actorName(r)); err != nil {
		a.serverError(w, err)
		return
	}
	// The switch goes through its own call, which stamps the backfill floor —
	// a plain field write would resume a chore without one and invent a month
	// of "you forgot".
	if rem.Active != existing.Active {
		if err := a.Reminders.SetActive(id, rem.Active); err != nil {
			a.serverError(w, err)
			return
		}
	}

	stored, changed, err := a.ruleChanged(id, ruleID, rrule, dtstart)
	if err != nil {
		a.serverError(w, err)
		return
	}
	if !changed {
		http.Redirect(w, r, "/reminders", http.StatusSeeOther)
		return
	}
	if amend {
		// ValidFromAt is carried over untouched: a correction says the text was
		// always wrong, not that the version started at a different moment.
		err = a.Reminders.AmendRule(model.ReminderRule{
			ID: stored.ID, ReminderID: id, ValidFromAt: stored.ValidFromAt,
			DTStart: dtstart.Format(model.LocalDatetime), RRule: rrule,
		})
	} else {
		_, err = a.Reminders.AddRule(id, rrule, dtstart, time.Now())
	}
	if err != nil {
		fail(ruleMessage(err))
		return
	}
	http.Redirect(w, r, "/reminders", http.StatusSeeOther)
}

// ruleChanged returns the stored version and whether the submitted rule
// differs from it. Without the comparison, saving a form where only the title
// changed would stamp a new version every time, and the history would fill
// with versions that say nothing.
//
// The ownership check is not decoration: ruleId comes from the form, and
// amending it would otherwise let one chore's save rewrite another's schedule.
func (a *App) ruleChanged(reminderID, ruleID int64, rrule string, dtstart time.Time) (model.ReminderRule, bool, error) {
	if ruleID == 0 {
		return model.ReminderRule{}, true, nil
	}
	stored, err := a.Store.GetRule(ruleID)
	if err != nil {
		return stored, false, err
	}
	if stored.ReminderID != reminderID {
		return stored, false, errors.New("web: rule does not belong to this reminder")
	}
	changed := stored.RRule != rrule || stored.DTStart != dtstart.Format(model.LocalDatetime)
	return stored, changed, nil
}

func (a *App) handleReminderDelete(w http.ResponseWriter, r *http.Request) {
	if !a.requireReminders(w, r) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	// Soft, like appointments: the occurrence history is the record of what
	// came due, and a hard delete would take it with the chore.
	if err := a.Store.SoftDeleteReminder(id); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/reminders", http.StatusSeeOther)
}

// handleReminderMark closes one occurrence — the thing the dashboard exists to
// let you do at midday without reaching for the phone.
func (a *App) handleReminderMark(w http.ResponseWriter, r *http.Request) {
	if !a.requireReminders(w, r) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	due, err := time.ParseInLocation(model.LocalDatetime, r.FormValue("dueAt"), time.Local)
	if err != nil {
		a.renderReminders(w, "не вдалося зрозуміти, про який момент ідеться")
		return
	}
	status := r.FormValue("status")
	back := r.FormValue("back")

	switch err := a.Reminders.Mark(id, due, status, actorName(r)); {
	case err == nil:
	case errors.Is(err, reminders.ErrFutureMark):
		a.renderReminders(w, "ця справа ще не настала")
		return
	case errors.Is(err, reminders.ErrNoSuchOccurrence):
		a.renderReminders(w, "такої справи на цей момент немає")
		return
	default:
		a.serverError(w, err)
		return
	}
	if back == "" {
		back = "/reminders"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (a *App) renderReminderFormError(w http.ResponseWriter, rem model.Reminder,
	rrule, date, clock string, ruleID int64, isEdit bool, msg string) {
	a.render(w, "reminder_form.html", "Справа", "reminders", reminderFormData{
		Reminder: rem, RRule: rrule, Date: date, Time: clock, RuleID: ruleID,
		Presets: presets, RuleText: reminders.Describe(rrule),
		IsEdit: isEdit, Today: today(), Error: msg,
	})
}

// ruleMessage turns a rejection into something to show next to the field. A
// bad rule is the person's to fix; anything else is ours, and saying "your
// recurrence rule is not understood" about a database failure sends them
// editing a rule that was fine.
func ruleMessage(err error) string {
	var fieldErr valid.FieldError
	switch {
	case errors.As(err, &fieldErr):
		return fieldErr.Message
	case errors.Is(err, recur.ErrEmptyRule):
		return "вкажи правило повторення"
	case errors.Is(err, recur.ErrBadRule):
		return "правило не вдалося розібрати"
	}
	return "не вдалося зберегти"
}

// parseLocalDatetime joins the date and time inputs into the instant a rule
// starts from.
func parseLocalDatetime(date, clock string) (time.Time, error) {
	return time.ParseInLocation(model.LocalDatetime,
		strings.TrimSpace(date)+"T"+strings.TrimSpace(clock), time.Local)
}
