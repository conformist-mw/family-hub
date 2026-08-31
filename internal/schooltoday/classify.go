package schooltoday

import "strings"

// Category sorts a portal event by what it is, so the ICS feed can carry the
// academic day without the surrounding routine — meals, recess, after-school
// care. The portal has no such field: every slot, from Алгебра to Обід, arrives
// as the same kind of event, distinguishable only by its title. So this is a
// heuristic over the subject text, kept as a pure function and pinned by tests
// rather than stored, so the rule can change without a re-sync.
type Category string

const (
	// CategoryLesson is an academic subject — the default, and everything that
	// is not recognised as one of the others. Erring towards "lesson" keeps a
	// newly-named subject in the calendar rather than silently dropping it.
	CategoryLesson Category = "lesson"
	// CategoryMeal is снідання / обід — served in the school's "Food Hub".
	CategoryMeal Category = "meal"
	// CategoryDaycare is "Група продовженого дня", the after-school block.
	CategoryDaycare Category = "daycare"
	// CategoryRoutine is the non-teaching day structure: прогулянка, the
	// morning check-in.
	CategoryRoutine Category = "routine"
)

// classifyMarkers maps a lowercased substring to the category it signals. Order
// does not matter: the markers are chosen not to overlap, and Classify returns
// on the first hit.
var classifyMarkers = []struct {
	needle string
	cat    Category
}{
	{"сніданок", CategoryMeal},
	{"обід", CategoryMeal},
	{"продовженого дня", CategoryDaycare},
	{"прогулянка", CategoryRoutine},
	{"ранкове налаштування", CategoryRoutine},
}

// Classify returns the category of a portal event from its subject. The match
// is case-insensitive and ignores the trailing "[9]" / "[Food Hub]" group tag,
// because it looks only for a substring the tag never contains.
func Classify(subject string) Category {
	s := strings.ToLower(subject)
	for _, m := range classifyMarkers {
		if strings.Contains(s, m.needle) {
			return m.cat
		}
	}
	return CategoryLesson
}

// ParseCategories turns a comma-separated allow-list ("lesson,meal") into a
// set, dropping blanks and unknown names. An empty or all-invalid input yields
// an empty set, which the caller reads as "the default" rather than "nothing".
func ParseCategories(csv string) map[Category]bool {
	valid := map[Category]bool{
		CategoryLesson: true, CategoryMeal: true,
		CategoryDaycare: true, CategoryRoutine: true,
	}
	out := map[Category]bool{}
	for _, p := range strings.Split(csv, ",") {
		c := Category(strings.ToLower(strings.TrimSpace(p)))
		if valid[c] {
			out[c] = true
		}
	}
	return out
}
