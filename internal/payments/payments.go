// Package payments holds the rules for recording money against a course —
// what a payment buys and how a form describing it is validated — above the
// store and below any HTTP surface.
//
// A payment means one of two things, and which one is not the person's to
// choose: a per-lesson course is paid for in lessons, a monthly one in whole
// calendar months. The enrollment decides, so the branch lives here rather
// than once in the web form and again in the Mini App.
package payments

import (
	"strconv"
	"strings"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
	"familyhub/internal/valid"
)

// Form is a payment as filled in, on either surface. Strings for the same
// reason the appointment and slot forms use them: that is what an input
// produces, and a rejected value has to survive a re-render.
type Form struct {
	Date        string // YYYY-MM-DD — the day the money moved
	Amount      string
	Lessons     string // per-lesson billing: how many lessons this buys
	CoversMonth string // monthly billing: "2026-09"
	Comment     string
}

// Parse validates the form for a course billed this way and returns the row to
// store. The payment comes back filled as far as parsing got, so a surface
// that re-renders the form still has what the person typed.
func (f Form) Parse(billingType string) (model.Payment, error) {
	p := model.Payment{
		Date:    strings.TrimSpace(f.Date),
		Comment: strings.TrimSpace(f.Comment),
	}
	if _, err := model.ParseDate(p.Date); err != nil {
		return p, valid.FieldError{Field: "date", Message: "вкажи коректну дату оплати"}
	}
	// Zero is allowed: a lesson can be given for free and still be worth
	// recording as one that was paid for.
	amount, err := strconv.ParseFloat(strings.TrimSpace(f.Amount), 64)
	if err != nil || amount < 0 {
		return p, valid.FieldError{Field: "amount", Message: "вкажи коректну суму"}
	}
	p.Amount = amount

	if billingType == model.BillingMonthly {
		from, until, err := monthRange(strings.TrimSpace(f.CoversMonth))
		if err != nil {
			return p, valid.FieldError{Field: "month", Message: "вкажи місяць, за який оплата"}
		}
		p.CoversFrom, p.CoversUntil = &from, &until
		return p, nil
	}

	lessons, err := strconv.ParseInt(strings.TrimSpace(f.Lessons), 10, 64)
	if err != nil || lessons <= 0 {
		return p, valid.FieldError{Field: "lessons", Message: "вкажи кількість оплачених занять"}
	}
	p.LessonsPaid = &lessons
	return p, nil
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

// Service performs the payment writes both surfaces share.
type Service struct {
	store *store.Store
}

func NewService(st *store.Store) *Service { return &Service{store: st} }

// Prepare resolves the course and validates the form against how it is billed.
// The returned payment is what would be stored; on a validation failure it
// carries whatever parsed, for re-rendering the form.
func (s *Service) Prepare(enrollmentID int64, f Form) (model.Payment, error) {
	if enrollmentID == 0 {
		return model.Payment{}, valid.FieldError{Field: "enrollment", Message: "вибери курс"}
	}
	enrollment, err := s.store.GetEnrollment(enrollmentID)
	if err != nil {
		return model.Payment{EnrollmentID: enrollmentID}, valid.FieldError{Field: "enrollment", Message: "курс не знайдено"}
	}
	p, err := f.Parse(enrollment.BillingType)
	p.EnrollmentID = enrollmentID
	return p, err
}

func (s *Service) Create(enrollmentID int64, f Form) (model.Payment, error) {
	p, err := s.Prepare(enrollmentID, f)
	if err != nil {
		return p, err
	}
	id, err := s.store.CreatePayment(p)
	if err != nil {
		return p, err
	}
	p.ID = id
	return p, nil
}

func (s *Service) Update(id, enrollmentID int64, f Form) (model.Payment, error) {
	p, err := s.Prepare(enrollmentID, f)
	p.ID = id
	if err != nil {
		return p, err
	}
	return p, s.store.UpdatePayment(p)
}

func (s *Service) Delete(id int64) error { return s.store.DeletePayment(id) }
