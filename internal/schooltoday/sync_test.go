package schooltoday

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

	// lessonBody is what /Timetable/LessonView answers with; empty means the
	// real fixture in testdata. lessonStatus overrides the HTTP status for one
	// event id, which is how a test stages a 404 (not a lesson) or a 500.
	lessonBody   []byte
	lessonStatus map[int64]int
	// lessonCalls records every (lessonID, lessonType) asked for, so a test can
	// assert the collector skipped what it should have skipped.
	lessonCalls []lessonCall
}

type lessonCall struct {
	id   int64
	kind int
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
	mux.HandleFunc("/Timetable/LessonView", func(w http.ResponseWriter, r *http.Request) {
		// The real portal refuses GET here with 405; pin that so a client that
		// switches verb is caught by the tests rather than in production.
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id, _ := strconv.ParseInt(r.URL.Query().Get("lessonID"), 10, 64)
		kind, _ := strconv.Atoi(r.URL.Query().Get("lessonType"))
		f.lessonCalls = append(f.lessonCalls, lessonCall{id: id, kind: kind})

		if status, ok := f.lessonStatus[id]; ok && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if f.lessonBody != nil {
			w.Write(f.lessonBody)
			return
		}
		w.Write(lessonFixture(nil))
	})
	return mux
}

