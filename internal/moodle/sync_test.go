package moodle

import (
	"context"
	"errors"
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
// (course 201 falls through to the grade_items.json fixture). courseBody and
// courseStatus do the same for any other function, keyed by courseKey.
type syncSite struct {
	*fakeMoodle
	mu           sync.Mutex
	gradeItems   map[string]string
	gradeStatus  map[string]int
	courseBody   map[string]string
	courseStatus map[string]int
	onCall       func(fn string)
}

func courseKey(fn, course string) string { return fn + "/" + course }

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
		otherStatus, otherFailed := s.courseStatus[courseKey(fn, course)]
		otherBody, otherCustom := s.courseBody[courseKey(fn, course)]
		s.mu.Unlock()
		if onCall != nil {
			onCall(fn)
		}
		if otherFailed {
			w.WriteHeader(otherStatus)
			return
		}
		if otherCustom {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, otherBody)
			return
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
		fakeMoodle:   &fakeMoodle{t: t, status: map[string]int{}, body: map[string]string{}},
		gradeItems:   map[string]string{"202": physicsItems},
		gradeStatus:  map[string]int{},
		courseBody:   map[string]string{},
		courseStatus: map[string]int{},
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

// Whichever read fails, the store keeps the previous sync and nothing is
// announced.
func TestSyncReadFailureAppliesNothing(t *testing.T) {
	for _, fn := range []string{
		"core_webservice_get_site_info",
		"core_enrol_get_users_courses",
		"gradereport_overview_get_course_grades",
		"gradereport_user_get_grade_items",
	} {
		t.Run(fn, func(t *testing.T) {
			f := newSyncFixture(t, day)
			f.syncOK(t)
			before := f.summary(t)
			f.site.set("202", physicsItemsWith("12"))
			f.site.fakeMoodle.mu.Lock()
			f.site.fakeMoodle.status[fn] = http.StatusServiceUnavailable
			f.site.fakeMoodle.mu.Unlock()
			if err := f.svc.SyncOnce(context.Background()); err == nil {
				t.Fatal("sync with a failing read succeeded")
			}
			after := f.summary(t)
			if len(after) != len(before) || after[202].Grades[0].GradeRaw != 9 {
				t.Errorf("store changed: %+v", after)
			}
			if len(f.sent) != 0 {
				t.Errorf("failed sync notified %v", f.sent)
			}
		})
	}
}

// A store failure (here a course name the schema refuses) leaves the
// previous sync in place and announces nothing.
func TestSyncStoreFailureAppliesNothing(t *testing.T) {
	f := newSyncFixture(t, day)
	f.syncOK(t)
	f.site.set("202", physicsItemsWith("12"))
	f.site.fakeMoodle.mu.Lock()
	f.site.fakeMoodle.body["core_enrol_get_users_courses"] = `[{"id":201,"fullname":"Алгебра 9"},{"id":202,"fullname":"  "}]`
	f.site.fakeMoodle.mu.Unlock()
	if err := f.svc.SyncOnce(context.Background()); err == nil {
		t.Fatal("sync the store refused succeeded")
	}
	if g := f.summary(t)[202]; g.Name != "Фізика" || g.Grades[0].GradeRaw != 9 {
		t.Errorf("physics = %+v, want the previous sync", g)
	}
	if len(f.sent) != 0 {
		t.Errorf("failed sync notified %v", f.sent)
	}
}

// A course whose gradebook Moodle refuses (no permission) must not hold every
// other course hostage: the rest syncs, and the refused course keeps what the
// store has, so nothing in it is lost or re-announced.
func TestSyncRefusedCourseKeepsItsGrades(t *testing.T) {
	f := newSyncFixture(t, day)
	f.syncOK(t)

	refused := `{"exception":"moodle_exception","errorcode":"nopermissiontoviewgrades","message":"No permission"}`
	f.site.set("202", refused)
	f.site.set("201", `{"usergrades":[{"courseid":201,"userid":1001,"gradeitems":[
 {"id":3001,"itemname":"Оцінка вчителя 10","itemtype":"mod","itemmodule":"lesson","iteminstance":501,"graderaw":7.8,"gradedategraded":1794643200,"gradeformatted":"7,8"},
 {"id":3005,"itemname":"Оцінка вчителя 11","itemtype":"mod","itemmodule":"lesson","iteminstance":505,"graderaw":9,"gradedategraded":1794729600,"gradeformatted":"9,0"}
]}],"warnings":[]}`)
	f.syncOK(t)
	got := f.summary(t)
	if ids := gradeIDs(got[201].Grades); !slices.Equal(ids, []int64{3001, 3005}) {
		t.Errorf("algebra grades = %v, want the new 3005 too", ids)
	}
	if ids := gradeIDs(got[202].Grades); !slices.Equal(ids, []int64{4001}) {
		t.Errorf("physics grades = %v, want the stored [4001] kept", ids)
	}
	if len(f.sent) != 1 || !slices.Equal(gradeIDs(f.sent[0]), []int64{3005}) {
		t.Errorf("sent = %v, want only 3005", f.sent)
	}

	// Readable again: its grades are known, so only a real change is news.
	f.site.set("202", physicsItemsWith("9"))
	f.syncOK(t)
	if len(f.sent) != 1 {
		t.Errorf("physics coming back re-announced its grades: %v", f.sent)
	}
}

// A refused course the store does not know yet is left out, to be seeded
// silently once it can be read; announcing its whole history then would be
// noise.
func TestSyncRefusedNewCourseIsSeededLater(t *testing.T) {
	f := newSyncFixture(t, day)
	f.site.set("202", `{"exception":"moodle_exception","errorcode":"nopermissiontoviewgrades","message":"No"}`)
	f.syncOK(t)
	if _, ok := f.summary(t)[202]; ok {
		t.Fatal("a refused new course was stored")
	}
	f.site.set("202", physicsItems)
	f.syncOK(t)
	if ids := gradeIDs(f.summary(t)[202].Grades); !slices.Equal(ids, []int64{4001}) {
		t.Errorf("physics grades = %v, want [4001]", ids)
	}
	if len(f.sent) != 0 {
		t.Errorf("seeding a course notified %v", f.sent)
	}
}

// A course that hides its gradebook from pupils is not read or stored.
func TestSyncSkipsHiddenGradebook(t *testing.T) {
	f := newSyncFixture(t, day)
	f.site.fakeMoodle.body["core_enrol_get_users_courses"] = `[
 {"id":201,"fullname":"Алгебра 9","showgrades":true},
 {"id":202,"fullname":"Фізика 9","showgrades":false}
]`
	f.site.fail("202", http.StatusForbidden) // would abort the sync if read
	f.syncOK(t)
	got := f.summary(t)
	if _, ok := got[202]; ok || len(got) != 1 {
		t.Errorf("courses = %v, want only algebra", got)
	}
}

// An enrolment list that suddenly comes back empty is not taken as the child
// leaving every subject.
func TestSyncEmptyEnrolmentKeepsStore(t *testing.T) {
	f := newSyncFixture(t, day)
	f.syncOK(t)
	f.site.fakeMoodle.body["core_enrol_get_users_courses"] = `[]`
	err := f.svc.SyncOnce(context.Background())
	if !errors.Is(err, store.ErrEmptySchoolSync) {
		t.Fatalf("err = %v, want ErrEmptySchoolSync", err)
	}
	if got := f.summary(t); len(got) != 2 || len(got[202].Grades) != 1 {
		t.Errorf("store after an empty sync = %+v, want it untouched", got)
	}
}

// An unnamed grade item takes its module's name, or a generic one, instead of
// failing the sync on the store's non-empty name.
func TestSyncUnnamedGradeItems(t *testing.T) {
	f := newSyncFixture(t, day)
	f.site.set("202", `{"usergrades":[{"courseid":202,"userid":1001,"gradeitems":[
 {"id":4001,"itemname":null,"itemtype":"mod","itemmodule":"quiz","iteminstance":601,"graderaw":9,"gradedategraded":0,"gradeformatted":"9,0"},
 {"id":4005,"itemname":"  ","itemtype":"mod","itemmodule":null,"iteminstance":605,"graderaw":8,"gradedategraded":0,"gradeformatted":"8,0"}
]}],"warnings":[]}`)
	f.syncOK(t)
	var names []string
	for _, g := range f.summary(t)[202].Grades {
		names = append(names, g.Name)
	}
	if !slices.Equal(names, []string{"quiz", "Оцінка"}) {
		t.Errorf("names = %q, want [quiz Оцінка]", names)
	}
}

// The notify callback runs once the client is free: a slow Telegram send must
// not hold up the weekly review.
func TestSyncNotifiesAfterReleasingTheClient(t *testing.T) {
	f := newSyncFixture(t, day)
	f.syncOK(t)
	f.site.set("202", physicsItemsWith("12"))
	called := false
	f.svc.SetNotify(func([]model.SchoolGrade) {
		called = true
		if !f.svc.site.TryLock() {
			t.Error("notify called with the client still locked")
			return
		}
		f.svc.site.Unlock()
	})
	f.syncOK(t)
	if !called {
		t.Fatal("notify not called")
	}
}

// The sync and the review share one client; under -race this fails if they
// ever use it at the same time.
func TestSyncAndWeekProgressConcurrently(t *testing.T) {
	f := newProgressFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := f.svc.SyncOnce(ctx); err != nil {
				t.Errorf("sync: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := f.svc.WeekProgress(ctx, f.svc.Week(day)); err != nil {
				t.Errorf("week progress: %v", err)
			}
		}()
	}
	wg.Wait()
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
		quiet     bool
	}{
		{21, 59, false},
		{22, 0, true},
		{23, 30, true},
		{0, 0, true},
		{6, 59, true},
		{7, 0, false},
		{12, 0, false},
	}
	for _, tc := range cases {
		if got := quiet(time.Date(2026, 11, 25, tc.hour, tc.min, 0, 0, syncLoc)); got != tc.quiet {
			t.Errorf("%02d:%02d: quiet=%v, want %v", tc.hour, tc.min, got, tc.quiet)
		}
	}
}

func TestSyncUnlessQuiet(t *testing.T) {
	for _, tc := range []struct {
		name string
		now  time.Time
		sync bool
	}{
		{"daytime", day, true},
		{"night", time.Date(2026, 11, 25, 23, 0, 0, 0, syncLoc), false},
		// 21:00 UTC is 23:00 in the service's zone: quiet.
		{"night in the service's zone", time.Date(2026, 11, 25, 21, 0, 0, 0, time.UTC), false},
	} {
		f := newSyncFixture(t, tc.now)
		f.svc.syncUnlessQuiet(context.Background())
		if calls := f.site.callCount(); (calls > 0) != tc.sync {
			t.Errorf("%s: %d requests, want sync=%v", tc.name, calls, tc.sync)
		}
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
