package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"familyhub/internal/model"
	"familyhub/internal/payments"
	"familyhub/internal/store"
	"familyhub/internal/valid"
)

type paymentsListData struct {
	Payments []model.Payment
	Persons  []model.Person
	PersonID int64
	Total    float64
	Page     int
	PrevURL  string
	NextURL  string
}

func (a *App) handlePayments(w http.ResponseWriter, r *http.Request) {
	personID, _ := strconv.ParseInt(r.URL.Query().Get("person"), 10, 64)
	page := parsePage(r)

	payments, err := a.Store.ListPayments(store.PaymentFilter{
		PersonID: personID,
		Limit:    pageSize + 1, Offset: (page - 1) * pageSize,
	})
	if err != nil {
		a.serverError(w, err)
		return
	}
	hasNext := len(payments) > pageSize
	if hasNext {
		payments = payments[:pageSize]
	}
	persons, err := a.Store.ListPersons()
	if err != nil {
		a.serverError(w, err)
		return
	}
	total, err := a.Store.TotalPaid(personID)
	if err != nil {
		a.serverError(w, err)
		return
	}

	vals := url.Values{"person": {r.URL.Query().Get("person")}}
	data := paymentsListData{
		Payments: payments,
		Persons:  persons,
		PersonID: personID,
		Total:    total,
		Page:     page,
	}
	if page > 1 {
		data.PrevURL = pageURL("/payments", vals, page-1)
	}
	if hasNext {
		data.NextURL = pageURL("/payments", vals, page+1)
	}
	a.render(w, "payments.html", "Оплати", "payments", data)
}

type paymentFormData struct {
	Payment     model.Payment
	Enrollments []model.Enrollment
	IsEdit      bool
	Today       string
	Error       string
}

func (a *App) handlePaymentNew(w http.ResponseWriter, r *http.Request) {
	enrollments, err := a.Store.ListEnrollments(true)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "payment_form.html", "Нова оплата", "payments", paymentFormData{
		Payment:     model.Payment{Date: today()},
		Enrollments: enrollments,
		Today:       today(),
	})
}

func (a *App) handlePaymentCreate(w http.ResponseWriter, r *http.Request) {
	enrollmentID, form, err := paymentForm(r)
	if err != nil {
		a.renderPaymentFormError(w, model.Payment{}, false, "не вдалося розібрати форму")
		return
	}
	p, err := a.Payments.Create(enrollmentID, form, actorName(r))
	if err != nil {
		a.paymentWriteError(w, p, false, err)
		return
	}
	http.Redirect(w, r, "/payments", http.StatusSeeOther)
}

func (a *App) handlePaymentEdit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	p, err := a.Store.GetPayment(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	enrollments, err := a.Store.ListEnrollments(false)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "payment_form.html", "Оплата", "payments", paymentFormData{
		Payment:     p,
		Enrollments: enrollments,
		IsEdit:      true,
		Today:       today(),
	})
}

func (a *App) handlePaymentUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	enrollmentID, form, err := paymentForm(r)
	if err != nil {
		a.renderPaymentFormError(w, model.Payment{ID: id}, true, "не вдалося розібрати форму")
		return
	}
	p, err := a.Payments.Update(id, enrollmentID, form, actorName(r))
	if err != nil {
		a.paymentWriteError(w, p, true, err)
		return
	}
	http.Redirect(w, r, "/payments", http.StatusSeeOther)
}

func (a *App) handlePaymentDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := a.Payments.Delete(id, actorName(r)); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/payments", http.StatusSeeOther)
}

// paymentForm lifts the posted fields into the shared form. Validation is not
// done here — it belongs to internal/payments, so this form and the Mini App's
// cannot disagree about what a payment for a monthly course must carry.
func paymentForm(r *http.Request) (int64, payments.Form, error) {
	if err := r.ParseForm(); err != nil {
		return 0, payments.Form{}, err
	}
	enrollmentID, _ := strconv.ParseInt(r.FormValue("enrollment_id"), 10, 64)
	return enrollmentID, payments.Form{
		Date:        r.FormValue("date"),
		Amount:      r.FormValue("amount"),
		Lessons:     r.FormValue("lessons_paid"),
		CoversMonth: r.FormValue("covers_month"),
		Comment:     normalizeName(r.FormValue("comment")),
	}, nil
}

// paymentWriteError puts a rejected write back on the screen: a validation
// failure is the person's to fix and re-renders the form with what they typed,
// anything else is ours.
func (a *App) paymentWriteError(w http.ResponseWriter, p model.Payment, isEdit bool, err error) {
	var invalid valid.FieldError
	if errors.As(err, &invalid) {
		a.renderPaymentFormError(w, p, isEdit, invalid.Message)
		return
	}
	a.serverError(w, err)
}

func (a *App) renderPaymentFormError(w http.ResponseWriter, p model.Payment, isEdit bool, msg string) {
	enrollments, _ := a.Store.ListEnrollments(false)
	w.WriteHeader(http.StatusUnprocessableEntity)
	a.render(w, "payment_form.html", "Оплата", "payments", paymentFormData{
		Payment:     p,
		Enrollments: enrollments,
		IsEdit:      isEdit,
		Today:       today(),
		Error:       msg,
	})
}
