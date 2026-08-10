package web

import (
	"errors"
	"net/http"
	"strconv"

	"familyhub/internal/model"
	"familyhub/internal/schedule"
	"familyhub/internal/store"
	"familyhub/internal/valid"
)

type billingOption struct {
	Code  string
	Label string
}

var billingOptions = []billingOption{
	{model.BillingPerLesson, "за заняття"},
	{model.BillingMonthly, "абонемент (щомісяця)"},
}

func (a *App) handleEnrollments(w http.ResponseWriter, r *http.Request) {
	enrollments, err := a.Store.ListEnrollments(false)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "enrollments.html", "Курси", "enrollments", enrollments)
}

type enrollmentFormData struct {
	Enrollment model.Enrollment
	Billing    []billingOption
	Persons    []model.Person
	ClassNames []string
	Trainers   []model.Trainer
	Slots      []model.Slot
	Weekdays   []weekdayOption
	IsEdit     bool
	Error      string
}

type weekdayOption struct {
	N     int
	Label string
}

// weekdayOptions lists weekdays for the schedule dropdown starting at Monday,
// which is how the week reads here. The N value stays Go's time.Weekday code
// (Sunday=0) so it matches what the scheduler compares against.
func weekdayOptions() []weekdayOption {
	order := []int{1, 2, 3, 4, 5, 6, 0} // Пн … Вс
	out := make([]weekdayOption, len(order))
	for i, n := range order {
		out[i] = weekdayOption{N: n, Label: model.WeekdayLabels[n]}
	}
	return out
}

func (a *App) handleEnrollmentNew(w http.ResponseWriter, r *http.Request) {
	a.renderEnrollmentForm(w, enrollmentFormData{
		Enrollment: model.Enrollment{BillingType: model.BillingPerLesson, LowThreshold: 2, Active: true},
	})
}

func (a *App) handleEnrollmentCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.serverError(w, err)
		return
	}
	person := normalizeName(r.FormValue("person"))
	name := normalizeName(r.FormValue("name"))
	description := normalizeName(r.FormValue("description"))
	billing := r.FormValue("billing_type")
	price, _ := strconv.ParseFloat(r.FormValue("current_price"), 64)
	low, _ := strconv.Atoi(r.FormValue("low_threshold"))
	notes := normalizeName(r.FormValue("notes"))
	trainer := normalizeName(r.FormValue("trainer"))

	formData := enrollmentFormData{
		Enrollment: model.Enrollment{
			Person: person, Name: name, Description: description, BillingType: billing,
			CurrentPrice: price, LowThreshold: low, Notes: notes, Active: true,
			Trainer: trainer,
		},
	}
	if person == "" || name == "" {
		formData.Error = "вкажи людину і назву заняття"
		a.renderEnrollmentForm(w, formData)
		return
	}
	if !isValidBilling(billing) {
		formData.Error = "вибери тип оплати"
		a.renderEnrollmentForm(w, formData)
		return
	}
	if price < 0 {
		formData.Error = "ціна не може бути відʼємною"
		a.renderEnrollmentForm(w, formData)
		return
	}
	trainerID, err := a.trainerIDFromForm(trainer)
	if err != nil {
		a.serverError(w, err)
		return
	}
	if _, err := a.Store.CreateEnrollment(person, name, description, billing, price, low, notes, trainerID); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/enrollments", http.StatusSeeOther)
}

func (a *App) handleEnrollmentEdit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	enr, err := a.Store.GetEnrollment(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	slots, err := a.Store.ListSlots(id)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.renderEnrollmentForm(w, enrollmentFormData{
		Enrollment: enr,
		Slots:      slots,
		IsEdit:     true,
	})
}

