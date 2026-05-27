package web

import "net/http"

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := a.Store.Stats()
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "stats.html", "Статистика", "stats", stats)
}
