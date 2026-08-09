package mini

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The home screen answers "what is going on right now": what is coming up,
// which courses are running out of paid lessons, and what was paid recently.
// It is the phone-shaped version of the web dashboard — the same three things,
// but a balance is one sentence instead of a table row, because that is what
// fits in a glance.

const (
	homeAppointments = 5
	homePayments     = 6
)

type homeCourseDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Person   string `json:"person"`
	State    string `json:"state"`    // ok | low | empty — drives the dot
	Balance  string `json:"balance"`  // "залишилось 3 заняття"
	Schedule string `json:"schedule"` // "Вт 13:35 · Чт 13:35"
	Absence  string `json:"absence"`  // why this course is quiet, if it is
}

type homePaymentDTO struct {
	ID     int64  `json:"id"`
	Date   string `json:"date"`
	Amount string `json:"amount"`
	Course string `json:"course"`
	Person string `json:"person"`
	Detail string `json:"detail"` // "10 занять" or "до 31.08"
}

type homeVisitDTO struct {
	ID     int64  `json:"id"`
	When   string `json:"when"` // "Сьогодні, 14:30"
	Title  string `json:"title"`
	Person string `json:"person"`
}

type homeDTO struct {
	Upcoming []homeVisitDTO   `json:"upcoming"`
	Courses  []homeCourseDTO  `json:"courses"`
	Payments []homePaymentDTO `json:"payments"`
}

func (rt *Router) handleHome(w http.ResponseWriter, r *http.Request) {
	if _, err := rt.v.authenticate(r); err != nil {
		rt.fail(w, err)
		return
	}
	now := rt.now().In(rt.loc)

	balances, err := rt.store.Balances()
	if err != nil {
		rt.log.Error("mini: balances", "err", err)
		rt.fail(w, errInternal)
		return
	}
	absences, err := rt.store.ActiveAbsenceByEnrollment(now.Format("2006-01-02"))
	if err != nil {
		rt.log.Error("mini: absences", "err", err)
		rt.fail(w, errInternal)
		return
	}
	slots, err := rt.store.AllActiveSlots()
	if err != nil {
		rt.log.Error("mini: slots", "err", err)
		rt.fail(w, errInternal)
		return
	}
	payments, err := rt.store.ListPayments(store.PaymentFilter{Limit: homePayments})
	if err != nil {
		rt.log.Error("mini: payments", "err", err)
		rt.fail(w, errInternal)
		return
	}
	// From this minute, not from the start of the day: this section is "what
	// is next", so a visit that already happened this morning does not belong
	// at the top of it. The Записи tab deliberately starts earlier — it groups
	// by day, and a "Сьогодні" heading that hides the morning reads as broken.
	upcoming, err := rt.store.UpcomingAppointments(now.Format(model.LocalDatetime), homeAppointments)
	if err != nil {
		rt.log.Error("mini: upcoming", "err", err)
		rt.fail(w, errInternal)
		return
	}

	rt.writeJSON(w, http.StatusOK, homeDTO{
		Upcoming: homeVisits(upcoming, now, rt.loc),
		Courses:  homeCourses(balances, absences, scheduleLines(slots)),
		Payments: homePaymentRows(payments),
	})
}

// scheduleLines folds the flat slot list into one line per course, Monday
// first — Go's weekday codes start on Sunday, which reads wrong on a card.
func scheduleLines(slots []store.SlotWithEnrollment) map[int64]string {
	lines := make(map[int64]string)
	for _, wd := range []int{1, 2, 3, 4, 5, 6, 0} {
		for _, s := range slots {
			if s.Slot.Weekday != wd {
				continue
			}
			part := model.WeekdayLabels[wd] + " " + s.Slot.Time
			if cur := lines[s.Enrollment.ID]; cur != "" {
				part = cur + " · " + part
			}
			lines[s.Enrollment.ID] = part
		}
	}
	return lines
}

func homeCourses(balances []model.Balance, absences map[int64]*model.TrainerAbsence, schedule map[int64]string) []homeCourseDTO {
	out := make([]homeCourseDTO, 0, len(balances))
	for _, b := range balances {
		if !b.Active {
			continue
		}
		c := homeCourseDTO{
			ID:       b.ID,
			Name:     b.Name,
			Person:   b.Person,
			State:    b.State(),
			Balance:  balanceLine(b),
			Schedule: schedule[b.ID],
		}
		// A course goes quiet while its trainer is away — reminders stop and
		// the ICS feed drops it — so the card says why rather than looking
		// broken.
		if a := absences[b.ID]; a != nil {
			c.Absence = absenceLine(*a)
		}
		out = append(out, c)
	}
	return out
}

// balanceLine states a balance the way a person would say it out loud.
func balanceLine(b model.Balance) string {
	if b.BillingType == model.BillingMonthly {
		if !b.CoveredNow {
			return "абонемент не оплачено"
		}
		return "абонемент до " + shortDate(b.CoversUntil) + ", " + plural(b.DaysLeft, "день", "дні", "днів")
	}
	switch {
	case b.Remaining < 0:
		return "борг " + plural(-b.Remaining, "заняття", "заняття", "занять")
	case b.Remaining == 0:
		return "оплачених занять немає"
	default:
		return "залишилось " + plural(b.Remaining, "заняття", "заняття", "занять")
	}
}

func absenceLine(a model.TrainerAbsence) string {
	kind := model.AbsenceKindLabels[a.Kind]
	if kind == "" {
		kind = a.Kind
	}
	return a.Trainer + ": " + kind + " до " + shortDate(a.DateTo)
}

func homePaymentRows(payments []model.Payment) []homePaymentDTO {
	out := make([]homePaymentDTO, 0, len(payments))
	for _, p := range payments {
		row := homePaymentDTO{
			ID:     p.ID,
			Date:   shortDate(p.Date),
			Amount: money(p.Amount),
			Course: p.Class,
			Person: p.Person,
		}
		switch {
		case p.LessonsPaid != nil && *p.LessonsPaid > 0:
			row.Detail = plural(int(*p.LessonsPaid), "заняття", "заняття", "занять")
		case p.CoversUntil != nil && *p.CoversUntil != "":
			row.Detail = "до " + shortDate(*p.CoversUntil)
		}
		out = append(out, row)
	}
	return out
}

func homeVisits(items []model.Appointment, now time.Time, loc *time.Location) []homeVisitDTO {
	out := make([]homeVisitDTO, 0, len(items))
	for _, a := range items {
		start, err := a.Start(loc)
		if err != nil {
			continue
		}
		out = append(out, homeVisitDTO{
			ID:     a.ID,
			When:   dayLabel(start, now) + ", " + start.Format("15:04"),
			Title:  a.Title,
			Person: a.Person,
		})
	}
	return out
}

// shortDate turns a stored YYYY-MM-DD into the 31.08 people write.
func shortDate(s string) string {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return s
	}
	return t.Format("02.01")
}

func money(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10) + " ₴"
	}
	return fmt.Sprintf("%.2f ₴", v)
}

// plural picks the Ukrainian form: 1 заняття, 2 заняття, 5 занять.
func plural(n int, one, few, many string) string {
	form := many
	switch mod100 := n % 100; {
	case mod100 >= 11 && mod100 <= 14:
	default:
		switch n % 10 {
		case 1:
			form = one
		case 2, 3, 4:
			form = few
		}
	}
	return strconv.Itoa(n) + " " + form
}
