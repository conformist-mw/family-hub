package mini

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"familyhub/internal/agenda"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The home screen answers "what is going on right now": what is happening
// today, what is coming up, which courses are running out of paid lessons, and
// what was paid recently. It is the phone-shaped version of the web hub — the
// same day, but a balance is one sentence instead of a table row, because that
// is what fits in a glance.
//
// The day itself comes from internal/agenda, shared with the web hub. This
// screen used to list only appointments, so the two things a schedule is for —
// "Карате at 16:00" and "the bins go out tonight" — were the ones it would not
// tell you.

const (
	// homeUpcoming caps the "Найближче" list. The Сьогодні block above it is
	// never capped: a day is as long as it is, and hiding the end of it would
	// be the bug this screen was opened to avoid.
	homeUpcoming = 6
	homePayments = 6
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
	Detail string `json:"detail"` // "10 занять", "до 31.08" or an extra's label
	// Kind is course | extra. The client marks an extra rather than letting
	// "Кімоно" sit in the same slot as "10 занять" unlabelled.
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Below is what the editor binds to, so tapping a row opens a filled form
	// with no second request — the same trade the appointments list makes.
	// Display and form values are separate fields on purpose: "5000 ₴" is not
	// something an input can take back.
	Billing string `json:"billing"` // monthly | per_lesson
	DateISO string `json:"dateISO"`
	Value   string `json:"value"` // amount as typed: "5000"
	Lessons string `json:"lessons"`
	Month   string `json:"month"` // "2026-09"
	Comment string `json:"comment"`
}

// agendaItemDTO is one row of the day, whatever kind of thing it is. Every
// string arrives rendered: the web hub shows the same day from the same
// builder, and a second formatting of "Ср 16:00" in JavaScript is how the two
// surfaces would start to disagree.
type agendaItemDTO struct {
	Kind   string `json:"kind"` // lesson | appointment | chore
	ID     int64  `json:"id"`
	When   string `json:"when"` // "16:00" today, "Ср 16:00" further out
	Title  string `json:"title"`
	Person string `json:"person"`
	Status string `json:"status"` // "проведено", "зроблено", "" while open
	Place  string `json:"place"`
}

type homeDTO struct {
	Date     string           `json:"date"` // "Понеділок, 10 серпня"
	Today    []agendaItemDTO  `json:"today"`
	Upcoming []agendaItemDTO  `json:"upcoming"`
	Courses  []homeCourseDTO  `json:"courses"`
	Payments []homePaymentDTO `json:"payments"`
	// Labels are the extra labels already in use, for the payment form's
	// quick-pick chips. They ride here rather than behind an endpoint of their
	// own because the form is opened from this screen and this payload is
	// already fetched by the time it can be.
	Labels []string `json:"labels"`
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
	// The whole day, from its start rather than from this minute: this block
	// is the picture of today, so a lesson that already happened this morning
	// is still part of it — with its status, if it has one.
	day, err := agenda.Load(rt.store, rt.reminders, now, rt.loc)
	if err != nil {
		rt.log.Error("mini: agenda", "err", err)
		rt.fail(w, errInternal)
		return
	}
	// Best-effort, unlike everything above it: a missing chip row costs a
	// shortcut, not the screen, and this one query must not be what turns the
	// home tab into an error.
	labels, err := rt.store.FrequentExtraLabels(6)
	if err != nil {
		rt.log.Error("mini: extra labels", "err", err)
		labels = nil
	}

	rt.writeJSON(w, http.StatusOK, homeDTO{
		Date:     model.WeekdayFull[int(now.Weekday())] + ", " + dayAndMonth(now),
		Today:    agendaRows(day.Today),
		Upcoming: agendaRows(head(day.Upcoming, homeUpcoming)),
		Courses:  homeCourses(balances, absences, scheduleLines(slots)),
		Payments: homePaymentRows(payments),
		Labels:   labels,
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
		switch {
		case b.CoveredNow:
			return "абонемент до " + shortDate(b.CoversUntil) + ", " + model.Plural(b.DaysLeft, "день", "дні", "днів")
		case b.PrepaidFrom != "":
			return "оплачено з " + shortDate(b.PrepaidFrom) + " до " + shortDate(b.CoversUntil)
		default:
			return "абонемент не оплачено"
		}
	}
	switch {
	case b.Remaining < 0:
		return "борг " + model.Plural(-b.Remaining, "заняття", "заняття", "занять")
	case b.Remaining == 0:
		return "оплачених занять немає"
	default:
		return "залишилось " + model.Plural(b.Remaining, "заняття", "заняття", "занять")
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
			ID:      p.ID,
			Date:    shortDate(p.Date),
			Amount:  money(p.Amount),
			Course:  p.Class,
			Person:  p.Person,
			Billing: p.Billing,
			Kind:    p.Kind,
			Label:   p.Label,
			DateISO: p.Date,
			Value:   strconv.FormatFloat(p.Amount, 'f', -1, 64),
			Month:   p.CoversMonth(),
			Comment: p.Comment,
		}
		switch {
		case p.IsExtra():
			row.Detail = p.Label
		case p.LessonsPaid != nil && *p.LessonsPaid > 0:
			row.Detail = model.Plural(int(*p.LessonsPaid), "заняття", "заняття", "занять")
			row.Lessons = strconv.FormatInt(*p.LessonsPaid, 10)
		case p.CoversUntil != nil && *p.CoversUntil != "":
			row.Detail = "до " + shortDate(*p.CoversUntil)
		}
		out = append(out, row)
	}
	return out
}

func agendaRows(items []agenda.Item) []agendaItemDTO {
	out := make([]agendaItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, agendaItemDTO{
			Kind:   it.Kind,
			ID:     it.ID,
			When:   it.When,
			Title:  it.Title,
			Person: it.Person,
			Status: it.Status,
			Place:  it.Place,
		})
	}
	return out
}

func head(items []agenda.Item, n int) []agenda.Item {
	if len(items) > n {
		return items[:n]
	}
	return items
}

// shortDate turns a stored YYYY-MM-DD into "31 сер". The numeric 31.08 read as
// a version number in a line that also carries counts and money.
func shortDate(s string) string {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return s
	}
	return strconv.Itoa(t.Day()) + " " + model.MonthsShort[int(t.Month())]
}

func money(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10) + " ₴"
	}
	return fmt.Sprintf("%.2f ₴", v)
}