func (a *App) handleEnrollmentUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := r.ParseForm(); err != nil {
		a.serverError(w, err)
		return
	}
	name := normalizeName(r.FormValue("name"))
	description := normalizeName(r.FormValue("description"))
	billing := r.FormValue("billing_type")
	price, _ := strconv.ParseFloat(r.FormValue("current_price"), 64)
	low, _ := strconv.Atoi(r.FormValue("low_threshold"))
	active := r.FormValue("active") == "on"
	notes := normalizeName(r.FormValue("notes"))
	trainer := normalizeName(r.FormValue("trainer"))

	if name == "" || !isValidBilling(billing) || price < 0 {
		enr, _ := a.Store.GetEnrollment(id)
		slots, _ := a.Store.ListSlots(id)
		a.renderEnrollmentForm(w, enrollmentFormData{
			Enrollment: enr, Slots: slots, IsEdit: true,
			Error: "перевір назву, тип оплати і ціну",
		})
		return
	}
	trainerID, err := a.trainerIDFromForm(trainer)
	if err != nil {
		a.serverError(w, err)
		return
	}
	if err := a.Store.UpdateEnrollment(id, name, description, billing, price, low, active, notes, trainerID); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/enrollments", http.StatusSeeOther)
}

func (a *App) handleEnrollmentDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := a.Store.DeleteEnrollment(id)
	if errors.Is(err, store.ErrEnrollmentHasData) {
		enr, _ := a.Store.GetEnrollment(id)
		slots, _ := a.Store.ListSlots(id)
		w.WriteHeader(http.StatusUnprocessableEntity)
		a.renderEnrollmentForm(w, enrollmentFormData{
			Enrollment: enr, Slots: slots, IsEdit: true, Error: err.Error(),
		})
		return
	}
	if err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/enrollments", http.StatusSeeOther)
}

func (a *App) handleSlotCreate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := r.ParseForm(); err != nil {
		a.serverError(w, err)
		return
	}
	// Slot rules live in internal/schedule, shared with the Mini App. They are
	// stricter than what used to be here: the time is now actually parsed, so
	// a typo can no longer reach the table and silently never fire a reminder.
	form := schedule.Form{
		Weekday:  r.FormValue("weekday"),
		Time:     r.FormValue("time"),
		Duration: r.FormValue("duration_min"),
	}
	if err := schedule.NewService(a.Store).Add(id, form); err != nil {
		var invalid valid.FieldError
		if errors.As(err, &invalid) {
			// This page has no slot-level error slot; the value is rejected
			// rather than stored, which is the part that matters.
			a.Logger.Warn("web: slot rejected", "err", invalid.Message, "enrollment", id)
			http.Redirect(w, r, "/enrollments/"+strconv.FormatInt(id, 10)+"/edit", http.StatusSeeOther)
			return
		}
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/enrollments/"+strconv.FormatInt(id, 10)+"/edit", http.StatusSeeOther)
}

func (a *App) handleSlotDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slotID, _ := strconv.ParseInt(r.PathValue("slotId"), 10, 64)
	if err := a.Store.DeleteSlot(slotID); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/enrollments/"+id+"/edit", http.StatusSeeOther)
}

func (a *App) renderEnrollmentForm(w http.ResponseWriter, data enrollmentFormData) {
	data.Billing = billingOptions
	data.Weekdays = weekdayOptions()
	trainers, _ := a.Store.ListTrainers()
	data.Trainers = trainers
	if !data.IsEdit {
		persons, _ := a.Store.ListPersons()
		names, _ := a.Store.DistinctClassNames()
		data.Persons = persons
		data.ClassNames = names
	}
	a.render(w, "enrollment_form.html", "Курс", "enrollments", data)
}

// trainerIDFromForm resolves the free-text trainer field: empty means no
// trainer (nil), anything else is found or created by name.
func (a *App) trainerIDFromForm(name string) (*int64, error) {
	if name == "" {
		return nil, nil
	}
	id, err := a.Store.FindOrCreateTrainer(name)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func isValidBilling(b string) bool {
	return b == model.BillingPerLesson || b == model.BillingMonthly
}
