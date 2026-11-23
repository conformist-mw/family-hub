package model

import (
	"strings"
	"unicode"
)

// LogEntry is one line of the chronicle: something done on a day, in free
// text, with tags to find it again.
type LogEntry struct {
	ID        int64
	Date      string // YYYY-MM-DD
	Text      string
	Tags      []string // normalised, sorted
	CreatedAt string
}

// TagCount is a tag and how many entries carry it, for the filter chips.
type TagCount struct {
	Tag   string
	Count int
}

// ParseTags reads the tags field the way people type it: "генератор, газ",
// "#генератор #газ" and "Генератор газ" are the same two tags. A tag is one
// word — spaces separate tags rather than belong to one, which is what makes
// the hashtag spelling work — so a two-word idea is written with a hyphen.
// The result is lower case, deduplicated and in input order.
func ParseTags(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '#' || unicode.IsSpace(r)
	})
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		t := strings.ToLower(f)
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}
