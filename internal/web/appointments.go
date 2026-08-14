package web

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"familyhub/internal/appointments"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The appointments page is the hands-on side of what the bot captures from
// free text: the bot is fast but lossy (no location, no note, no fixing a
// typo), so everything it stores must be editable here.

var apptStatusOptions = []statusOption{
	{model.ApptStatusPlanned, model.ApptStatusLabels[model.ApptStatusPlanned]},
	{model.ApptStatusDone, model.ApptStatusLabels[model.ApptStatusDone]},
	{model.ApptStatusCancelled, model.ApptStatusLabels[model.ApptStatusCancelled]},
}

type appointmentsListData struct {
	Appointments []model.Appointment
	Now          string // LocalDatetime; splits upcoming from past in the template
	Page         int
	PrevURL      string
	NextURL      string
}

func (a *App) handleAppointments(w http.ResponseWriter, r *http.Request) {
	page := parsePage(r)
	now := time.Now().Format(model.LocalDatetime)

	items, err := a.Store.ListAppointments(store.AppointmentFilter{
		Now: now, Limit: pageSize + 1, Offset: (page - 1) * pageSize,
	})
	if err != nil {
		a.serverError(w, err)
		return
	}
	hasNext := len(items) > pageSize
	if hasNext {
		items = items[:pageSize]
	}

	data := appointmentsListData{Appointments: items, Now: now, Page: page}
	if page > 1 {
		data.PrevURL = pageURL("/appointments", url.Values{}, page-1)
	}
	if hasNext {
		data.NextURL = pageURL("/appointments", url.Values{}, page+1)
	}
	a.render(w, "appointments.html", "Записи", "appointments", data)
}

type appointmentFormData struct {
	Appointment model.Appointment
	Date        string // form field: YYYY-MM-DD, split out of StartsAt
	Time        string // form field: HH:MM
	Cost        string // form field: raw text, so a bad value survives a re-render
	Persons     []model.Person
	Statuses    []statusOption
	IsEdit      bool
	Today       string
	Error       string
}

func (a *App) handleAppointmentNew(w http.ResponseWriter, r *http.Request) {
	a.render(w, "appointment_form.html", "Новий запис", "appointments", appointmentFormData{
		Appointment: model.Appointment{Status: model.ApptStatusPlanned},
		Date:        today(),
		Persons:     a.appointmentPersons(),
		Statuses:    apptStatusOptions,
		Today:       today(),
	})
}

func (a *App) handleAppointmentEdit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	appt, err := a.Store.GetAppointment(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	date, hhmm := splitLocalDatetime(appt.StartsAt)
	a.render(w, "appointment_form.html", "Запис", "appointments", appointmentFormData{
		Appointment: appt,
		Date:        date,
		Time:        hhmm,
		Cost:        formatCost(appt.Cost),
		Persons:     a.appointmentPersons(),
		Statuses:    apptStatusOptions,
		IsEdit:      true,
		Today:       today(),
	})
}

// The writes go through appointments.Service rather than straight to the store:
// it owns the validation the Mini App shares, and the message that tells the
// family group what just happened. A handler that wrote its own SQL would be
// silent, which is how this page came to be the one place a booking could
// change without anybody hearing about it.

func (a *App) handleAppointmentCreate(w http.ResponseWriter, r *http.Request) {
	form, ok := appointmentForm(r)
	if !ok {
		a.renderAppointmentFormError(w, model.Appointment{}, form, false, "не вдалося розібрати форму")
		return
	}
	appt, err := a.Appointments.Create(form, actorName(r))
	if err != nil {
		a.appointmentWriteError(w, err, appt, form, false)
		return
	}
	http.Redirect(w, r, "/appointments", http.StatusSeeOther)
}

