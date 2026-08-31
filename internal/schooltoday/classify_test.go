package schooltoday

import "testing"

func TestClassify(t *testing.T) {
	cases := map[string]Category{
		"Алгебра [9]":                     CategoryLesson,
		"Українська мова [9]":             CategoryLesson,
		"soft skills [8]":                 CategoryLesson,
		"Інтегрований курс літератур [9]": CategoryLesson,
		"Сніданок [Food Hub]":             CategoryMeal,
		"Обід [Food Hub]":                 CategoryMeal,
		"Група продовженого дня [8]":      CategoryDaycare,
		"Прогулянка [9]":                  CategoryRoutine,
		"Ранкове налаштування [9]":        CategoryRoutine,
		// An unrecognised subject falls to lesson rather than being dropped.
		"Астрономія": CategoryLesson,
		"":           CategoryLesson,
	}
	for subject, want := range cases {
		if got := Classify(subject); got != want {
			t.Errorf("Classify(%q) = %q, want %q", subject, got, want)
		}
	}
}

func TestParseCategories(t *testing.T) {
	got := ParseCategories(" lesson , MEAL , nonsense ,")
	if !got[CategoryLesson] || !got[CategoryMeal] {
		t.Fatalf("lesson and meal should be included: %v", got)
	}
	if got[CategoryDaycare] || got[CategoryRoutine] {
		t.Errorf("unlisted categories should be absent: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("nonsense and blanks should be dropped, got %d entries: %v", len(got), got)
	}
}

// An empty allow-list yields an empty set, which callers read as "the default",
// not as "include nothing".
func TestParseCategoriesEmpty(t *testing.T) {
	if got := ParseCategories("  , ,"); len(got) != 0 {
		t.Fatalf("blank input should yield an empty set, got %v", got)
	}
}
