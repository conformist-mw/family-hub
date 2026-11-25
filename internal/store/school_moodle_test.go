package store_test

import (
	"slices"
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

var syncNow = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

func schoolCourse(id int64, name string, pos int, avg float64) model.SchoolCourse {
	c := model.SchoolCourse{ID: id, Name: name, Position: pos}
	if avg > 0 {
		c.Average = &avg
		c.AverageText = model.FormatGrade(avg)
	}
	return c
}

func schoolGrade(item, course int64, name string, raw float64, at string) model.SchoolGrade {
	return model.SchoolGrade{
		ItemID: item, CourseID: course, ItemModule: "quiz", ItemInstance: item * 10,
		Name: name, GradeRaw: raw, GradeText: model.FormatGrade(raw), GradedAt: at,
	}
}

func applySchool(t *testing.T, st *store.Store, courses []model.SchoolCourse, grades []model.SchoolGrade) []model.SchoolGrade {
	t.Helper()
	fresh, err := st.ApplySchoolSync(courses, grades, syncNow)
	if err != nil {
		t.Fatalf("apply sync: %v", err)
	}
	return fresh
}

func itemIDs(gs []model.SchoolGrade) []int64 {
	var out []int64
	for _, g := range gs {
		out = append(out, g.ItemID)
	}
	return out
}

func TestFirstSchoolSyncIsSilent(t *testing.T) {
	st := testStore(t)
	courses := []model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 5.5), schoolCourse(2, "Фізика", 1, 0)}
	grades := []model.SchoolGrade{
		schoolGrade(101, 1, "Оцінка вчителя 1", 7, "2026-09-08T10:00"),
		schoolGrade(102, 1, "Оцінка вчителя 2", 4, "2026-09-15T10:00"),
	}

	if fresh := applySchool(t, st, courses, grades); len(fresh) != 0 {
		t.Fatalf("first sync reported %v as new", itemIDs(fresh))
	}

	sum, err := st.SchoolSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(sum) != 2 || sum[0].Name != "Алгебра" || sum[1].Name != "Фізика" {
		t.Fatalf("summary courses = %+v", sum)
	}
	if got := itemIDs(sum[0].Grades); !slices.Equal(got, []int64{101, 102}) {
		t.Fatalf("algebra grades = %v", got)
	}
	if sum[0].Average == nil || *sum[0].Average != 5.5 || sum[0].AverageText != "5,5" {
		t.Fatalf("algebra average = %v %q", sum[0].Average, sum[0].AverageText)
	}
	if sum[1].Average != nil || sum[1].AverageText != "" || len(sum[1].Grades) != 0 {
		t.Fatalf("a course without grades read back as %+v", sum[1])
	}
	if sum[0].SyncedAt != "2026-11-25T10:00" {
		t.Fatalf("synced_at = %q", sum[0].SyncedAt)
	}
	g := sum[0].Grades[0]
	if g.ItemModule != "quiz" || g.ItemInstance != 1010 || g.CourseName != "Алгебра" || g.CourseAverage != "5,5" {
		t.Fatalf("grade read back as %+v", g)
	}
}

// The first sync of a course that has no grades yet still seeds the course,
// so its first grade later on is news and not another silent seed.
func TestFirstGradeAfterAnEmptyFirstSyncIsFresh(t *testing.T) {
	st := testStore(t)
	courses := []model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 0)}
	applySchool(t, st, courses, nil)

	courses = []model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 2.2)}
	fresh := applySchool(t, st, courses, []model.SchoolGrade{
		schoolGrade(101, 1, "Оцінка вчителя 12", 2.2, "2026-11-24T09:00"),
	})
	if len(fresh) != 1 || fresh[0].ItemID != 101 {
		t.Fatalf("fresh = %v, want [101]", itemIDs(fresh))
	}
	if fresh[0].CourseName != "Алгебра" || fresh[0].CourseAverage != "2,2" {
		t.Fatalf("fresh grade carries %q / %q", fresh[0].CourseName, fresh[0].CourseAverage)
	}
}

