package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tele "gopkg.in/telebot.v4"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/moodle"
	"familyhub/internal/store"
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
		{Name: "Оцінка вчителя 12", GradeRaw: 4, GradeText: "4,0", CourseName: "Історія & право",
			CourseAverage: "6,3", CoursePrevAverage: "7,5", CourseAverageNum: avgOf(6.3)},
	})
	want := `<h4>🆕 Нова оцінка</h4>` +
		`<table compact><tr><th>Предмет</th><th align="center">Оцінка</th><th>Середній</th></tr>` +
		`<tr><td>🟡 Історія &amp; право</td><td align="center"><b>4</b></td><td>7,5 → 6,3</td></tr>` +
		`</table><footer>Оцінка вчителя</footer>`
	if text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", text, want)
	}
}

// Grades of different kinds say their kind beside the grade; an average that
// did not move, or had no value before, is shown once.
func TestFormatNewGradesMixedKinds(t *testing.T) {
	text := formatNewGrades([]model.SchoolGrade{
		{Name: "Контрольна робота 3", GradeText: "10,0", CourseName: "Фізика",
			CourseAverage: "8,0", CoursePrevAverage: "8", CourseAverageNum: avgOf(8)},
		{Name: "Оцінка вчителя 1", GradeText: "2,2", CourseName: "Хімія",
			CourseAverage: "2,2", CourseAverageNum: avgOf(2.2)},
		{Name: "Тест <вступ>", GradeText: "9", CourseName: "Історія"},
	})
	want := `<h4>🆕 Нові оцінки · 3</h4>` +
		`<table compact><tr><th>Предмет</th><th align="center">Оцінка</th><th>Середній</th></tr>` +
		`<tr><td>🟢 Фізика</td><td align="center"><b>10</b> <i>КР</i></td><td>8</td></tr>` +
		`<tr><td>🔴 Хімія</td><td align="center"><b>2,2</b> <i>ОВ</i></td><td>2,2</td></tr>` +
		`<tr><td>Історія</td><td align="center"><b>9</b> <i>Тест &lt;вступ&gt;</i></td><td></td></tr>` +
		`</table>`
	if text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", text, want)
	}
}

func TestFormatNewGradesEmpty(t *testing.T) {
	if got := formatNewGrades(nil); got != "" {
		t.Fatalf("no grades should give no message, got %q", got)
	}
}

func TestFormatGradesTable(t *testing.T) {
	text := formatGradesTable([]model.SchoolCourse{
		{Name: "Алгебра", Average: avgOf(5.5), AverageText: "5,5", Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 1", GradeRaw: 7.8, GradeText: "7,80"},
			{Name: "Контрольна робота 1", GradeRaw: 10, GradeText: "10,0"},
			{Name: "Тест 3", GradeRaw: 9, GradeText: "9"},
		}},
		{Name: "Хімія"},
		{Name: "Історія & право", Average: avgOf(9.25), AverageText: "9,3", Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 1", GradeRaw: 9.25, GradeText: "9,25"},
		}},
		{Name: "Фізика", Average: avgOf(3.5), AverageText: "3,5", Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 1", GradeRaw: 3.5, GradeText: "3,5"},
		}},
		{Name: "Географія", Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 1", GradeRaw: 8, GradeText: "8,0"},
		}},
		{Name: "Біологія"},
		{Name: "Мистецтво", Average: avgOf(7), AverageText: ""},
	}, reviewFriday)
	want := `<h3>📚 Оцінки · 27.11</h3>` +
		`<table striped compact><tr><th>Предмет</th><th align="center">Сер.</th><th>Оцінки</th></tr>` +
		`<tr><td>🔴 Фізика</td><td align="center"><b>3,5</b></td><td>3,5</td></tr>` +
		`<tr><td>🟡 Алгебра</td><td align="center"><b>5,5</b></td><td>7,8 · <b>10</b> · 9</td></tr>` +
		`<tr><td>🟢 Мистецтво</td><td align="center"><b>7</b></td><td></td></tr>` +
		`<tr><td>🟢 Історія &amp; право</td><td align="center"><b>9,3</b></td><td>9,25</td></tr>` +
		`<tr><td>Географія</td><td align="center"><b></b></td><td>8</td></tr>` +
		`</table><footer>Жирним — контрольні. Ще 2 предмети без оцінок.</footer>`
	if text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", text, want)
	}
}

