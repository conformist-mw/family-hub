package bot

import (
	"context"
	"familyhub/internal/audit"
	"familyhub/internal/db"
	"familyhub/internal/schooltoday"
	"familyhub/internal/store"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"familyhub/internal/model"
)

// sept1 is a Tuesday; the fixtures below are the real shape of that day.
var sept1 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func lesson(subject, beg, fin, topic string) model.SchoolLesson {
	return model.SchoolLesson{
		Subject:  subject,
		StartsAt: "2026-09-01T" + beg,
		EndsAt:   "2026-09-01T" + fin,
		Topic:    topic,
	}
}

// A Tuesday: lessons stop at 13:10, but the child is at school until 15:40.
// This is the day the lessons-only digest got wrong.
func tuesday() []model.SchoolLesson {
	return []model.SchoolLesson{
		lesson("Сніданок [Food Hub]", "08:30", "08:45", ""),
		lesson("Ранкове налаштування [9]", "08:45", "09:00", ""),
		lesson("Алгебра [9]", "09:00", "09:40", ""),
		lesson("Українська мова [9]", "09:50", "10:30", "Вступ. Розвиток української мови. "),
		lesson("Географія [9]", "11:30", "12:10", ""),
		lesson("Фізична культура [Activity H]", "12:30", "13:10", ""),
		lesson("Прогулянка [9]", "13:10", "13:40", ""),
		lesson("Обід [Food Hub]", "13:40", "14:00", ""),
		lesson("Група продовженого дня [8]", "14:10", "14:50", ""),
		lesson("Група продовженого дня [8]", "15:00", "15:40", ""),
	}
}

func render(t *testing.T, day time.Time, lessons []model.SchoolLesson) string {
	t.Helper()
	got, ok := schoolDigestText(day, lessons, time.UTC)
	if !ok {
		t.Fatal("digest suppressed, want a message")
	}
	return got
}

// The whole point of the feature. The last lesson ends at 13:10 and the
// after-school block runs to 15:40; a parent reading 13:10 drives over two and
// a half hours early.
func TestTheDigestSaysWhenTheChildIsActuallyFree(t *testing.T) {
	got := render(t, sept1, tuesday())
	if !strings.Contains(got, "🏁 <b>Вільний у 15:40</b>") {
		t.Fatalf("wrong or missing free-at line:\n%s", got)
	}
	if strings.Contains(got, "Вільний у 13:10") {
		t.Fatalf("free-at read the last lesson instead of the last slot:\n%s", got)
	}
}

// A day whose last slot *is* a lesson must not gain a phantom tail.
func TestTheFreeTimeIsTheLastLessonWhenNothingFollowsIt(t *testing.T) {
	got := render(t, sept1, []model.SchoolLesson{
		lesson("Сніданок [Food Hub]", "08:30", "08:45", ""),
		lesson("Алгебра [9]", "09:00", "09:40", ""),
		lesson("Англійська мова [9]", "15:00", "15:40", ""),
	})
	if !strings.Contains(got, "🏁 <b>Вільний у 15:40</b>") {
		t.Fatalf("free-at line wrong:\n%s", got)
	}
}

