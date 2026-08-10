package mini

import (
	"net/http"

	"familyhub/internal/model"
	"familyhub/internal/schedule"
)

// The lessons screen: which courses are running, and when. Its reason to exist
// is the schedule — until now a course's weekly times could be added and
// deleted but never moved, so "Логопед is Tuesday and Thursday at 13:35" was a
// message in the family chat that nobody could act on from a phone.

type slotDTO struct {
	ID          int64  `json:"id"`
	Weekday     int    `json:"weekday"`
	WeekdayName string `json:"weekdayName"`
	Time        string `json:"time"`
	DurationMin int    `json:"durationMin"`
}

type courseDTO struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Person string `json:"person"`
	Note   string `json:"note"`
	// The same three strings the home screen shows. A person who came here to
	// move a lesson is the person who wants to know whether it is paid for,
	// and switching tabs to find out is a tab too many.
	State    string    `json:"state"`   // ok | low | empty
	Balance  string    `json:"balance"` // "залишилось 6 занять"
	Absence  string    `json:"absence"`
	Schedule []slotDTO `json:"schedule"`
}

type coursesDTO struct {
	Courses  []courseDTO `json:"courses"`
	Weekdays []string    `json:"weekdays"`
}

func (rt *Router) handleCourses(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}

	enrollments, err := rt.store.ListEnrollments(true)
	if err != nil {
		rt.log.Error("mini: list enrollments", "err", err)
		rt.fail(w, errInternal)
		return
	}
	balances, err := rt.store.Balances()
	if err != nil {
		rt.log.Error("mini: balances", "err", err)
		rt.fail(w, errInternal)
		return
	}
	byEnrollment := make(map[int64]model.Balance, len(balances))
	for _, b := range balances {
		byEnrollment[b.ID] = b
	}
	absences, err := rt.store.ActiveAbsenceByEnrollment(rt.now().In(rt.loc).Format("2006-01-02"))
	if err != nil {
		rt.log.Error("mini: absences", "err", err)
		rt.fail(w, errInternal)
		return
	}

	courses := make([]courseDTO, 0, len(enrollments))
	for _, e := range enrollments {
		slots, err := rt.schedule.List(e.ID)
		if err != nil {
			rt.log.Error("mini: list slots", "err", err, "enrollment", e.ID)
			rt.fail(w, errInternal)
			return
		}
		c := courseDTO{
			ID:       e.ID,
			Name:     e.Name,
			Person:   e.Person,
			Note:     e.Description,
			Schedule: slotDTOs(slots),
		}
		// A course with no balance row yet keeps State empty, and the client
		// then draws the card without the gauge rather than an empty one.
		if b, ok := byEnrollment[e.ID]; ok {
			c.State = b.State()
			c.Balance = balanceLine(b)
		}
		if a := absences[e.ID]; a != nil {
			c.Absence = absenceLine(*a)
		}
		courses = append(courses, c)
	}

	rt.writeJSON(w, http.StatusOK, coursesDTO{
		Courses:  courses,
		Weekdays: model.WeekdayLabels[:],
	})
}

func slotDTOs(slots []model.Slot) []slotDTO {
	out := make([]slotDTO, 0, len(slots))
	for _, s := range slots {
		if !s.Active {
			continue
		}
		out = append(out, slotDTO{
			ID:          s.ID,
			Weekday:     s.Weekday,
			WeekdayName: model.WeekdayLabels[s.Weekday],
			Time:        s.Time,
			DurationMin: s.DurationMin,
		})
	}
	return out
}

// slotForm is the JSON body of a slot write, mirroring schedule.Form.
type slotForm struct {
	Weekday  string `json:"weekday"`
	Time     string `json:"time"`
	Duration string `json:"duration"`
}

func decodeSlot(r *http.Request) (schedule.Form, *apiError) {
	var body slotForm
	if err := decodeJSON(r, &body); err != nil {
		return schedule.Form{}, errBadRequest
	}
	return schedule.Form{Weekday: body.Weekday, Time: body.Time, Duration: body.Duration}, nil
}

func (rt *Router) handleSlotCreate(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	form, bad := decodeSlot(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	if _, err := rt.store.GetEnrollment(id); err != nil {
		rt.fail(w, errNotFound)
		return
	}
	if err := rt.schedule.Add(id, form); err != nil {
		rt.writeError(w, err, "create slot")
		return
	}
	rt.writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

// handleSlotUpdate moves a slot in place. Recreating it instead would hand the
// ICS feed a new uid — Home Assistant keys on that, and the family calendar
// would end up with the lesson twice.
func (rt *Router) handleSlotUpdate(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	form, bad := decodeSlot(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	if _, err := rt.store.GetSlot(id); err != nil {
		rt.fail(w, errNotFound)
		return
	}
	if err := rt.schedule.Update(id, form); err != nil {
		rt.writeError(w, err, "update slot")
		return
	}
	rt.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (rt *Router) handleSlotDelete(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	if _, err := rt.store.GetSlot(id); err != nil {
		rt.fail(w, errNotFound)
		return
	}
	if err := rt.schedule.Delete(id); err != nil {
		rt.log.Error("mini: delete slot", "err", err)
		rt.fail(w, errInternal)
		return
	}
	rt.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
