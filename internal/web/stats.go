package web

import "net/http"

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := a.Store.Stats()
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "stats.html", "Статистика · Заняття", "stats_lessons", stats)
}

// handleStatsOverview is the statistics world's landing page: the totals of
// each world side by side. It reads the same Stats the lessons breakdown does
// — the utilities figures join it when they exist, and until then this page
// carries one row rather than an empty promise of two.
func (a *App) handleStatsOverview(w http.ResponseWriter, r *http.Request) {
	stats, err := a.Store.Stats()
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "stats_overview.html", "Разом", "stats", stats)
}
