package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"strings"
	"time"

	"familyhub/internal/model"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

var funcs = template.FuncMap{
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

type pageData struct {
	Title  string
	Active string
	Flash  string
	Data   any
}

func staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

func parseTemplates() map[string]*template.Template {
	pages := []string{
		"dashboard.html",
		"visits.html",
		"visit_form.html",
		"appointments.html",
		"appointment_form.html",
		"payments.html",
		"payment_form.html",
		"reminders.html",
		"reminder_form.html",
		"enrollments.html",
		"enrollment_form.html",
		"stats.html",
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
	pd := pageData{Title: title, Active: active, Data: data}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base", pd); err != nil {
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
