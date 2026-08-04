package web

import (
	"database/sql"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

type App struct {
	DB        *sql.DB
	Store     *store.Store
	Logger    *slog.Logger
	Notifier  Notifier // nil — bot disabled, send-to-group hidden
	templates map[string]*template.Template
}

func NewRouter(db *sql.DB, logger *slog.Logger, webhookPath string, webhook http.Handler, notifier Notifier) http.Handler {
	a := &App{
		DB:        db,
		Store:     store.New(db),
		Logger:    logger,
		Notifier:  notifier,
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

	mux.HandleFunc("GET /appointments", a.handleAppointments)
	mux.HandleFunc("GET /appointments/new", a.handleAppointmentNew)
	mux.HandleFunc("POST /appointments", a.handleAppointmentCreate)
	mux.HandleFunc("GET /appointments/{id}/edit", a.handleAppointmentEdit)
	mux.HandleFunc("POST /appointments/{id}", a.handleAppointmentUpdate)
	mux.HandleFunc("POST /appointments/{id}/delete", a.handleAppointmentDelete)

	mux.HandleFunc("GET /payments", a.handlePayments)
	mux.HandleFunc("GET /payments/new", a.handlePaymentNew)
	mux.HandleFunc("POST /payments", a.handlePaymentCreate)
	mux.HandleFunc("GET /payments/{id}/edit", a.handlePaymentEdit)
	mux.HandleFunc("POST /payments/{id}", a.handlePaymentUpdate)
	mux.HandleFunc("POST /payments/{id}/delete", a.handlePaymentDelete)

	mux.HandleFunc("GET /stats", a.handleStats)

	mux.HandleFunc("GET /trainers", a.handleTrainers)
	mux.HandleFunc("POST /trainers/{id}/absences", a.handleAbsenceCreate)
	mux.HandleFunc("POST /trainers/{id}/absences/{absenceId}/delete", a.handleAbsenceDelete)

	mux.HandleFunc("GET /enrollments", a.handleEnrollments)
	mux.HandleFunc("GET /enrollments/new", a.handleEnrollmentNew)
	mux.HandleFunc("POST /enrollments", a.handleEnrollmentCreate)
	mux.HandleFunc("GET /enrollments/{id}/edit", a.handleEnrollmentEdit)
	mux.HandleFunc("POST /enrollments/{id}", a.handleEnrollmentUpdate)
	mux.HandleFunc("POST /enrollments/{id}/delete", a.handleEnrollmentDelete)
	mux.HandleFunc("POST /enrollments/{id}/slots", a.handleSlotCreate)
	mux.HandleFunc("POST /enrollments/{id}/slots/{slotId}/delete", a.handleSlotDelete)
	mux.HandleFunc("GET /enrollments/{id}/audit", a.handleAudit)
	mux.HandleFunc("POST /enrollments/{id}/audit/send", a.handleAuditSend)

	return csrfGuard(webhookPath, logger, mux)
}

// csrfGuard rejects cross-site POSTs. Auth is an oauth2-proxy cookie, so
// until now a malicious page could ride it with a form POST (mitigated only
// by the browser's default SameSite=Lax — accidental, not designed). Modern
// browsers stamp Sec-Fetch-Site on every request; older ones send Origin on
// cross-origin POSTs. Requests with neither header (curl, Telegram) pass —
// CSRF is a browser attack, and the Telegram webhook (skipped explicitly)
// carries its own secret-token check.
func csrfGuard(webhookPath string, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && (webhookPath == "" || r.URL.Path != webhookPath) && !sameOriginPost(r) {
			logger.Warn("web: cross-site POST rejected", "path", r.URL.Path,
				"origin", r.Header.Get("Origin"), "sec_fetch_site", r.Header.Get("Sec-Fetch-Site"))
			http.Error(w, "запрос отклонён (cross-site)", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOriginPost(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none": // own pages / direct address-bar navigation
		return true
	case "":
		// No fetch metadata (older browser or non-browser client) — fall
		// back to the Origin header below.
	default: // "cross-site" or "same-site" (another subdomain) — reject both
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

const (
	dashboardPayments     = 8
	dashboardAppointments = 5
)

type dashboardData struct {
	Balances     []model.Balance
	Absences     map[int64]*model.TrainerAbsence // enrollment id → absence covering today
	Schedule     map[int64]string                // enrollment id → "Пн 18:00 · Чт 18:00"
	Payments     []model.Payment                 // most recent, for the table under the cards
	Appointments []model.Appointment             // next few, beside the payments table
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	balances, err := a.Store.Balances()
	if err != nil {
		a.serverError(w, err)
		return
	}
	absences, err := a.Store.ActiveAbsenceByEnrollment(today())
	if err != nil {
		a.serverError(w, err)
		return
	}
	slots, err := a.Store.AllActiveSlots()
	if err != nil {
		a.serverError(w, err)
		return
	}
	// Slots arrive ordered Sunday-first (Go weekday codes); the card line
	// should read Monday-first, so take weekdays 1..6 and then 0.
	schedule := make(map[int64]string, len(balances))
	for _, wd := range []int{1, 2, 3, 4, 5, 6, 0} {
		for _, s := range slots {
			if s.Slot.Weekday != wd {
				continue
			}
			line := model.WeekdayLabels[wd] + " " + s.Slot.Time
			if cur := schedule[s.Enrollment.ID]; cur != "" {
				line = cur + " · " + line
			}
			schedule[s.Enrollment.ID] = line
		}
	}
	payments, err := a.Store.ListPayments(store.PaymentFilter{Limit: dashboardPayments})
	if err != nil {
		a.serverError(w, err)
		return
	}
	appointments, err := a.Store.UpcomingAppointments(
		time.Now().Format(model.LocalDatetime), dashboardAppointments)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "dashboard.html", "Баланс", "dashboard", dashboardData{
		Balances: balances, Absences: absences, Schedule: schedule,
		Payments: payments, Appointments: appointments,
	})
}

func (a *App) serverError(w http.ResponseWriter, err error) {
	a.Logger.Error("server error", "err", err)
	http.Error(w, "внутренняя ошибка", http.StatusInternalServerError)
}
