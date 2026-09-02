package web

import (
	"net/http"
	"sort"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := a.Store.Stats()
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "stats.html", "Статистика · Заняття", "stats_lessons", stats)
}

// overviewMonth is one month with both worlds' spend beside each other. The
// point of the page: rent-and-bills against lessons, in the only unit they
// share, which is the month they fell in.
type overviewMonth struct {
	Period    string
	Lessons   float64
	Utilities float64
	Total     float64
}

type overviewData struct {
	Lessons Stats
	Months  []overviewMonth
	Max     float64
	// Totals over the window the months cover, not over all history: a figure
	// summed across a different range than the bars beneath it invites the
	// reader to check the arithmetic and find it wrong.
	LessonsTotal   float64
	UtilitiesTotal float64
}

// Stats is the lessons breakdown, aliased so the template can reach both
// halves through one Data.
type Stats = store.Stats

// handleStatsOverview is the statistics world's landing page: what the
// household spends, by month, split into the two things it spends on.
func (a *App) handleStatsOverview(w http.ResponseWriter, r *http.Request) {
	lessons, err := a.Store.Stats()
	if err != nil {
		a.serverError(w, err)
		return
	}
	utilities, err := a.Store.MonthlyTotalsByAddress(statsWindowStart())
	if err != nil {
		a.serverError(w, err)
		return
	}

	// Keyed by month and then ordered, because the two sources arrive in
	// opposite orders and over ranges that need not match: a month with
	// lessons and no bills still has to appear, and so does the reverse.
	byMonth := map[string]*overviewMonth{}
	at := func(period string) *overviewMonth {
		if m, ok := byMonth[period]; ok {
			return m
		}
		m := &overviewMonth{Period: period}
		byMonth[period] = m
		return m
	}
	for _, m := range lessons.ByMonth {
		if m.Month >= statsWindowStart() {
			at(m.Month).Lessons += m.Amount
		}
	}
	for _, row := range utilities {
		at(row.Period).Utilities += row.Total
	}

	d := overviewData{Lessons: lessons}
	for _, m := range byMonth {
		m.Total = m.Lessons + m.Utilities
		d.Months = append(d.Months, *m)
		d.LessonsTotal += m.Lessons
		d.UtilitiesTotal += m.Utilities
		if m.Total > d.Max {
			d.Max = m.Total
		}
	}
	sort.Slice(d.Months, func(i, j int) bool { return d.Months[i].Period > d.Months[j].Period })

	a.render(w, "stats_overview.html", "Разом", "stats", d)
}

// statsWindowStart is how far back the comparison reaches: twelve months, the
// same window the lessons bars already use, so the two halves of a row cover
// the same stretch.
func statsWindowStart() string {
	return time.Now().AddDate(0, -11, 0).Format("2006-01")
}

// metersStatsData is the utilities world seen over time.
type metersStatsData struct {
	Addresses []model.Address
	// Totals is every property's month, so the table reads as one grid rather
	// than one table per property.
	Periods   []string
	ByAddress map[string]map[int64]float64
	Totals    map[string]float64
	Max       float64
	Currency  string

	// Compare is the cross-property consumption of one service, chosen from
	// the names that exist at more than one address.
	Names       []string
	Name        string
	Unit        string
	Consumption []store.MonthlyConsumption
	MaxUsed     float64
}

func (a *App) handleStatsMeters(w http.ResponseWriter, r *http.Request) {
	addresses, err := a.Store.AddressesActive()
	if err != nil {
		a.serverError(w, err)
		return
	}
	from := statsWindowStart()
	rows, err := a.Store.MonthlyTotalsByAddress(from)
	if err != nil {
		a.serverError(w, err)
		return
	}

	d := metersStatsData{
		Addresses: addresses,
		ByAddress: map[string]map[int64]float64{},
		Totals:    map[string]float64{},
	}
	if len(addresses) > 0 {
		d.Currency = addresses[0].Currency
	}
	for _, row := range rows {
		if _, ok := d.ByAddress[row.Period]; !ok {
			d.ByAddress[row.Period] = map[int64]float64{}
			d.Periods = append(d.Periods, row.Period)
		}
		d.ByAddress[row.Period][row.AddressID] += row.Total
		d.Totals[row.Period] += row.Total
		if d.Totals[row.Period] > d.Max {
			d.Max = d.Totals[row.Period]
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(d.Periods)))

	if d.Names, err = a.Store.UtilityNames(); err != nil {
		a.serverError(w, err)
		return
	}
	// The first shared name is the default rather than none: a comparison you
	// have to choose before seeing one is a comparison nobody looks at.
	if len(d.Names) > 0 {
		d.Name = d.Names[0]
		if picked := r.URL.Query().Get("utility"); picked != "" {
			for _, n := range d.Names {
				if n == picked {
					d.Name = picked
				}
			}
		}
		if d.Unit, err = a.Store.UnitForUtilityName(d.Name); err != nil {
			a.serverError(w, err)
			return
		}
		if d.Consumption, err = a.Store.MonthlyConsumptionByAddress(d.Name, from); err != nil {
			a.serverError(w, err)
			return
		}
		for _, c := range d.Consumption {
			if c.Consumed > d.MaxUsed {
				d.MaxUsed = c.Consumed
			}
		}
	}

	a.render(w, "stats_meters.html", "Статистика · Комуналка", "stats_meters", d)
}
