package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The utilities world — household bills, moved in from home-meters.
//
// /meters is a month, not a ledger: the question it exists to answer is "what
// still has to be entered and what still has to be paid", and that question is
// always about one month at one property. The full history is the report
// (task 10); this screen is the thing opened on the first of the month.

// metersReadingsData is one month at one property.
type metersReadingsData struct {
	Addresses  []model.Address
	AddressID  int64
	Currency   string
	Period     string
	PrevPeriod string
	// NextPeriod is empty when Period is the current month: there is nothing
	// to enter for a month that has not happened.
	NextPeriod string
	Rows       []store.UtilityMonthlyStatus
	Total      float64
	Unpaid     float64
	// Missing counts utilities with no reading — the number the screen is
	// opened to drive to zero.
	Missing int
	// CanSend is false when the bot is off, which hides the button rather than
	// offering one that answers with an error.
	CanSend bool
	Sent    bool
}

func (a *App) handleMeterReadings(w http.ResponseWriter, r *http.Request) {
	addresses, err := a.Store.AddressesActive()
	if err != nil {
		a.serverError(w, err)
		return
	}

	d := metersReadingsData{
		Addresses: addresses,
		Period:    currentPeriod(),
		CanSend:   a.Notifier != nil,
		Sent:      r.URL.Query().Get("sent") == "1",
	}
	if p := r.URL.Query().Get("period"); isPeriod(p) {
		d.Period = p
	}
	d.PrevPeriod = shiftPeriod(d.Period, -1)
	if d.Period < currentPeriod() {
		d.NextPeriod = shiftPeriod(d.Period, +1)
	}

	// An app with no properties yet renders its empty state rather than
	// refusing to load: nothing is broken, there is simply nothing here.
	if len(addresses) > 0 {
		d.AddressID = addresses[0].ID
		if id, err := strconv.ParseInt(r.URL.Query().Get("address_id"), 10, 64); err == nil {
			for _, ad := range addresses {
				if ad.ID == id {
					d.AddressID = id
				}
			}
		}
		for _, ad := range addresses {
			if ad.ID == d.AddressID {
				d.Currency = ad.Currency
			}
		}

		if d.Rows, err = a.Store.CurrentMonthStatus(d.Period, d.AddressID); err != nil {
			a.serverError(w, err)
			return
		}
		for _, row := range d.Rows {
			d.Total += row.Amount
			if !row.HasReading {
				d.Missing++
			} else if !row.Paid {
				d.Unpaid += row.Amount
			}
		}
	}

	a.render(w, "meters_readings.html", "Показання", "readings", d)
}

func currentPeriod() string { return time.Now().Format("2006-01") }

// isPeriod guards the month coming in from the query string. The value reaches
// SQL as a parameter, so this is not about injection — it is about a typo in a
// hand-edited URL silently returning an empty month that reads as "nothing was
// entered".
func isPeriod(p string) bool {
	if len(p) != 7 || p[4] != '-' {
		return false
	}
	_, err := time.Parse("2006-01", p)
	return err == nil
}

// shiftPeriod moves a "YYYY-MM" by whole months, carrying across the year.
func shiftPeriod(period string, delta int) string {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return period
	}
	return t.AddDate(0, delta, 0).Format("2006-01")
}

// ── the reading form ─────────────────────────────────────────────────────────

// readingFormData is one reading being entered or corrected.
type readingFormData struct {
	Reading model.Reading
	Utility model.Utility
	// Tariff is the one this reading is priced at: the utility's current tariff
	// when creating, the stored one when editing. Shown, never chosen — see
	// handleReadingCreate.
	Tariff  model.Tariff
	Address model.Address
	IsEdit  bool
	Error   string
	// Prev1/Prev2 as offered before the form is touched, so a blanked field
	// stays blank instead of silently refilling itself.
	Today string
}

