package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The pre-worlds URLs are in bookmarks, in the family chat, and in whatever
// pages were open when the move shipped. They must land on the new address,
// not on a 404.
func TestOldLessonPathsRedirect(t *testing.T) {
	router := smokeRouter(t)
	for _, tc := range []struct {
		method, from, want string
		code               int
	}{
		{http.MethodGet, "/visits", "/lessons/visits", http.StatusMovedPermanently},
		{http.MethodGet, "/visits/new", "/lessons/visits/new", http.StatusMovedPermanently},
		{http.MethodGet, "/visits/7/edit", "/lessons/visits/7/edit", http.StatusMovedPermanently},
		{http.MethodGet, "/payments", "/lessons/payments", http.StatusMovedPermanently},
		{http.MethodGet, "/payments/new", "/lessons/payments/new", http.StatusMovedPermanently},
		{http.MethodGet, "/enrollments", "/lessons/enrollments", http.StatusMovedPermanently},
		{http.MethodGet, "/enrollments/3/edit", "/lessons/enrollments/3/edit", http.StatusMovedPermanently},
		{http.MethodGet, "/enrollments/3/audit", "/lessons/enrollments/3/audit", http.StatusMovedPermanently},
		{http.MethodGet, "/trainers", "/lessons/trainers", http.StatusMovedPermanently},
		// Nested writes keep their method and body: a 301 would let the
		// browser retry them as GET and quietly drop the write.
		{http.MethodPost, "/visits/7/delete", "/lessons/visits/7/delete", http.StatusPermanentRedirect},
		{http.MethodPost, "/enrollments/3/slots", "/lessons/enrollments/3/slots", http.StatusPermanentRedirect},
		{http.MethodPost, "/enrollments/3/audit/send", "/lessons/enrollments/3/audit/send", http.StatusPermanentRedirect},
		{http.MethodPost, "/trainers/2/absences/5/delete", "/lessons/trainers/2/absences/5/delete", http.StatusPermanentRedirect},
	} {
		t.Run(tc.method+" "+tc.from, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.from, nil)
			// Own-page POSTs only; the CSRF guard sits in front of the mux.
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			router.ServeHTTP(rec, req)
			if rec.Code != tc.code {
				t.Fatalf("%s %s = %d, want %d", tc.method, tc.from, rec.Code, tc.code)
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Fatalf("%s %s → %q, want %q", tc.method, tc.from, got, tc.want)
			}
		})
	}
}

// A redirect that drops the query turns "the month I was looking at" into
// "the default range", which is the kind of loss nobody reports as a bug.
func TestARedirectKeepsTheQueryString(t *testing.T) {
	rec := httptest.NewRecorder()
	smokeRouter(t).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/enrollments/3/audit?range=month", nil))
	if got, want := rec.Header().Get("Location"), "/lessons/enrollments/3/audit?range=month"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// The redirects are a courtesy to old links, not a place for the app to send
// its own users. A page still pointing at a pre-worlds path costs every
// visitor an extra round trip and hides the real address.
func TestNoRenderedPageLinksToAPreWorldsPath(t *testing.T) {
	router := smokeRouter(t)
	for _, path := range []string{
		"/", "/lessons", "/lessons/visits", "/lessons/visits/new",
		"/lessons/payments", "/lessons/payments/new",
		"/lessons/enrollments", "/lessons/enrollments/new",
		"/lessons/enrollments/1/edit", "/lessons/enrollments/1/audit",
		"/lessons/trainers", "/stats", "/stats/lessons",
		"/appointments", "/reminders",
	} {
		body := getBody(t, router, path)
		for _, stale := range []string{
			`"/visits`, `"/payments`, `"/enrollments`, `"/trainers`,
		} {
			if strings.Contains(body, stale) {
				t.Errorf("%s still links to %s…", path, stale)
			}
		}
	}
}

// Both statistics pages exist and are distinct: the world's landing page is
// the totals, the lessons page is the breakdown.
func TestTheStatisticsWorldHasBothPages(t *testing.T) {
	router := smokeRouter(t)
	overview := getBody(t, router, "/stats")
	if !strings.Contains(overview, "Разом") {
		t.Fatalf("/stats is not the overview:\n%s", overview)
	}
	if strings.Contains(overview, "По людях") {
		t.Fatal("/stats carries the lessons breakdown instead of the totals")
	}
	if breakdown := getBody(t, router, "/stats/lessons"); !strings.Contains(breakdown, "По людях") {
		t.Fatalf("/stats/lessons is not the breakdown:\n%s", breakdown)
	}
}