func TestACourseAddedMidYearIsSeededSilently(t *testing.T) {
	st := testStore(t)
	applySchool(t, st, []model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 0)}, nil)

	fresh := applySchool(t, st,
		[]model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 0), schoolCourse(2, "Фізика", 1, 8)},
		[]model.SchoolGrade{
			schoolGrade(201, 2, "Оцінка вчителя 1", 8, "2026-09-10T10:00"),
			schoolGrade(101, 1, "Оцінка вчителя 1", 9, "2026-11-24T10:00"),
		})
	if !slices.Equal(itemIDs(fresh), []int64{101}) {
		t.Fatalf("fresh = %v, want only the known course's grade", itemIDs(fresh))
	}

	// The seeded course reports normally from the next sync on.
	fresh = applySchool(t, st,
		[]model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 9), schoolCourse(2, "Фізика", 1, 8)},
		[]model.SchoolGrade{
			schoolGrade(201, 2, "Оцінка вчителя 1", 8, "2026-09-10T10:00"),
			schoolGrade(202, 2, "Оцінка вчителя 2", 8, "2026-11-25T09:00"),
			schoolGrade(101, 1, "Оцінка вчителя 1", 9, "2026-11-24T10:00"),
		})
	if !slices.Equal(itemIDs(fresh), []int64{202}) {
		t.Fatalf("fresh = %v, want [202]", itemIDs(fresh))
	}
}

func TestAChangedGradeIsFreshAndAnUnchangedSyncIsNot(t *testing.T) {
	st := testStore(t)
	courses := []model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 6)}
	grades := []model.SchoolGrade{
		schoolGrade(101, 1, "Оцінка вчителя 1", 6, "2026-11-20T10:00"),
		schoolGrade(102, 1, "Контрольна робота 1", 6, "2026-11-21T10:00"),
	}
	applySchool(t, st, courses, grades)

	if fresh := applySchool(t, st, courses, grades); len(fresh) != 0 {
		t.Fatalf("a repeat sync reported %v", itemIDs(fresh))
	}

	// Only the grade counts: a reworded name or a new text is not news.
	grades[0].Name = "Оцінка вчителя 1 (усно)"
	grades[1].GradeRaw, grades[1].GradeText = 9, "9"
	fresh := applySchool(t, st, courses, grades)
	if !slices.Equal(itemIDs(fresh), []int64{102}) {
		t.Fatalf("fresh = %v, want [102]", itemIDs(fresh))
	}
	if fresh[0].GradeText != "9" {
		t.Fatalf("fresh grade = %+v", fresh[0])
	}

	sum, err := st.SchoolSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum[0].Grades[0].Name != "Оцінка вчителя 1 (усно)" || sum[0].Grades[1].GradeRaw != 9 {
		t.Fatalf("stored grades not updated: %+v", sum[0].Grades)
	}
}

func TestFreshGradesComeInCourseOrderThenByTime(t *testing.T) {
	st := testStore(t)
	courses := []model.SchoolCourse{schoolCourse(5, "Фізика", 0, 0), schoolCourse(3, "Алгебра", 1, 0)}
	applySchool(t, st, courses, nil)

	fresh := applySchool(t, st, courses, []model.SchoolGrade{
		schoolGrade(301, 3, "ОВ", 7, "2026-11-24T09:00"),
		schoolGrade(502, 5, "ОВ", 7, "2026-11-25T09:00"),
		schoolGrade(501, 5, "ОВ", 7, "2026-11-24T12:00"),
	})
	if !slices.Equal(itemIDs(fresh), []int64{501, 502, 301}) {
		t.Fatalf("fresh order = %v", itemIDs(fresh))
	}
}

