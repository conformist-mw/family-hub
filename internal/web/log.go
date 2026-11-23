package web

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"familyhub/internal/model"
)

// The chronicle is a place to write down that something was done and when —
// what used to go to Telegram's Saved Messages. The page is the form and the
// list together: writing an entry is the common case, so it is not a click
// away, and the list under it is where you look back.

// logForm is the form as typed, kept as text so a rejected value is shown
// back rather than lost.
type logForm struct {
	Date string
	Text string
	Tags string
}

type logPageData struct {
	Entries []model.LogEntry
	Tags    []model.TagCount
	Filter  string // the tag the list is narrowed to, "" for all
	Form    logForm
	Error   string
	Today   string
	Yday    string
}

type logEditData struct {
	ID    int64
	Form  logForm
	Tags  []model.TagCount
	Error string
	Today string
	Yday  string
}

func (a *App) handleLog(w http.ResponseWriter, r *http.Request) {
	filter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tag")))
	// Writing from a filtered list is writing another entry of the same kind,
	// so the tag is already filled in.
	a.renderLog(w, filter, logForm{Date: today(), Tags: filter}, "")
}

func (a *App) renderLog(w http.ResponseWriter, filter string, form logForm, errMsg string) {
	entries, err := a.Store.ListLogEntries(filter)
	if err != nil {
		a.serverError(w, err)
		return
	}
	tags, err := a.Store.LogTags()
	if err != nil {
		a.serverError(w, err)
		return
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	a.render(w, "log.html", "Хроніка", "log", logPageData{
		Entries: entries, Tags: tags, Filter: filter, Form: form, Error: errMsg,
		Today: today(), Yday: daysAgo(1),
	})
}

func (a *App) handleLogCreate(w http.ResponseWriter, r *http.Request) {
	form, filter := readLogForm(r), strings.TrimSpace(r.FormValue("filter"))
	entry, msg := form.parse()
	if msg != "" {
		a.renderLog(w, filter, form, msg)
		return
	}
	if _, err := a.Store.CreateLogEntry(entry); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, logURL(filter), http.StatusSeeOther)
}

func (a *App) handleLogEdit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	e, err := a.Store.GetLogEntry(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, err)
		return
	}
	a.renderLogEdit(w, id, logForm{Date: e.Date, Text: e.Text, Tags: strings.Join(e.Tags, " ")}, "")
}

func (a *App) renderLogEdit(w http.ResponseWriter, id int64, form logForm, errMsg string) {
	tags, err := a.Store.LogTags()
	if err != nil {
		a.serverError(w, err)
		return
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	a.render(w, "log_form.html", "Запис у хроніці", "log", logEditData{
		ID: id, Form: form, Tags: tags, Error: errMsg, Today: today(), Yday: daysAgo(1),
	})
}

func (a *App) handleLogUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	form := readLogForm(r)
	entry, msg := form.parse()
	if msg != "" {
		a.renderLogEdit(w, id, form, msg)
		return
	}
	entry.ID = id
	if err := a.Store.UpdateLogEntry(entry); errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/log", http.StatusSeeOther)
}

func (a *App) handleLogDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := a.Store.DeleteLogEntry(id); errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/log", http.StatusSeeOther)
}

func readLogForm(r *http.Request) logForm {
	r.ParseForm()
	return logForm{
		Date: strings.TrimSpace(r.FormValue("date")),
		Text: strings.TrimSpace(r.FormValue("text")),
		Tags: strings.TrimSpace(r.FormValue("tags")),
	}
}

// parse checks the form and returns the entry, or a message for the person.
func (f logForm) parse() (model.LogEntry, string) {
	if _, err := time.Parse(time.DateOnly, f.Date); err != nil {
		return model.LogEntry{}, "вкажіть дату"
	}
	if f.Text == "" {
		return model.LogEntry{}, "напишіть, що зроблено"
	}
	return model.LogEntry{Date: f.Date, Text: f.Text, Tags: model.ParseTags(f.Tags)}, ""
}

func logURL(tag string) string {
	if tag == "" {
		return "/log"
	}
	return "/log?tag=" + url.QueryEscape(tag)
}
