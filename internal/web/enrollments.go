package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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

// attendanceOptions is a separate axis from billing on purpose: a monthly club
// may want every lesson marked, a school with a fixed fee does not.
var attendanceOptions = []billingOption{
	{model.AttendancePerSession, model.AttendanceModeLabels[model.AttendancePerSession]},
	{model.AttendanceExceptionsOnly, model.AttendanceModeLabels[model.AttendanceExceptionsOnly]},
}

// noticeUnits are the multipliers behind the payment-notice field. Minutes is
// what the column stores; the unit exists because the same question — how much
// warning do I want — is answered in hours for a lesson and in days for a
// monthly pass, and 7200 is not a number anyone types into a form.
type noticeUnit struct {
	Code  string
	Label string
	Min   int
}

var noticeUnits = []noticeUnit{
	{"min", "хв", 1},
	{"hour", "год", 60},
	{"day", "днів", model.MinutesPerDay},
}

// splitNotice renders stored minutes as the largest unit that divides evenly,
// so 7200 comes back as "5 днів" rather than "7200 хв".
func splitNotice(minutes int) (int, string) {
	for i := len(noticeUnits) - 1; i >= 0; i-- {
		u := noticeUnits[i]
		if minutes >= u.Min && minutes%u.Min == 0 {
			return minutes / u.Min, u.Code
		}
	}
	return minutes, "min"
}

func noticeMinutes(value int, unit string) int {
	for _, u := range noticeUnits {
		if u.Code == unit {
			return value * u.Min
		}
	}
	return value
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
	Attendance []billingOption
	// NoticeValue and NoticeUnit are PaymentNoticeMin split for the form.
	NoticeValue int
	NoticeUnit  string
	NoticeUnits []noticeUnit
	Persons     []model.Person
	ClassNames  []string
	Trainers    []model.Trainer
	Slots       []model.Slot
	Weekdays    []weekdayOption
	IsEdit      bool
	Error       string
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
		Enrollment: model.Enrollment{
			BillingType:      model.BillingPerLesson,
			AttendanceMode:   model.AttendancePerSession,
			LowThreshold:     2,
			PaymentNoticeMin: 2 * 60,
			Active:           true,
		},
	})
}

func (a *App) handleEnrollmentCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.serverError(w, err)
		return
	}
	e := parseEnrollmentForm(r)
	e.Person = normalizeName(r.FormValue("person"))
	e.Active = true

	formData := enrollmentFormData{Enrollment: e}
	if e.Person == "" || e.Name == "" {
		formData.Error = "вкажи людину і назву заняття"
		a.renderEnrollmentForm(w, formData)
		return
	}
	if msg := validateEnrollment(e); msg != "" {
		formData.Error = msg
		a.renderEnrollmentForm(w, formData)
		return
	}
	trainerID, err := a.trainerIDFromForm(e.Trainer)
	if err != nil {
		a.serverError(w, err)
		return
	}
	e.TrainerID = trainerID
	if _, err := a.Store.CreateEnrollment(e); err != nil {
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
	e := parseEnrollmentForm(r)
	e.ID = id
	e.Active = r.FormValue("active") == "on"

	if e.Name == "" || validateEnrollment(e) != "" {
		enr, _ := a.Store.GetEnrollment(id)
		slots, _ := a.Store.ListSlots(id)
		a.renderEnrollmentForm(w, enrollmentFormData{
			Enrollment: enr, Slots: slots, IsEdit: true,
			Error: "перевір назву, тип оплати і ціну",
		})
		return
	}
	trainerID, err := a.trainerIDFromForm(e.Trainer)
	if err != nil {
		a.serverError(w, err)
		return
	}
	e.TrainerID = trainerID
	if err := a.Store.UpdateEnrollment(e); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/enrollments", http.StatusSeeOther)
}

// parseEnrollmentForm reads the fields both create and update share. Person
// and Active differ between the two and are set by the caller.
func parseEnrollmentForm(r *http.Request) model.Enrollment {
	price, _ := strconv.ParseFloat(r.FormValue("current_price"), 64)
	low, _ := strconv.Atoi(r.FormValue("low_threshold"))
	noticeValue, _ := strconv.Atoi(r.FormValue("payment_notice_value"))
	return model.Enrollment{
		Name:                normalizeName(r.FormValue("name")),
		Description:         normalizeName(r.FormValue("description")),
		BillingType:         r.FormValue("billing_type"),
		CurrentPrice:        price,
		LowThreshold:        low,
		Notes:               normalizeName(r.FormValue("notes")),
		Trainer:             normalizeName(r.FormValue("trainer")),
		AttendanceMode:      r.FormValue("attendance_mode"),
		PaymentInstructions: strings.TrimSpace(r.FormValue("payment_instructions")),
		PaymentNoticeMin:    noticeMinutes(noticeValue, r.FormValue("payment_notice_unit")),
	}
}

// validateEnrollment returns a user-facing message, or "" when the enrollment
// may be written. The attendance mode is checked because it reaches a CHECK
// constraint: a bad value would surface as a 500 instead of a form error.
func validateEnrollment(e model.Enrollment) string {
	if !isValidBilling(e.BillingType) {
		return "вибери тип оплати"
	}
	if !isValidAttendance(e.AttendanceMode) {
		return "вибери режим відміток"
	}
	if e.CurrentPrice < 0 {
		return "ціна не може бути відʼємною"
	}
	return ""
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
	if err := schedule.NewService(a.Store, nil, nil).Add(id, form); err != nil {
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
	data.Attendance = attendanceOptions
	data.NoticeUnits = noticeUnits
	data.NoticeValue, data.NoticeUnit = splitNotice(data.Enrollment.PaymentNoticeMin)
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

func isValidAttendance(m string) bool {
	return m == model.AttendancePerSession || m == model.AttendanceExceptionsOnly
}
