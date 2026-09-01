package web

import (
	"net/http"
	"strconv"
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
}

func (a *App) handleMeterReadings(w http.ResponseWriter, r *http.Request) {
	addresses, err := a.Store.AddressesActive()
	if err != nil {
		a.serverError(w, err)
		return
	}

	d := metersReadingsData{Addresses: addresses, Period: currentPeriod()}
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
