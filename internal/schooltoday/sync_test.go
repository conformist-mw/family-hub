package schooltoday

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/store"
)

const validEmail, validPassword = "parent@example.com", "s3cret"

// fakePortal stands in for school-today.com: the antiforgery-gated login and
// the timetable endpoint, serving whatever dataset the test currently sets.
type fakePortal struct {
	dataset string
}

func (f *fakePortal) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/Account/Login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: ".AspNetCore.Antiforgery.t", Value: "af", Path: "/"})
			io.WriteString(w, `<input name="__RequestVerificationToken" type="hidden" value="TOK123" />`)
			return
		}
		r.ParseForm()
		// Step=Login is the field the real server branches on, and the creds
		// must match — otherwise no Identity cookie, which is how a failure
		// reads to the client.
		if r.PostForm.Get("Step") != "Login" ||
			r.PostForm.Get("Email") != validEmail || r.PostForm.Get("Password") != validPassword {
			io.WriteString(w, "login page again")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: identityCookie, Value: "session", Path: "/"})
	})
	mux.HandleFunc("/api/TimetableApi/GetTimetableByPupil", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, f.dataset)
	})
	return mux
}

func migratedStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(database)
}

// fixedNow pins the clock to Tuesday 2026-09-01 10:00; startOfWeek is then
// Monday 2026-08-31, so events dated 2026-09-01 fall inside the replaced window.
func newSyncService(t *testing.T, st *store.Store, baseURL, password string) *Service {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC) }
	return NewService(st, NewClient(baseURL), Config{
		Email:      validEmail,
		Password:   password,
		PupilID:    79311,
		WeeksAhead: 1,
	}, time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil)), now)
}

const weekOneDataset = `{"events":[
	{"eventID":11714596,"subject":"Алгебра [9]","start":"2026-09-01T09:00:00","end":"2026-09-01T09:40:00","topic":null,"themeColor":"#1E983B","hasMarks":false,"isFullDay":false,"isDeleted":false,"isCanceled":false},
	{"eventID":11714608,"subject":"Обід [Food Hub]","start":"2026-09-01T13:40:00","end":"2026-09-01T14:00:00","hasMarks":false,"isFullDay":false},
	{"eventID":999,"subject":"Скасований урок [9]","start":"2026-09-01T10:00:00","end":"2026-09-01T10:40:00","isCanceled":true},
	{"eventID":998,"subject":"День знань","start":"2026-09-01T00:00:00","end":"2026-09-02T00:00:00","isFullDay":true}
]}`

func TestSyncMirrorsTimetableAndSkipsNonLessons(t *testing.T) {
	portal := &fakePortal{dataset: weekOneDataset}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	svc := newSyncService(t, st, srv.URL, validPassword)
	if err := svc.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	got, err := st.SchoolLessons("2026-08-01T00:00", "2026-10-01T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Two survive: the cancelled and the all-day event are dropped.
	if len(got) != 2 {
		t.Fatalf("want 2 stored lessons, got %d: %+v", len(got), got)
	}
	algebra := got[0]
	if algebra.Subject != "Алгебра [9]" || algebra.StartsAt != "2026-09-01T09:00" || algebra.EndsAt != "2026-09-01T09:40" {
		t.Errorf("unexpected first lesson: %+v", algebra)
	}
	if algebra.ThemeColor != "#1E983B" {
		t.Errorf("theme colour not mirrored: %q", algebra.ThemeColor)
	}
}

// A re-sync replaces the window: a lesson gone from the portal is gone here.
func TestSyncReplacesWindow(t *testing.T) {
	portal := &fakePortal{dataset: weekOneDataset}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	svc := newSyncService(t, st, srv.URL, validPassword)
	if err := svc.Sync(context.Background()); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	portal.dataset = `{"events":[
		{"eventID":777,"subject":"Географія [9]","start":"2026-09-02T11:30:00","end":"2026-09-02T12:10:00"}
	]}`
	if err := svc.Sync(context.Background()); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	got, err := st.SchoolLessons("2026-08-01T00:00", "2026-10-01T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].EventID != 777 {
		t.Fatalf("window not replaced, got %+v", got)
	}
}

// A login failure aborts before the store is touched: the last good cache must
// survive a wrong password or a portal that stopped accepting the session.
func TestSyncLoginFailureLeavesCacheIntact(t *testing.T) {
	portal := &fakePortal{dataset: weekOneDataset}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	if err := newSyncService(t, st, srv.URL, validPassword).Sync(context.Background()); err != nil {
		t.Fatalf("seed sync: %v", err)
	}

	bad := newSyncService(t, st, srv.URL, "wrong-password")
	if err := bad.Sync(context.Background()); err == nil {
		t.Fatal("sync with wrong password should error")
	}

	got, err := st.SchoolLessons("2026-08-01T00:00", "2026-10-01T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("cache should be untouched after a failed login, got %d rows", len(got))
	}
}

// The portal pads a filled-in topic with trailing whitespace, which every
// consumer would otherwise render as a gap before the closing tag.
func TestAPaddedTopicIsTrimmed(t *testing.T) {
	svc := &Service{cfg: Config{PupilID: 7}, loc: time.UTC}
	padded := "Вступ. Розвиток української мови. "
	got, ok := svc.toLesson(Event{
		EventID: 1, Subject: "Українська мова [9]",
		Start: "2026-09-01T09:50:00", End: "2026-09-01T10:30:00",
		Topic: &padded,
	})
	if !ok {
		t.Fatal("lesson dropped")
	}
	if got.Topic != "Вступ. Розвиток української мови." {
		t.Fatalf("topic = %q, want it trimmed", got.Topic)
	}
}
