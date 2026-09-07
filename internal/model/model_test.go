package model

import "testing"

func TestPlural(t *testing.T) {
	// Ukrainian needs three forms, and the teens are the trap: 11 and 12 take
	// the same form as 5, not the same as 1 and 2.
	cases := map[int]string{
		1: "1 заняття", 2: "2 заняття", 4: "4 заняття", 5: "5 занять",
		11: "11 занять", 12: "12 занять", 14: "14 занять",
		21: "21 заняття", 22: "22 заняття", 25: "25 занять",
		101: "101 заняття", 111: "111 занять", 0: "0 занять",
	}
	for n, want := range cases {
		if got := Plural(n, "заняття", "заняття", "занять"); got != want {
			t.Errorf("Plural(%d) = %q, want %q", n, got, want)
		}
	}
}

// The portal writes a dash run where a teacher left a field blank, and every
// screen that rendered it verbatim said nothing in a whole line.
func TestPortalTextTreatsDashRunsAsBlank(t *testing.T) {
	for _, blank := range []string{"", " ", "-", "---", "—", "–", " — ", "...", "·", "_"} {
		if got := PortalText(blank); got != "" {
			t.Errorf("PortalText(%q) = %q, want blank", blank, got)
		}
	}
	// Anything with a letter or a digit is content, punctuation included.
	for in, want := range map[string]string{
		"Дискримінант ":    "Дискримінант",
		"§12, впр. 3-5":    "§12, впр. 3-5",
		"с. 4 — 7":         "с. 4 — 7",
		"9":                "9",
		" Тема - підтема ": "Тема - підтема",
	} {
		if got := PortalText(in); got != want {
			t.Errorf("PortalText(%q) = %q, want %q", in, got, want)
		}
	}
}
