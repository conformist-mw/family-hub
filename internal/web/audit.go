package web

import (
	"net/http"
	"net/url"
	"strconv"

	"familyhub/internal/audit"
	"familyhub/internal/model"
)

// Notifier posts a message to the family group. The bot implements it; web
// stays free of telebot. Nil means the bot is off: the audit page's send button
// is hidden and appointment writes go unannounced.
//
// NotifyHTML is what appointments.Service needs — this interface is the superset
// the whole web surface uses, and it satisfies that one by having its method.
type Notifier interface {
	NotifyText(text string) error
	NotifyHTML(text string) error
}

type auditPageData struct {
	E           model.Enrollment
	Range       string
	From, To    string // effective period bounds (custom form inputs)
	PeriodLabel string
	Rows        []audit.Row
	Summary     audit.Summary
	Forecast    audit.Forecast
	PerLesson   bool
	Statuses    []statusOption
	Text        string
	CanSend     bool
	Sent        bool
	Error       string
}

// buildAuditPage asks audit.Service for the reconciliation and dresses it for
// this template. The period rules and the reads live there, shared with the
// Mini App, so the two screens cannot end up disagreeing about what "з
// останньої оплати" means.
func (a *App) buildAuditPage(enrollmentID int64, q url.Values) (auditPageData, error) {
	page, err := a.Audit.Build(enrollmentID, audit.Params{
		Range: q.Get("range"), From: q.Get("from"), To: q.Get("to"),
	})
	if err != nil {
		return auditPageData{}, err
	}
	return auditPageData{
		E:           page.Enrollment,
		Range:       page.Range,
		From:        page.From,
		To:          page.To,
		PeriodLabel: page.PeriodLabel,
		Rows:        page.Rows,
		Summary:     page.Summary,
		Forecast:    page.Forecast,
		PerLesson:   page.PerLesson,
		Statuses:    statusOptions,
		Text:        page.Text,
		CanSend:     a.Notifier != nil,
		Error:       page.Error,
	}, nil
}

func (a *App) handleAudit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	data, err := a.buildAuditPage(id, r.URL.Query())
	if err != nil {
		a.serverError(w, err)
		return
	}
	data.Sent = r.URL.Query().Get("sent") == "1"
	a.render(w, "audit.html", "Звірка", "enrollments", data)
}

// handleAuditSend recomputes the same text the page shows and pushes it to
// the family group via the bot, then bounces back with a flash flag.
func (a *App) handleAuditSend(w http.ResponseWriter, r *http.Request) {
	if a.Notifier == nil {
		http.Error(w, "бот вимкнений", http.StatusServiceUnavailable)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := r.ParseForm(); err != nil {
		a.serverError(w, err)
		return
	}
	data, err := a.buildAuditPage(id, url.Values(r.PostForm))
	if err != nil {
		a.serverError(w, err)
		return
	}
	if err := a.Notifier.NotifyText(data.Text); err != nil {
		a.serverError(w, err)
		return
	}
	back := url.Values{"range": {data.Range}, "sent": {"1"}}
	if data.Range == "custom" {
		back.Set("from", data.From)
		back.Set("to", data.To)
	}
	http.Redirect(w, r, "/lessons/enrollments/"+strconv.FormatInt(id, 10)+"/audit?"+back.Encode(), http.StatusSeeOther)
}

func dateShort(s string) string {
	t, err := model.ParseDate(s)
	if err != nil {
		return s
	}
	return t.Format("02.01.2006")
}
