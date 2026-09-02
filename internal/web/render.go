package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/reminders"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// monthNames is the nominative form a heading reads in ("серпень 2026"),
// unlike the genitive a date takes ("1 вересня").
var monthNames = [12]string{"січень", "лютий", "березень", "квітень", "травень",
	"червень", "липень", "серпень", "вересень", "жовтень", "листопад", "грудень"}

var funcs = template.FuncMap{
	// num renders an optional meter figure for an input's value. A nil pointer
	// is an unread meter, and the template's own formatting prints that as the
	// literal "<nil>" — which lands in the form field and is then submitted.
	"add": func(a, b float64) float64 { return a + b },
	"num": func(p *float64) string {
		if p == nil {
			return ""
		}
		return strconv.FormatFloat(*p, 'f', -1, 64)
	},
	// amount is money in a stated currency. The utilities world is the only
	// place a currency is stored per row rather than assumed, so it cannot use
	// the hard-coded symbol below.
	"amount": amountIn,
	// tariffKind names how a tariff calculates, which is what tells a reader
	// why one row has meter numbers and the next has only a sum.
	"tariffKind": func(kind string) string {
		switch kind {
		case model.KindMeter:
			return "лічильник"
		case model.KindMeterZoned:
			return "двозонний"
		case model.KindFlat:
			return "фіксований"
		}
		return kind
	},
	// periodLabel turns "2026-08" into "серпень 2026" for a heading; the raw
	// form stays in URLs and inputs.
	"periodLabel": periodLabelOf,
	"money": func(v float64) string {
		if v == math.Trunc(v) {
			return fmt.Sprintf("%.0f ₴", v)
		}
		return fmt.Sprintf("%.2f ₴", v)
	},
	// choreWhen dates an occurrence the way the chore lists read it: the day
	// and the time, without a year nobody needs on a 30-day window.
	"choreWhen": func(t time.Time) string {
		return t.Format("02.01, 15:04")
	},
	// choreDueAt is the instant as the Mark form posts it back — the identity
	// of an occurrence is the whole datetime, because a rule can put two on
	// one day.
	"choreDueAt": func(t time.Time) string {
		return t.Format(model.LocalDatetime)
	},
	// choreMark is the glyph the calendar and the bot already use, so the three
	// surfaces read as one app.
	"choreMark": func(o reminders.Occurrence) string {
		switch o.Status {
		case model.OccDone:
			return "✓"
		case model.OccSkipped:
			return "✗"
		}
		if o.Due.After(time.Now()) {
			return "·"
		}
		return "○"
	},
	// choreStatusLabel separates "you forgot" from "not yet", which is the
	// difference between a record and a shaming wall.
	"choreStatusLabel": func(o reminders.Occurrence) string {
		switch o.Status {
		case model.OccDone:
			return "закрито"
		case model.OccSkipped:
			return "не треба було"
		}
		if o.Due.After(time.Now()) {
			return "ще не настало"
		}
		return "не закрито"
	},
	"choreDate": func(t time.Time) string {
		return t.Format("02.01.2006")
	},
	// choreOftenMissed flags a chore worth moving, dropping or handing to
	// somebody else. Half of what came due, and more than a couple of times —
	// a single miss is a bad week, not a habit.
	"choreOftenMissed": func(t reminders.Tally) bool {
		return t.Missed >= 3 && t.MissRate() >= 0.5
	},
	"weekday": func(w int) string {
		if w < 0 || w > 6 {
			return "?"
		}
		return model.WeekdayLabels[w]
	},
	"statusLabel": func(s string) string {
		if l, ok := model.StatusLabels[s]; ok {
			return l
		}
		return s
	},
	"dateShort": func(s string) string {
		t, err := model.ParseDate(s)
		if err != nil {
			return s
		}
		return t.Format("02.01.2006")
	},
	"billingLabel": func(b string) string {
		if b == model.BillingMonthly {
			return "абонемент"
		}
		return "за заняття"
	},
	// costLabel renders an optional amount: "" when nothing was recorded, so
	// the column stays visually empty rather than showing a fake 0 ₴.
	"costLabel": func(c *float64) string {
		if c == nil {
			return ""
		}
		if *c == math.Trunc(*c) {
			return fmt.Sprintf("%.0f ₴", *c)
		}
		return fmt.Sprintf("%.2f ₴", *c)
	},
	"apptStatusLabel": func(s string) string {
		if l, ok := model.ApptStatusLabels[s]; ok {
			return l
		}
		return s
	},
	// apptWhen renders a stored LocalDatetime as "07.08, 19:06"; apptTime is
	// just the "19:06" half (end times, where the date is implied).
	"apptWhen": func(s string) string {
		t, err := time.ParseInLocation(model.LocalDatetime, s, time.Local)
		if err != nil {
			return s
		}
		return t.Format("02.01.2006, 15:04")
	},
	"apptTime": func(s string) string {
		t, err := time.ParseInLocation(model.LocalDatetime, s, time.Local)
		if err != nil {
			return ""
		}
		return t.Format("15:04")
	},
	"absenceKind": func(k string) string {
		if l, ok := model.AbsenceKindLabels[k]; ok {
			return l
		}
		return k
	},
	"deref": func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	},
	"deref64": func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	},
	"classLabel": func(name, desc string) string {
		if desc == "" {
			return name
		}
		return name + " (" + desc + ")"
	},
	"barPct": func(v, max float64) int {
		if max <= 0 {
			return 0
		}
		p := int(v / max * 100)
		if p < 2 && v > 0 {
			p = 2
		}
		return p
	},
	"monthShort": func(ym string) string {
		if len(ym) != 7 {
			return ym
		}
		y := ym[:4]
		var m int
		fmt.Sscanf(ym[5:], "%d", &m)
		if m < 1 || m > 12 {
			return ym
		}
		return model.MonthsShort[m] + " " + y
	},
	"addedAt": func(s string) string {
		if s == "" {
			return ""
		}
		// new rows: SQLite datetime('now') in UTC; imported rows: RFC3339 with offset
		for _, l := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
			if t, err := time.Parse(l, s); err == nil {
				return t.Local().Format("02.01 15:04")
			}
		}
		return s
	},
}

