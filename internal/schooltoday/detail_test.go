package schooltoday

import (
	"strings"
	"testing"
)

// The golden case: a real captured response, parsed whole. This is the test
// that will break when the portal is redesigned, which is the point — the
// parser has no other contract than "that page, as it is served".
func TestParseLessonDetailReadsARealResponse(t *testing.T) {
	d, err := ParseLessonDetail(lessonFixture(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if d.Teacher != "Петренко Оксана" {
		t.Errorf("teacher = %q", d.Teacher)
	}
	if !strings.HasPrefix(d.Topic, "ПОВТОРЕННЯ, УЗАГАЛЬНЕННЯ") ||
		!strings.HasSuffix(d.Topic, "(повторення)") {
		t.Errorf("topic = %q", d.Topic)
	}
	// The notes are the half a parent actually wants: what was done, not what
	// the curriculum calls it.
	if !strings.Contains(d.Notes, "Опрацювали вправи 1,2,3 на сторінці 10") {
		t.Errorf("notes = %q", d.Notes)
	}
	if d.Homework != "виконати вправи 4, 7 ст 11, вправу 10 ст.13" {
		t.Errorf("homework = %q", d.Homework)
	}

	if len(d.Marks) != 1 || d.Marks[0].Kind != "Поточна" || d.Marks[0].Value != "9,00" {
		t.Errorf("marks = %+v", d.Marks)
	}
	if len(d.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(d.Files))
	}
	for _, f := range d.Files {
		if !strings.HasPrefix(f.URL, "https://") || f.Kind != "homework" {
			t.Errorf("file = %+v", f)
		}
	}
	if !strings.HasSuffix(d.Files[0].Title, ".png") {
		t.Errorf("file title = %q, want the original filename", d.Files[0].Title)
	}
}

// The topic must not swallow the field that follows it. The general tab is a
// flat run of labels and bare text, so a walker that does not stop at the next
// <b> returns the whole rest of the tab as one value.
func TestParsedFieldsDoNotBleedIntoEachOther(t *testing.T) {
	d, err := ParseLessonDetail(lessonFixture(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Contains(d.Topic, "Нотатки") || strings.Contains(d.Topic, "Повторили") {
		t.Errorf("topic ran into the notes: %q", d.Topic)
	}
	if strings.Contains(d.Teacher, "Кімната") || strings.Contains(d.Teacher, "Classroom") {
		t.Errorf("teacher ran into the room: %q", d.Teacher)
	}
}

// A lesson the teacher has not written up yet is the ordinary state of most of
// the week, not a parse failure.
func TestParseLessonDetailOnAnUnfilledLesson(t *testing.T) {
	const bare = `<html><body>
		<div class="tab-content">
			<div class="tab-pane" id="general">
				<b>Предмет: </b>Алгебра
				<b>Клас: </b>9-НУШ
				<b>Вчитель: </b>Петренко Оксана
				<b>Тема: </b>
				<b>Нотатки: </b>
			</div>
			<div class="tab-pane" id="lessonhomework">
				<table><thead><tr><th>Учень</th><th>Домашнє завдання</th></tr></thead>
				<tbody><tr><td>Іваненко Тарас</td><td></td></tr></tbody></table>
			</div>
			<div class="tab-pane" id="mark">
				<table><thead><tr><th>Учень</th></tr></thead>
				<tbody><tr><td>Іваненко Тарас</td></tr></tbody></table>
			</div>
		</div></body></html>`

	d, err := ParseLessonDetail([]byte(bare))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.Teacher != "Петренко Оксана" {
		t.Errorf("teacher = %q", d.Teacher)
	}
	if d.Topic != "" || d.Notes != "" || d.Homework != "" {
		t.Errorf("empty fields came back filled: %+v", d)
	}
	if len(d.Marks) != 0 || len(d.Files) != 0 {
		t.Errorf("marks = %+v, files = %+v", d.Marks, d.Files)
	}
}

// A whole class's worth of rows serves the same markup to a teacher account.
// One extra row must not become a parse failure — the first one is ours.
func TestParseLessonDetailTakesTheFirstPupilRow(t *testing.T) {
	const twoPupils = `<html><body>
		<div class="tab-pane" id="mark">
			<table><thead><tr><th>Учень</th><th>Поточна</th></tr></thead>
			<tbody>
				<tr><td>Іваненко Тарас</td><td>9,00</td></tr>
				<tr><td>Хтось Інший</td><td>6,00</td></tr>
			</tbody></table>
		</div></body></html>`

	d, err := ParseLessonDetail([]byte(twoPupils))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(d.Marks) != 1 || d.Marks[0].Value != "9,00" {
		t.Fatalf("marks = %+v, want only the first row's", d.Marks)
	}
}

// Several marks at one lesson (поточна and тематична on the same day) come as
// extra columns, and both belong in the review.
func TestParseLessonDetailReadsSeveralMarkColumns(t *testing.T) {
	const twoKinds = `<html><body>
		<div class="tab-pane" id="mark">
			<table><thead><tr><th>Учень</th><th>Поточна</th><th>Тематична</th></tr></thead>
			<tbody><tr><td>Іваненко Тарас</td><td>9,00</td><td>10,00</td></tr></tbody></table>
		</div></body></html>`

	d, err := ParseLessonDetail([]byte(twoKinds))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(d.Marks) != 2 ||
		d.Marks[0] != (Mark{Kind: "Поточна", Value: "9,00"}) ||
		d.Marks[1] != (Mark{Kind: "Тематична", Value: "10,00"}) {
		t.Fatalf("marks = %+v", d.Marks)
	}
}

// An ungraded column is a blank cell, not a mark with no value — storing it
// would render as a subject graded with nothing.
func TestParseLessonDetailSkipsABlankMarkCell(t *testing.T) {
	const blank = `<html><body>
		<div class="tab-pane" id="mark">
			<table><thead><tr><th>Учень</th><th>Поточна</th><th>Тематична</th></tr></thead>
			<tbody><tr><td>Іваненко Тарас</td><td></td><td>10,00</td></tr></tbody></table>
		</div></body></html>`

	d, err := ParseLessonDetail([]byte(blank))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(d.Marks) != 1 || d.Marks[0].Kind != "Тематична" {
		t.Fatalf("marks = %+v, want only Тематична", d.Marks)
	}
}

// Markup with none of the expected tabs — a redesign, an error page that still
// returned 200 — must come back empty rather than half-invented. The collector
// turns that into a lesson with no записів, which is visible in the review.
func TestParseLessonDetailOnUnrecognisedMarkup(t *testing.T) {
	for _, body := range []string{"", "<html><body><p>Сталася помилка</p></body></html>"} {
		d, err := ParseLessonDetail([]byte(body))
		if err != nil {
			t.Fatalf("parse %q: %v", body, err)
		}
		if d.Topic != "" || d.Notes != "" || d.Homework != "" ||
			len(d.Marks) != 0 || len(d.Files) != 0 {
			t.Fatalf("invented content from %q: %+v", body, d)
		}
	}
}

// The portal pads its cells with tabs and newlines; unhandled they arrive in
// the middle of a Telegram message.
func TestParsedTextIsWhitespaceNormalised(t *testing.T) {
	d, err := ParseLessonDetail(lessonFixture(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for name, v := range map[string]string{
		"topic": d.Topic, "notes": d.Notes, "homework": d.Homework, "teacher": d.Teacher} {
		if strings.Contains(v, "\n") || strings.Contains(v, "\t") || strings.Contains(v, "  ") {
			t.Errorf("%s carries raw whitespace: %q", name, v)
		}
	}
}
