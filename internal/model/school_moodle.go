package model

import (
	"regexp"
	"strconv"
	"strings"
)

// SchoolCourse is one subject from the child's Moodle enrolment list, with the
// course average the gradebook shows for it. Grades is filled by reads that
// return a course together with its grades (store.SchoolSummary) and ignored
// on write.
type SchoolCourse struct {
	ID       int64 // Moodle course id
	Name     string
	Position int
	// Average is nil while the course has no average yet (Moodle shows "-").
	Average     *float64
	AverageText string // Moodle's rendering ("5,5"), "" when there is none
	SyncedAt    string // LocalDatetime
	Grades      []SchoolGrade
}

// SchoolGrade is one graded item of a course. ItemID is Moodle's grade item
// id; it is what tells a new grade from one already seen.
//
// CourseName and CourseAverage are not stored on the grade: reads join them
// from the course so a notification or a weekly list can name the subject
// without a second lookup.
type SchoolGrade struct {
	ItemID       int64
	CourseID     int64
	ItemModule   string // quiz, lesson, assign... ("" if Moodle gave none)
	ItemInstance int64
	Name         string
	GradeRaw     float64
	GradeText    string // Moodle's rendering ("7,8")
	// GradedAt is a LocalDatetime. Left empty on write for a grade Moodle
	// gave no date for; store.ApplySchoolSync then dates it.
	GradedAt string

	CourseName    string
	CourseAverage string
	// Only on the grades a sync reports as new: the course average as a
	// number (for its level) and as Moodle wrote it before that sync.
	CourseAverageNum  *float64
	CoursePrevAverage string
}

var trailingGradeNumber = regexp.MustCompile(`\s+\d+$`)

// CourseName drops the class number Moodle appends to every subject
// ("Алгебра 9" → "Алгебра"): it is the same on every course and only takes
// room in a chat message. A name that is nothing but a number is kept as is.
func CourseName(full string) string {
	full = strings.TrimSpace(full)
	if short := trailingGradeNumber.ReplaceAllString(full, ""); short != "" {
		return short
	}
	return full
}

// maxItemName bounds a grade item's name in a chat line; longer ones are
// quiz titles that are whole sentences.
const maxItemName = 40

var itemAbbreviations = []struct{ long, short string }{
	{"Оцінка вчителя", "ОВ"},
	{"Контрольна робота", "КР"},
}

// ShortItemName abbreviates the two grade items that make up most of the
// gradebook ("Оцінка вчителя 12" → "ОВ 12", "Контрольна робота 3" → "КР 3")
// and cuts any other name to maxItemName characters.
func ShortItemName(name string) string {
	name = strings.TrimSpace(name)
	for _, a := range itemAbbreviations {
		if rest, ok := strings.CutPrefix(name, a.long); ok {
			name = a.short + rest
			break
		}
	}
	if r := []rune(name); len(r) > maxItemName {
		return strings.TrimSpace(string(r[:maxItemName-1])) + "…"
	}
	return name
}

// FormatGrade renders a grade or an average the way the school writes it: one
// decimal with a comma, and without a ",0" on a whole number (10, 7,8).
func FormatGrade(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return strings.Replace(s, ".", ",", 1)
}
