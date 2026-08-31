package web

import "testing"

// The header highlights a world, the second row highlights a page inside it,
// and spaceOf is the only thing relating the two. A tab missing from the map
// silently loses its world highlight, which looks like a rendering bug rather
// than a missing entry — so the map is asserted rather than assumed.
func TestEveryTabResolvesToItsWorld(t *testing.T) {
	for tab, want := range map[string]string{
		"balance":     "lessons",
		"visits":      "lessons",
		"payments":    "lessons",
		"enrollments": "lessons",
		"trainers":    "lessons",
		"readings":    "meters",
		"tariffs":     "meters",
		"utilities":   "meters",
		"addresses":   "meters",
		"report":      "meters",
		"stats":       "stats",
	} {
		if got := spaceOf[tab]; got != want {
			t.Errorf("spaceOf[%q] = %q, want %q", tab, got, want)
		}
	}
}

// The shell's own screens belong to no world. They are not an oversight: a
// visit to the dentist is not part of the lessons domain, and neither is a
// chore, so nothing in the header should light up for them.
func TestShellScreensBelongToTheHub(t *testing.T) {
	for _, tab := range []string{"hub", "appointments", "reminders"} {
		if got := spaceOf[tab]; got != "" {
			t.Errorf("spaceOf[%q] = %q, want no world", tab, got)
		}
	}
}

// Every value in the map has to be one the template knows how to draw a
// second-tier nav for. A typo here renders a page with no navigation at all.
func TestNoTabPointsAtAnUnknownWorld(t *testing.T) {
	known := map[string]bool{"lessons": true, "meters": true, "stats": true}
	for tab, space := range spaceOf {
		if !known[space] {
			t.Errorf("tab %q claims unknown world %q", tab, space)
		}
	}
}
