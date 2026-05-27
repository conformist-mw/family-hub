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

	"lessons/internal/model"
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
	"dateRu": func(s string) string {
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
		return "за занятие"
	},
	"deref": func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	},
	"classLabel": func(name, desc string) string {
		if desc == "" {
			return name
		}
		return name + " (" + desc + ")"
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
		"payments.html",
		"payment_form.html",
		"enrollments.html",
		"enrollment_form.html",
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