func (a *App) handleReadingNew(w http.ResponseWriter, r *http.Request) {
	utilityID, _ := strconv.ParseInt(r.URL.Query().Get("utility_id"), 10, 64)
	d, err := a.newReadingForm(utilityID, r.URL.Query().Get("period"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.render(w, "reading_form.html", "Показання", "readings", d)
}

// newReadingForm assembles a blank reading for one utility: this month's
// "previous" is last month's "current", so the meter numbers carry forward and
// only the new ones have to be typed.
func (a *App) newReadingForm(utilityID int64, period string) (readingFormData, error) {
	u, err := a.Store.UtilityByID(utilityID)
	if err != nil {
		return readingFormData{}, err
	}
	addr, err := a.Store.AddressByID(u.AddressID)
	if err != nil {
		return readingFormData{}, err
	}
	if u.CurrentTariffID == nil {
		return readingFormData{}, errNoTariff
	}
	t, err := a.Store.TariffByID(*u.CurrentTariffID)
	if err != nil {
		return readingFormData{}, err
	}

	if !isPeriod(period) {
		period = currentPeriod()
	}
	d := readingFormData{
		Reading: model.Reading{UtilityID: utilityID, Period: period},
		Utility: u,
		Tariff:  t,
		Address: addr,
		Today:   today(),
	}
	if last, err := a.Store.LatestReadingForUtility(utilityID); err == nil {
		d.Reading.Prev1 = last.Curr1
		d.Reading.Prev2 = last.Curr2
	} else if !errors.Is(err, sql.ErrNoRows) {
		return readingFormData{}, err
	}
	return d, nil
}

// errNoTariff is a utility nobody has priced yet. Recorded as a refusal rather
// than a zero: a reading with no tariff has no amount, and writing one anyway
// would put a silent 0 ₴ into the month's total.
var errNoTariff = errors.New("сервіс не має тарифу")

func (a *App) handleReadingCreate(w http.ResponseWriter, r *http.Request) {
	utilityID, _ := strconv.ParseInt(r.FormValue("utility_id"), 10, 64)
	d, err := a.newReadingForm(utilityID, r.FormValue("period"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// The tariff written is the one in effect now, and it is never re-read
	// after: change the price next year and this month must keep the number it
	// was actually billed at. It is also what makes the replacement month
	// recordable — a second reading in the same period picks up the new tariff
	// and so does not collide with the first.
	reading := readingFromForm(r, d.Reading)
	reading.TariffID = d.Tariff.ID
	reading.ComputeAmount(d.Tariff)

	if _, err := a.Store.CreateReading(reading); err != nil {
		d.Reading = reading
		a.renderReadingError(w, d, err)
		return
	}
	http.Redirect(w, r, metersMonthURL(d.Address.ID, reading.Period), http.StatusSeeOther)
}

func (a *App) handleReadingEdit(w http.ResponseWriter, r *http.Request) {
	d, err := a.editReadingForm(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.render(w, "reading_form.html", "Показання", "readings", d)
}

func (a *App) editReadingForm(rawID string) (readingFormData, error) {
	id, _ := strconv.ParseInt(rawID, 10, 64)
	rv, err := a.Store.ReadingByID(id)
	if err != nil {
		return readingFormData{}, err
	}
	u, err := a.Store.UtilityByID(rv.UtilityID)
	if err != nil {
		return readingFormData{}, err
	}
	addr, err := a.Store.AddressByID(u.AddressID)
	if err != nil {
		return readingFormData{}, err
	}
	// The stored tariff, not the utility's current one: this is what the month
	// was billed at.
	t, err := a.Store.TariffByID(rv.TariffID)
	if err != nil {
		return readingFormData{}, err
	}
	return readingFormData{
		Reading: rv.Reading, Utility: u, Tariff: t, Address: addr,
		IsEdit: true, Today: today(),
	}, nil
}

func (a *App) handleReadingUpdate(w http.ResponseWriter, r *http.Request) {
	d, err := a.editReadingForm(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	reading := readingFromForm(r, d.Reading)
	reading.ComputeAmount(d.Tariff) // priced at what it was billed at, not at today's rate

	if err := a.Store.UpdateReading(reading); err != nil {
		d.Reading = reading
		a.renderReadingError(w, d, err)
		return
	}
	http.Redirect(w, r, metersMonthURL(d.Address.ID, reading.Period), http.StatusSeeOther)
}

func (a *App) handleReadingPaid(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rv, err := a.Store.ReadingByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := a.Store.TogglePaid(id, today()); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, metersMonthURL(rv.AddressID, rv.Period), http.StatusSeeOther)
}

func (a *App) handleReadingDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rv, err := a.Store.ReadingByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := a.Store.DeleteReading(id); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, metersMonthURL(rv.AddressID, rv.Period), http.StatusSeeOther)
}

// metersMonthURL is where every write returns: the month it belongs to, at the
// property it belongs to, rather than whichever month happens to be current.
func metersMonthURL(addressID int64, period string) string {
	return "/meters?address_id=" + strconv.FormatInt(addressID, 10) + "&period=" + period
}

func (a *App) renderReadingError(w http.ResponseWriter, d readingFormData, err error) {
	d.Error = err.Error()
	if !errors.Is(err, store.ErrDuplicateReading) {
		a.Logger.Error("web: reading write", "err", err)
		d.Error = "не вдалося зберегти показання"
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	a.render(w, "reading_form.html", "Показання", "readings", d)
}

// readingFromForm reads the posted fields onto base, which already carries the
// identity (id, utility, tariff) the form does not get to change.
func readingFromForm(r *http.Request, base model.Reading) model.Reading {
	out := base
	if p := r.FormValue("period"); isPeriod(p) {
		out.Period = p
	}
	out.ReadingDate = optionalText(r.FormValue("reading_date"))
	out.Comment = strings.TrimSpace(r.FormValue("comment"))
	out.Prev1 = optionalFloat(r.FormValue("prev1"))
	out.Curr1 = optionalFloat(r.FormValue("curr1"))
	out.Prev2 = optionalFloat(r.FormValue("prev2"))
	out.Curr2 = optionalFloat(r.FormValue("curr2"))
	// Paid is a checkbox rather than a date: the exact day a transfer cleared
	// is not something anyone recalls a week later, and the toggle on the month
	// view writes today's date anyway.
	if r.FormValue("paid") == "on" {
		if out.PaidDate == nil {
			out.PaidDate = optionalText(today())
		}
	} else {
		out.PaidDate = nil
	}
	return out
}

// optionalFloat keeps an empty field empty. A blank meter box means "not read
// yet", which is a different fact from a reading of zero — and the difference
// decides whether the month has an amount at all.
func optionalFloat(raw string) *float64 {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", "."))
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &v
}

func optionalText(raw string) *string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return &raw
}