// lessonFixture is the real LessonView response, with the child's and
// teacher's names replaced. Read from disk rather than inlined: the parser
// exists to survive this exact markup, and a hand-trimmed copy would drift
// from it silently.
func lessonFixture(t *testing.T) []byte {
	body, err := os.ReadFile(filepath.Join("testdata", "lessonview.html"))
	if err != nil {
		if t != nil {
			t.Fatalf("read fixture: %v", err)
		}
		panic(err)
	}
	return body
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

// A week as the portal actually serves one: academic lessons (type 1), meals
// and after-school care (types 0 and 3), a cancelled slot and an all-day
// banner. Only the three real lessons should be read in detail.
const collectWeekDataset = `{"events":[
	{"eventID":101,"subject":"Алгебра [9]","type":1,"start":"2026-09-01T09:00:00","end":"2026-09-01T09:40:00","hasMarks":false},
	{"eventID":102,"subject":"Українська мова [9]","type":1,"start":"2026-09-01T09:50:00","end":"2026-09-01T10:30:00","hasMarks":true},
	{"eventID":103,"subject":"Біологія [9]","type":1,"start":"2026-09-02T09:00:00","end":"2026-09-02T09:40:00"},
	{"eventID":201,"subject":"Обід [Food Hub]","type":0,"start":"2026-09-01T13:40:00","end":"2026-09-01T14:00:00"},
	{"eventID":202,"subject":"Група продовженого дня [9]","type":3,"start":"2026-09-01T14:10:00","end":"2026-09-01T15:40:00"},
	{"eventID":203,"subject":"Класна година [9]","type":1,"start":"2026-09-02T08:00:00","end":"2026-09-02T08:40:00"},
	{"eventID":301,"subject":"Скасований урок [9]","type":1,"start":"2026-09-02T11:00:00","end":"2026-09-02T11:40:00","isCanceled":true},
	{"eventID":302,"subject":"День знань","type":1,"start":"2026-09-01T00:00:00","end":"2026-09-02T00:00:00","isFullDay":true}
]}`

// Meals, after-school care, cancelled slots and all-day banners must not even
// be asked for: each would 404, and thirty phantom failures a week would make
// the "skipped" count useless as a signal.
func TestCollectWeekOnlyReadsAcademicLessons(t *testing.T) {
	portal := &fakePortal{dataset: collectWeekDataset}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	svc := newSyncService(t, st, srv.URL, validPassword)

	details, skipped, err := svc.CollectWeek(context.Background(),
		time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	// Four asks, not three: "Класна година" is type 1 and Classify calls
	// anything it does not recognise a lesson, so it is asked for and weeded
	// out by the portal's own 404 (see the next test). What must never be
	// asked for is the meal, the after-school block, the cancelled slot and
	// the all-day banner — those are the thirty-odd phantom failures a week
	// that would make the skipped count meaningless.
	asked := map[int64]bool{}
	for _, c := range portal.lessonCalls {
		asked[c.id] = true
		if c.kind != lessonEventType {
			t.Errorf("asked for lessonType %d on event %d", c.kind, c.id)
		}
	}
	for _, id := range []int64{201, 202, 301, 302} {
		if asked[id] {
			t.Errorf("event %d is not a lesson and should never have been asked for", id)
		}
	}
	for _, id := range []int64{101, 102, 103} {
		if !asked[id] {
			t.Errorf("lesson %d was not read", id)
		}
	}
	if len(details) != 4 {
		t.Fatalf("collected %d lessons, want 4 (three subjects + the homeroom slot)", len(details))
	}
}

// The detail belongs to the event that produced it: subject and start come
// from the timetable (group tag and all, the spelling the rest of the school
// code expects), the rest from the page.
func TestCollectWeekJoinsTheEventWithItsDetail(t *testing.T) {
	portal := &fakePortal{dataset: collectWeekDataset}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	svc := newSyncService(t, st, srv.URL, validPassword)

	details, _, err := svc.CollectWeek(context.Background(),
		time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	first := details[0]
	if first.Subject != "Алгебра [9]" {
		t.Errorf("subject = %q, want the timetable spelling", first.Subject)
	}
	if first.StartsAt != "2026-09-01T09:00" {
		t.Errorf("starts_at = %q, want model.LocalDatetime", first.StartsAt)
	}
	if first.PupilID != 79311 {
		t.Errorf("pupil = %d", first.PupilID)
	}
	// From the fixture the fake portal serves for every lesson.
	if first.Teacher != "Петренко Оксана" || first.Topic == "" || len(first.Marks) != 1 {
		t.Errorf("detail fields did not come through: %+v", first)
	}
}

// What was collected has to survive the sync that runs hours later — the whole
// reason these rows are not columns on school_lessons.
func TestCollectWeekPersistsWhatItRead(t *testing.T) {
	portal := &fakePortal{dataset: collectWeekDataset}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	svc := newSyncService(t, st, srv.URL, validPassword)

	if _, _, err := svc.CollectWeek(context.Background(),
		time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("collect: %v", err)
	}

	stored, err := st.LessonDetails("2026-08-31T00:00", "2026-09-07T00:00")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored) != 4 {
		t.Fatalf("stored %d lessons, want 4", len(stored))
	}
}

// A 404 means "not a lesson after all" — the homeroom slot Classify let
// through. It is skipped silently, because counting it would put a permanent
// "пропущено 1" on every otherwise perfect week, indistinguishable from a real
// portal failure.
func TestCollectWeekDoesNotCountA404AsMissed(t *testing.T) {
	portal := &fakePortal{
		dataset:      collectWeekDataset,
		lessonStatus: map[int64]int{203: http.StatusNotFound},
	}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	svc := newSyncService(t, st, srv.URL, validPassword)

	details, skipped, err := svc.CollectWeek(context.Background(),
		time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0 — a 404 is not a failure", skipped)
	}
	if len(details) != 3 {
		t.Errorf("collected %d, want the three real subjects", len(details))
	}
}

// A real failure is counted and the rest of the week still goes out: 28 out of
// 29 lessons is worth sending, and the count is what makes the gap visible.
func TestCollectWeekCountsAServerErrorAndKeepsGoing(t *testing.T) {
	portal := &fakePortal{
		dataset:      collectWeekDataset,
		lessonStatus: map[int64]int{102: http.StatusInternalServerError},
	}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	svc := newSyncService(t, st, srv.URL, validPassword)

	details, skipped, err := svc.CollectWeek(context.Background(),
		time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(details) != 3 {
		t.Errorf("collected %d, want the other 3", len(details))
	}
}

// An expired session fails every remaining lesson identically. Stopping at the
// first one keeps one dead cookie from being reported as a portal-wide outage,
// and surfaces the real cause.
func TestCollectWeekStopsOnAnExpiredSession(t *testing.T) {
	portal := &fakePortal{
		dataset: collectWeekDataset,
		lessonBody: []byte(
			`<form><input name="__RequestVerificationToken" type="hidden" value="TOK" /></form>`),
	}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	svc := newSyncService(t, st, srv.URL, validPassword)

	_, _, err := svc.CollectWeek(context.Background(),
		time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("err = %v, want ErrSessionExpired", err)
	}
	if len(portal.lessonCalls) != 1 {
		t.Errorf("kept going after the session died: %d calls", len(portal.lessonCalls))
	}
}

// Nothing was read, so nothing may be written: a failed login must not leave
// an empty week behind that /schoolweek would then report as "no lessons".
func TestCollectWeekWritesNothingWhenLoginFails(t *testing.T) {
	portal := &fakePortal{dataset: collectWeekDataset}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	svc := newSyncService(t, st, srv.URL, "wrong-password")

	if _, _, err := svc.CollectWeek(context.Background(),
		time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("a bad password came back as success")
	}
	if len(portal.lessonCalls) != 0 {
		t.Errorf("asked for lessons without a session: %d calls", len(portal.lessonCalls))
	}
}

// The Friday collect is ~29 sequential requests; shutdown must not wait for
// all of them.
func TestCollectWeekStopsOnACancelledContext(t *testing.T) {
	portal := &fakePortal{dataset: collectWeekDataset}
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	st := migratedStore(t)
	svc := newSyncService(t, st, srv.URL, validPassword)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := svc.CollectWeek(ctx,
		time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("a cancelled context came back as success")
	}
}
