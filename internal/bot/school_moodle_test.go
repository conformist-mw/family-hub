package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/model"
	"familyhub/internal/moodle"
)

func avgOf(v float64) *float64 { return &v }

var htmlTag = regexp.MustCompile(`<(/?)([a-z]+)>`)

// assertLinesBalanced checks that every line opens and closes its own tags in
// order, so cutting the text between lines can never break the HTML.
func assertLinesBalanced(t *testing.T, text string) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		var stack []string
		for _, m := range htmlTag.FindAllStringSubmatch(line, -1) {
			if m[1] == "" {
				stack = append(stack, m[2])
				continue
			}
			if len(stack) == 0 || stack[len(stack)-1] != m[2] {
				t.Fatalf("unbalanced </%s> in line %q", m[2], line)
			}
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			t.Fatalf("unclosed <%s> in line %q", stack[len(stack)-1], line)
		}
	}
}

func assertContainsAll(t *testing.T, text string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("missing %q in:\n%s", w, text)
		}
	}
}

func TestFormatNewGrades(t *testing.T) {
	text := formatNewGrades([]model.SchoolGrade{
		{Name: "Оцінка вчителя 12", GradeRaw: 2.2, GradeText: "2,2", CourseName: "Алгебра", CourseAverage: "5,5"},
		{Name: "Контрольна робота 3", GradeRaw: 10, GradeText: "10,0", CourseName: "Фізика", CourseAverage: "8,0"},
		{Name: "Тест <вступ>", GradeRaw: 9, GradeText: "", CourseName: "Історія & право", CourseAverage: ""},
	})
	want := "🆕 Нові оцінки\n" +
		"<b>Алгебра</b>: ОВ 12 — 2,2 (сер. 5,5)\n" +
		"<b>Фізика</b>: КР 3 — 10 (сер. 8)\n" +
		"<b>Історія &amp; право</b>: Тест &lt;вступ&gt; — 9"
	if text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", text, want)
	}
	assertLinesBalanced(t, text)
}

func TestFormatNewGradesEmpty(t *testing.T) {
	if got := formatNewGrades(nil); got != "" {
		t.Fatalf("no grades should give no message, got %q", got)
	}
}

func TestFormatGradesSummary(t *testing.T) {
	text := formatGradesSummary([]model.SchoolCourse{
		{Name: "Алгебра", Average: avgOf(5.5), AverageText: "5,5", Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 1", GradeRaw: 7.8, GradeText: "7,80"},
			{Name: "Контрольна робота 1", GradeRaw: 10, GradeText: "10,0"},
			{Name: "Тест 3", GradeRaw: 9, GradeText: "9"},
		}},
		{Name: "Фізика", Average: avgOf(3.5), AverageText: "3,5", Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 1", GradeRaw: 3.5, GradeText: "3,5"},
		}},
		{Name: "Хімія"},
		{Name: "Біологія"},
		{Name: "Мистецтво", Average: avgOf(4), AverageText: "4,0"},
	})
	want := "📚 Оцінки\n" +
		"<b>Алгебра</b> — 5,5\n" +
		"   <i>ОВ 7,8 · КР 10 · Тест 3 — 9</i>\n" +
		"<b>Фізика</b> — 3,5 ⚠️\n" +
		"   <i>ОВ 3,5</i>\n" +
		"<b>Мистецтво</b> — 4\n" +
		"<i>Без оцінок: 2 предмети</i>"
	if text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", text, want)
	}
	assertLinesBalanced(t, text)
}

func TestFormatGradesSummaryLowAverageByNumber(t *testing.T) {
	// The warning follows the stored number, not Moodle's text.
	text := formatGradesSummary([]model.SchoolCourse{
		{Name: "Алгебра", Average: avgOf(3.96), AverageText: "4,0", Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 1", GradeRaw: 4, GradeText: "4"},
		}},
	})
	assertContainsAll(t, text, "<b>Алгебра</b> — 4 ⚠️")
}

