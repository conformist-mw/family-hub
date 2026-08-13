package web

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

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
		return model.Payment{}, "не вдалося розібрати форму"
	}
	enrollmentID, _ := strconv.ParseInt(r.FormValue("enrollment_id"), 10, 64)
	p := model.Payment{
		EnrollmentID: enrollmentID,
		Date:         r.FormValue("date"),
		Comment:      normalizeName(r.FormValue("comment")),
	}
	if p.EnrollmentID == 0 {
		return p, "вибери курс"
	}
	if _, err := model.ParseDate(p.Date); err != nil {
		return p, "вкажи коректну дату оплати"
	}
	amount, err := strconv.ParseFloat(r.FormValue("amount"), 64)
	if err != nil || amount < 0 {
		return p, "вкажи коректну суму"
	}
	p.Amount = amount

	enr, err := a.Store.GetEnrollment(enrollmentID)
	if err != nil {
		return p, "курс не знайдено"
	}

	if enr.BillingType == model.BillingMonthly {
		from, until, err := monthRange(r.FormValue("covers_month"))
		if err != nil {
			return p, "вкажи місяць, за який оплата"
		}
		p.CoversFrom = &from
		p.CoversUntil = &until
	} else {
		lessons, err := strconv.ParseInt(r.FormValue("lessons_paid"), 10, 64)
		if err != nil || lessons <= 0 {
			return p, "вкажи кількість оплачених занять"
		}
		p.LessonsPaid = &lessons
	}
	return p, ""
}

// monthRange expands "2026-09" into the first and last day of that month.
//
// The form takes a month rather than two free dates so a coverage range is
// always exactly one calendar month. That is what keeps the "за оплачені
// періоди" chart honest: a single payment spanning September to December would
// otherwise land wholly in September. The columns stay a date range, so a
// free-form period can come back without a migration.
func monthRange(v string) (string, string, error) {
	first, err := time.ParseInLocation("2006-01", v, time.Local)
	if err != nil {
		return "", "", err
	}
	last := first.AddDate(0, 1, -1)
	return first.Format("2006-01-02"), last.Format("2006-01-02"), nil
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
