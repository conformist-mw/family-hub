package mini

import (
	"net/http"
	"strconv"

	"familyhub/internal/audit"
	"familyhub/internal/model"
)

// The reconciliation — "what was paid, what happened, what is left" for one
// course — was the last thing that only the web had. It is also the thing most
// likely to be wanted mid-conversation, standing in a corridor after a lesson,
// which is precisely where the browser behind oauth is not.
//
// Everything is formatted here. The client renders strings and never parses a
// date, the same rule the rest of this API follows.

type auditRowDTO struct {
	Date   string `json:"date"`   // "16 сер"
	Kind   string `json:"kind"`   // payment | visit | future
	Status string `json:"status"` // visit rows, for the pill: done | cancelled | …
	Label  string `json:"label"`  // "оплата +10", "проведено", "за розкладом"
	// Amount is the money column, payments only; Balance is the running
	// per-lesson balance, empty on a monthly course where counting lessons
	// means nothing.
	Amount  string `json:"amount"`
	Balance string `json:"balance"`
	Comment string `json:"comment"`
	Covered bool   `json:"covered"` // future rows: is it already paid for
}

type auditDTO struct {
	Course string `json:"course"`
	Person string `json:"person"`
	Period string `json:"period"` // "з останньої оплати (01.08.2026) по 16.08.2026"
	Range  string `json:"range"`
	From   string `json:"from"`
	To     string `json:"to"`
	// Summary and Forecast are ready-made lines: what the web draws as a header
	// strip, which on a phone is a stack of short sentences.
	Summary  []string      `json:"summary"`
	Forecast []string      `json:"forecast"`
	Rows     []auditRowDTO `json:"rows"`
	// Notice says the asked-for period was rejected and the default one is on
	// screen instead. Not the API's error contract — the payload is valid.
	Notice string `json:"notice"`
}

func (rt *Router) handleAudit(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	if _, err := rt.store.GetEnrollment(id); err != nil {
		rt.fail(w, errNotFound)
		return
	}
	q := r.URL.Query()
	page, err := rt.audit.Build(id, audit.Params{
		Range: q.Get("range"), From: q.Get("from"), To: q.Get("to"),
	})
	if err != nil {
		rt.log.Error("mini: audit", "err", err, "enrollment", id)
		rt.fail(w, errInternal)
		return
	}
	rt.writeJSON(w, http.StatusOK, auditPage(page))
}

func auditPage(page audit.Page) auditDTO {
	e := page.Enrollment
	dto := auditDTO{
		Course:   e.Name,
		Person:   e.Person,
		Period:   page.PeriodLabel,
		Range:    page.Range,
		From:     page.From,
		To:       page.To,
		Summary:  auditSummary(page),
		Forecast: auditForecast(page),
		Rows:     auditRows(page),
		Notice:   page.Error,
	}
	if e.Description != "" {
		dto.Course += " · " + e.Description
	}
	return dto
}

// statusOrder is the web's order, so the two summaries read alike.
var statusOrder = []string{
	model.StatusDone, model.StatusRescheduled, model.StatusCancelled, model.StatusSkipped,
}

func auditSummary(page audit.Page) []string {
	out := make([]string, 0, 6)
	for _, st := range statusOrder {
		if n := page.Summary.ByStatus[st]; n > 0 {
			out = append(out, strconv.Itoa(n)+" "+model.StatusLabels[st])
		}
	}
	if page.Summary.PaidAmount > 0 || page.Summary.PaidLessons > 0 {
		line := "оплачено: "
		if page.PerLesson {
			line += model.Plural(page.Summary.PaidLessons, "заняття", "заняття", "занять") + " · "
		}
		out = append(out, line+money(page.Summary.PaidAmount))
	}
	if page.PerLesson {
		out = append(out, "залишок: "+strconv.Itoa(page.Summary.Opening)+" → "+strconv.Itoa(page.Summary.Closing))
	}
	return out
}

func auditForecast(page audit.Page) []string {
	f := page.Forecast
	out := make([]string, 0, 2)
	if f.PaidThrough != "" {
		out = append(out, "оплаченого вистачить до "+shortDate(f.PaidThrough))
	}
	if f.TopUpCount > 0 {
		unit := "зан."
		if !page.PerLesson {
			unit = "міс."
		}
		line := "доплатити: " + strconv.Itoa(f.TopUpCount) + " " + unit +
			" × " + money(f.TopUpAmount/float64(f.TopUpCount)) + " = " + money(f.TopUpAmount)
		if f.Debt > 0 {
			line += " (борг " + strconv.Itoa(f.Debt) + " + наперед " + strconv.Itoa(f.Uncovered) + ")"
		}
		out = append(out, line)
	}
	return out
}

func auditRows(page audit.Page) []auditRowDTO {
	rows := make([]auditRowDTO, 0, len(page.Rows)+len(page.Forecast.Rows))
	for _, r := range page.Rows {
		rows = append(rows, auditRow(r, page.PerLesson))
	}
	for _, r := range page.Forecast.Rows {
		rows = append(rows, auditRow(r, page.PerLesson))
	}
	return rows
}

func auditRow(r audit.Row, perLesson bool) auditRowDTO {
	row := auditRowDTO{
		Date:    shortDate(r.Date),
		Kind:    r.Kind,
		Status:  r.Status,
		Comment: r.Comment,
		Covered: r.Covered,
	}
	switch r.Kind {
	case audit.KindPayment:
		row.Amount = money(r.Amount)
		switch {
		case r.Lessons > 0:
			row.Label = "оплата +" + strconv.Itoa(r.Lessons)
		case r.Covers != "":
			row.Label = "абонемент до " + shortDate(r.Covers)
		default:
			row.Label = "оплата"
		}
	case audit.KindFuture:
		row.Label = "за розкладом"
		if !r.Covered {
			row.Label += " · не оплачено"
		}
		// A future row carries no balance: it has not happened, and showing the
		// number from the row above it would read as a fact.
		return row
	default:
		row.Label = model.StatusLabels[r.Status]
	}
	if perLesson {
		row.Balance = strconv.Itoa(r.Balance)
	}
	return row
}
