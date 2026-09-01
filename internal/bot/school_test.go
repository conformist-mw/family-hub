package bot

import (
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

	daily, _, _, school := prod.dueThisMinute(at(19, 30), "", "", "", "")
	if !school {
		t.Fatal("the school digest did not fire in the production shape")
	}
	if daily {
		t.Fatal("an appointment digest fired with notifications off")
	}
	if _, _, _, school = prod.dueThisMinute(at(19, 31), "", "", "", ""); school {
		t.Fatal("fired at the wrong minute")
	}
	if _, _, _, school = prod.dueThisMinute(at(19, 30), "", "", "", "2026-09-01"); school {
		t.Fatal("re-fired within the same day")
	}
}

func TestTheSchoolDigestStaysOffWithoutATime(t *testing.T) {
	if (Config{NotifyChat: -100}).schoolDigestEnabled() {
		t.Fatal("the school digest is on with no time set")
	}
}
