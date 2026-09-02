// Package appointments holds the appointment write rules — what a valid
// appointment is, and what saving one means — above the store and below any
// HTTP surface.
//
// It exists because there are now two ways in: the web form and the Mini App's
// JSON API. Without it the same rules ("give it a name", "the end must be after
// the start", "an empty amount is not zero") would live in two handlers and
// drift apart. Neither surface may re-implement them.
//
// Telling the family group is one of those rules — see notify.go — and so is
// how an appointment reads once told; see format.go.
package appointments

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"familyhub/internal/actor"
	"familyhub/internal/model"
	"familyhub/internal/store"
	"familyhub/internal/valid"
)

// maxDurationMin keeps a mistyped duration from producing an appointment that
// runs for weeks. A day is already far past anything real here.
const maxDurationMin = 24 * 60

// Form is what a person filled in, on either surface. Every field is a string
// because that is what a form produces, and because a bad value has to survive
// a re-render so it can be corrected rather than silently dropped.
type Form struct {
	Title    string
	Person   string
	Location string
	Date     string // YYYY-MM-DD
	Time     string // HH:MM
	EndTime  string // HH:MM; empty means open-ended
	Duration string // minutes; an alternative to EndTime, not an addition
	Status   string
	Note     string
	Cost     string // empty means "nobody wrote it down"; "0" means it was free
}

// InvalidField is the shared field-level validation error. It stays named here
// because that is how the appointment rules read at their call sites.
type InvalidField = valid.FieldError

// Parse validates the form and builds the appointment it describes. loc is the
// wall-clock zone the times are written in — appointments are stored as naive
// local time, so this is the zone they mean, not a conversion target.
func (f Form) Parse(loc *time.Location) (model.Appointment, error) {
	a := model.Appointment{
		Title:    strings.TrimSpace(f.Title),
		Person:   strings.TrimSpace(f.Person),
		Location: strings.TrimSpace(f.Location),
		Note:     strings.TrimSpace(f.Note),
		Status:   strings.TrimSpace(f.Status),
	}
	if a.Title == "" {
		return a, InvalidField{Field: "title", Message: "вкажи назву"}
	}

	date := strings.TrimSpace(f.Date)
	hhmm := strings.TrimSpace(f.Time)
	start, err := time.ParseInLocation(model.LocalDatetime, date+"T"+hhmm, loc)
	if err != nil {
		return a, InvalidField{Field: "date", Message: "вкажи коректну дату й час"}
	}
	a.StartsAt = date + "T" + hhmm

	// Two ways to say when it ends, because the two surfaces ask differently:
	// the web form has a second clock, while on a phone "how long does it take"
	// is a tap on a chip. An explicit end time wins when both are given.
	switch end := strings.TrimSpace(f.EndTime); {
	case end != "":
		if _, err := time.ParseInLocation(model.LocalDatetime, date+"T"+end, loc); err != nil {
			return a, InvalidField{Field: "endTime", Message: "вкажи коректний час завершення"}
		}
		// Same-day only on this path, and the string compare is safe because
		// both are zero-padded HH:MM.
		if end <= hhmm {
			return a, InvalidField{Field: "endTime", Message: "час завершення має бути пізніше початку"}
		}
		a.EndsAt = date + "T" + end

	case strings.TrimSpace(f.Duration) != "":
		minutes, err := strconv.Atoi(strings.TrimSpace(f.Duration))
		if err != nil || minutes <= 0 {
			return a, InvalidField{Field: "duration", Message: "тривалість має бути числом хвилин"}
		}
		if minutes > maxDurationMin {
			return a, InvalidField{Field: "duration", Message: "надто довго — вкажи менше доби"}
		}
		// Adding to the parsed start rather than to the string means a late
		// evening appointment can legitimately end after midnight.
		a.EndsAt = start.Add(time.Duration(minutes) * time.Minute).Format(model.LocalDatetime)
	}

	if _, ok := model.ApptStatusLabels[a.Status]; !ok {
		return a, InvalidField{Field: "status", Message: "вибери статус"}
	}

	// An empty amount means "not recorded" (NULL), which is not the same as 0 —
	// a free visit is recorded by typing 0.
	if raw := strings.TrimSpace(f.Cost); raw != "" {
		cost, ok := ParseCost(raw)
		if !ok {
			return a, InvalidField{Field: "cost", Message: "сума має бути числом, напр. 800 (або 0)"}
		}
		a.Cost = &cost
	}
	return a, nil
}