// Only starts were sent before, so 12:30–13:10 read as "12:30" and looked like
// the day ended forty minutes earlier than it did.
func TestALessonCarriesBothEnds(t *testing.T) {
	got := render(t, sept1, tuesday())
	for _, want := range []string{"09:00–09:40", "12:30–13:10"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

// Two back-to-back blocks of after-school care are one stretch to a parent.
func TestAdjacentAfterSchoolBlocksMergeIntoOneStretch(t *testing.T) {
	got := render(t, sept1, tuesday())
	if !strings.Contains(got, "14:10–15:40") {
		t.Fatalf("the two after-school blocks did not merge:\n%s", got)
	}
	if n := strings.Count(got, "Група продовженого дня"); n != 1 {
		t.Fatalf("after-school care named %d times, want 1:\n%s", n, got)
	}
}

// The portal splits one afternoon of care across two teaching groups, so the
// tags differ while the thing itself does not. Merging keys on the subject
// after the tag is stripped, which is what lets these join.
func TestAfterSchoolBlocksMergeAcrossDifferentGroupTags(t *testing.T) {
	got := render(t, sept1, []model.SchoolLesson{
		lesson("Алгебра [9]", "09:00", "09:40", ""),
		lesson("Група продовженого дня [Activity H]", "15:00", "15:40", ""),
		lesson("Група продовженого дня [8]", "15:50", "16:30", ""),
	})
	if !strings.Contains(got, "15:00–16:30") {
		t.Fatalf("differently-tagged care blocks did not merge:\n%s", got)
	}
}

// Merging is for the day's structure, not for teaching. Two periods of one
// subject are two lessons, and each can carry its own topic.
func TestTwoPeriodsOfTheSameSubjectStayTwoLessons(t *testing.T) {
	got := render(t, sept1, []model.SchoolLesson{
		lesson("Алгебра [9]", "09:00", "09:40", ""),
		lesson("Алгебра [9]", "09:50", "10:30", ""),
	})
	if n := strings.Count(got, "Алгебра"); n != 2 {
		t.Fatalf("two lesson periods collapsed into %d line(s):\n%s", n, got)
	}
}

// Meals, the walk and after-school care belong in the message — they are why
// the child is still at school — but they are not lessons, and their place in
// the message is their place in the day. Breakfast is first because it is
// first, not last under a heading.
func TestTheDayReadsInTimeOrder(t *testing.T) {
	got := render(t, sept1, tuesday())
	want := []string{
		"Сніданок", "Ранкове налаштування", // before the lessons
		"Алгебра", "Українська мова", "Географія", "Фізична культура",
		"Прогулянка", "Обід", "Група продовженого дня", // after them
	}
	at := 0
	for _, w := range want {
		i := strings.Index(got[at:], w)
		if i < 0 {
			t.Fatalf("%q is missing or out of order:\n%s", w, got)
		}
		at += i
	}
}

// The runs are grouped by a blank line rather than a heading, so a lesson and
// the breakfast before it never sit in the same paragraph.
func TestTheLessonsAreSeparatedFromTheDayAroundThem(t *testing.T) {
	got := render(t, sept1, tuesday())
	if !strings.Contains(got, "Ранкове налаштування\n\n🕐 09:00") {
		t.Fatalf("no break between the morning and the lessons:\n%s", got)
	}
	if !strings.Contains(got, "Фізична культура\n\n🚶 13:10") {
		t.Fatalf("no break between the lessons and the rest of the day:\n%s", got)
	}
}

// A Wednesday: the walk and lunch fall *between* two lessons. They must stay
// there — pushing them to the bottom would say the day ended at 13:10, and
// pulling them up would say it ended at 12:10.
func TestNonLessonsBetweenTwoLessonsStayBetweenThem(t *testing.T) {
	got := render(t, sept1, []model.SchoolLesson{
		lesson("Сніданок [Food Hub]", "08:30", "08:45", ""),
		lesson("Інформатика [Інф.]", "12:30", "13:10", ""),
		lesson("Прогулянка [9]", "13:10", "13:40", ""),
		lesson("Обід [Food Hub]", "13:40", "14:00", ""),
		lesson("soft skills [8]", "14:10", "14:50", ""),
	})
	want := []string{"Сніданок", "Інформатика", "Прогулянка", "Обід", "soft skills"}
	at := 0
	for _, w := range want {
		i := strings.Index(got[at:], w)
		if i < 0 {
			t.Fatalf("%q is missing or out of order:\n%s", w, got)
		}
		at += i
	}
	if !strings.Contains(got, "🏁 <b>Вільний у 14:50</b>") {
		t.Fatalf("free-at line wrong:\n%s", got)
	}
}

// Order is this function's own invariant, not something the caller has to
// remember — the merge below depends on it too.
func TestRowsArrivingOutOfOrderAreSorted(t *testing.T) {
	got := render(t, sept1, []model.SchoolLesson{
		lesson("Група продовженого дня [8]", "15:00", "15:40", ""),
		lesson("Алгебра [9]", "09:00", "09:40", ""),
		lesson("Група продовженого дня [8]", "14:10", "14:50", ""),
	})
	if strings.Index(got, "Алгебра") > strings.Index(got, "Група продовженого дня") {
		t.Fatalf("out-of-order rows were not sorted:\n%s", got)
	}
	if !strings.Contains(got, "14:10–15:40") {
		t.Fatalf("the care blocks did not merge after sorting:\n%s", got)
	}
}

// Topics are filled in by teachers and usually still empty the evening before,
// so they show when present and take no room when not.
func TestATopicShowsOnlyWhenTheTeacherFilledItIn(t *testing.T) {
	got := render(t, sept1, tuesday())
	if !strings.Contains(got, "<i>Вступ. Розвиток української мови.</i>") {
		t.Fatalf("topic not rendered:\n%s", got)
	}
	if strings.Contains(got, "мови. </i>") {
		t.Fatalf("padding from the portal reached the message:\n%s", got)
	}
	// Алгебра has no topic; the line after it must be the next lesson.
	if strings.Contains(got, "Алгебра\n     <i>") {
		t.Fatalf("empty topic rendered as a blank line:\n%s", got)
	}
}

// The tag names the teaching group, not the subject, and is noise in a family
// message.
func TestTheGroupTagIsStripped(t *testing.T) {
	got := render(t, sept1, tuesday())
	if strings.Contains(got, "[9]") || strings.Contains(got, "[Food Hub]") ||
		strings.Contains(got, "[Activity H]") {
		t.Fatalf("a group tag survived:\n%s", got)
	}
	if !strings.Contains(got, "Алгебра\n") {
		t.Fatalf("subject lost more than its tag:\n%s", got)
	}
}

// A subject whose own title carries brackets keeps them.
func TestBracketsInsideATitleSurvive(t *testing.T) {
	got := render(t, sept1, []model.SchoolLesson{
		lesson("Інтегрований курс літератур (української та зарубіжної) [9]", "10:40", "11:20", ""),
	})
	if !strings.Contains(got, "Інтегрований курс літератур (української та зарубіжної)") {
		t.Fatalf("title mangled:\n%s", got)
	}
}

// The case that used to produce nothing at all: the school fills in meals and
// after-school care before it fills in subjects, so the lessons-only view saw
// an empty day and stayed silent — indistinguishable from a broken sync.
func TestADayWithNoPublishedLessonsStillSends(t *testing.T) {
	got := render(t, sept1, []model.SchoolLesson{
		lesson("Сніданок [Food Hub]", "08:30", "08:45", ""),
		lesson("Ранкове налаштування [9]", "08:45", "09:00", ""),
		lesson("Прогулянка [9]", "13:10", "13:40", ""),
		lesson("Обід [Food Hub]", "13:40", "14:00", ""),
	})
	if !strings.Contains(got, "ще не опубліковано") {
		t.Fatalf("no word that the timetable is missing:\n%s", got)
	}
	if !strings.Contains(got, "🏁 <b>Вільний у 14:00</b>") {
		t.Fatalf("free-at line wrong for a lessonless day:\n%s", got)
	}
}

// A weekend or holiday holds nothing at all, and silence is the right answer.
func TestADayWithNothingAtAllSendsNothing(t *testing.T) {
	if _, ok := schoolDigestText(sept1, nil, time.UTC); ok {
		t.Fatal("sent a message for an empty day")
	}
}

// Sent as HTML, so a subject or topic carrying angle brackets would break the
// markup — or inject some.
func TestSubjectAndTopicAreEscaped(t *testing.T) {
	got := render(t, sept1, []model.SchoolLesson{
		lesson("Алгебра <b>9</b>", "09:00", "09:40", "Тема & <i>ще</i>"),
	})
	if strings.Contains(got, "<b>9</b>") || strings.Contains(got, "<i>ще</i>") {
		t.Fatalf("markup from the portal survived:\n%s", got)
	}
	if !strings.Contains(got, "&lt;b&gt;9&lt;/b&gt;") ||
		!strings.Contains(got, "Тема &amp; &lt;i&gt;ще&lt;/i&gt;") {
		t.Fatalf("not escaped:\n%s", got)
	}
}

// The header names the day the digest is about, in Ukrainian, so nobody has to
// work out which "tomorrow" a message from last night meant.
func TestTheHeaderNamesTomorrowsDate(t *testing.T) {
	got := render(t, sept1, tuesday())
	if !strings.Contains(got, "вівторок, 1 вересня") {
		t.Fatalf("header wrong:\n%s", got)
	}
}

// A row the portal sent with unusable times is dropped, not rendered broken.
func TestAnUnparseableRowIsSkipped(t *testing.T) {
	got := render(t, sept1, []model.SchoolLesson{
		{Subject: "Алгебра [9]", StartsAt: "nonsense", EndsAt: "2026-09-01T09:40"},
		lesson("Географія [9]", "11:30", "12:10", ""),
	})
	if strings.Contains(got, "Алгебра") {
		t.Fatalf("a row with a bad start was rendered:\n%s", got)
	}
	if !strings.Contains(got, "Географія") {
		t.Fatalf("the good row was lost with the bad one:\n%s", got)
	}
}

// The digest answers to its own time and nothing else: NOTIFICATIONS_ENABLED is
// off in prod because HA sends the appointment summaries, and HA cannot send
// this one — its calendar API drops the category that separates a lesson from
// after-school care.
func TestTheSchoolDigestFiresWithTheAppointmentDigestsSwitchedOff(t *testing.T) {
	prod := Config{
		NotifyChat:           -100,
		NotificationsEnabled: false, // the production shape
		DailyDigestTime:      "08:00",
		SchoolDigestTime:     "19:30",
	}
	at := func(hh, mm int) time.Time { return time.Date(2026, 9, 1, hh, mm, 0, 0, time.UTC) }

	daily, _, _, school, _ := prod.dueThisMinute(at(19, 30), "", "", "", "", "")
	if !school {
		t.Fatal("the school digest did not fire in the production shape")
	}
	if daily {
		t.Fatal("an appointment digest fired with notifications off")
	}
	if _, _, _, school, _ = prod.dueThisMinute(at(19, 31), "", "", "", "", ""); school {
		t.Fatal("fired at the wrong minute")
	}
	if _, _, _, school, _ = prod.dueThisMinute(at(19, 30), "", "", "", "2026-09-01", ""); school {
		t.Fatal("re-fired within the same day")
	}
}

func TestTheSchoolDigestStaysOffWithoutATime(t *testing.T) {
	if (Config{NotifyChat: -100}).schoolDigestEnabled() {
		t.Fatal("the school digest is on with no time set")
	}
}

// --- the Friday week review ------------------------------------------------

func reviewLesson(startsAt, subject, topic, notes, homework string, marks ...string) model.SchoolLessonDetail {
	d := model.SchoolLessonDetail{
		StartsAt: startsAt, Subject: subject, Topic: topic, Notes: notes, Homework: homework,
	}
	for _, m := range marks {
		d.Marks = append(d.Marks, model.SchoolMark{Kind: "Поточна", Value: m})
	}
	return d
}

func reviewWeek() []model.SchoolLessonDetail {
	return []model.SchoolLessonDetail{
		reviewLesson("2026-08-31T09:00", "Алгебра [9]", "Квадратні рівняння", "", "№ 4, 7 ст. 11"),
		reviewLesson("2026-08-31T09:50", "Українська мова [9]", "Головні члени речення",
			"Опрацювали вправи 1,2,3", "вправа 10 ст.13", "9,00"),
		reviewLesson("2026-09-02T09:00", "Алгебра [9]", "Дискримінант", "Розвʼязали 12 задач", "", "11,00"),
	}
}

// The heading a parent scans: the subject, how often it met, and what was
// graded — before any of the detail underneath.
func TestTheWeekReviewLeadsWithSubjectCountAndMarks(t *testing.T) {
	got, ok := schoolWeekReviewText(
		time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), reviewWeek(), 0, time.UTC)
	if !ok {
		t.Fatal("a week with lessons rendered nothing")
	}
	if !strings.Contains(got, "<b>Алгебра</b> · 2 уроки · 11") {
		t.Errorf("algebra heading missing:\n%s", got)
	}
	if !strings.Contains(got, "<b>Українська мова</b> · 1 урок · 9") {
		t.Errorf("ukrainian heading missing:\n%s", got)
	}
	if !strings.Contains(got, "Тиждень 31 серпня – 4 вересня") {
		t.Errorf("heading range missing:\n%s", got)
	}
}

// Two lessons of the same subject on different days are one block, in the
// order they happened — that is the whole reason the review regroups instead
// of replaying the timetable.
func TestTheWeekReviewGroupsASubjectAcrossDays(t *testing.T) {
	got, _ := schoolWeekReviewText(
		time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), reviewWeek(), 0, time.UTC)

	first := strings.Index(got, "Квадратні рівняння")
	second := strings.Index(got, "Дискримінант")
	other := strings.Index(got, "Головні члени речення")
	if first < 0 || second < 0 || other < 0 {
		t.Fatalf("a topic is missing:\n%s", got)
	}
	if !(first < second) {
		t.Errorf("algebra topics are out of order:\n%s", got)
	}
	if !(second < other) {
		t.Errorf("the algebra block was split by another subject:\n%s", got)
	}
}