func TestFormatGradesSummaryEmpty(t *testing.T) {
	if got := formatGradesSummary(nil); !strings.Contains(got, "Оцінок поки немає") {
		t.Fatalf("no courses: got %q", got)
	}
	got := formatGradesSummary([]model.SchoolCourse{{Name: "Алгебра"}})
	if got != "📚 Оцінки\n<i>Без оцінок: 1 предмет</i>" {
		t.Fatalf("courses without grades: got %q", got)
	}
}

func reviewWeek() moodle.Week {
	loc := time.FixedZone("EET", 2*3600)
	start := time.Date(2026, 11, 16, 0, 0, 0, 0, loc)
	return moodle.Week{N: 11, Start: start, End: start.AddDate(0, 0, 7)}
}

func TestFormatWeekReview(t *testing.T) {
	text := formatWeekReview(reviewWeek(), []moodle.CourseProgress{
		{Name: "Алгебра", Average: avgOf(5.5), AverageText: "5,5", Done: 3, Total: 5, Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 11", GradeRaw: 7.8, GradeText: "7,8"},
			{Name: "Контрольна робота 2", GradeRaw: 10, GradeText: "10,0"},
		}},
		{Name: "Фізика", Average: avgOf(3.2), AverageText: "3,2", Done: 0, Total: 2},
		// Nothing tracked and no grade this week: left out.
		{Name: "Фізкультура", Average: avgOf(9), AverageText: "9,0"},
		// No section of its own, but graded this week: shown with a note.
		{Name: "Хімія", NoSection: true, Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 11", GradeRaw: 6, GradeText: "6,0"},
		}},
		// No section and nothing graded: left out.
		{Name: "Мистецтво", NoSection: true},
		// Progress unreadable: shown anyway, grades and average still there.
		{Name: "Біологія", Unavailable: true, Average: avgOf(8.4), AverageText: "8,4", Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 11", GradeRaw: 9, GradeText: "9"},
		}},
		{Name: "Географія", Unavailable: true},
	})
	want := "📅 Тиждень 11 (16.11–22.11)\n" +
		"<b>Алгебра</b> — пройдено 3/5 · сер. 5,5\n" +
		"   <i>ОВ 11 — 7,8 · КР 2 — 10</i>\n" +
		"<b>Фізика</b> — пройдено 0/2 · сер. 3,2 ⚠️\n" +
		"<b>Хімія</b> — секції «Тиждень 11» немає\n" +
		"   <i>ОВ 11 — 6</i>\n" +
		"<b>Біологія</b> — прогрес недоступний · сер. 8,4\n" +
		"   <i>ОВ 11 — 9</i>\n" +
		"<b>Географія</b> — прогрес недоступний"
	if text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", text, want)
	}
	assertLinesBalanced(t, text)
}

func TestFormatWeekReviewNoSectionAnywhere(t *testing.T) {
	// Every readable course lacks the week's section: the anchor is off, and
	// that is said once rather than per subject.
	text := formatWeekReview(reviewWeek(), []moodle.CourseProgress{
		{Name: "Алгебра", NoSection: true, AverageText: "5,5", Average: avgOf(5.5), Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 11", GradeRaw: 7, GradeText: "7"},
		}},
		{Name: "Фізика", NoSection: true},
		{Name: "Біологія", Unavailable: true},
	})
	want := "📅 Тиждень 11 (16.11–22.11)\n" +
		"<i>Секції «Тиждень 11» немає в жодному предметі.</i>\n" +
		"<b>Алгебра</b> — сер. 5,5\n" +
		"   <i>ОВ 11 — 7</i>\n" +
		"<b>Біологія</b> — прогрес недоступний"
	if text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", text, want)
	}
	assertLinesBalanced(t, text)
}

