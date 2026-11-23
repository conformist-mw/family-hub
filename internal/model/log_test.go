package model

import (
	"slices"
	"testing"
)

// People type tags the way they type them elsewhere — with commas, with
// hashes, capitalised — and all of those have to land on the same tag, or the
// filter splits one subject into three.
func TestParseTags(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"генератор", []string{"генератор"}},
		{"Генератор, газ", []string{"генератор", "газ"}},
		{"#генератор #газ", []string{"генератор", "газ"}},
		{"генератор  газ\tмасло", []string{"генератор", "газ", "масло"}},
		{"газ; ГАЗ #газ", []string{"газ"}},
		{"заміна-масла", []string{"заміна-масла"}},
	}
	for _, c := range cases {
		if got := ParseTags(c.in); !slices.Equal(got, c.want) {
			t.Errorf("ParseTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
