package store_test

import (
	"testing"

	"familyhub/internal/model"
)

func detail(eventID int64, startsAt, subject string) model.SchoolLessonDetail {
	return model.SchoolLessonDetail{
		EventID: eventID, PupilID: 79311, StartsAt: startsAt, Subject: subject,
	}
}

// The whole point of the separate tables: the mirror is a rolling window that
// ReplaceSchoolLessons wipes every sync, and the record of what happened at a
// lesson must not go with it. If this ever fails, the feature is silently
// losing a week of topics and marks twelve hours after collecting them.
func TestLessonDetailsSurviveALessonReplace(t *testing.T) {
	st := schoolStore(t)

	d := detail(11714619, "2026-09-03T09:50", "Українська мова [9]")
	d.Topic = "Головні та другорядні члени речення"
	d.Marks = []model.SchoolMark{{Kind: "Поточна", Value: "9,00"}}
	if err := st.SaveLessonDetails([]model.SchoolLessonDetail{d}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := st.ReplaceSchoolLessons(79311, "2026-08-31T00:00", "2026-09-07T00:00",
		[]model.SchoolLesson{}); err != nil {
		t.Fatalf("replace lessons: %v", err)
	}

	got, err := st.LessonDetails("2026-08-31T00:00", "2026-09-07T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d details after the mirror was wiped, want 1", len(got))
	}
	if got[0].Topic == "" || len(got[0].Marks) != 1 {
		t.Fatalf("detail came back hollowed out: %+v", got[0])
	}
}

// A Friday restart re-arms the review and collects the same week again. The
// second write must land on the same rows, not beside them.
func TestSaveLessonDetailsIsIdempotent(t *testing.T) {
	st := schoolStore(t)

	d := detail(1, "2026-09-03T09:50", "Алгебра [9]")
	d.Marks = []model.SchoolMark{{Kind: "Поточна", Value: "9,00"}}
	d.Files = []model.SchoolFile{{Kind: "homework", URL: "https://example.com/a.png", Title: "a.png"}}

	for i := 0; i < 3; i++ {
		if err := st.SaveLessonDetails([]model.SchoolLessonDetail{d}); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	got, err := st.LessonDetails("2026-09-01T00:00", "2026-09-07T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows after three saves, want 1", len(got))
	}
	if len(got[0].Marks) != 1 || len(got[0].Files) != 1 {
		t.Fatalf("children accumulated: %d marks, %d files", len(got[0].Marks), len(got[0].Files))
	}
}

// The portal sometimes serves a detail page with the topic still blank — a
// teacher mid-edit, a partial render. A re-collect that hour must not erase
// what an earlier one correctly captured.
func TestABlankFieldDoesNotOverwriteAFilledOne(t *testing.T) {
	st := schoolStore(t)

	full := detail(1, "2026-09-03T09:50", "Алгебра [9]")
	full.Topic = "Квадратні рівняння"
	full.Notes = "Розвʼязали 12 задач"
	full.Homework = "№ 4, 7 ст. 11"
	full.Teacher = "Зайцева В."
	if err := st.SaveLessonDetails([]model.SchoolLessonDetail{full}); err != nil {
		t.Fatalf("save full: %v", err)
	}
	if err := st.SaveLessonDetails([]model.SchoolLessonDetail{
		detail(1, "2026-09-03T09:50", "Алгебра [9]")}); err != nil {
		t.Fatalf("save blank: %v", err)
	}

	got, err := st.LessonDetails("2026-09-01T00:00", "2026-09-07T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got[0].Topic != "Квадратні рівняння" || got[0].Notes != "Розвʼязали 12 задач" ||
		got[0].Homework != "№ 4, 7 ст. 11" || got[0].Teacher != "Зайцева В." {
		t.Fatalf("a blank collect erased a filled record: %+v", got[0])
	}
}

// Marks are the other way round: a mark the teacher removed is gone, and the
// record should follow rather than keep the withdrawn grade forever.
func TestMarksAreReplacedWholesale(t *testing.T) {
	st := schoolStore(t)

	d := detail(1, "2026-09-03T09:50", "Алгебра [9]")
	d.Marks = []model.SchoolMark{{Kind: "Поточна", Value: "7,00"}}
	if err := st.SaveLessonDetails([]model.SchoolLessonDetail{d}); err != nil {
		t.Fatalf("save: %v", err)
	}

	d.Marks = []model.SchoolMark{{Kind: "Поточна", Value: "9,00"}}
	if err := st.SaveLessonDetails([]model.SchoolLessonDetail{d}); err != nil {
		t.Fatalf("re-save: %v", err)
	}

	got, err := st.LessonDetails("2026-09-01T00:00", "2026-09-07T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got[0].Marks) != 1 || got[0].Marks[0].Value != "9,00" {
		t.Fatalf("corrected mark did not replace the old one: %+v", got[0].Marks)
	}
}

// The review asks for one week; a neighbouring week's lesson must not bleed
// into it, and the upper bound is exclusive so adjacent windows tile.
func TestLessonDetailsWindowIsHalfOpen(t *testing.T) {
	st := schoolStore(t)

	if err := st.SaveLessonDetails([]model.SchoolLessonDetail{
		detail(1, "2026-08-30T09:50", "Раніше [9]"),
		detail(2, "2026-08-31T09:50", "Понеділок [9]"),
		detail(3, "2026-09-04T09:50", "Пʼятниця [9]"),
		detail(4, "2026-09-07T00:00", "Наступний тиждень [9]"),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := st.LessonDetails("2026-08-31T00:00", "2026-09-07T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var ids []int64
	for _, d := range got {
		ids = append(ids, d.EventID)
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 3 {
		t.Fatalf("window returned %v, want [2 3]", ids)
	}
}

// Children belong to their own parent: the read stitches marks back by
// event_id, and a mix-up would show one subject's grade under another.
func TestChildRowsStayWithTheirLesson(t *testing.T) {
	st := schoolStore(t)

	a := detail(1, "2026-09-03T09:50", "Алгебра [9]")
	a.Marks = []model.SchoolMark{{Kind: "Поточна", Value: "9,00"}}
	b := detail(2, "2026-09-03T10:50", "Біологія [9]")
	b.Files = []model.SchoolFile{{Kind: "homework", URL: "https://example.com/b.png"}}
	if err := st.SaveLessonDetails([]model.SchoolLessonDetail{a, b}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := st.LessonDetails("2026-09-01T00:00", "2026-09-07T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got[0].Marks) != 1 || len(got[0].Files) != 0 {
		t.Fatalf("Алгебра: %d marks, %d files", len(got[0].Marks), len(got[0].Files))
	}
	if len(got[1].Marks) != 0 || len(got[1].Files) != 1 {
		t.Fatalf("Біологія: %d marks, %d files", len(got[1].Marks), len(got[1].Files))
	}
}

// An empty week is not an error — it is the holidays, and the renderer decides
// what to do about it.
func TestLessonDetailsOnAnEmptyWeek(t *testing.T) {
	st := schoolStore(t)

	got, err := st.LessonDetails("2026-09-01T00:00", "2026-09-07T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d details from an empty store", len(got))
	}
}
