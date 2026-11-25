package moodle

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"
)

// physicsWeeks is course 202's page: week 12 holds a lesson, a label, a book,
// the quiz behind physics' only grade (instance 601) and an ungraded quiz;
// weeks 1 and 13 hold quizzes that must not leak into week 12.
const physicsWeeks = `[
 {"id":920,"name":"Загальне","section":0,"modules":[]},
 {"id":921,"name":"Тиждень 1. Вступ","section":1,"modules":[
  {"id":810,"name":"Оцінка вчителя 1","instance":620,"modname":"quiz"}
 ]},
 {"id":932,"name":"Тиждень 12","section":12,"modules":[
  {"id":801,"name":"Урок 12","instance":611,"modname":"lesson"},
  {"id":805,"name":"Оцінка вчителя 11","instance":601,"modname":"quiz"},
  {"id":802,"name":"Контрольна робота 3","instance":612,"modname":"quiz"},
  {"id":803,"name":"Пояснення","instance":613,"modname":"label"},
  {"id":804,"name":"Підручник","instance":614,"modname":"book"}
 ]},
 {"id":933,"name":"Тиждень 13","section":13,"modules":[
  {"id":811,"name":"Оцінка вчителя 12","instance":615,"modname":"quiz"}
 ]}
]`

const fnContents = "core_course_get_contents"

// newProgressFixture is a synced fixture with the week anchored at Monday
// 31 Aug 2026, so `day` (Wed 25 Nov) is in week 12. After the sync, algebra's
// only grade is on lesson 501, dated Sat 14 Nov (week 10), and physics'
// undated grade on quiz 601 is dated by the sync, in week 12.
func newProgressFixture(t *testing.T) *syncFixture {
	t.Helper()
	f := newSyncFixture(t, day)
	f.svc.cfg.Week0 = time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	f.site.courseBody[courseKey(fnContents, "202")] = physicsWeeks
	f.syncOK(t)
	return f
}

func (f *syncFixture) progress(t *testing.T, at time.Time) map[int64]CourseProgress {
	t.Helper()
	w := f.svc.Week(at)
	got, err := f.svc.WeekProgress(context.Background(), w)
	if err != nil {
		t.Fatalf("week progress: %v", err)
	}
	out := map[int64]CourseProgress{}
	for _, p := range got {
		out[p.CourseID] = p
	}
	return out
}

func TestWeekProgressCountsGradedQuizzes(t *testing.T) {
	f := newProgressFixture(t)
	if w := f.svc.Week(day); w.N != 12 {
		t.Fatalf("fixture day is week %d, want 12", w.N)
	}
	got, err := f.svc.WeekProgress(context.Background(), f.svc.Week(day))
	if err != nil {
		t.Fatalf("week progress: %v", err)
	}
	if len(got) != 2 || got[0].CourseID != 201 || got[1].CourseID != 202 {
		t.Fatalf("courses = %+v, want 201 then 202", got)
	}

	// Algebra (fixture): the ungraded quiz counts, the graded lesson and the
	// label do not.
	alg := got[0]
	if alg.QuizzesGraded != 0 || alg.QuizzesTotal != 1 || alg.NoSection || alg.Unavailable {
		t.Errorf("algebra = %d/%d noSection=%v unavailable=%v, want 0/1",
			alg.QuizzesGraded, alg.QuizzesTotal, alg.NoSection, alg.Unavailable)
	}
	if alg.Name != "Алгебра" || alg.Average == nil || *alg.Average != 5.5 || alg.AverageText != "5,5" {
		t.Errorf("algebra = %q avg %v %q, want Алгебра 5,5", alg.Name, alg.Average, alg.AverageText)
	}

	// Physics: the graded quiz and the ungraded one count; the lesson, label
	// and book do not, and the quizzes of weeks 1 and 13 stay out.
	phys := got[1]
	if phys.QuizzesGraded != 1 || phys.QuizzesTotal != 2 || phys.NoSection || phys.Unavailable {
		t.Errorf("physics = %d/%d noSection=%v unavailable=%v, want 1/2",
			phys.QuizzesGraded, phys.QuizzesTotal, phys.NoSection, phys.Unavailable)
	}
	if phys.Average != nil || phys.AverageText != "" {
		t.Errorf("physics average = %v %q, want none", phys.Average, phys.AverageText)
	}

	// Week 12 grades: only physics' grade dated by this week's sync.
	if len(alg.Grades) != 0 {
		t.Errorf("algebra week grades = %v, want none (graded in week 10)", gradeIDs(alg.Grades))
	}
	if ids := gradeIDs(phys.Grades); !slices.Equal(ids, []int64{4001}) {
		t.Errorf("physics week grades = %v, want [4001]", ids)
	}
	if g := phys.Grades[0]; g.CourseName != "Фізика" || g.GradeText != "9,0" {
		t.Errorf("physics grade = %+v, want Фізика 9,0", g)
	}
}

func TestWeekProgressGradesOfThatWeek(t *testing.T) {
	f := newProgressFixture(t)
	// Week 10 (9–15 Nov): algebra's grade from Sat 14 Nov; neither course
	// has a "Тиждень 10" section in the fixtures.
	got := f.progress(t, time.Date(2026, 11, 11, 12, 0, 0, 0, syncLoc))
	if ids := gradeIDs(got[201].Grades); !slices.Equal(ids, []int64{3001}) {
		t.Errorf("algebra week 10 grades = %v, want [3001]", ids)
	}
	if len(got[202].Grades) != 0 {
		t.Errorf("physics week 10 grades = %v, want none", gradeIDs(got[202].Grades))
	}
	for id, p := range got {
		if !p.NoSection || p.QuizzesGraded != 0 || p.QuizzesTotal != 0 {
			t.Errorf("course %d week 10 = %d/%d noSection=%v, want no section",
				id, p.QuizzesGraded, p.QuizzesTotal, p.NoSection)
		}
	}
}

