package schooltoday

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// The lesson detail page is the only place the portal publishes what actually
// happened at a lesson: the topic, the teacher's notes, the homework and the
// marks. None of it is in the timetable JSON, which carries a bare `hasMarks`
// boolean, and the documented Open API has no marks endpoint at all — so this
// is HTML scraping by necessity, not by preference.
//
// The page is four Bootstrap tab panes. Parsing keys on their ids and on the
// bold field labels inside them, both of which are the portal's own markup and
// will change without notice; the tests pin the parser to a real captured
// response so a redesign fails loudly here rather than quietly downstream.

// Tab pane ids on the lesson detail page.
const (
	tabGeneral  = "general"
	tabHomework = "lessonhomework"
	tabMark     = "mark"
)

// Field labels inside the general tab, rendered as "<b>Тема: </b>value".
const (
	labelTeacher = "Вчитель:"
	labelTopic   = "Тема:"
	labelNotes   = "Нотатки:"
)

// LessonDetail is one lesson's detail page, parsed. Subject is deliberately
// absent: the page carries one, but the collector takes the subject from the
// timetable event instead, because that is the spelling — group tag and all —
// the rest of the school code strips and classifies.
type LessonDetail struct {
	Teacher  string
	Topic    string
	Notes    string
	Homework string
	Marks    []Mark
	Files    []File
}

// Mark is one mark as the portal presents it: the column it sits under and the
// value as rendered ("9,00"). Not parsed into a number — nothing computes with
// it, and the decimal comma is the school's business.
type Mark struct {
	Kind  string
	Value string
}

// File is an attachment link. Title is the original filename from the anchor's
// title attribute, which is the only human-readable name available.
type File struct {
	Kind  string // "homework" | "lesson"
	URL   string
	Title string
}

// ParseLessonDetail reads one LessonView response.
//
// Missing pieces are not errors: a lesson with no marks, no homework or no
// notes yet is the ordinary state of a lesson the teacher has not written up,
// and the review renders it as such. Only markup it cannot parse at all is.
func ParseLessonDetail(body []byte) (LessonDetail, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return LessonDetail{}, fmt.Errorf("school-today: parse lesson detail: %w", err)
	}

	var d LessonDetail
	if general := elementByID(doc, tabGeneral); general != nil {
		d.Teacher = labelledValue(general, labelTeacher)
		d.Topic = labelledValue(general, labelTopic)
		d.Notes = labelledValue(general, labelNotes)
	}
	if homework := elementByID(doc, tabHomework); homework != nil {
		d.Homework, d.Files = parseHomework(homework)
	}
	if marks := elementByID(doc, tabMark); marks != nil {
		d.Marks = parseMarks(marks)
	}
	return d, nil
}

// labelledValue returns the text that follows a "<b>Label </b>" inside n. The
// general tab is a flat run of bold labels and bare text rather than a table,
// so the value is simply whatever text comes next before the following label.
func labelledValue(n *html.Node, label string) string {
	var (
		found   bool
		out     strings.Builder
		walk    func(*html.Node)
		labels  = []string{labelTeacher, labelTopic, labelNotes, "Предмет:", "Клас:", "Кімната:"}
		isLabel = func(s string) bool {
			for _, l := range labels {
				if strings.HasPrefix(s, l) {
					return true
				}
			}
			return false
		}
	)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "b" {
			text := strings.TrimSpace(nodeText(node))
			switch {
			case strings.HasPrefix(text, label):
				found = true
			case found && isLabel(text):
				// The next field has started; whatever followed ours is all of it.
				found = false
			}
			return
		}
		if found && node.Type == html.TextNode {
			out.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(out.String())
}

// parseHomework reads the homework tab: a "Учень | Домашнє завдання" table
// whose second cell holds the assignment text and any attachment links.
//
// The first body row is taken rather than the only one. The parent login sees
// one pupil today, but the same markup serves a teacher looking at a whole
// class, and a second row must not turn into a parse failure.
func parseHomework(n *html.Node) (string, []File) {
	row := firstBodyRow(n)
	if row == nil {
		return "", nil
	}
	cells := childElements(row, "td")
	if len(cells) < 2 {
		return "", nil
	}
	cell := cells[1]

	var files []File
	for _, a := range descendants(cell, "a") {
		href := attr(a, "href")
		if href == "" {
			continue
		}
		files = append(files, File{Kind: "homework", URL: href, Title: attr(a, "title")})
	}
	// The links render as paperclip icons with no text of their own, so the
	// cell's text is the assignment and nothing else.
	return strings.TrimSpace(nodeText(cell)), files
}

// parseMarks reads the marks tab, zipping the header row's mark kinds against
// the first body row's values. Both skip their first column, which names the
// pupil.
func parseMarks(n *html.Node) []Mark {
	head := firstRowIn(n, "thead")
	row := firstBodyRow(n)
	if head == nil || row == nil {
		return nil
	}
	kinds := childElements(head, "th")
	values := childElements(row, "td")
	if len(kinds) < 2 || len(values) < 2 {
		return nil
	}

	var out []Mark
	for i := 1; i < len(kinds) && i < len(values); i++ {
		kind := strings.TrimSpace(nodeText(kinds[i]))
		value := strings.TrimSpace(nodeText(values[i]))
		// An empty cell is a lesson that was not graded, not a blank mark.
		if kind == "" || value == "" {
			continue
		}
		out = append(out, Mark{Kind: kind, Value: value})
	}
	return out
}

// --- small html helpers -----------------------------------------------------

func elementByID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode && attr(n, "id") == id {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := elementByID(c, id); found != nil {
			return found
		}
	}
	return nil
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// nodeText is the concatenated text of a subtree, with runs of whitespace
// collapsed — the portal pads its cells with tabs and newlines, which would
// otherwise arrive in the middle of a Telegram message.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// descendants collects every element with the given tag under n.
func descendants(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == tag {
			out = append(out, node)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// childElements returns the direct element children of n with the given tag.
func childElements(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			out = append(out, c)
		}
	}
	return out
}

// firstBodyRow is the first <tr> inside the first <tbody> under n.
func firstBodyRow(n *html.Node) *html.Node { return firstRowIn(n, "tbody") }

func firstRowIn(n *html.Node, section string) *html.Node {
	for _, s := range descendants(n, section) {
		if rows := descendants(s, "tr"); len(rows) > 0 {
			return rows[0]
		}
	}
	return nil
}