func (a *App) handleAppointmentUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	form, ok := appointmentForm(r)
	if !ok {
		a.renderAppointmentFormError(w, model.Appointment{ID: id}, form, true, "не вдалося розібрати форму")
		return
	}
	appt, err := a.Appointments.Update(id, form, actorName(r))
	appt.ID = id
	if err != nil {
		a.appointmentWriteError(w, err, appt, form, true)
		return
	}
	http.Redirect(w, r, "/appointments", http.StatusSeeOther)
}

func (a *App) handleAppointmentDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := a.Appointments.Delete(id, actorName(r)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/appointments", http.StatusSeeOther)
}

// appointmentWriteError separates the person's mistake from ours: a rejected
// field is re-rendered with the value still in it, a gone row is a 404, and
// anything else is a server error.
func (a *App) appointmentWriteError(w http.ResponseWriter, err error, appt model.Appointment, form appointments.Form, isEdit bool) {
	var invalid appointments.InvalidField
	switch {
	case errors.As(err, &invalid):
		a.renderAppointmentFormError(w, appt, form, isEdit, invalid.Error())
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "запис не знайдено", http.StatusNotFound)
	default:
		a.serverError(w, err)
	}
}

// actorName names whoever is signed in, for the byline on the group message.
// oauth2-proxy is the only thing that knows who that is, and only if it is
// configured to forward the identity it verified; the value is cosmetic, so an
// absent header falls back to naming the surface instead of a person.
func actorName(r *http.Request) string {
	for _, h := range []string{"X-Forwarded-Preferred-Username", "X-Forwarded-User", "X-Forwarded-Email"} {
		v := strings.TrimSpace(r.Header.Get(h))
		if v == "" {
			continue
		}
		if local, _, ok := strings.Cut(v, "@"); ok { // an email address
			v = local
		}
		if v != "" {
			return v
		}
	}
	return "веб"
}

// appointmentPersons feeds the "хто" datalist. Appointment.Person is free text
// (it may be a doctor's patient, a guest, "обоє"), so the enrolled persons are
// a suggestion list, never a constraint.
func (a *App) appointmentPersons() []model.Person {
	persons, err := a.Store.ListPersons()
	if err != nil {
		a.Logger.Error("appointment persons", "err", err)
		return nil
	}
	return persons
}

func appointmentForm(r *http.Request) (appointments.Form, bool) {
	if err := r.ParseForm(); err != nil {
		return appointments.Form{}, false
	}
	return appointments.Form{
		Title:    r.FormValue("title"),
		Person:   r.FormValue("person"),
		Location: r.FormValue("location"),
		Date:     r.FormValue("date"),
		Time:     r.FormValue("time"),
		EndTime:  r.FormValue("end_time"),
		Status:   r.FormValue("status"),
		Note:     r.FormValue("note"),
		Cost:     r.FormValue("cost"),
	}, true
}

// formatCost renders a stored amount back into the form field.
func formatCost(c *float64) string { return appointments.FormatCost(c) }

// renderAppointmentFormError re-renders the form with the rejected values still
// in it — a value that vanishes on error cannot be corrected, only retyped. The
// date, time and amount come from the form rather than the parsed appointment
// precisely because parsing is what failed.
func (a *App) renderAppointmentFormError(w http.ResponseWriter, appt model.Appointment, form appointments.Form, isEdit bool, msg string) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	a.render(w, "appointment_form.html", "Запис", "appointments", appointmentFormData{
		Appointment: appt,
		Date:        strings.TrimSpace(form.Date),
		Time:        strings.TrimSpace(form.Time),
		Cost:        strings.TrimSpace(form.Cost),
		Persons:     a.appointmentPersons(),
		Statuses:    apptStatusOptions,
		IsEdit:      isEdit,
		Today:       today(),
		Error:       msg,
	})
}

// splitLocalDatetime splits "2006-01-02T15:04" into the date and time inputs.
func splitLocalDatetime(s string) (string, string) {
	if d, hhmm, ok := strings.Cut(s, "T"); ok {
		return d, hhmm
	}
	return s, ""
}
