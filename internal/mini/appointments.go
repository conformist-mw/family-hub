package mini

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"familyhub/internal/appointments"
	"familyhub/internal/model"
	"familyhub/internal/valid"
)

// writeForm is the JSON body of a create or edit. It mirrors
// appointments.Form field for field: validation belongs to that package, and
// this layer only carries values across HTTP.
type writeForm struct {
	Title    string `json:"title"`
	Person   string `json:"person"`
	Location string `json:"location"`
	Date     string `json:"date"`
	Time     string `json:"time"`
	EndTime  string `json:"endTime"`
	Duration string `json:"duration"`
	Status   string `json:"status"`
	Note     string `json:"note"`
	Cost     string `json:"cost"`
}

func (w writeForm) form() appointments.Form {
	return appointments.Form{
		Title: w.Title, Person: w.Person, Location: w.Location,
		Date: w.Date, Time: w.Time, EndTime: w.EndTime, Duration: w.Duration,
		Status: w.Status, Note: w.Note, Cost: w.Cost,
	}
}

// decodeJSON reads a bounded request body and rejects unknown fields, so a
// client typo fails loudly instead of silently saving half a record.
func decodeJSON(r *http.Request, into any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

func decodeWrite(r *http.Request) (appointments.Form, *apiError) {
	var body writeForm
	if err := decodeJSON(r, &body); err != nil {
		return appointments.Form{}, errBadRequest
	}
	return body.form(), nil
}

// writeError maps a failed write onto the JSON error contract. A validation
// failure is the person's to fix and names the field; anything else is ours.
func (rt *Router) writeError(w http.ResponseWriter, err error, what string) {
	var invalid valid.FieldError
	if errors.As(err, &invalid) {
		rt.writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": map[string]string{
				"code": "validation", "message": invalid.Message, "field": invalid.Field,
			},
		})
		return
	}
	rt.log.Error("mini: "+what, "err", err)
	rt.fail(w, errInternal)
}

func (rt *Router) handleAppointmentCreate(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}
	form, bad := decodeWrite(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	appt, err := rt.appointments.Create(form)
	if err != nil {
		rt.writeError(w, err, "create appointment")
		return
	}
	rt.writeJSON(w, http.StatusCreated, map[string]int64{"id": appt.ID})
}

func (rt *Router) handleAppointmentUpdate(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	form, bad := decodeWrite(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	// Editing a row that is gone must not silently create nothing and report
	// success — the store's UPDATE would match no rows and return no error.
	if _, err := rt.appointments.Get(id); err != nil {
		rt.fail(w, errNotFound)
		return
	}
	if _, err := rt.appointments.Update(id, form); err != nil {
		rt.writeError(w, err, "update appointment")
		return
	}
	rt.writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (rt *Router) handleAppointmentDelete(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	if _, err := rt.appointments.Get(id); err != nil {
		rt.fail(w, errNotFound)
		return
	}
	if err := rt.appointments.Delete(id); err != nil {
		rt.log.Error("mini: delete appointment", "err", err)
		rt.fail(w, errInternal)
		return
	}
	rt.writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

// handlePersons feeds the "хто" suggestion list. Appointment.Person is free
// text — it can be a guest or "обоє" — so these are hints, never a constraint.
func (rt *Router) handlePersons(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}
	persons, err := rt.store.ListPersons()
	if err != nil {
		rt.log.Error("mini: list persons", "err", err)
		rt.fail(w, errInternal)
		return
	}
	names := make([]string, 0, len(persons))
	for _, p := range persons {
		names = append(names, p.Name)
	}
	rt.writeJSON(w, http.StatusOK, map[string][]string{"persons": names})
}

func pathID(r *http.Request) (int64, *apiError) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errBadRequest
	}
	return id, nil
}

// The upcoming-appointments screen. This replaces the bot's /list — a
// self-editing message paged one calendar week at a time — with a plain
// scrolling list, which fits "what is coming up" better than paging does.