// Subjects appear in the order the week met them, not alphabetically: the
// review reads like the week did.
func TestTheWeekReviewOrdersSubjectsByFirstLesson(t *testing.T) {
	got, _ := schoolWeekReviewText(
		time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), reviewWeek(), 0, time.UTC)
	if strings.Index(got, "Алгебра") > strings.Index(got, "Українська мова") {
		t.Errorf("subjects are not in first-lesson order:\n%s", got)
	}
}

// Notes are what the class actually did, and the review is mostly worth
// reading for them; homework has to survive too.
func TestTheWeekReviewCarriesNotesAndHomework(t *testing.T) {
	got, _ := schoolWeekReviewText(
		time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), reviewWeek(), 0, time.UTC)
	if !strings.Contains(got, "<i>Розвʼязали 12 задач</i>") {
		t.Errorf("notes missing:\n%s", got)
	}
	if !strings.Contains(got, "📕 № 4, 7 ст. 11") {
		t.Errorf("homework missing:\n%s", got)
	}
}

// A subject that met but has nothing written up must say so. Rendered as an
// empty block it is indistinguishable from a collector that dropped it.
func TestASubjectWithNoRecordsSaysSo(t *testing.T) {
	got, _ := schoolWeekReviewText(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		[]model.SchoolLessonDetail{
			reviewLesson("2026-08-31T09:00", "Фізична культура [Activity H]", "", "", "")},
		0, time.UTC)

	if !strings.Contains(got, "<i>без записів</i>") {
		t.Errorf("an empty subject rendered as nothing:\n%s", got)
	}
	if strings.Contains(got, "📕") {
		t.Errorf("an empty subject grew a homework line:\n%s", got)
	}
}

