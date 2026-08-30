package web

import (
	"database/sql"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"familyhub/internal/appointments"
	"familyhub/internal/audit"
	"familyhub/internal/model"
	"familyhub/internal/payments"
	"familyhub/internal/reminders"
	"familyhub/internal/store"
)

type App struct {
	DB     *sql.DB
	Store  *store.Store
	Logger *slog.Logger
	// Appointments holds the write rules and the group notification shared with
	// the Mini App, so the two surfaces cannot drift on either.
	Appointments *appointments.Service
	// Payments is shared with the Mini App for the same reason: what a payment
	// for a monthly course must carry is one rule, not one per form.
	Payments *payments.Service
	// Audit builds the reconciliation both surfaces show.
	Audit *audit.Service
	// Reminders feeds recurring chores into the ICS feed. Shared with the Mini
	// App and the bot; nil means the feed simply carries none.
	Reminders *reminders.Service
	Notifier  Notifier // nil — bot disabled, send-to-group hidden
	templates map[string]*template.Template
}

func NewRouter(db *sql.DB, logger *slog.Logger, webhookPath string, webhook http.Handler,
	notifier Notifier, chores *reminders.Service) http.Handler {
	st := store.New(db)
	a := &App{
		DB:     db,
		Store:  st,
		Logger: logger,
		// time.Local is the zone appointments are written in — TZ comes from the
		// container env, the same source the bot and the Mini App read.
		Appointments: appointments.NewService(st, time.Local, notifier, logger),
		Payments:     payments.NewService(st, notifier, logger),
		Audit:        audit.NewService(st, time.Now),
		Reminders:    chores,
		Notifier:     notifier,
		templates:    parseTemplates(),
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

	mux.HandleFunc("GET /reminders", a.handleReminders)
	mux.HandleFunc("GET /reminders/new", a.handleReminderNew)
	mux.HandleFunc("GET /reminders/history", a.handleReminderHistory)
	mux.HandleFunc("GET /reminders/{id}/history", a.handleChoreHistory)
	mux.HandleFunc("POST /reminders", a.handleReminderCreate)
	mux.HandleFunc("GET /reminders/{id}/edit", a.handleReminderEdit)
	mux.HandleFunc("POST /reminders/{id}", a.handleReminderUpdate)
	mux.HandleFunc("POST /reminders/{id}/delete", a.handleReminderDelete)
	mux.HandleFunc("POST /reminders/{id}/mark", a.handleReminderMark)

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
			http.Error(w, "запит відхилено (cross-site)", http.StatusForbidden)
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
	// OpenChores came due today and nobody answered. Opening the app at midday
	// used to say nothing about the cashback forgotten at 08:00, which is the
	// one thing the dashboard is well placed to say.
	OpenChores []reminders.Occurrence
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
	// Today only, and only what came due: yesterday's unanswered chore is the
	// chores page's business, and tonight's is not yet anybody's.
	var openChores []reminders.Occurrence
	if a.Reminders != nil {
		now := time.Now()
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		due, err := a.Reminders.Upcoming(start, now)
		if err != nil {
			a.serverError(w, err)
			return
		}
		for _, o := range due {
			if o.Status == model.OccPending {
				openChores = append(openChores, o)
			}
		}
	}
	a.render(w, "dashboard.html", "Баланс", "dashboard", dashboardData{
		Balances: balances, Absences: absences, Schedule: schedule,
		Payments: payments, Appointments: appointments, OpenChores: openChores,
	})
}

func (a *App) serverError(w http.ResponseWriter, err error) {
	a.Logger.Error("server error", "err", err)
	http.Error(w, "внутрішня помилка", http.StatusInternalServerError)
}
