package mini

import (
	"net/http"
	"strconv"
	"time"

	"familyhub/internal/model"
)

// The upcoming-appointments screen. This replaces the bot's /list — a
// self-editing message paged one calendar week at a time — with a plain
// scrolling list, which fits "what is coming up" better than paging does.

// Times are formatted here, not on the client. appointments.starts_at is a
// naive local wall clock in the app's zone; sent as an ISO timestamp the client
// would parse it in the *device's* zone and shift it, and the device is a phone
// that can be anywhere. So the client never parses a date — it renders strings.
type itemDTO struct {
	ID       int64  `json:"id"`
	Time     string `json:"time"`
	EndTime  string `json:"endTime"`
	Title    string `json:"title"`
	Person   string `json:"person"`
	Location string `json:"location"`
	Note     string `json:"note"`
	Status   string `json:"status"`
}

type dayDTO struct {
	Date  string    `json:"date"` // render key, never displayed
	Label string    `json:"label"`
	Items []itemDTO `json:"items"`
}

type appointmentsDTO struct {
	Days []dayDTO `json:"days"`
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

	items, err := rt.store.UpcomingAppointments(from.Format(model.LocalDatetime), maxAppointments)
	if err != nil {
		rt.log.Error("mini: upcoming appointments", "err", err)
		rt.fail(w, errInternal)
		return
	}
	rt.writeJSON(w, http.StatusOK, appointmentsDTO{Days: groupByDay(items, now, rt.loc)})
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