// The holidays are silence, exactly as with the evening digest.
func TestAnEmptyWeekSendsNothing(t *testing.T) {
	if _, ok := schoolWeekReviewText(
		time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), nil, 0, time.UTC); ok {
		t.Fatal("an empty week produced a message")
	}
}

// A week the collector half-failed must say so — but only when the count is
// actually known. /schoolweek reads rows and cannot know, and a silent "0"
// there would claim a completeness nobody verified.
func TestTheMissedCountAppearsOnlyWhenKnown(t *testing.T) {
	week := reviewWeek()
	start := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)

	got, _ := schoolWeekReviewText(start, week, 2, time.UTC)
	if !strings.Contains(got, "Не вдалося прочитати уроків: 2") {
		t.Errorf("a known miss count was not reported:\n%s", got)
	}
	for _, skipped := range []int{0, skippedUnknown} {
		got, _ := schoolWeekReviewText(start, week, skipped, time.UTC)
		if strings.Contains(got, "Не вдалося") {
			t.Errorf("skipped=%d reported a miss:\n%s", skipped, got)
		}
	}
}

// The portal pads whole marks to two decimals; "9,00" is a nine to a reader.
func TestWholeMarksLoseTheirDecimalPadding(t *testing.T) {
	got, _ := schoolWeekReviewText(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		[]model.SchoolLessonDetail{
			reviewLesson("2026-08-31T09:00", "Алгебра [9]", "Тема", "", "", "9,00", "9,50")},
		0, time.UTC)

	if !strings.Contains(got, "· 9, 9,50") {
		t.Errorf("marks = wrong shape:\n%s", got)
	}
}