// Times are formatted here, not on the client. appointments.starts_at is a
// naive local wall clock in the app's zone; sent as an ISO timestamp the client
// would parse it in the *device's* zone and shift it, and the device is a phone
// that can be anywhere. So the client never parses a date — it renders strings.
// Time and EndTime double as display text and as the form's HH:MM fields, so
// opening the editor needs no second request — the list already carries
// everything the form binds to. Cost rides along for the same reason.
type itemDTO struct {
	ID       int64  `json:"id"`
	Time     string `json:"time"`
	EndTime  string `json:"endTime"`
	Title    string `json:"title"`
	Person   string `json:"person"`
	Location string `json:"location"`
	Note     string `json:"note"`
	Status   string `json:"status"`
	Cost     string `json:"cost"`
	// Duration is what the editor's chips bind to; EndTime stays for display.
	Duration string `json:"duration"`
}

type dayDTO struct {
	Date  string    `json:"date"` // render key, never displayed
	Label string    `json:"label"`
	Items []itemDTO `json:"items"`
}

type appointmentsDTO struct {
	Days []dayDTO `json:"days"`
	// Truncated says the cap was reached and there is more beyond the last
	// day shown. Without it the list would simply end, and a family with a
	// busy month would have no way of telling a quiet future from a cut-off
	// one. It is not pagination — it is refusing to lie about the horizon.
	Truncated bool `json:"truncated"`
}

func (rt *Router) handleAppointments(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}

	// From the start of today, not from this minute: the list is grouped under
	// day headers, and a "Сьогодні" section that silently drops the morning
	// reads as broken. It also answers "what did we have today".
	now := rt.now().In(rt.loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, rt.loc)

	// One more than the cap, so reaching it is detectable rather than assumed.
	items, err := rt.store.UpcomingAppointments(from.Format(model.LocalDatetime), maxAppointments+1)
	if err != nil {
		rt.log.Error("mini: upcoming appointments", "err", err)
		rt.fail(w, errInternal)
		return
	}
	truncated := len(items) > maxAppointments
	if truncated {
		items = items[:maxAppointments]
		rt.log.Info("mini: upcoming list truncated", "cap", maxAppointments)
	}
	rt.writeJSON(w, http.StatusOK, appointmentsDTO{
		Days:      groupByDay(items, now, rt.loc),
		Truncated: truncated,
	})
}

// groupByDay folds the flat, already-ascending list into day sections. Rows
// whose starts_at will not parse are dropped rather than failing the screen —
// one bad row must not hide the rest of the week.
func groupByDay(items []model.Appointment, now time.Time, loc *time.Location) []dayDTO {
	days := make([]dayDTO, 0, 8)
	for _, a := range items {
		start, err := a.Start(loc)
		if err != nil {
			continue
		}
		date := start.Format("2006-01-02")
		if len(days) == 0 || days[len(days)-1].Date != date {
			days = append(days, dayDTO{Date: date, Label: dayLabel(start, now)})
		}
		d := &days[len(days)-1]
		d.Items = append(d.Items, itemDTO{
			ID:       a.ID,
			Time:     start.Format("15:04"),
			EndTime:  endTime(a, loc),
			Title:    a.Title,
			Person:   a.Person,
			Location: a.Location,
			Note:     a.Note,
			Status:   a.Status,
			Cost:     appointments.FormatCost(a.Cost),
			Duration: appointments.DurationOf(a, loc),
		})
	}
	return days
}

func endTime(a model.Appointment, loc *time.Location) string {
	if a.EndsAt == "" {
		return ""
	}
	end, err := time.ParseInLocation(model.LocalDatetime, a.EndsAt, loc)
	if err != nil {
		return ""
	}
	return end.Format("15:04")
}

// dayLabel names a day relative to now: "Сьогодні, 6 серпня", "Завтра, …",
// otherwise "Чт, 13 серпня".
func dayLabel(day, now time.Time) string {
	date := day.Format("2006-01-02")
	switch date {
	case now.Format("2006-01-02"):
		return "Сьогодні, " + dayAndMonth(day)
	case now.AddDate(0, 0, 1).Format("2006-01-02"):
		return "Завтра, " + dayAndMonth(day)
	}
	return model.WeekdayLabels[int(day.Weekday())] + ", " + dayAndMonth(day)
}

func dayAndMonth(t time.Time) string {
	return strconv.Itoa(t.Day()) + " " + model.MonthsGenitive[int(t.Month())]
}