func TestFormatWeekReviewEmpty(t *testing.T) {
	for name, progress := range map[string][]moodle.CourseProgress{
		"no courses":      nil,
		"nothing tracked": {{Name: "Алгебра", AverageText: "5,5", Average: avgOf(5.5)}},
	} {
		got := formatWeekReview(reviewWeek(), progress)
		if got != "📅 Тиждень 11 (16.11–22.11)\nЗа тиждень ні завдань, ні оцінок." {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

func TestFormatWeekReviewEscapesNames(t *testing.T) {
	text := formatWeekReview(reviewWeek(), []moodle.CourseProgress{
		{Name: "Мова <і> література", Done: 1, Total: 1, Grades: []model.SchoolGrade{
			{Name: "Есе \"Я & світ\"", GradeRaw: 8, GradeText: "8"},
		}},
	})
	assertContainsAll(t, text, "<b>Мова &lt;і&gt; література</b>", "Есе &#34;Я &amp; світ&#34; — 8")
	assertLinesBalanced(t, text)
}

func TestTidyGrade(t *testing.T) {
	for in, want := range map[string]string{
		"7,8":   "7,8",
		"7,80":  "7,8",
		"10,0":  "10",
		"10,00": "10",
		"100,0": "100",
		"10":    "10",
		"-":     "",
		"":      "",
		" 5,5 ": "5,5",
		"A":     "A",
		"85 %":  "85 %",
	} {
		if got := tidyGrade(in); got != want {
			t.Errorf("tidyGrade(%q) = %q, want %q", in, got, want)
		}
	}
}

// sentMessage is one sendMessage call the fake Telegram API received.
type sentMessage struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

// fakeTelegram points b at an offline telebot whose API is a local server
// that accepts every sendMessage and keeps it. The returned func reads what
// was sent so far.
func fakeTelegram(t *testing.T, b *Bot) func() []sentMessage {
	t.Helper()
	var mu sync.Mutex
	var sent []sentMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("unexpected Telegram call %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		var m sentMessage
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("decode sendMessage: %v", err)
		}
		mu.Lock()
		sent = append(sent, m)
		n := len(sent)
		mu.Unlock()
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":0,"chat":{"id":1,"type":"group"}}}`, n)
	}))
	t.Cleanup(srv.Close)
	tb, err := tele.NewBot(tele.Settings{Token: "test", URL: srv.URL, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	b.b = tb
	return func() []sentMessage {
		mu.Lock()
		defer mu.Unlock()
		return append([]sentMessage(nil), sent...)
	}
}

// seedSchool stores courses and their grades the way a first sync does.
func seedSchool(t *testing.T, b *Bot, courses []model.SchoolCourse, grades []model.SchoolGrade) {
	t.Helper()
	if _, err := b.store.ApplySchoolSync(courses, grades, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed school: %v", err)
	}
}

func runGrades(t *testing.T, b *Bot) {
	t.Helper()
	c := b.b.NewContext(tele.Update{Message: &tele.Message{Chat: &tele.Chat{ID: 42}, Text: "/grades"}})
	if err := b.cmdGrades(c); err != nil {
		t.Fatalf("cmdGrades: %v", err)
	}
}

func TestGradesCommandAnswersFromTheStore(t *testing.T) {
	b := menuBot(t)
	sent := fakeTelegram(t, b)
	seedSchool(t, b, []model.SchoolCourse{
		{ID: 11, Name: "Алгебра", Position: 0, Average: avgOf(5.5), AverageText: "5,5"},
		{ID: 12, Name: "Фізика", Position: 1},
	}, []model.SchoolGrade{
		{ItemID: 101, CourseID: 11, Name: "Оцінка вчителя 1", GradeRaw: 7.8, GradeText: "7,8", GradedAt: "2026-11-10T10:00"},
		{ItemID: 102, CourseID: 11, Name: "Контрольна робота 1", GradeRaw: 3.2, GradeText: "3,2", GradedAt: "2026-11-12T10:00"},
	})

	runGrades(t, b)
	got := sent()
	if len(got) != 1 {
		t.Fatalf("sent %d messages, want 1: %+v", len(got), got)
	}
	want := "📚 Оцінки\n<b>Алгебра</b> — 5,5\n   <i>ОВ 7,8 · КР 3,2</i>\n<i>Без оцінок: 1 предмет</i>"
	if got[0].Text != want || got[0].ParseMode != "HTML" || got[0].ChatID != "42" {
		t.Fatalf("sent %+v, want HTML %q to chat 42", got[0], want)
	}
}

func TestGradesCommandBeforeTheFirstSync(t *testing.T) {
	b := menuBot(t)
	sent := fakeTelegram(t, b)
	runGrades(t, b)
	got := sent()
	if len(got) != 1 || !strings.Contains(got[0].Text, "Оцінок поки немає") {
		t.Fatalf("sent %+v, want the empty-state answer", got)
	}
}

func TestGradesCommandSplitsALongAnswer(t *testing.T) {
	b := menuBot(t)
	var courses []model.SchoolCourse
	var grades []model.SchoolGrade
	for c := range 20 {
		id := int64(100 + c)
		courses = append(courses, model.SchoolCourse{
			ID: id, Name: fmt.Sprintf("Предмет з довгою назвою %c", 'А'+rune(c)), Position: c,
			Average: avgOf(6), AverageText: "6,0",
		})
		for g := range 25 {
			grades = append(grades, model.SchoolGrade{
				ItemID: id*1000 + int64(g), CourseID: id, Name: fmt.Sprintf("Тест до теми %d", g+1),
				GradeRaw: 9, GradeText: "9,00", GradedAt: fmt.Sprintf("2026-11-%02dT10:00", g%28+1),
			})
		}
	}
	seedSchool(t, b, courses, grades)

	whole, err := b.store.SchoolSummary()
	if err != nil {
		t.Fatal(err)
	}
	full := formatGradesSummary(whole)
	if len(full) <= messageLimit {
		t.Fatalf("fixture too small to split: %d bytes", len(full))
	}

	sent := fakeTelegram(t, b)
	runGrades(t, b)
	got := sent()
	if len(got) < 2 {
		t.Fatalf("sent %d messages, want the answer split", len(got))
	}
	texts := make([]string, len(got))
	for i, m := range got {
		if len(m.Text) > messageLimit {
			t.Errorf("chunk %d is %d bytes, over %d", i, len(m.Text), messageLimit)
		}
		if m.ParseMode != "HTML" {
			t.Errorf("chunk %d parse mode %q", i, m.ParseMode)
		}
		assertLinesBalanced(t, m.Text)
		texts[i] = m.Text
	}
	if strings.Join(texts, "\n") != full {
		t.Fatal("the chunks do not add up to the whole answer")
	}
}

func TestNotifyNewGrades(t *testing.T) {
	fresh := []model.SchoolGrade{
		{Name: "Оцінка вчителя 12", GradeRaw: 2.2, GradeText: "2,2", CourseName: "Алгебра", CourseAverage: "5,5"},
	}

	t.Run("posts the grades to the group as HTML", func(t *testing.T) {
		b := menuBot(t)
		b.cfg.NotifyChat = -100
		sent := fakeTelegram(t, b)
		b.NotifyNewGrades(fresh)
		got := sent()
		if len(got) != 1 || got[0].ChatID != "-100" || got[0].ParseMode != "HTML" ||
			got[0].Text != "🆕 Нові оцінки\n<b>Алгебра</b>: ОВ 12 — 2,2 (сер. 5,5)" {
			t.Fatalf("sent %+v", got)
		}
	})

	t.Run("no grades, no message", func(t *testing.T) {
		b := menuBot(t)
		b.cfg.NotifyChat = -100
		sent := fakeTelegram(t, b)
		b.NotifyNewGrades(nil)
		if got := sent(); len(got) != 0 {
			t.Fatalf("sent %+v", got)
		}
	})

	t.Run("no notify chat, nothing sent", func(t *testing.T) {
		b := menuBot(t)
		sent := fakeTelegram(t, b)
		b.NotifyNewGrades(fresh)
		if got := sent(); len(got) != 0 {
			t.Fatalf("sent %+v", got)
		}
	})
}

func TestGradesIsListedOnlyWithMoodle(t *testing.T) {
	without := &Bot{}
	with := &Bot{cfg: Config{Moodle: &moodle.Service{}}}
	if strings.Contains(without.helpText(), "/grades") || strings.Contains(without.startText(1), "/grades") {
		t.Error("/grades is advertised without Moodle")
	}
	if !strings.Contains(with.helpText(), "/grades") || !strings.Contains(with.startText(1), "/grades") {
		t.Error("/grades is missing from help or start with Moodle")
	}
}