// Ukrainian needs three plural forms and the teens break the last-digit rule.
func TestLessonCountPlurals(t *testing.T) {
	for n, want := range map[int]string{1: "урок", 2: "уроки", 4: "уроки", 5: "уроків",
		11: "уроків", 12: "уроків", 21: "урок", 22: "уроки", 25: "уроків"} {
		if got := pluralLessons(n); got != want {
			t.Errorf("pluralLessons(%d) = %q, want %q", n, got, want)
		}
	}
}

// Everything from the portal is user input as far as Telegram's HTML parser is
// concerned; a teacher's "<" would otherwise break the whole message.
func TestTheWeekReviewEscapesPortalText(t *testing.T) {
	got, _ := schoolWeekReviewText(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		[]model.SchoolLessonDetail{
			reviewLesson("2026-08-31T09:00", "Алгебра [9]", "a < b", "<b>жирно</b>", "")},
		0, time.UTC)

	if strings.Contains(got, "a < b") || !strings.Contains(got, "a &lt; b") {
		t.Errorf("topic was not escaped:\n%s", got)
	}
	if strings.Contains(got, "<b>жирно</b>") {
		t.Errorf("notes markup reached the message:\n%s", got)
	}
}

// A row whose start will not parse cannot be placed in the week; it is dropped
// rather than rendered under the wrong subject, and the rest survives.
func TestAnUnparseableStartDropsOnlyThatLesson(t *testing.T) {
	got, ok := schoolWeekReviewText(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		[]model.SchoolLessonDetail{
			reviewLesson("не дата", "Алгебра [9]", "Загублена тема", "", ""),
			reviewLesson("2026-08-31T09:50", "Географія [9]", "Материки", "", "")},
		0, time.UTC)

	if !ok || strings.Contains(got, "Загублена тема") {
		t.Errorf("the bad row was rendered:\n%s", got)
	}
	if !strings.Contains(got, "Материки") {
		t.Errorf("the good row was lost with the bad one:\n%s", got)
	}
}

