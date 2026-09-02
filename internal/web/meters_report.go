package web

import (
	"fmt"
	"html"
	"net/http"
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

	address, err := a.Store.AddressByID(addressID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rows, err := a.Store.CurrentMonthStatus(period, addressID)
	if err != nil {
		a.serverError(w, err)
		return
	}
	if err := a.Notifier.NotifyHTML(meterReportText(address.Name, address.Currency, period, rows)); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, metersMonthURL(addressID, period)+"&sent=1", http.StatusSeeOther)
}

// meterReportText is the month as the family group reads it. It reports the
// month honestly rather than assuming it is complete: the button can be
// pressed on any month, including one that is half entered, and a total that
// silently left out the unread services would be worse than saying which they
// are.
func meterReportText(addressName, currency, period string, rows []store.UtilityMonthlyStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🏠 <b>%s</b> · %s\n\n", html.EscapeString(addressName), periodLabelOf(period))

	var total, unpaid float64
	var missing []string
	for _, row := range rows {
		if !row.HasReading {
			missing = append(missing, row.UtilityName)
			continue
		}
		total += row.Amount
		mark := ""
		if !row.Paid {
			unpaid += row.Amount
			mark = " · <i>не сплачено</i>"
		}
		fmt.Fprintf(&b, "%s %s — %s%s\n",
			row.UtilityIcon, html.EscapeString(row.UtilityName),
			amountIn(row.Amount, row.Currency), mark)
	}

	fmt.Fprintf(&b, "\n<b>Разом: %s</b>", amountIn(total, currency))
	if unpaid > 0 {
		fmt.Fprintf(&b, "\nНе сплачено: <b>%s</b>", amountIn(unpaid, currency))
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "\nЩе не внесено: %s", html.EscapeString(strings.Join(missing, ", ")))
	}
	return b.String()
}