// A quiz of week 10 graded only in week 12 still counts as graded in week 10,
// though its grade is listed under week 12. A lesson grade does not mark a
// quiz that happens to share its instance id: instance ids are per module.
func TestWeekProgressQuizGradedLater(t *testing.T) {
	f := newProgressFixture(t)
	f.site.courseBody[courseKey(fnContents, "202")] = `[
 {"id":930,"name":"Тиждень 10","section":10,"modules":[
  {"id":805,"name":"Оцінка вчителя 11","instance":601,"modname":"quiz"}
 ]}
]`
	f.site.courseBody[courseKey(fnContents, "201")] = `[
 {"id":910,"name":"Тиждень 10","section":10,"modules":[
  {"id":701,"name":"Урок 1","instance":501,"modname":"lesson"},
  {"id":706,"name":"Оцінка вчителя 10","instance":501,"modname":"quiz"}
 ]}
]`
	got := f.progress(t, time.Date(2026, 11, 11, 12, 0, 0, 0, syncLoc))

	phys := got[202]
	if phys.QuizzesGraded != 1 || phys.QuizzesTotal != 1 || phys.NoSection {
		t.Errorf("physics week 10 = %d/%d noSection=%v, want 1/1",
			phys.QuizzesGraded, phys.QuizzesTotal, phys.NoSection)
	}
	if len(phys.Grades) != 0 {
		t.Errorf("physics week 10 grades = %v, want none (graded in week 12)", gradeIDs(phys.Grades))
	}

	alg := got[201]
	if alg.QuizzesGraded != 0 || alg.QuizzesTotal != 1 {
		t.Errorf("algebra week 10 = %d/%d, want 0/1 (only the lesson is graded)",
			alg.QuizzesGraded, alg.QuizzesTotal)
	}
	if ids := gradeIDs(alg.Grades); !slices.Equal(ids, []int64{3001}) {
		t.Errorf("algebra week 10 grades = %v, want [3001]", ids)
	}
}

func TestWeekProgressNoSection(t *testing.T) {
	f := newProgressFixture(t)
	f.site.courseBody[courseKey(fnContents, "202")] = `[
 {"id":931,"name":"Тиждень 11","section":11,"modules":[{"id":805,"name":"Тест","instance":601,"modname":"quiz"}]},
 {"id":940,"name":"Тиждень 120","section":40,"modules":[{"id":811,"name":"Тест","instance":615,"modname":"quiz"}]}
]`
	got := f.progress(t, day)
	phys := got[202]
	if !phys.NoSection || phys.QuizzesGraded != 0 || phys.QuizzesTotal != 0 || phys.Unavailable {
		t.Errorf("physics = %+v, want NoSection with 0/0", phys)
	}
	if ids := gradeIDs(phys.Grades); !slices.Equal(ids, []int64{4001}) {
		t.Errorf("physics grades = %v, want [4001] even without a section", ids)
	}
	if got[201].NoSection {
		t.Error("algebra lost its section")
	}
}

func TestWeekProgressUnreadableCourse(t *testing.T) {
	f := newProgressFixture(t)
	f.site.courseStatus[courseKey(fnContents, "202")] = http.StatusServiceUnavailable
	got := f.progress(t, day)
	phys := got[202]
	if !phys.Unavailable {
		t.Errorf("physics = %+v, want Unavailable", phys)
	}
	if ids := gradeIDs(phys.Grades); !slices.Equal(ids, []int64{4001}) {
		t.Errorf("physics grades = %v, want [4001] from the store", ids)
	}
	if alg := got[201]; alg.Unavailable || alg.QuizzesGraded != 0 || alg.QuizzesTotal != 1 {
		t.Errorf("algebra = %+v, want 0/1 despite physics failing", alg)
	}
}

func TestWeekProgressLoginFailure(t *testing.T) {
	f := newProgressFixture(t)
	f.svc.cfg.Password = "wrong-password"
	if _, err := f.svc.WeekProgress(context.Background(), f.svc.Week(day)); err == nil {
		t.Fatal("WeekProgress with a failed login returned no error")
	}
}

func TestWeekProgressBeforeFirstSync(t *testing.T) {
	f := newSyncFixture(t, day)
	f.svc.cfg.Week0 = time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	got, err := f.svc.WeekProgress(context.Background(), f.svc.Week(day))
	if err != nil || len(got) != 0 {
		t.Errorf("WeekProgress before any sync = %+v, %v; want empty, nil", got, err)
	}
}

func TestSectionWeek(t *testing.T) {
	cases := []struct {
		name string
		n    int
		ok   bool
	}{
		{"Тиждень 12", 12, true},
		{"Тиждень 12. Квадратні рівняння", 12, true},
		{"Тиждень 3 (повторення)", 3, true},
		{"  тиждень  7", 7, true},
		{"Тиждень 120", 120, true},
		{"Тема 12", 0, false},
		{"Загальне", 0, false},
		{"Повторення. Тиждень 12", 0, false},
		{"Тиждень", 0, false},
	}
	for _, tc := range cases {
		n, ok := sectionWeek(tc.name)
		if n != tc.n || ok != tc.ok {
			t.Errorf("sectionWeek(%q) = %d, %v; want %d, %v", tc.name, n, ok, tc.n, tc.ok)
		}
	}
}