// A real week is well past Telegram's 4096-byte cap, and notify() splits on
// line boundaries. Pinned because the renderer is what makes the split safe:
// every line closes its own tags, so no chunk can start mid-<b>.
func TestAFullWeekSplitsIntoWholeLines(t *testing.T) {
	var week []model.SchoolLessonDetail
	subjects := []string{"Алгебра", "Геометрія", "Українська мова", "Біологія", "Географія",
		"Фізика", "Правознавство", "Англійська мова", "Інформатика", "Мистецтво",
		"Інтегрований курс літератур", "Фізична культура", "soft skills", "Закріплення матеріалу"}
	for i, s := range subjects {
		for lesson := 0; lesson < 2; lesson++ {
			week = append(week, reviewLesson(
				fmt.Sprintf("2026-08-3%dT%02d:00", 1+lesson, 9+i%6), s+" [9]",
				"ПОВТОРЕННЯ, УЗАГАЛЬНЕННЯ ТА ПОГЛИБЛЕННЯ ВИВЧЕНОГО. Головні та другорядні члени речення",
				"Повторили головні та другорядні члени речення, питання до них. Опрацювали вправи 1,2,3 на сторінці 10",
				"виконати вправи 4, 7 ст 11, вправу 10 ст.13"))
		}
	}

	text, ok := schoolWeekReviewText(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), week, 0, time.UTC)
	if !ok {
		t.Fatal("a full week rendered nothing")
	}
	if len(text) <= 4096 {
		t.Fatalf("the fixture is too small to exercise splitting: %d bytes", len(text))
	}

	chunks := audit.SplitMessage(text, 4000)
	if len(chunks) < 2 {
		t.Fatalf("a %d-byte review was not split", len(text))
	}
	for i, c := range chunks {
		if len(c) > 4000 {
			t.Errorf("chunk %d is %d bytes, over the limit", i, len(c))
		}
		if strings.Count(c, "<b>") != strings.Count(c, "</b>") {
			t.Errorf("chunk %d splits a bold tag:\n%s", i, c)
		}
	}
}

// The review answers to its own day and time, and needs a portal service as
// well: without credentials there is nothing to collect, so a configured time
// alone must not arm it.
func TestTheWeekReviewFiresOnItsDayOnly(t *testing.T) {
	prod := Config{
		NotifyChat:           -100,
		NotificationsEnabled: false, // the production shape
		SchoolDigestTime:     "19:30",
		SchoolWeekReviewDOW:  5, // Friday
		SchoolWeekReviewTime: "19:45",
		School:               &schooltoday.Service{},
	}
	friday := func(hh, mm int) time.Time { return time.Date(2026, 9, 4, hh, mm, 0, 0, time.UTC) }

	if _, _, _, _, review := prod.dueThisMinute(friday(19, 45), "", "", "", "", ""); !review {
		t.Fatal("the review did not fire on Friday at its time")
	}
	if _, _, _, _, review := prod.dueThisMinute(friday(19, 46), "", "", "", "", ""); review {
		t.Error("fired at the wrong minute")
	}
	if _, _, _, _, review := prod.dueThisMinute(
		friday(19, 45), "", "", "", "", "2026-09-04"); review {
		t.Error("re-fired within the same day")
	}
	thursday := time.Date(2026, 9, 3, 19, 45, 0, 0, time.UTC)
	if _, _, _, _, review := prod.dueThisMinute(thursday, "", "", "", "", ""); review {
		t.Error("fired on the wrong day")
	}
}

func TestTheWeekReviewStaysOffWithoutAPortal(t *testing.T) {
	cfg := Config{NotifyChat: -100, SchoolWeekReviewDOW: 5, SchoolWeekReviewTime: "19:45"}
	if cfg.schoolWeekReviewEnabled() {
		t.Fatal("the review is armed with no portal service to collect from")
	}
	if _, _, _, _, review := cfg.dueThisMinute(
		time.Date(2026, 9, 4, 19, 45, 0, 0, time.UTC), "", "", "", "", ""); review {
		t.Fatal("it fired anyway")
	}
}

// The production shape has NOTIFICATIONS_ENABLED off, no nag and no reminders.
// RunDigests returns early when nothing is enabled, and the review has to be
// part of that test — otherwise a deploy that turns on only the review gets a
// ticker that never starts.
func TestTheReviewAloneKeepsTheDigestLoopAlive(t *testing.T) {
	cfg := Config{
		NotifyChat:           -100,
		NotificationsEnabled: false,
		SchoolWeekReviewDOW:  5,
		SchoolWeekReviewTime: "19:45",
		School:               &schooltoday.Service{},
	}
	if cfg.appointmentDigestsEnabled() || cfg.reminderNagEnabled() ||
		cfg.reminderPushEnabled() || cfg.schoolDigestEnabled() {
		t.Fatal("the fixture is not the review-only shape")
	}
	if !cfg.schoolWeekReviewEnabled() {
		t.Fatal("the review alone does not arm the loop; RunDigests would return early")
	}
}

