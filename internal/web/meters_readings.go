package web

import "net/http"

// The utilities world — household bills, moving in from home-meters.
//
// These screens are deliberately empty rather than absent. The tables exist
// and the data copy is a scheduled event, not a code change, so the shape of
// the app is settled now and only its contents are pending. A world you can
// walk through and find empty says that; a world missing from the menu until
// some later PR says nothing, and makes the navigation change twice.
//
// Reads land in task 5 (the store) and task 6 (real lists); writes in tasks
// 7-8. Until then every handler renders its own empty state, which is also
// what these pages will show for a month nobody has entered yet — so the
// state is not throwaway scaffolding.

func (a *App) handleMeterReadings(w http.ResponseWriter, r *http.Request) {
	a.render(w, "meters_readings.html", "Показання", "readings", nil)
}