// spaceOf maps a page's Active tab to the world it belongs to. The navigation
// has two tiers — the header picks a world, the world picks a page — and this
// is what relates them.
//
// Derived rather than passed: making Space a second argument to render would
// mean editing two dozen call sites to restate something every one of them
// already implies. A page's world is a property of the page, not of the
// request that reached it.
//
// A tab that is not here belongs to the hub, which is the right answer for the
// hub's own pages and a visible one (no world highlighted) for a tab somebody
// forgot to add.
var spaceOf = map[string]string{
	"balance":     "lessons",
	"visits":      "lessons",
	"payments":    "lessons",
	"enrollments": "lessons",
	"trainers":    "lessons",

	"readings":  "meters",
	"tariffs":   "meters",
	"utilities": "meters",
	"addresses": "meters",
	"report":    "meters",

	"stats":         "stats",
	"stats_lessons": "stats",
	"stats_meters":  "stats",
}

type pageData struct {
	Title  string
	Active string
	// Space is the world Active belongs to; see spaceOf. "hub" covers the
	// hub itself and the two screens that live in the shell with it.
	Space string
	Flash string
	Data  any
}

func staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

func parseTemplates() map[string]*template.Template {
	pages := []string{
		"hub.html",
		"dashboard.html",
		"visits.html",
		"visit_form.html",
		"appointments.html",
		"appointment_form.html",
		"payments.html",
		"payment_form.html",
		"reminders.html",
		"reminder_form.html",
		"chore_history.html",
		"enrollments.html",
		"enrollment_form.html",
		"stats.html",
		"stats_overview.html",
		"stats_meters.html",
		"meters_readings.html",
		"meters_report.html",
		"reading_form.html",
		"address_form.html",
		"utility_form.html",
		"tariff_form.html",
		"meters_tariffs.html",
		"meters_utilities.html",
		"meters_addresses.html",
		"trainers.html",
		"audit.html",
	}
	m := map[string]*template.Template{}
	for _, p := range pages {
		t := template.New("base.html").Funcs(funcs)
		t = template.Must(t.ParseFS(templateFS, "templates/base.html", "templates/"+p))
		m[p] = t
	}
	return m
}

func (a *App) render(w http.ResponseWriter, page, title, active string, data any) {
	tmpl, ok := a.templates[page]
	if !ok {
		http.Error(w, "unknown template: "+page, http.StatusInternalServerError)
		return
	}
	space := spaceOf[active]
	if space == "" {
		space = "hub"
	}
	pd := pageData{Title: title, Active: active, Space: space, Data: data}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base", pd); err != nil {
		a.Logger.Error("render", "page", page, "err", err)
	}
}

// renderBare renders a page without the shell — no header, no world
// navigation, no flash. The report exists to be screenshotted into a chat, and
// a screenshot of a page wrapped in navigation is mostly navigation.
func (a *App) renderBare(w http.ResponseWriter, page, title string, data any) {
	tmpl, ok := a.templates[page]
	if !ok {
		http.Error(w, "unknown template: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "bare", pageData{Title: title, Data: data}); err != nil {
		a.Logger.Error("render", "page", page, "err", err)
	}
}

func today() string {
	return time.Now().Format("2006-01-02")
}

func daysAgo(n int) string {
	return time.Now().AddDate(0, 0, -n).Format("2006-01-02")
}

func isValidStatus(s string) bool {
	_, ok := model.StatusLabels[s]
	return ok
}

func normalizeName(s string) string {
	return strings.TrimSpace(s)
}

// amountIn and periodLabelOf are functions rather than closures in the map
// because the report sent to the family group has to format a sum and a month
// exactly as the page does — two spellings of the same figure in two places is
// how they drift.
func amountIn(v float64, currency string) string {
	sym := model.CurrencySymbol(currency)
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f %s", v, sym)
	}
	return fmt.Sprintf("%.2f %s", v, sym)
}

func periodLabelOf(period string) string {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return period
	}
	return monthNames[t.Month()-1] + " " + t.Format("2006")
}