func TestGradeLevelFollowsTheNumber(t *testing.T) {
	// The level follows the stored number, not Moodle's rounded text.
	for _, tc := range []struct {
		avg  *float64
		want string
	}{{nil, ""}, {avgOf(0), "🔴"}, {avgOf(3.96), "🔴"}, {avgOf(4), "🟡"}, {avgOf(6.99), "🟡"}, {avgOf(7), "🟢"}, {avgOf(12), "🟢"}} {
		if got := gradeLevel(tc.avg); got != tc.want {
			t.Errorf("gradeLevel(%v) = %q, want %q", tc.avg, got, tc.want)
		}
	}
}

func TestFormatGradesTableNoGradesYet(t *testing.T) {
	got := formatGradesTable([]model.SchoolCourse{{Name: "Алгебра"}}, reviewFriday)
	if got != "<h3>📚 Оцінки · 27.11</h3><footer>Жирним — контрольні. Ще 1 предмет без оцінок.</footer>" {
		t.Fatalf("courses without grades: got %q", got)
	}
}

// An average Moodle sent no text for is rendered from the number.
func TestAverageTextFromTheNumber(t *testing.T) {
	if got := averageText(avgOf(3.5), ""); got != "3,5 ⚠️" {
		t.Errorf("averageText(3.5, \"\") = %q, want \"3,5 ⚠️\"", got)
	}
	if got := averageText(nil, ""); got != "" {
		t.Errorf("averageText(nil, \"\") = %q, want empty", got)
	}
}

// report wraps courses as a review of data synced just now.
func report(courses ...moodle.CourseProgress) moodle.WeekReport {
	return moodle.WeekReport{Courses: courses, SyncedAt: reviewWeek().Start.AddDate(0, 0, 4)}
}

func reviewWeek() moodle.Week {
	loc := time.FixedZone("EET", 2*3600)
	start := time.Date(2026, 11, 16, 0, 0, 0, 0, loc)
	return moodle.Week{N: 11, Start: start, End: start.AddDate(0, 0, 7)}
}

func TestFormatWeekReview(t *testing.T) {
	text := formatWeekReview(reviewWeek(), report(
		moodle.CourseProgress{Name: "Алгебра", Average: avgOf(5.5), AverageText: "5,5", QuizzesGraded: 1, QuizzesTotal: 3, Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 11", GradeRaw: 7.8, GradeText: "7,8"},
			{Name: "Контрольна робота 2", GradeRaw: 10, GradeText: "10,0"},
		}},
		moodle.CourseProgress{Name: "Фізика", Average: avgOf(3.2), AverageText: "3,2", QuizzesGraded: 0, QuizzesTotal: 2},
		// No quiz and no grade this week: left out.
		moodle.CourseProgress{Name: "Фізкультура", Average: avgOf(9), AverageText: "9,0"},
		// No section of its own, but graded this week: shown with a note.
		moodle.CourseProgress{Name: "Хімія", NoSection: true, Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 11", GradeRaw: 6, GradeText: "6,0"},
		}},
		// No section and nothing graded: left out.
		moodle.CourseProgress{Name: "Мистецтво", NoSection: true},
		// Not a weekly course, but graded this week: its grades, no note.
		moodle.CourseProgress{Name: "Історія", NotWeekly: true, Grades: []model.SchoolGrade{
			{Name: "Семестрова контрольна робота", GradeRaw: 8, GradeText: "8,0"},
		}},
		moodle.CourseProgress{Name: "Право", NotWeekly: true},
		// Progress unreadable: shown anyway, grades and average still there.
		moodle.CourseProgress{Name: "Біологія", Unavailable: true, Average: avgOf(8.4), AverageText: "8,4", Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 11", GradeRaw: 9, GradeText: "9"},
		}},
		moodle.CourseProgress{Name: "Географія", Unavailable: true},
	))
	want := "📅 Тиждень 11 (16.11–22.11)\n" +
		"<b>Алгебра</b> — оцінено 1/3 тестів · сер. 5,5\n" +
		"   <i>ОВ 11 — 7,8 · КР 2 — 10</i>\n" +
		"<b>Фізика</b> — оцінено 0/2 тестів · сер. 3,2 ⚠️\n" +
		"<b>Хімія</b> — секції «Тиждень 11» немає\n" +
		"   <i>ОВ 11 — 6</i>\n" +
		"<b>Історія</b>\n" +
		"   <i>Семестрова контрольна робота — 8</i>\n" +
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
	// A course that is not weekly does not count either way.
	text := formatWeekReview(reviewWeek(), report(
		moodle.CourseProgress{Name: "Алгебра", NoSection: true, AverageText: "5,5", Average: avgOf(5.5), Grades: []model.SchoolGrade{
			{Name: "Оцінка вчителя 11", GradeRaw: 7, GradeText: "7"},
		}},
		moodle.CourseProgress{Name: "Фізика", NoSection: true},
		moodle.CourseProgress{Name: "Право", NotWeekly: true},
		moodle.CourseProgress{Name: "Біологія", Unavailable: true},
	))
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
	got := formatWeekReview(reviewWeek(), report(moodle.CourseProgress{Name: "Алгебра", AverageText: "5,5", Average: avgOf(5.5)}))
	if got != "📅 Тиждень 11 (16.11–22.11)\nЗа тиждень ні завдань, ні оцінок." {
		t.Errorf("no quizzes: got %q", got)
	}
}

