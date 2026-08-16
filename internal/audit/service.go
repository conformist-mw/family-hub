package audit

import (
	"errors"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// Assembling the view — resolving the period, reading the store, deciding
// whether a forecast is even in range — used to live in the web handler, which
// made the Mini App's version of this screen a rewrite rather than a call. The
// arithmetic above stays pure; this file is the one part that reads.

// Params is a period as asked for, in the query-string vocabulary both
// surfaces already speak: last_payment | month | all | custom (+ From/To).
type Params struct {
	Range string
	From  string // custom only
	To    string // custom only
}

// Page is everything a surface needs to draw the reconciliation, plus the
// plain-text rendering of exactly what it shows, so copy-and-send can never
// diverge from the screen.
type Page struct {
	Enrollment  model.Enrollment
	Range       string
	From, To    string // the effective bounds, whichever range produced them
	PeriodLabel string
	Rows        []Row
	Summary     Summary
	Forecast    Forecast
	PerLesson   bool
	Text        string
	// Error is set when the requested period was rejected and the default one
	// was used instead: a bad custom date must not leave a blank screen.
	Error string
}

type Service struct {
	store *store.Store
	now   func() time.Time
}

// NewService wires the reads. now is injectable for tests; nil means time.Now.
func NewService(st *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: st, now: now}
}

func (s *Service) today() string { return s.now().Format("2006-01-02") }

// Build assembles the reconciliation for one enrollment over the asked-for
// period.
func (s *Service) Build(enrollmentID int64, p Params) (Page, error) {
	e, err := s.store.GetEnrollment(enrollmentID)
	if err != nil {
		return Page{}, err
	}
	page := Page{Enrollment: e, PerLesson: e.BillingType != model.BillingMonthly}

	rng, from, to, label, err := s.resolveRange(enrollmentID, p)
	if err != nil {
		// Bad custom input: keep the screen usable on the default period.
		page.Error = err.Error()
		rng, from, to, label, err = s.resolveRange(enrollmentID, Params{})
		if err != nil {
			return page, err
		}
	}
	page.Range, page.From, page.To = rng, from, to
	page.PeriodLabel = label
	if rng != "custom" { // the custom label already carries both dates
		page.PeriodLabel += " по " + dateFull(to)
	}

	d, err := s.store.AuditData(enrollmentID, from, to)
	if err != nil {
		return page, err
	}
	page.Rows, page.Summary = BuildLedger(d)

	td := s.today()
	if to > td {
		coversUntil := ""
		if e.BillingType == model.BillingMonthly {
			bal, err := s.store.BalanceFor(enrollmentID)
			if err != nil {
				return page, err
			}
			coversUntil = bal.CoversUntil
		}
		hasToday, err := s.store.VisitExistsForDate(enrollmentID, td)
		if err != nil {
			return page, err
		}
		page.Forecast = BuildForecast(e, d.Slots, d.Absences,
			page.Summary.Closing, coversUntil, td, to, hasToday)
	}

	page.Text = RenderText(View{
		Title:       e.Person + " · " + e.Name,
		PeriodLabel: page.PeriodLabel,
		BillingType: e.BillingType,
		Rows:        page.Rows,
		Summary:     page.Summary,
		Forecast:    page.Forecast,
	})
	return page, nil
}

// resolveRange turns the asked-for period into concrete bounds. It defaults to
// "since the last payment"; an enrollment with no payments falls back to
// all-time. Only a custom range may extend into the future — that is how the
// forecast is reached.
func (s *Service) resolveRange(enrollmentID int64, p Params) (rng, from, to, label string, err error) {
	td := s.today()
	rng = p.Range
	if rng == "" {
		rng = "last_payment"
	}
	switch rng {
	case "last_payment":
		lp, err := s.store.LastPaymentDate(enrollmentID)
		if err != nil {
			return "", "", "", "", err
		}
		if lp == "" {
			return "all", "", td, "за весь час", nil
		}
		return rng, lp, td, "з останньої оплати (" + dateFull(lp) + ")", nil
	case "month":
		return rng, s.now().Format("2006-01") + "-01", td, "цей місяць", nil
	case "all":
		return rng, "", td, "за весь час", nil
	case "custom":
		from, to = p.From, p.To
		if _, e1 := model.ParseDate(from); e1 != nil {
			return rng, "", "", "", errors.New("вкажи коректні дати")
		}
		if _, e2 := model.ParseDate(to); e2 != nil {
			return rng, "", "", "", errors.New("вкажи коректні дати")
		}
		if from > to {
			return rng, "", "", "", errors.New("початок періоду пізніше кінця")
		}
		return rng, from, to, dateFull(from) + " — " + dateFull(to), nil
	default:
		return "", "", "", "", errors.New("невідомий період")
	}
}

// dateFull is the period label's format — a whole date, unlike the ledger's
// compact dateShort, because a period that spans a new year has to say so.
func dateFull(s string) string {
	t, err := model.ParseDate(s)
	if err != nil {
		return s
	}
	return t.Format("02.01.2006")
}
