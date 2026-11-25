package moodle

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

var syncLoc = time.FixedZone("EET", 2*3600)

// physicsItems is course 202's gradebook: one dated-less graded activity, a
// category total, and an activity nobody has graded.
const physicsItems = `{"usergrades":[{"courseid":202,"userid":1001,"gradeitems":[
 {"id":4001,"itemname":"Оцінка вчителя 11","itemtype":"mod","itemmodule":"quiz","iteminstance":601,"graderaw":9,"gradedategraded":null,"gradeformatted":"9,0"},
 {"id":4002,"itemname":"Тема 1","itemtype":"category","itemmodule":null,"iteminstance":70,"graderaw":8,"gradedategraded":null,"gradeformatted":"8,0"},
 {"id":4003,"itemname":"Контрольна робота 1","itemtype":"mod","itemmodule":"quiz","iteminstance":602,"graderaw":null,"gradedategraded":null,"gradeformatted":"-"}
]}],"warnings":[]}`

func physicsItemsWith(raw string) string {
	return `{"usergrades":[{"courseid":202,"userid":1001,"gradeitems":[
 {"id":4001,"itemname":"Оцінка вчителя 11","itemtype":"mod","itemmodule":"quiz","iteminstance":601,"graderaw":` + raw + `,"gradedategraded":0,"gradeformatted":"-"}
]}],"warnings":[]}`
}

// syncSite is the fake Moodle from the client tests with a per-course
// gradebook: gradeItems and gradeStatus override the answer for one course id
// (course 201 falls through to the grade_items.json fixture).
type syncSite struct {
	*fakeMoodle
	mu          sync.Mutex
	gradeItems  map[string]string
	gradeStatus map[string]int
	onCall      func(fn string)
}

func (s *syncSite) set(course, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gradeItems[course] = body
}

func (s *syncSite) fail(course string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gradeStatus[course] = status
}

func (s *syncSite) heal(course string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.gradeStatus, course)
}

func (s *syncSite) callCount() int {
	s.fakeMoodle.mu.Lock()
	defer s.fakeMoodle.mu.Unlock()
	return len(s.calls)
}

func (s *syncSite) handler() http.Handler {
	inner := s.fakeMoodle.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			s.t.Errorf("parse form: %v", err)
		}
		fn := r.PostForm.Get("wsfunction")
		if fn == "" {
			fn = "login"
		}
		s.mu.Lock()
		onCall := s.onCall
		course := r.PostForm.Get("courseid")
		status, failed := s.gradeStatus[course]
		body, custom := s.gradeItems[course]
		s.mu.Unlock()
		if onCall != nil {
			onCall(fn)
		}
		if fn == "gradereport_user_get_grade_items" {
			if failed {
				w.WriteHeader(status)
				return
			}
			if custom {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
				return
			}
		}
		inner.ServeHTTP(w, r)
	})
}

type syncFixture struct {
	site  *syncSite
	svc   *Service
	store *store.Store
	now   time.Time
	sent  [][]model.SchoolGrade
}

func (f *syncFixture) clock() time.Time { return f.now }

func newSyncFixture(t *testing.T, now time.Time) *syncFixture {
	t.Helper()
	site := &syncSite{
		fakeMoodle:  &fakeMoodle{t: t, status: map[string]int{}, body: map[string]string{}},
		gradeItems:  map[string]string{"202": physicsItems},
		gradeStatus: map[string]int{},
	}
	srv := httptest.NewServer(site.handler())
	t.Cleanup(srv.Close)

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(database)

	f := &syncFixture{site: site, store: st, now: now}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.svc = NewService(st, NewClient(srv.URL), Config{Username: testUser, Password: testPassword},
		syncLoc, logger, f.clock)
	f.svc.SetNotify(func(gs []model.SchoolGrade) { f.sent = append(f.sent, gs) })
	return f
}

