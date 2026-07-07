package web

import (
	"database/sql"
	"html/template"
	"log/slog"
	"net/http"

	"lessons/internal/store"
)

type App struct {
	DB        *sql.DB
	Store     *store.Store
	Logger    *slog.Logger
	templates map[string]*template.Template
}

func NewRouter(db *sql.DB, logger *slog.Logger, webhookPath string, webhook http.Handler) http.Handler {
	a := &App{
		DB:        db,
		Store:     store.New(db),
		Logger:    logger,
		templates: parseTemplates(),
	}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", staticHandler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	if webhook != nil && webhookPath != "" {
		mux.Handle("POST "+webhookPath, webhook)
	}
	mux.HandleFunc("GET /calendar.ics", a.handleCalendarICS)
	mux.HandleFunc("GET /{$}", a.handleDashboard)

	mux.HandleFunc("GET /visits", a.handleVisits)
	mux.HandleFunc("GET /visits/new", a.handleVisitNew)
	mux.HandleFunc("POST /visits", a.handleVisitCreate)
	mux.HandleFunc("GET /visits/{id}/edit", a.handleVisitEdit)
	mux.HandleFunc("POST /visits/{id}", a.handleVisitUpdate)
	mux.HandleFunc("POST /visits/{id}/delete", a.handleVisitDelete)

	mux.HandleFunc("GET /payments", a.handlePayments)
	mux.HandleFunc("GET /payments/new", a.handlePaymentNew)
	mux.HandleFunc("POST /payments", a.handlePaymentCreate)
	mux.HandleFunc("GET /payments/{id}/edit", a.handlePaymentEdit)
	mux.HandleFunc("POST /payments/{id}", a.handlePaymentUpdate)
	mux.HandleFunc("POST /payments/{id}/delete", a.handlePaymentDelete)

	mux.HandleFunc("GET /stats", a.handleStats)

	mux.HandleFunc("GET /enrollments", a.handleEnrollments)
	mux.HandleFunc("GET /enrollments/new", a.handleEnrollmentNew)
	mux.HandleFunc("POST /enrollments", a.handleEnrollmentCreate)
	mux.HandleFunc("GET /enrollments/{id}/edit", a.handleEnrollmentEdit)
	mux.HandleFunc("POST /enrollments/{id}", a.handleEnrollmentUpdate)
	mux.HandleFunc("POST /enrollments/{id}/delete", a.handleEnrollmentDelete)
	mux.HandleFunc("POST /enrollments/{id}/slots", a.handleSlotCreate)
	mux.HandleFunc("POST /enrollments/{id}/slots/{slotId}/delete", a.handleSlotDelete)

	return mux
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	balances, err := a.Store.Balances()
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "dashboard.html", "Баланс", "dashboard", balances)
}

func (a *App) serverError(w http.ResponseWriter, err error) {
	a.Logger.Error("server error", "err", err)
	http.Error(w, "внутренняя ошибка", http.StatusInternalServerError)
}