// ParseCost accepts what a person types into an amount field: "800", "1 200",
// "1200,50". Negative is rejected; 0 means the visit was free.
func ParseCost(s string) (float64, bool) {
	s = strings.NewReplacer(" ", "", "\u00a0", "", ",", ".").Replace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// FormatCost renders a stored amount back into a form field: "" when unset, no
// trailing zeros for whole numbers.
func FormatCost(c *float64) string {
	if c == nil {
		return ""
	}
	return strconv.FormatFloat(*c, 'f', -1, 64)
}

// SplitStart pulls a stored StartsAt apart into the date and time fields a form
// edits. A value that will not split is returned as-is in the date half so the
// form shows something rather than silently blanking the row.
func SplitStart(startsAt string) (date, hhmm string) {
	d, t, ok := strings.Cut(startsAt, "T")
	if !ok {
		return startsAt, ""
	}
	return d, t
}

// Service performs the appointment writes both surfaces share, and posts the
// result to the family group — see notify.go for why that belongs here.
type Service struct {
	store  *store.Store
	loc    *time.Location
	notify Notifier // nil — bot off or no group configured; writes stay silent
	log    *slog.Logger
}

// NewService wires the writes. notify may be nil; logger may be nil, in which
// case the default logger is used.
func NewService(st *store.Store, loc *time.Location, notify Notifier, logger *slog.Logger) *Service {
	if loc == nil {
		loc = time.Local
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, loc: loc, notify: notify, log: logger}
}

func (s *Service) Get(id int64) (model.Appointment, error) {
	return s.store.GetAppointment(id)
}

// by names whoever is making the change: it is the group message's byline, it
// is recorded as the row's author, and it is what a person field written as
// "Я" resolves to. It is empty when the surface cannot tell.
func (s *Service) Create(f Form, by string) (model.Appointment, error) {
	a, err := f.Parse(s.loc)
	if err != nil {
		return a, err
	}
	a.Person = actor.Resolve(a.Person, by)
	if by != actor.Unknown {
		a.CreatedBy = by
	}
	saved, err := s.store.CreateAppointment(a)
	if err != nil {
		return saved, err
	}
	s.announce(GroupAddText([]model.Appointment{saved}, by, s.loc))
	return saved, nil
}

// Update rewrites the editable fields of an existing appointment. Fields the
// form does not own — the captured `raw` text, the cost-prompt message id, the
// Home Assistant outbox — are left alone by the store.
func (s *Service) Update(id int64, f Form, by string) (model.Appointment, error) {
	a, err := f.Parse(s.loc)
	if err != nil {
		return a, err
	}
	// The previous row decides what the group is told: an edit that switches the
	// status to cancelled is the cancellation, and reading "🔄 Візит змінено"
	// would bury it.
	prev, err := s.store.GetAppointment(id)
	if err != nil {
		return a, err
	}
	a.ID = id
	// Resolved on edit too, so re-saving a form that shows "Я" does not write
	// it back. created_by is not touched: it says who entered the row, and an
	// editor is not the author.
	a.Person = actor.Resolve(a.Person, by)
	a.CreatedBy = prev.CreatedBy
	if err := s.store.UpdateAppointment(a); err != nil {
		return a, err
	}
	if a.Status == model.ApptStatusCancelled && prev.Status != model.ApptStatusCancelled {
		s.announce(GroupCancelText(a, by, s.loc))
	} else {
		s.announce(GroupChangeText(a, "змінено", by, s.loc))
	}
	return a, nil
}

func (s *Service) Delete(id int64, by string) error {
	a, err := s.store.GetAppointment(id)
	if err != nil {
		return err
	}
	if err := s.store.SoftDeleteAppointment(id); err != nil {
		return err
	}
	s.announce(GroupDeleteText(a, by, s.loc))
	return nil
}

// announce is best-effort: the write already succeeded, so a Telegram outage
// must not be reported back as a failed save — that invites a second attempt
// and a duplicate row. It is logged instead.
func (s *Service) announce(text string) {
	if s.notify == nil {
		return
	}
	if err := s.notify.NotifyHTML(text); err != nil {
		s.log.Error("appointments: notify group", "err", err)
	}
}

// DurationOf reports how many minutes an appointment lasts, as the form's
// field: "" when no end was recorded. It is the inverse of the Duration path
// in Parse, so the editor can pre-select the chip that was chosen.
func DurationOf(a model.Appointment, loc *time.Location) string {
	if a.EndsAt == "" {
		return ""
	}
	start, err := a.Start(loc)
	if err != nil {
		return ""
	}
	end, err := time.ParseInLocation(model.LocalDatetime, a.EndsAt, loc)
	if err != nil {
		return ""
	}
	minutes := int(end.Sub(start).Minutes())
	if minutes <= 0 {
		return ""
	}
	return strconv.Itoa(minutes)
}
