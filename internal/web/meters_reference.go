package web

import (
	"net/http"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The utilities world's reference data: what you are billed for, at what
// price, and where. Changed a couple of times a year, which is why it stays
// web-only and never reaches the Mini App.
//
// Every list shows archived rows too, marked. Archiving hides a utility from
// the month view; a page whose whole job is to manage them must still show
// what there is to un-archive.

func (a *App) handleMeterTariffs(w http.ResponseWriter, r *http.Request) {
	tariffs, err := a.Store.TariffsAll()
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "meters_tariffs.html", "Тарифи", "tariffs", tariffs)
}

func (a *App) handleMeterUtilities(w http.ResponseWriter, r *http.Request) {
	utilities, err := a.Store.UtilitiesAll()
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "meters_utilities.html", "Сервіси", "utilities", utilities)
}

// addressRow is an address plus what archiving it would take with it.
type addressRow struct {
	model.Address
	Utilities int
}

func (a *App) handleMeterAddresses(w http.ResponseWriter, r *http.Request) {
	addresses, err := a.Store.AddressesAll()
	if err != nil {
		a.serverError(w, err)
		return
	}
	rows := make([]addressRow, 0, len(addresses))
	for _, ad := range addresses {
		n, err := a.Store.UtilitiesCountForAddress(ad.ID)
		if err != nil {
			a.serverError(w, err)
			return
		}
		rows = append(rows, addressRow{Address: ad, Utilities: n})
	}
	a.render(w, "meters_addresses.html", "Адреси", "addresses", rows)
}

// Compile-time reminder that the tariff list is the decorated type: the plain
// model.Tariff has no usage counters, and the template needs them.
var _ = []store.TariffWithUsage(nil)
