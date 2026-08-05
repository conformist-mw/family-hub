package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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

func (a *App) handleAppointmentCreate(w http.ResponseWriter, r *http.Request) {
	appt, date, hhmm, costRaw, formErr := parseAppointmentForm(r)
	if formErr != "" {
		a.renderAppointmentFormError(w, appt, date, hhmm, costRaw, false, formErr)
		return
	}
	if _, err := a.Store.CreateAppointment(appt); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/appointments", http.StatusSeeOther)
}

func (a *App) handleAppointmentUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	appt, date, hhmm, costRaw, formErr := parseAppointmentForm(r)
	appt.ID = id
	if formErr != "" {
		a.renderAppointmentFormError(w, appt, date, hhmm, costRaw, true, formErr)
		return
	}
	if err := a.Store.UpdateAppointment(appt); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/appointments", http.StatusSeeOther)
}

func (a *App) handleAppointmentDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := a.Store.SoftDeleteAppointment(id); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/appointments", http.StatusSeeOther)
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

func parseAppointmentForm(r *http.Request) (model.Appointment, string, string, string, string) {
	if err := r.ParseForm(); err != nil {
		return model.Appointment{}, "", "", "", "не вдалося розібрати форму"
	}
	date := strings.TrimSpace(r.FormValue("date"))
	hhmm := strings.TrimSpace(r.FormValue("time"))
	endTime := strings.TrimSpace(r.FormValue("end_time"))
	costRaw := strings.TrimSpace(r.FormValue("cost"))

	appt := model.Appointment{
		Title:    strings.TrimSpace(r.FormValue("title")),
		Person:   normalizeName(r.FormValue("person")),
		Location: strings.TrimSpace(r.FormValue("location")),
		Note:     strings.TrimSpace(r.FormValue("note")),
		Status:   r.FormValue("status"),
	}
	if appt.Title == "" {
		return appt, date, hhmm, costRaw, "вкажи назву"
	}
	if _, err := time.ParseInLocation(model.LocalDatetime, date+"T"+hhmm, time.Local); err != nil {
		return appt, date, hhmm, costRaw, "вкажи коректну дату й час"
	}
	appt.StartsAt = date + "T" + hhmm
	if endTime != "" {
		if _, err := time.ParseInLocation(model.LocalDatetime, date+"T"+endTime, time.Local); err != nil {
			return appt, date, hhmm, costRaw, "вкажи коректний час завершення"
		}
		if endTime <= hhmm { // same-day only: an appointment crossing midnight isn't a thing here
			return appt, date, hhmm, costRaw, "час завершення має бути пізніше початку"
		}
		appt.EndsAt = date + "T" + endTime
	}
	if !isValidApptStatus(appt.Status) {
		return appt, date, hhmm, costRaw, "вибери статус"
	}
	// An empty field means "not recorded" (NULL), which is not the same as 0 —
	// a free visit is recorded by typing 0.
	if costRaw != "" {
		cost, ok := parseCost(costRaw)
		if !ok {
			return appt, date, hhmm, costRaw, "сума має бути числом, напр. 800 (або 0)"
		}
		appt.Cost = &cost
	}
	return appt, date, hhmm, costRaw, ""
}

// parseCost accepts what a person types into the amount field: "800", "1 200",
// "1200,50". Negative is rejected; 0 means the visit was free.
func parseCost(s string) (float64, bool) {
	s = strings.NewReplacer(" ", "", "\u00a0", "", ",", ".").Replace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// formatCost renders a stored amount back into the form field ("" when unset,
// no trailing zeros for whole numbers).
func formatCost(c *float64) string {
	if c == nil {
		return ""
	}
	return strconv.FormatFloat(*c, 'f', -1, 64)
}

func (a *App) renderAppointmentFormError(w http.ResponseWriter, appt model.Appointment, date, hhmm, costRaw string, isEdit bool, msg string) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	a.render(w, "appointment_form.html", "Запис", "appointments", appointmentFormData{
		Appointment: appt,
		Date:        date,
		Time:        hhmm,
		Cost:        costRaw,
		Persons:     a.appointmentPersons(),
		Statuses:    apptStatusOptions,
		IsEdit:      isEdit,
		Today:       today(),
		Error:       msg,
	})
}

func isValidApptStatus(s string) bool {
	_, ok := model.ApptStatusLabels[s]
	return ok
}

// splitLocalDatetime splits "2006-01-02T15:04" into the date and time inputs.
func splitLocalDatetime(s string) (string, string) {
	if d, hhmm, ok := strings.Cut(s, "T"); ok {
		return d, hhmm
	}
	return s, ""
}
