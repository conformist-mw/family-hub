package web

import (
	"errors"
	"net/http"
	"strconv"

	"lessons/internal/model"
	"lessons/internal/store"
)

type billingOption struct {
	Code  string
	Label string
}

var billingOptions = []billingOption{
	{model.BillingPerLesson, "за занятие"},
	{model.BillingMonthly, "абонемент (помесячно)"},
}

func (a *App) handleEnrollments(w http.ResponseWriter, r *http.Request) {
	enrollments, err := a.Store.ListEnrollments(false)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "enrollments.html", "Курсы", "enrollments", enrollments)
}

type enrollmentFormData struct {
	Enrollment model.Enrollment
	Billing    []billingOption
	Persons    []model.Person
	ClassNames []string
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

	formData := enrollmentFormData{
		Enrollment: model.Enrollment{
			Person: person, Name: name, Description: description, BillingType: billing,
			CurrentPrice: price, LowThreshold: low, Notes: notes, Active: true,
		},
	}
	if person == "" || name == "" {
		formData.Error = "укажи человека и название занятия"
		a.renderEnrollmentForm(w, formData)
		return
	}
	if !isValidBilling(billing) {
		formData.Error = "выбери тип оплаты"
		a.renderEnrollmentForm(w, formData)
		return
	}
	if price < 0 {
		formData.Error = "цена не может быть отрицательной"
		a.renderEnrollmentForm(w, formData)
		return
	}
	if _, err := a.Store.CreateEnrollment(person, name, description, billing, price, low, notes); err != nil {
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

	if name == "" || !isValidBilling(billing) || price < 0 {
		enr, _ := a.Store.GetEnrollment(id)
		slots, _ := a.Store.ListSlots(id)
		a.renderEnrollmentForm(w, enrollmentFormData{
			Enrollment: enr, Slots: slots, IsEdit: true,
			Error: "проверь название, тип оплаты и цену",
		})
		return
	}
	if err := a.Store.UpdateEnrollment(id, name, description, billing, price, low, active, notes); err != nil {
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
	weekday, _ := strconv.Atoi(r.FormValue("weekday"))
	t := normalizeName(r.FormValue("time"))
	if weekday < 0 || weekday > 6 || t == "" {
		http.Redirect(w, r, "/enrollments/"+strconv.FormatInt(id, 10)+"/edit", http.StatusSeeOther)
		return
	}
	if err := a.Store.CreateSlot(id, weekday, t); err != nil {
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
	if !data.IsEdit {
		persons, _ := a.Store.ListPersons()
		names, _ := a.Store.DistinctClassNames()
		data.Persons = persons
		data.ClassNames = names
	}
	a.render(w, "enrollment_form.html", "Курс", "enrollments", data)
}

func isValidBilling(b string) bool {
	return b == model.BillingPerLesson || b == model.BillingMonthly
}
