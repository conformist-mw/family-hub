package bot

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// reviewPortal is a stand-in for school-today.com for the Friday review tests.
// schooltoday has a fake of its own, but it is unexported and this package
// needs a different shape anyway: a login that can be made to fail, a blocking
// lesson handler for the overlap test, and counters read from another
// goroutine — the collect runs off the caller's.
type reviewPortal struct {
	failLogin bool
	// block, when set, holds every lesson request until it is closed.
	block chan struct{}

	mu          sync.Mutex
	lessonCalls int
	loginCalls  int
}

func newReviewPortal() *reviewPortal { return &reviewPortal{} }

func (p *reviewPortal) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lessonCalls
}

func (p *reviewPortal) logins() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.loginCalls
}

func (p *reviewPortal) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/Account/Login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `<input name="__RequestVerificationToken" type="hidden" value="TOK" />`)
			return
		}
		p.mu.Lock()
		p.loginCalls++
		fail := p.failLogin
		p.mu.Unlock()
		if fail {
			io.WriteString(w, "login page again")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: ".AspNetCore.Identity.Application", Value: "session", Path: "/"})
	})
	mux.HandleFunc("/api/TimetableApi/GetTimetableByPupil", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reviewWeekDataset)
	})
	mux.HandleFunc("/Timetable/LessonView", func(w http.ResponseWriter, r *http.Request) {
		if p.block != nil {
			<-p.block
		}
		p.mu.Lock()
		p.lessonCalls++
		p.mu.Unlock()

		body, err := os.ReadFile(filepath.Join("..", "schooltoday", "testdata", "lessonview.html"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(body)
	})
	return mux
}

// Two lessons and a lunch break in the week of Monday 2026-08-31.
const reviewWeekDataset = `{"events":[
	{"eventID":101,"subject":"Алгебра [9]","type":1,"start":"2026-08-31T09:00:00","end":"2026-08-31T09:40:00"},
	{"eventID":102,"subject":"Українська мова [9]","type":1,"start":"2026-09-01T09:50:00","end":"2026-09-01T10:30:00"},
	{"eventID":201,"subject":"Обід [Food Hub]","type":0,"start":"2026-08-31T13:40:00","end":"2026-08-31T14:00:00"}
]}`