func (f *syncFixture) syncOK(t *testing.T) {
	t.Helper()
	if err := f.svc.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

func (f *syncFixture) summary(t *testing.T) map[int64]model.SchoolCourse {
	t.Helper()
	courses, err := f.store.SchoolSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	out := map[int64]model.SchoolCourse{}
	for _, c := range courses {
		out[c.ID] = c
	}
	return out
}

func gradeIDs(gs []model.SchoolGrade) []int64 {
	var out []int64
	for _, g := range gs {
		out = append(out, g.ItemID)
	}
	return out
}

var day = time.Date(2026, 11, 25, 10, 0, 0, 0, syncLoc)

func TestSyncStoresOnlyGradedActivities(t *testing.T) {
	f := newSyncFixture(t, day)
	f.syncOK(t)

	got := f.summary(t)
	if len(got) != 2 {
		t.Fatalf("courses = %d, want 2", len(got))
	}
	alg, phys := got[201], got[202]
	if alg.Name != "Алгебра" || alg.Position != 0 || phys.Name != "Фізика" || phys.Position != 1 {
		t.Errorf("courses = %+v / %+v, want Алгебра@0, Фізика@1", alg, phys)
	}
	if alg.Average == nil || *alg.Average != 5.5 || alg.AverageText != "5,5" {
		t.Errorf("algebra average = %v %q, want 5.5 \"5,5\"", alg.Average, alg.AverageText)
	}
	if phys.Average != nil || phys.AverageText != "" {
		t.Errorf("physics average = %v %q, want none", phys.Average, phys.AverageText)
	}
	// 3002 and 4003 are ungraded, 3003 is the course total, 4002 a category.
	if ids := gradeIDs(alg.Grades); !slices.Equal(ids, []int64{3001}) {
		t.Errorf("algebra grades = %v, want [3001]", ids)
	}
	if ids := gradeIDs(phys.Grades); !slices.Equal(ids, []int64{4001}) {
		t.Errorf("physics grades = %v, want [4001]", ids)
	}

	g := alg.Grades[0]
	wantAt := time.Unix(1794643200, 0).In(syncLoc).Format(model.LocalDatetime)
	if g.Name != "Оцінка вчителя 10" || g.GradeRaw != 7.8 || g.GradeText != "7,8" ||
		g.ItemModule != "lesson" || g.ItemInstance != 501 || g.GradedAt != wantAt {
		t.Errorf("algebra grade = %+v, want ОВ 10 7.8 graded %s", g, wantAt)
	}
	if len(f.sent) != 0 {
		t.Errorf("first sync notified %v, want silence", f.sent)
	}
}

func TestSyncUndatedGradeKeepsFirstSeenDate(t *testing.T) {
	f := newSyncFixture(t, day)
	f.syncOK(t)
	first := day.Format(model.LocalDatetime)
	if at := f.summary(t)[202].Grades[0].GradedAt; at != first {
		t.Fatalf("undated grade at %s, want sync time %s", at, first)
	}

	f.now = day.Add(4 * time.Hour)
	f.site.set("202", physicsItemsWith("9"))
	f.syncOK(t)
	if at := f.summary(t)[202].Grades[0].GradedAt; at != first {
		t.Errorf("unchanged undated grade re-dated to %s, want %s", at, first)
	}
	if len(f.sent) != 0 {
		t.Errorf("unchanged grade notified: %v", f.sent)
	}

	f.now = day.Add(6 * time.Hour)
	f.site.set("202", physicsItemsWith("10"))
	f.syncOK(t)
	g := f.summary(t)[202].Grades[0]
	if g.GradedAt != f.now.Format(model.LocalDatetime) || g.GradeText != "10" {
		t.Errorf("changed grade = %+v, want dated %s with text 10", g, f.now.Format(model.LocalDatetime))
	}
	if len(f.sent) != 1 || !slices.Equal(gradeIDs(f.sent[0]), []int64{4001}) {
		t.Errorf("sent = %v, want one batch with 4001", f.sent)
	}
}

func TestSyncNotifiesNewGradesOfKnownCourses(t *testing.T) {
	f := newSyncFixture(t, day)
	f.syncOK(t)

	f.site.set("201", `{"usergrades":[{"courseid":201,"userid":1001,"gradeitems":[
 {"id":3001,"itemname":"Оцінка вчителя 10","itemtype":"mod","itemmodule":"lesson","iteminstance":501,"graderaw":7.8,"gradedategraded":1794643200,"gradeformatted":"7,8"},
 {"id":3002,"itemname":"Контрольна робота 2","itemtype":"mod","itemmodule":"quiz","iteminstance":502,"graderaw":10,"gradedategraded":1794729600,"gradeformatted":"10,0"}
]}],"warnings":[]}`)
	f.syncOK(t)

	if len(f.sent) != 1 {
		t.Fatalf("sent %d batches, want 1", len(f.sent))
	}
	batch := f.sent[0]
	if len(batch) != 1 {
		t.Fatalf("batch = %+v, want only 3002", batch)
	}
	g := batch[0]
	if g.ItemID != 3002 || g.CourseName != "Алгебра" || g.CourseAverage != "5,5" || g.GradeText != "10,0" {
		t.Errorf("fresh grade = %+v, want 3002 in Алгебра (5,5)", g)
	}

	// The same sync again is not news.
	f.syncOK(t)
	if len(f.sent) != 1 {
		t.Errorf("repeat sync notified again: %d batches", len(f.sent))
	}
}

func TestSyncWithoutNotifyStillStores(t *testing.T) {
	f := newSyncFixture(t, day)
	f.svc.SetNotify(nil)
	f.syncOK(t)
	f.site.set("202", physicsItemsWith("11"))
	f.syncOK(t)
	if g := f.summary(t)[202].Grades[0]; g.GradeRaw != 11 {
		t.Errorf("grade = %v, want 11 stored without a notifier", g.GradeRaw)
	}
}

func TestSyncCourseFailureAppliesNothing(t *testing.T) {
	f := newSyncFixture(t, day)

	// A first sync with a failing course seeds nothing at all.
	f.site.fail("202", http.StatusInternalServerError)
	if err := f.svc.SyncOnce(context.Background()); err == nil {
		t.Fatal("sync with a failing course succeeded")
	}
	if got := f.summary(t); len(got) != 0 {
		t.Fatalf("failed first sync stored %d courses", len(got))
	}

	f.site.heal("202")
	f.syncOK(t)

	// Physics fails while algebra gains a grade: neither the new grade nor the
	// physics deletion reaches the store, and nothing is announced.
	f.site.fail("202", http.StatusBadGateway)
	f.site.set("201", `{"usergrades":[{"courseid":201,"userid":1001,"gradeitems":[
 {"id":3005,"itemname":"Оцінка вчителя 11","itemtype":"mod","itemmodule":"lesson","iteminstance":505,"graderaw":9,"gradedategraded":1794729600,"gradeformatted":"9,0"}
]}],"warnings":[]}`)
	if err := f.svc.SyncOnce(context.Background()); err == nil {
		t.Fatal("sync with a failing course succeeded")
	}
	got := f.summary(t)
	if ids := gradeIDs(got[201].Grades); !slices.Equal(ids, []int64{3001}) {
		t.Errorf("algebra grades = %v, want the previous [3001]", ids)
	}
	if ids := gradeIDs(got[202].Grades); !slices.Equal(ids, []int64{4001}) {
		t.Errorf("physics grades = %v, want the previous [4001]", ids)
	}
	if len(f.sent) != 0 {
		t.Errorf("failed sync notified %v", f.sent)
	}
}

func TestSyncLoginFailure(t *testing.T) {
	f := newSyncFixture(t, day)
	f.svc.cfg.Password = "wrong"
	if err := f.svc.SyncOnce(context.Background()); err == nil {
		t.Fatal("sync with a wrong password succeeded")
	}
	if n := f.site.callCount(); n != 1 {
		t.Errorf("made %d requests after a failed login, want only the login", n)
	}
}

func TestQuietHours(t *testing.T) {
	cases := []struct {
		hour, min int
		sync      bool
	}{
		{21, 59, true},
		{22, 0, false},
		{23, 30, false},
		{0, 0, false},
		{6, 59, false},
		{7, 0, true},
		{12, 0, true},
	}
	for _, tc := range cases {
		f := newSyncFixture(t, time.Date(2026, 11, 25, tc.hour, tc.min, 0, 0, syncLoc))
		got := f.svc.syncUnlessQuiet(context.Background())
		calls := f.site.callCount()
		if got != tc.sync || (calls > 0) != tc.sync {
			t.Errorf("%02d:%02d: synced=%v with %d requests, want sync=%v", tc.hour, tc.min, got, calls, tc.sync)
		}
	}
}

func TestQuietHoursUseServiceLocation(t *testing.T) {
	// 21:00 UTC is 23:00 in the service's zone: quiet.
	f := newSyncFixture(t, time.Date(2026, 11, 25, 21, 0, 0, 0, time.UTC))
	if f.svc.syncUnlessQuiet(context.Background()) {
		t.Error("synced at 23:00 local")
	}
}

func TestRunSyncsAtOnceAndStops(t *testing.T) {
	f := newSyncFixture(t, day)
	logins := make(chan struct{}, 4)
	f.site.mu.Lock()
	f.site.onCall = func(fn string) {
		if fn == "login" {
			logins <- struct{}{}
		}
	}
	f.site.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.svc.Run(ctx)
		close(done)
	}()
	select {
	case <-logins:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not sync at start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func TestWeek(t *testing.T) {
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	week0, _ := time.Parse("2006-01-02", "2026-08-31")
	svc := NewService(nil, nil, Config{Week0: week0}, kyiv, nil, nil)
	if !svc.HasWeekAnchor() {
		t.Fatal("anchor set but HasWeekAnchor is false")
	}

	at := func(m time.Month, d, h, min int) time.Time { return time.Date(2026, m, d, h, min, 0, 0, kyiv) }
	date := func(m time.Month, d int) time.Time { return at(m, d, 0, 0) }
	cases := []struct {
		name       string
		t          time.Time
		n          int
		start, end time.Time
	}{
		{"anchor Monday start", date(time.August, 31), 0, date(time.August, 31), date(time.September, 7)},
		{"week 0 last minute", at(time.September, 6, 23, 59), 0, date(time.August, 31), date(time.September, 7)},
		{"week 1 first minute", date(time.September, 7), 1, date(time.September, 7), date(time.September, 14)},
		{"Sunday DST ends, late", at(time.October, 25, 23, 30), 7, date(time.October, 19), date(time.October, 26)},
		{"Monday after DST", date(time.October, 26), 8, date(time.October, 26), date(time.November, 2)},
		{"mid-term Wednesday", at(time.November, 25, 12, 0), 12, date(time.November, 23), date(time.November, 30)},
		{"day before anchor", at(time.August, 30, 23, 59), -1, date(time.August, 24), date(time.August, 31)},
		{"Monday before anchor", date(time.August, 24), -1, date(time.August, 24), date(time.August, 31)},
		{"two weeks before", at(time.August, 23, 12, 0), -2, date(time.August, 17), date(time.August, 24)},
	}
	for _, tc := range cases {
		w := svc.Week(tc.t)
		if w.N != tc.n || !w.Start.Equal(tc.start) || !w.End.Equal(tc.end) {
			t.Errorf("%s: Week(%s) = %d [%s, %s), want %d [%s, %s)", tc.name, tc.t,
				w.N, w.Start, w.End, tc.n, tc.start, tc.end)
		}
	}

	// An instant in another zone is placed by its date in the service's zone:
	// Sunday 23:30 UTC on 6 Sep is already Monday in Kyiv.
	if w := svc.Week(time.Date(2026, 9, 6, 23, 30, 0, 0, time.UTC)); w.N != 1 {
		t.Errorf("UTC instant in Kyiv Monday = week %d, want 1", w.N)
	}
}

func TestWeekAnchorNotMondayOrWestOfUTC(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	// A Wednesday anchor parsed as UTC midnight: still week 0 from Monday 31
	// Aug, even though that instant is Tuesday evening in New York.
	week0, _ := time.Parse("2006-01-02", "2026-09-02")
	svc := NewService(nil, nil, Config{Week0: week0}, ny, nil, nil)
	w := svc.Week(time.Date(2026, 8, 31, 0, 0, 0, 0, ny))
	if w.N != 0 || !w.Start.Equal(time.Date(2026, 8, 31, 0, 0, 0, 0, ny)) {
		t.Errorf("Week = %d from %s, want 0 from Monday 31 Aug", w.N, w.Start)
	}
}

func TestNoWeekAnchor(t *testing.T) {
	if NewService(nil, nil, Config{}, syncLoc, nil, nil).HasWeekAnchor() {
		t.Error("HasWeekAnchor true without Week0")
	}
}