// Before the first sync there is no week to review, not an empty one.
func TestFormatWeekReviewBeforeTheFirstSync(t *testing.T) {
	got := formatWeekReview(reviewWeek(), moodle.WeekReport{})
	if got != "📅 Тиждень 11 (16.11–22.11)\nMoodle ще не синхронізувався — огляду немає." {
		t.Errorf("got %q", got)
	}
}

func TestFormatWeekReviewStale(t *testing.T) {
	r := report(moodle.CourseProgress{Name: "Алгебра", QuizzesGraded: 1, QuizzesTotal: 2})
	r.Stale = true
	r.SyncedAt = time.Date(2026, 11, 18, 21, 30, 0, 0, time.UTC)
	text := formatWeekReview(reviewWeek(), r)
	want := "📅 Тиждень 11 (16.11–22.11)\n" +
		"<i>⚠️ Moodle не синхронізувався з 18.11 21:30 — оцінки тижня можуть бути неповні.</i>\n" +
		"<b>Алгебра</b> — оцінено 1/2 тестів"
	if text != want {
		t.Fatalf("got:\n%s\nwant:\n%s", text, want)
	}
	assertLinesBalanced(t, text)
}

func TestFormatWeekReviewEscapesNames(t *testing.T) {
	text := formatWeekReview(reviewWeek(), report(
		moodle.CourseProgress{Name: "Мова <і> література", QuizzesGraded: 1, QuizzesTotal: 1, Grades: []model.SchoolGrade{
			{Name: "Есе \"Я & світ\"", GradeRaw: 8, GradeText: "8"},
		}},
	))
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

// sentMessage is one sendMessage or sendRichMessage call the fake Telegram
// API received. A rich message has its content in Rich.HTML and no Text;
// telebot sends rich_message as a JSON-encoded string, kept in RichRaw.
type sentMessage struct {
	ChatID    json.Number `json:"chat_id"`
	Text      string      `json:"text"`
	ParseMode string      `json:"parse_mode"`
	RichRaw   string      `json:"rich_message"`
	Rich      struct {
		HTML string `json:"html"`
	} `json:"-"`
}

// fakeTelegram points b at an offline telebot whose API is a local server
// that accepts every sendMessage and sendRichMessage and keeps it. The returned func reads what
// was sent so far.
func fakeTelegram(t *testing.T, b *Bot) func() []sentMessage {
	t.Helper()
	var mu sync.Mutex
	var sent []sentMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") && !strings.HasSuffix(r.URL.Path, "/sendRichMessage") {
			t.Errorf("unexpected Telegram call %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		var m sentMessage
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("decode %s: %v", r.URL.Path, err)
		}
		if m.RichRaw != "" {
			if err := json.Unmarshal([]byte(m.RichRaw), &m.Rich); err != nil {
				t.Errorf("decode rich_message %q: %v", m.RichRaw, err)
			}
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
	if _, err := b.store.ApplySchoolSync(courses, grades, reviewFriday.Add(-time.Hour)); err != nil {
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
	if got[0].ChatID != "42" || got[0].Text != "" {
		t.Fatalf("sent %+v, want a rich message to chat 42", got[0])
	}
	assertContainsAll(t, got[0].Rich.HTML,
		"<table striped compact>",
		`<tr><td>🟡 Алгебра</td><td align="center"><b>5,5</b></td><td>7,8 · <b>3,2</b></td></tr>`,
		"Ще 1 предмет без оцінок.")
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

// A refused rich message is the command's error, for the handler's logging:
// there is no second format to fall back to.
func TestGradesCommandRichSendFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse rich message"}`)
	}))
	t.Cleanup(srv.Close)
	b := menuBot(t)
	seedSchool(t, b, []model.SchoolCourse{{ID: 11, Name: "Алгебра", Average: avgOf(5.5), AverageText: "5,5"}}, nil)
	tb, err := tele.NewBot(tele.Settings{Token: "test", URL: srv.URL, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	b.b = tb
	c := b.b.NewContext(tele.Update{Message: &tele.Message{Chat: &tele.Chat{ID: 42}, Text: "/grades"}})
	if err := b.cmdGrades(c); err == nil || !strings.Contains(err.Error(), "rich message") {
		t.Fatalf("cmdGrades error = %v, want the API's refusal", err)
	}
}

func TestNotifyNewGrades(t *testing.T) {
	fresh := []model.SchoolGrade{
		{Name: "Оцінка вчителя 12", GradeRaw: 2.2, GradeText: "2,2", CourseName: "Алгебра", CourseAverage: "5,5"},
	}

	t.Run("posts the grades to the group as a rich message", func(t *testing.T) {
		b := menuBot(t)
		b.cfg.NotifyChat = -100
		sent := fakeTelegram(t, b)
		b.NotifyNewGrades(fresh)
		got := sent()
		if len(got) != 1 || got[0].ChatID != "-100" || got[0].Rich.HTML != formatNewGrades(fresh) {
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

// fakeMoodle is a Moodle site with one course, 11, whose «Тиждень 12» section
// has a lesson, a label and two quizzes (instances 502 and 504). Each login waits for gate (when not
// nil) and is counted in logins; failLogin makes it refuse the credentials.
func fakeMoodle(t *testing.T, gate <-chan struct{}, logins *atomic.Int32, failLogin bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.URL.Path == "/login/token.php" {
			logins.Add(1)
			if gate != nil {
				<-gate
			}
			if failLogin {
				fmt.Fprint(w, `{"error":"Invalid login","errorcode":"invalidlogin"}`)
				return
			}
			fmt.Fprint(w, `{"token":"test-token"}`)
			return
		}
		switch fn := r.PostForm.Get("wsfunction"); fn {
		case "core_course_get_contents":
			fmt.Fprint(w, `[{"id":912,"name":"Тиждень 12. Квадратні рівняння","section":12,"modules":[
				{"id":701,"name":"Урок 1","modname":"lesson","instance":501},
				{"id":702,"name":"Контрольна робота 2","modname":"quiz","instance":502},
				{"id":703,"name":"Пояснення","modname":"label","instance":503},
				{"id":704,"name":"Оцінка вчителя 12","modname":"quiz","instance":504}]}]`)
		default:
			t.Errorf("unexpected Moodle call %q", fn)
			fmt.Fprint(w, `{"exception":"x","errorcode":"unexpected","message":"unexpected"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// reviewBot is a bot posting to the group, wired to a Moodle service on site
// and holding course 11 with a grade in week 12 (23–29.11.2026) and one from
// the week before. Only the week-12 grade is on a quiz of that week (502);
// quiz 504 is still ungraded.
func reviewBot(t *testing.T, site string) (*Bot, func() []sentMessage) {
	t.Helper()
	b := menuBot(t)
	b.cfg.NotifyChat = -100
	b.cfg.Moodle = moodle.NewService(b.store, moodle.NewClient(site), moodle.Config{
		Username: "pupil", Password: "secret",
		Week0: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
	}, time.UTC, b.logger, func() time.Time { return reviewFriday })
	seedSchool(t, b, []model.SchoolCourse{
		{ID: 11, Name: "Алгебра", Position: 0, Average: avgOf(5.5), AverageText: "5,5"},
	}, []model.SchoolGrade{
		{ItemID: 101, CourseID: 11, ItemModule: "quiz", ItemInstance: 490, Name: "Оцінка вчителя 11", GradeRaw: 7, GradeText: "7,0", GradedAt: "2026-11-18T10:00"},
		{ItemID: 102, CourseID: 11, ItemModule: "quiz", ItemInstance: 502, Name: "Оцінка вчителя 12", GradeRaw: 2.2, GradeText: "2,2", GradedAt: "2026-11-24T10:00"},
	})
	return b, fakeTelegram(t, b)
}

var reviewFriday = time.Date(2026, 11, 27, 19, 45, 0, 0, time.UTC)

func TestSchoolReviewPostsTheCurrentWeek(t *testing.T) {
	var logins atomic.Int32
	b, sent := reviewBot(t, fakeMoodle(t, nil, &logins, false))

	b.sendSchoolReview(t.Context(), reviewFriday)
	waitFor(t, "the review to finish", func() bool { return !b.schoolReviewRunning.Load() })

	got := sent()
	want := "📅 Тиждень 12 (23.11–29.11)\n" +
		"<b>Алгебра</b> — оцінено 1/2 тестів · сер. 5,5\n" +
		"   <i>ОВ 12 — 2,2</i>"
	if len(got) != 1 || got[0].ChatID != "-100" || got[0].ParseMode != "HTML" || got[0].Text != want {
		t.Fatalf("sent %+v, want HTML %q to the group", got, want)
	}
}

// A review still reading Moodle is not joined by a second one: the group would
// get the week twice.
func TestSchoolReviewRunsOneAtATime(t *testing.T) {
	gate := make(chan struct{})
	var logins atomic.Int32
	b, sent := reviewBot(t, fakeMoodle(t, gate, &logins, false))

	b.sendSchoolReview(t.Context(), reviewFriday)
	waitFor(t, "the first review to reach Moodle", func() bool { return logins.Load() == 1 })
	b.sendSchoolReview(t.Context(), reviewFriday)
	close(gate)
	waitFor(t, "the review to finish", func() bool { return !b.schoolReviewRunning.Load() })

	if n := logins.Load(); n != 1 {
		t.Fatalf("Moodle was logged into %d times, want once", n)
	}
	if got := sent(); len(got) != 1 {
		t.Fatalf("sent %d messages, want one review", len(got))
	}
}

// A review that cannot read Moodle is not sent: an empty one would read as a
// quiet week.
func TestSchoolReviewSkipsWhenMoodleFails(t *testing.T) {
	var logins atomic.Int32
	b, sent := reviewBot(t, fakeMoodle(t, nil, &logins, true))

	b.sendSchoolReview(t.Context(), reviewFriday)
	waitFor(t, "the review to finish", func() bool { return !b.schoolReviewRunning.Load() })

	if logins.Load() != 1 {
		t.Fatal("the review never tried Moodle")
	}
	if got := sent(); len(got) != 0 {
		t.Fatalf("sent %+v, want nothing", got)
	}
}

// The summer before the anchor's school year has no week to review.
func TestSchoolReviewSkipsBeforeTheSchoolYear(t *testing.T) {
	var logins atomic.Int32
	b, sent := reviewBot(t, fakeMoodle(t, nil, &logins, false))
	b.sendSchoolReview(t.Context(), time.Date(2026, 8, 21, 19, 45, 0, 0, time.UTC))
	if b.schoolReviewRunning.Load() || logins.Load() != 0 {
		t.Fatal("a review started before the school year")
	}
	if got := sent(); len(got) != 0 {
		t.Fatalf("sent %+v", got)
	}
}

func TestGradesCommandStoreFailure(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{cfg: Config{Loc: time.UTC}, store: store.New(database),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	database.Close() // every read now fails
	sent := fakeTelegram(t, b)
	runGrades(t, b)
	if got := sent(); len(got) != 1 || got[0].Text != "Не вдалося прочитати оцінки." {
		t.Fatalf("sent %+v, want the failure reply", got)
	}
}

// A failed send is logged and swallowed: the sync calling it has nobody to
// hand the error to.
func TestNotifyNewGradesSendFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"ok":false,"error_code":500,"description":"down"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	var logs strings.Builder
	b := menuBot(t)
	b.cfg.NotifyChat = -100
	b.logger = slog.New(slog.NewTextHandler(&logs, nil))
	tb, err := tele.NewBot(tele.Settings{Token: "test", URL: srv.URL, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	b.b = tb
	b.NotifyNewGrades([]model.SchoolGrade{{Name: "ОВ 1", GradeText: "7", CourseName: "Алгебра"}})
	if !strings.Contains(logs.String(), "notify new grades") {
		t.Errorf("send failure not logged: %q", logs.String())
	}
}

func TestSchoolReviewWithoutMoodleDoesNothing(t *testing.T) {
	b := menuBot(t)
	b.cfg.NotifyChat = -100
	sent := fakeTelegram(t, b)
	b.sendSchoolReview(t.Context(), reviewFriday)
	if b.schoolReviewRunning.Load() {
		t.Fatal("a review started without a service")
	}
	if got := sent(); len(got) != 0 {
		t.Fatalf("sent %+v", got)
	}
}
