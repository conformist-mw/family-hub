package web

import (
	"net/http"
	"net/url"
	"strconv"

	"familyhub/internal/model"
	"familyhub/internal/store"
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
	a.render(w, "payments.html", "Оплаты", "payments", data)
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
	a.render(w, "payment_form.html", "Новая оплата", "payments", paymentFormData{
		Payment:     model.Payment{Date: today()},
		Enrollments: enrollments,
		Today:       today(),
	})
}

func (a *App) handlePaymentCreate(w http.ResponseWriter, r *http.Request) {
	p, formErr := a.parsePaymentForm(r)
	if formErr != "" {
		a.renderPaymentFormError(w, p, false, formErr)
		return
	}
	if _, err := a.Store.CreatePayment(p); err != nil {
		a.serverError(w, err)
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
	p, formErr := a.parsePaymentForm(r)
	p.ID = id
	if formErr != "" {
		a.renderPaymentFormError(w, p, true, formErr)
		return
	}
	if err := a.Store.UpdatePayment(p); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/payments", http.StatusSeeOther)
}

func (a *App) handlePaymentDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := a.Store.DeletePayment(id); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/payments", http.StatusSeeOther)
}

func (a *App) parsePaymentForm(r *http.Request) (model.Payment, string) {
	if err := r.ParseForm(); err != nil {
		return model.Payment{}, "не удалось разобрать форму"
	}
	enrollmentID, _ := strconv.ParseInt(r.FormValue("enrollment_id"), 10, 64)
	p := model.Payment{
		EnrollmentID: enrollmentID,
		Date:         r.FormValue("date"),
		Comment:      normalizeName(r.FormValue("comment")),
	}
	if p.EnrollmentID == 0 {
		return p, "выбери курс"
	}
	if _, err := model.ParseDate(p.Date); err != nil {
		return p, "укажи корректную дату оплаты"
	}
	amount, err := strconv.ParseFloat(r.FormValue("amount"), 64)
	if err != nil || amount < 0 {
		return p, "укажи корректную сумму"
	}
	p.Amount = amount

	enr, err := a.Store.GetEnrollment(enrollmentID)
	if err != nil {
		return p, "курс не найден"
	}

	if enr.BillingType == model.BillingMonthly {
		from := r.FormValue("covers_from")
		until := r.FormValue("covers_until")
		fromT, errFrom := model.ParseDate(from)
		untilT, errUntil := model.ParseDate(until)
		if errFrom != nil || errUntil != nil {
			return p, "укажи период абонемента (с / по)"
		}
		if untilT.Before(fromT) {
			return p, "дата «по» раньше даты «с»"
		}
		p.CoversFrom = &from
		p.CoversUntil = &until
	} else {
		lessons, err := strconv.ParseInt(r.FormValue("lessons_paid"), 10, 64)
		if err != nil || lessons <= 0 {
			return p, "укажи количество оплаченных занятий"
		}
		p.LessonsPaid = &lessons
	}
	return p, ""
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
