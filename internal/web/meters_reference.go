package web

import "net/http"

// The utilities world's reference data: what you are billed for and at what
// price. Edited a couple of times a year, which is why it stays web-only and
// never reaches the Mini App.
//
// Empty for the same reason as the readings screen — see meters_readings.go.

func (a *App) handleMeterTariffs(w http.ResponseWriter, r *http.Request) {
	a.render(w, "meters_tariffs.html", "Тарифи", "tariffs", nil)
}

func (a *App) handleMeterUtilities(w http.ResponseWriter, r *http.Request) {
	a.render(w, "meters_utilities.html", "Сервіси", "utilities", nil)
}

func (a *App) handleMeterAddresses(w http.ResponseWriter, r *http.Request) {
	a.render(w, "meters_addresses.html", "Адреси", "addresses", nil)
}