// reviewBot wires a real collector against a fake portal. NotifyChat is left
// at zero on purpose: notify() returns immediately without touching telebot,
// so the collect-render-send path runs end to end in-process and only the
// final Telegram call is absent.
func reviewBot(t *testing.T, portalURL string) *Bot {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(database)

	svc := schooltoday.NewService(st, schooltoday.NewClient(portalURL),
		schooltoday.Config{Email: "parent@example.com", Password: "s3cret", PupilID: 79311},
		time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)

	return &Bot{
		cfg:    Config{Loc: time.UTC, School: svc},
		store:  st,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// waitFor polls until cond holds, because the collect deliberately runs off
// the caller's goroutine.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole Friday path: collect the week from the portal, render it, hand it
// to notify. The collect must not block the caller — RunDigests' ticker drops
// ticks while its receiver is busy, and this one can take minutes.
func TestTheWeekReviewCollectsOffTheTickerGoroutine(t *testing.T) {
	portal := newReviewPortal()
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	b := reviewBot(t, srv.URL)
	done := make(chan struct{})
	go func() {
		b.sendSchoolWeekReview(context.Background(), time.Date(2026, 9, 4, 19, 45, 0, 0, time.UTC))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sendSchoolWeekReview blocked its caller")
	}

	waitFor(t, "the collect to finish", func() bool { return portal.calls() > 0 })
	waitFor(t, "the week to be stored", func() bool {
		stored, err := b.store.LessonDetails("2026-08-31T00:00", "2026-09-07T00:00")
		return err == nil && len(stored) > 0
	})
}

// A portal that is down produces no message at all. Half a review reads as a
// quiet week, which is worse than nothing.
func TestAFailedCollectSendsNothing(t *testing.T) {
	portal := newReviewPortal()
	portal.failLogin = true
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	b := reviewBot(t, srv.URL)
	b.sendSchoolWeekReview(context.Background(), time.Date(2026, 9, 4, 19, 45, 0, 0, time.UTC))

	waitFor(t, "the failed collect to settle", func() bool { return !b.reviewRunning.Load() })
	stored, err := b.store.LessonDetails("2026-08-31T00:00", "2026-09-07T00:00")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("a failed collect wrote %d rows", len(stored))
	}
}

// Shutdown must not wait on ~29 sequential portal requests.
func TestACancelledContextAbandonsTheCollect(t *testing.T) {
	portal := newReviewPortal()
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	b := reviewBot(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.sendSchoolWeekReview(ctx, time.Date(2026, 9, 4, 19, 45, 0, 0, time.UTC))

	waitFor(t, "the cancelled collect to settle", func() bool { return !b.reviewRunning.Load() })
	if portal.calls() > 1 {
		t.Errorf("kept fetching after cancellation: %d lesson calls", portal.calls())
	}
}

// A portal slow enough to outlast the minute must not have a second collect
// piled on top: the group would get the same week twice.
func TestASecondReviewDoesNotStartWhileOneIsRunning(t *testing.T) {
	portal := newReviewPortal()
	release := make(chan struct{})
	portal.block = release
	srv := httptest.NewServer(portal.handler())
	defer srv.Close()

	b := reviewBot(t, srv.URL)
	at := time.Date(2026, 9, 4, 19, 45, 0, 0, time.UTC)
	b.sendSchoolWeekReview(context.Background(), at)
	waitFor(t, "the first collect to start", func() bool { return b.reviewRunning.Load() })

	b.sendSchoolWeekReview(context.Background(), at)
	close(release)

	waitFor(t, "the first collect to finish", func() bool { return !b.reviewRunning.Load() })
	if portal.logins() != 1 {
		t.Errorf("the portal saw %d logins, want 1 — a second review started", portal.logins())
	}
}

// --- /schoolweek -----------------------------------------------------------

// A typo must not be answered with data. Reading "/schoolweek дві" as "this
// week" would show a confidently wrong week and nobody would know.
func TestParseWeeksBack(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    int
		ok      bool
	}{
		{"", 0, true},
		{"0", 0, true},
		{"2", 2, true},
		{"  3 ", 3, true},
		{"52", 52, true},
		{"53", 0, false},  // past anything the records could hold
		{"-1", 0, false},  // the future is not a stored week
		{"дві", 0, false}, // a typo, answered with a hint rather than data
		{"2.5", 0, false},
	} {
		got, ok := parseWeeksBack(tc.payload)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseWeeksBack(%q) = (%d, %v), want (%d, %v)",
				tc.payload, got, ok, tc.want, tc.ok)
		}
	}
}

