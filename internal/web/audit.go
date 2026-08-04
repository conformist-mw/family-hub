package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"familyhub/internal/audit"
	"familyhub/internal/model"
)

// Notifier posts a plain-text message to the family group. The bot
// implements it; web stays free of telebot. Nil means the bot is off and
// the send button is hidden.
type Notifier interface {
	NotifyText(text string) error
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

// resolveAuditRange turns the range/from/to query params into concrete
// bounds. Defaults to "с последней оплаты"; an enrollment with no payments
// falls back to all-time. Only a custom range may extend into the future —
// that is how the forecast is reached.
func (a *App) resolveAuditRange(enrollmentID int64, q url.Values) (rng, from, to, label string, err error) {
	td := today()
	rng = q.Get("range")
	if rng == "" {
		rng = "last_payment"
	}
	switch rng {
	case "last_payment":
		lp, lerr := a.Store.LastPaymentDate(enrollmentID)
		if lerr != nil {
			return "", "", "", "", lerr
		}
		if lp == "" {
			rng = "all"
			return rng, "", td, "за всё время", nil
		}
		return rng, lp, td, "с последней оплаты (" + ruDate(lp) + ")", nil
	case "month":
		from = time.Now().Format("2006-01") + "-01"
		return rng, from, td, "этот месяц", nil
	case "all":
		return rng, "", td, "за всё время", nil
	case "custom":
		from, to = q.Get("from"), q.Get("to")
		if _, e1 := model.ParseDate(from); e1 != nil {
			return rng, "", "", "", fmt.Errorf("укажи корректные даты")
		}
		if _, e2 := model.ParseDate(to); e2 != nil {
			return rng, "", "", "", fmt.Errorf("укажи корректные даты")
		}
		if from > to {
			return rng, "", "", "", fmt.Errorf("начало периода позже конца")
		}
		return rng, from, to, ruDate(from) + " — " + ruDate(to), nil
	default:
		return "", "", "", "", fmt.Errorf("неизвестный период")
	}
}

// buildAuditPage assembles everything the page and the text version share.
func (a *App) buildAuditPage(enrollmentID int64, q url.Values) (auditPageData, error) {
	e, err := a.Store.GetEnrollment(enrollmentID)
	if err != nil {
		return auditPageData{}, err
	}
	data := auditPageData{E: e, PerLesson: e.BillingType != model.BillingMonthly, Statuses: statusOptions}

	rng, from, to, label, err := a.resolveAuditRange(enrollmentID, q)
	if err != nil {
		// Bad custom input: keep the page usable on the default period.
		data.Error = err.Error()
		rng, from, to, label, err = a.resolveAuditRange(enrollmentID, url.Values{})
		if err != nil {
			return data, err
		}
	}
	data.Range, data.From, data.To = rng, from, to
	td := today()
	data.PeriodLabel = label
	if rng != "custom" { // the custom label already carries both dates
		data.PeriodLabel += " по " + ruDate(to)
	}

	d, err := a.Store.AuditData(enrollmentID, from, to)
	if err != nil {
		return data, err
	}
	data.Rows, data.Summary = audit.BuildLedger(d)

	if to > td {
		coversUntil := ""
		if e.BillingType == model.BillingMonthly {
			bal, berr := a.Store.BalanceFor(enrollmentID)
			if berr != nil {
				return data, berr
			}
			coversUntil = bal.CoversUntil
		}
		hasToday, herr := a.Store.VisitExistsForDate(enrollmentID, td)
		if herr != nil {
			return data, herr
		}
		data.Forecast = audit.BuildForecast(e, d.Slots, d.Absences,
			data.Summary.Closing, coversUntil, td, to, hasToday)
	}

	data.Text = audit.RenderText(audit.View{
		Title:       e.Person + " · " + e.Name,
		PeriodLabel: data.PeriodLabel,
		BillingType: e.BillingType,
		Rows:        data.Rows,
		Summary:     data.Summary,
		Forecast:    data.Forecast,
	})
	data.CanSend = a.Notifier != nil
	return data, nil
}

func (a *App) handleAudit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	data, err := a.buildAuditPage(id, r.URL.Query())
	if err != nil {
		a.serverError(w, err)
		return
	}
	data.Sent = r.URL.Query().Get("sent") == "1"
	a.render(w, "audit.html", "Сверка", "enrollments", data)
}

// handleAuditSend recomputes the same text the page shows and pushes it to
// the family group via the bot, then bounces back with a flash flag.
func (a *App) handleAuditSend(w http.ResponseWriter, r *http.Request) {
	if a.Notifier == nil {
		http.Error(w, "бот выключен", http.StatusServiceUnavailable)
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
	http.Redirect(w, r, "/enrollments/"+strconv.FormatInt(id, 10)+"/audit?"+back.Encode(), http.StatusSeeOther)
}

func ruDate(s string) string {
	t, err := model.ParseDate(s)
	if err != nil {
		return s
	}
	return t.Format("02.01.2006")
}
