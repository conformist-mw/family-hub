package web

import (
	"net/http"
	"net/url"
	"strconv"

	"lessons/internal/model"
	"lessons/internal/store"
)

const pageSize = 50

func parsePage(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		return 1
	}
	return p
}

func pageURL(path string, vals url.Values, page int) string {
	q := url.Values{}
	for k, v := range vals {
		if len(v) > 0 && v[0] != "" {
			q.Set(k, v[0])
		}
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if enc := q.Encode(); enc != "" {
		return path + "?" + enc
	}
	return path
}

type statusOption struct {
	Code  string
	Label string
}

var statusOptions = []statusOption{
	{model.StatusDone, "проведено"},
	{model.StatusRescheduled, "перенесено"},
	{model.StatusCancelled, "отменено"},
	{model.StatusSkipped, "пропущено"},
}

type visitsListData struct {
	Visits   []model.Visit
	Persons  []model.Person
	Statuses []statusOption
	PersonID int64
	Status   string
	Page     int
	PrevURL  string
	NextURL  string
}

func (a *App) handleVisits(w http.ResponseWriter, r *http.Request) {
	personID, _ := strconv.ParseInt(r.URL.Query().Get("person"), 10, 64)
	status := r.URL.Query().Get("status")
	page := parsePage(r)

	visits, err := a.Store.ListVisits(store.VisitFilter{
		PersonID: personID, Status: status,
		Limit: pageSize + 1, Offset: (page - 1) * pageSize,
	})
	if err != nil {
		a.serverError(w, err)
		return
	}
	hasNext := len(visits) > pageSize
	if hasNext {
		visits = visits[:pageSize]
	}
	persons, err := a.Store.ListPersons()
	if err != nil {
		a.serverError(w, err)
		return
	}

	vals := url.Values{"person": {r.URL.Query().Get("person")}, "status": {status}}
	data := visitsListData{
		Visits:   visits,
		Persons:  persons,
		Statuses: statusOptions,
		PersonID: personID,
		Status:   status,
		Page:     page,
	}
	if page > 1 {
		data.PrevURL = pageURL("/visits", vals, page-1)
	}
	if hasNext {
		data.NextURL = pageURL("/visits", vals, page+1)
	}
	a.render(w, "visits.html", "Занятия", "visits", data)
}

type visitFormData struct {
	Visit       model.Visit
	Enrollments []model.Enrollment
	Frequent    []model.Enrollment
	Statuses    []statusOption
	IsEdit      bool
	Today       string
	Yesterday   string
	Before      string
	Error       string
}

func (a *App) frequentEnrollments() []model.Enrollment {
	fe, err := a.Store.FrequentActiveEnrollments(8)
	if err != nil {
		a.Logger.Error("frequent enrollments", "err", err)
		return nil
	}
	return fe
}

func (a *App) handleVisitNew(w http.ResponseWriter, r *http.Request) {
	enrollments, err := a.Store.ListEnrollments(true)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "visit_form.html", "Новое занятие", "visits", visitFormData{
		Visit:       model.Visit{Date: today(), Status: model.StatusDone},
		Enrollments: enrollments,
		Frequent:    a.frequentEnrollments(),
		Statuses:    statusOptions,
		Today:       today(),
		Yesterday:   daysAgo(1),
		Before:      daysAgo(2),
	})
}

func (a *App) handleVisitCreate(w http.ResponseWriter, r *http.Request) {
	v, formErr := a.parseVisitForm(r)
	if formErr != "" {
		a.renderVisitFormError(w, v, false, formErr)
		return
	}
	if _, err := a.Store.CreateVisit(v.EnrollmentID, v.Date, v.Status, v.Comment); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/visits", http.StatusSeeOther)
}

func (a *App) handleVisitEdit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	v, err := a.Store.GetVisit(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	enrollments, err := a.Store.ListEnrollments(false)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "visit_form.html", "Занятие", "visits", visitFormData{
		Visit:       v,
		Enrollments: enrollments,
		Frequent:    a.frequentEnrollments(),
		Statuses:    statusOptions,
		IsEdit:      true,
		Today:       today(),
		Yesterday:   daysAgo(1),
		Before:      daysAgo(2),
	})
}

func (a *App) handleVisitUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	v, formErr := a.parseVisitForm(r)
	v.ID = id
	if formErr != "" {
		a.renderVisitFormError(w, v, true, formErr)
		return
	}
	if err := a.Store.UpdateVisit(id, v.EnrollmentID, v.Date, v.Status, v.Comment); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/visits", http.StatusSeeOther)
}

func (a *App) handleVisitDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := a.Store.DeleteVisit(id); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/visits", http.StatusSeeOther)
}

func (a *App) parseVisitForm(r *http.Request) (model.Visit, string) {
	if err := r.ParseForm(); err != nil {
		return model.Visit{}, "не удалось разобрать форму"
	}
	enrollmentID, _ := strconv.ParseInt(r.FormValue("enrollment_id"), 10, 64)
	v := model.Visit{
		EnrollmentID: enrollmentID,
		Date:         r.FormValue("date"),
		Status:       r.FormValue("status"),
		Comment:      normalizeName(r.FormValue("comment")),
	}
	if v.EnrollmentID == 0 {
		return v, "выбери курс"
	}
	if _, err := model.ParseDate(v.Date); err != nil {
		return v, "укажи корректную дату"
	}
	if !isValidStatus(v.Status) {
		return v, "выбери статус"
	}
	return v, ""
}

func (a *App) renderVisitFormError(w http.ResponseWriter, v model.Visit, isEdit bool, msg string) {
	enrollments, _ := a.Store.ListEnrollments(false)
	w.WriteHeader(http.StatusUnprocessableEntity)
	a.render(w, "visit_form.html", "Занятие", "visits", visitFormData{
		Visit:       v,
		Enrollments: enrollments,
		Frequent:    a.frequentEnrollments(),
		Statuses:    statusOptions,
		IsEdit:      isEdit,
		Today:       today(),
		Yesterday:   daysAgo(1),
		Before:      daysAgo(2),
		Error:       msg,
	})
}