// weekBot is a bot with a store and nothing else the /schoolweek path needs.
func weekBot(t *testing.T) *Bot {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &Bot{
		cfg:    Config{Loc: time.UTC},
		store:  store.New(database),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// fridayClock pins "now" for the /schoolweek tests: Friday 2026-09-04, whose
// week starts Monday 2026-08-31.
var fridayClock = time.Date(2026, 9, 4, 20, 0, 0, 0, time.UTC)

// The argument selects a week, and the wrong offset would silently show the
// wrong one — the failure this command exists to prevent.
func TestSchoolWeekSelectsTheWeekAsked(t *testing.T) {
	b := weekBot(t)
	if err := b.store.SaveLessonDetails([]model.SchoolLessonDetail{
		{EventID: 1, PupilID: 79311, StartsAt: "2026-08-31T09:00",
			Subject: "Алгебра [9]", Topic: "Цього тижня"},
		{EventID: 2, PupilID: 79311, StartsAt: "2026-08-24T09:00",
			Subject: "Геометрія [9]", Topic: "Минулого тижня"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	current, err := b.schoolWeekChunks(fridayClock, 0)
	if err != nil {
		t.Fatalf("current week: %v", err)
	}
	if !strings.Contains(current[0], "Цього тижня") || strings.Contains(current[0], "Минулого") {
		t.Errorf("current week reply:\n%s", current[0])
	}

	previous, err := b.schoolWeekChunks(fridayClock, 1)
	if err != nil {
		t.Fatalf("previous week: %v", err)
	}
	if !strings.Contains(previous[0], "Минулого тижня") || strings.Contains(previous[0], "Цього") {
		t.Errorf("previous week reply:\n%s", previous[0])
	}
}

// Unlike the Friday push, a command must always answer: silence would read as
// the bot being broken rather than the week being empty.
func TestSchoolWeekSaysSoWhenTheWeekIsEmpty(t *testing.T) {
	b := weekBot(t)
	chunks, err := b.schoolWeekChunks(fridayClock, 3)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(chunks) != 1 || !strings.Contains(chunks[0], "записів немає") {
		t.Fatalf("reply = %v", chunks)
	}
}

// The command replays stored rows and cannot know what the original collect
// missed, so it must not print a count that would read as "nothing was".
func TestSchoolWeekDoesNotClaimNothingWasMissed(t *testing.T) {
	b := weekBot(t)
	if err := b.store.SaveLessonDetails([]model.SchoolLessonDetail{
		{EventID: 1, PupilID: 79311, StartsAt: "2026-08-31T09:00",
			Subject: "Алгебра [9]", Topic: "Тема"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	chunks, err := b.schoolWeekChunks(fridayClock, 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if strings.Contains(chunks[0], "Не вдалося прочитати") {
		t.Errorf("the reply claimed a miss count it cannot know:\n%s", chunks[0])
	}
}

// A full week does not fit in one Telegram message, and cmdWeek's single Send
// would fail on it.
func TestSchoolWeekRepliesInChunks(t *testing.T) {
	b := weekBot(t)
	var week []model.SchoolLessonDetail
	for i := 0; i < 28; i++ {
		week = append(week, model.SchoolLessonDetail{
			EventID: int64(i + 1), PupilID: 79311,
			StartsAt: fmt.Sprintf("2026-08-31T%02d:00", 8+i%10),
			Subject:  fmt.Sprintf("Предмет %d [9]", i%14),
			Topic:    "ПОВТОРЕННЯ, УЗАГАЛЬНЕННЯ ТА ПОГЛИБЛЕННЯ ВИВЧЕНОГО. Головні члени речення",
			Notes:    "Повторили головні та другорядні члени речення. Опрацювали вправи 1,2,3 на сторінці 10",
			Homework: "виконати вправи 4, 7 ст 11, вправу 10 ст.13",
		})
	}
	if err := b.store.SaveLessonDetails(week); err != nil {
		t.Fatalf("seed: %v", err)
	}

	chunks, err := b.schoolWeekChunks(fridayClock, 0)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("a full week came back as %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 4000 {
			t.Errorf("chunk %d is %d bytes", i, len(c))
		}
	}
}