// A new school year is a new enrolment list: last year's courses and their
// grades go rather than mixing into this year's view.
func TestACourseMissingFromTheSyncIsDeletedWithItsGrades(t *testing.T) {
	st := testStore(t)
	applySchool(t, st,
		[]model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 7), schoolCourse(2, "Фізика", 1, 8)},
		[]model.SchoolGrade{
			schoolGrade(101, 1, "ОВ 1", 7, "2026-11-20T10:00"),
			schoolGrade(201, 2, "ОВ 1", 8, "2026-11-20T10:00"),
		})

	applySchool(t, st,
		[]model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 7)},
		[]model.SchoolGrade{schoolGrade(101, 1, "ОВ 1", 7, "2026-11-20T10:00")})

	sum, err := st.SchoolSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(sum) != 1 || sum[0].ID != 1 {
		t.Fatalf("courses after the sync = %+v", sum)
	}
	all, err := st.SchoolGradesBetween("2000-01-01T00:00", "2100-01-01T00:00")
	if err != nil {
		t.Fatalf("grades: %v", err)
	}
	if !slices.Equal(itemIDs(all), []int64{101}) {
		t.Fatalf("grades left = %v, want the dropped course's gone", itemIDs(all))
	}

	// Should the course come back, it is a new course: seeded, not reported.
	fresh := applySchool(t, st,
		[]model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 7), schoolCourse(2, "Фізика", 1, 8)},
		[]model.SchoolGrade{
			schoolGrade(101, 1, "ОВ 1", 7, "2026-11-20T10:00"),
			schoolGrade(201, 2, "ОВ 1", 8, "2026-11-20T10:00"),
		})
	if len(fresh) != 0 {
		t.Fatalf("a returning course reported %v", itemIDs(fresh))
	}
}

func TestAGradeRemovedInMoodleIsRemovedHere(t *testing.T) {
	st := testStore(t)
	courses := []model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 7)}
	applySchool(t, st, courses, []model.SchoolGrade{
		schoolGrade(101, 1, "ОВ 1", 7, "2026-11-20T10:00"),
		schoolGrade(102, 1, "ОВ 2", 3, "2026-11-21T10:00"),
	})
	applySchool(t, st, courses, []model.SchoolGrade{schoolGrade(101, 1, "ОВ 1", 7, "2026-11-20T10:00")})

	sum, err := st.SchoolSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if !slices.Equal(itemIDs(sum[0].Grades), []int64{101}) {
		t.Fatalf("grades = %v", itemIDs(sum[0].Grades))
	}
}

func TestAGradeOfACourseOutsideTheSyncIsRejected(t *testing.T) {
	st := testStore(t)
	_, err := st.ApplySchoolSync(
		[]model.SchoolCourse{schoolCourse(1, "Алгебра", 0, 0)},
		[]model.SchoolGrade{schoolGrade(901, 9, "ОВ 1", 7, "2026-11-20T10:00")}, syncNow)
	if err == nil {
		t.Fatal("a grade of an unknown course was accepted")
	}
	if sum, _ := st.SchoolSummary(); len(sum) != 0 {
		t.Fatalf("a rejected sync left %+v behind", sum)
	}
}

func TestSchoolSummaryFollowsMoodlePosition(t *testing.T) {
	st := testStore(t)
	applySchool(t, st, []model.SchoolCourse{
		schoolCourse(1, "Алгебра", 2, 0), schoolCourse(2, "Фізика", 0, 0), schoolCourse(3, "Історія", 1, 0),
	}, nil)
	sum, err := st.SchoolSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	var names []string
	for _, c := range sum {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"Фізика", "Історія", "Алгебра"}) {
		t.Fatalf("order = %q", names)
	}
}

func TestSchoolGradesBetweenIsHalfOpen(t *testing.T) {
	st := testStore(t)
	applySchool(t, st,
		[]model.SchoolCourse{schoolCourse(1, "Алгебра", 1, 0), schoolCourse(2, "Фізика", 0, 0)},
		[]model.SchoolGrade{
			schoolGrade(101, 1, "ОВ 1", 7, "2026-11-22T23:59"), // Sunday before
			schoolGrade(102, 1, "ОВ 2", 7, "2026-11-23T00:00"), // Monday
			schoolGrade(201, 2, "ОВ 1", 7, "2026-11-27T15:00"),
			schoolGrade(103, 1, "ОВ 3", 7, "2026-11-30T00:00"), // next Monday
		})

	week, err := st.SchoolGradesBetween("2026-11-23T00:00", "2026-11-30T00:00")
	if err != nil {
		t.Fatalf("between: %v", err)
	}
	if !slices.Equal(itemIDs(week), []int64{201, 102}) {
		t.Fatalf("week = %v, want [201 102] (course order)", itemIDs(week))
	}
	if week[0].CourseName != "Фізика" {
		t.Fatalf("course name = %q", week[0].CourseName)
	}
}
