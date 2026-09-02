package web

import (
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"

	"familyhub/internal/store"
)

// Sending a month to the family group.
//
// A button rather than an event. The old app fired two automatic messages —
// "everything is entered" and "everything is paid" — and both are gone.
//
// "Everything is entered" said what a closed chore already says: #5 «Записать
// данные счётчиков» has its own reminders, its evening nag and a record of
// whether it was actually done, which is the whole reason the scheduler did
// not move across with the data.
//
// "Everything is paid" had to be re-checked from two places — writing a
// reading and toggling paid — and forgetting the second meant it would never
// fire at all. A button has no such trap, and it sends any month rather than
// only the one that happened to complete itself while the app was watching.
//
// Nothing is recorded about what was sent. There is no delivery log because
// there is nothing to deduplicate: pressing the button is the decision, and
// pressing it twice is a decision too.

// reportData is one month at one property, ordered by what it cost.
type reportData struct {
	AddressName string
	Currency    string
	Period      string
	Rows        []store.UtilityMonthlyStatus
	Total       float64
	Unpaid      float64
	Missing     []string
}

// handleMeterReportPage is the screenshot. Rows run biggest first, because the
// question a bill summary is read for is what the money went on, and the
// answer is at the top rather than wherever the service happens to sort in the
// list.
func (a *App) handleMeterReportPage(w http.ResponseWriter, r *http.Request) {
	d, err := a.monthReport(formInt64(r.URL.Query().Get("address_id")), r.URL.Query().Get("period"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.renderBare(w, "meters_report.html", "Звіт", d)
}

// monthReport gathers one month, sorted and totalled. Shared by the page and
// the message so the two cannot disagree about a figure.
func (a *App) monthReport(addressID int64, period string) (reportData, error) {
	if !isPeriod(period) {
		period = currentPeriod()
	}
	address, err := a.Store.AddressByID(addressID)
	if err != nil {
		return reportData{}, err
	}
	rows, err := a.Store.CurrentMonthStatus(period, addressID)
	if err != nil {
		return reportData{}, err
	}

	d := reportData{AddressName: address.Name, Currency: address.Currency, Period: period}
	for _, row := range rows {
		if !row.HasReading {
			d.Missing = append(d.Missing, row.UtilityName)
			continue
		}
		d.Rows = append(d.Rows, row)
		d.Total += row.Amount
		if !row.Paid {
			d.Unpaid += row.Amount
		}
	}
	sort.SliceStable(d.Rows, func(i, j int) bool { return d.Rows[i].Amount > d.Rows[j].Amount })
	return d, nil
}

func (a *App) handleMeterReport(w http.ResponseWriter, r *http.Request) {
	if a.Notifier == nil {
		http.Error(w, "бот вимкнений", http.StatusServiceUnavailable)
		return
	}
	addressID := formInt64(r.FormValue("address_id"))
	period := r.FormValue("period")
	if !isPeriod(period) {
		period = currentPeriod()
	}

	d, err := a.monthReport(addressID, period)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := a.Notifier.NotifyHTML(meterReportText(d)); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, metersMonthURL(addressID, period)+"&sent=1", http.StatusSeeOther)
}

// meterReportText is the month as the family group reads it — the same figures
// the page shows, in the same order. It reports the month honestly rather than
// assuming it is complete: the button can be pressed on a half-entered month,
// and a total that silently left out the unread services would be worse than
// saying which they are.
func meterReportText(d reportData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🏠 <b>%s</b> · %s\n\n", html.EscapeString(d.AddressName), periodLabelOf(d.Period))

	for _, row := range d.Rows {
		mark := ""
		if !row.Paid {
			mark = " · <i>не сплачено</i>"
		}
		fmt.Fprintf(&b, "%s %s — %s%s\n",
			row.UtilityIcon, html.EscapeString(row.UtilityName),
			amountIn(row.Amount, row.Currency), mark)
	}

	fmt.Fprintf(&b, "\n<b>Разом: %s</b>", amountIn(d.Total, d.Currency))
	if d.Unpaid > 0 {
		fmt.Fprintf(&b, "\nНе сплачено: <b>%s</b>", amountIn(d.Unpaid, d.Currency))
	}
	if len(d.Missing) > 0 {
		fmt.Fprintf(&b, "\nЩе не внесено: %s", html.EscapeString(strings.Join(d.Missing, ", ")))
	}
	return b.String()
}
