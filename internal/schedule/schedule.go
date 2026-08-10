// Package schedule holds the rules for a course's weekly slots — the
// "Вівторок, Четвер, 13:35" of an enrollment — above the store and below any
// HTTP surface.
//
// Changing when a course happens is the one thing the app could not do at all:
// slots could be added and deleted, never edited, so moving a lesson meant
// adding the new times and hunting down the old ones. Editing is why this
// package exists; sharing the rules with the web form is why it is a package
// and not a handler.
package schedule

import (
	"strconv"
	"strings"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
	"familyhub/internal/valid"
)

// DefaultDurationMin is what a slot lasts when nobody says otherwise. Only the
// ICS feed reads it; the reminder scheduler keys off the start.
const DefaultDurationMin = 60

// Form is one weekly slot as filled in, on either surface. Strings for the same
// reason the appointment form uses them: that is what an input produces, and a
// rejected value has to survive a re-render.
type Form struct {
	Weekday  string // "0" (Sunday) .. "6"
	Time     string // HH:MM
	Duration string // minutes; empty means DefaultDurationMin
}

// Slot is a validated slot, ready for the store.
type Slot struct {
	Weekday     int
	Time        string
	DurationMin int
}

func (f Form) Parse() (Slot, error) {
	var s Slot

	weekday, err := strconv.Atoi(strings.TrimSpace(f.Weekday))
	if err != nil || weekday < 0 || weekday > 6 {
		return s, valid.FieldError{Field: "weekday", Message: "вибери день тижня"}
	}
	s.Weekday = weekday

	// The web form used to accept any non-empty string here, so "abc" could
	// reach the database and silently never fire a reminder.
	hhmm := strings.TrimSpace(f.Time)
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return s, valid.FieldError{Field: "time", Message: "вкажи час у форматі 13:35"}
	}
	// Reformat so "9:05" and "09:05" cannot both live in the table: the bot
	// compares these as strings.
	s.Time = t.Format("15:04")

	s.DurationMin = DefaultDurationMin
	if d := strings.TrimSpace(f.Duration); d != "" {
		minutes, err := strconv.Atoi(d)
		if err != nil || minutes <= 0 {
			return s, valid.FieldError{Field: "duration", Message: "тривалість має бути числом хвилин"}
		}
		s.DurationMin = minutes
	}
	return s, nil
}

// FormOf renders a stored slot back into the fields a form edits.
func FormOf(s model.Slot) Form {
	return Form{
		Weekday:  strconv.Itoa(s.Weekday),
		Time:     s.Time,
		Duration: strconv.Itoa(s.DurationMin),
	}
}

// Service performs the slot writes both surfaces share.
type Service struct {
	store *store.Store
}

func NewService(st *store.Store) *Service { return &Service{store: st} }

func (s *Service) List(enrollmentID int64) ([]model.Slot, error) {
	return s.store.ListSlots(enrollmentID)
}

func (s *Service) Add(enrollmentID int64, f Form) error {
	slot, err := f.Parse()
	if err != nil {
		return err
	}
	return s.store.CreateSlot(enrollmentID, slot.Weekday, slot.Time, slot.DurationMin)
}

// Update moves an existing slot. This is the operation the app was missing:
// without it, "Логопед is now Tuesday and Thursday at 13:35" meant adding two
// slots and deleting two others by hand.
func (s *Service) Update(slotID int64, f Form) error {
	slot, err := f.Parse()
	if err != nil {
		return err
	}
	return s.store.UpdateSlot(slotID, slot.Weekday, slot.Time, slot.DurationMin)
}

func (s *Service) Delete(slotID int64) error {
	return s.store.DeleteSlot(slotID)
}
