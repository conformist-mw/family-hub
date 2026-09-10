package actor

import "testing"

func TestResolveNamesTheAuthorBehindЯ(t *testing.T) {
	for _, tc := range []struct {
		name       string
		person, by string
		want       string
	}{
		{"the capitalised form the keyboard produces", "Я", "Оксана", "Оксана"},
		{"typed in lower case", "я", "Оксана", "Оксана"},
		{"padded by the form", "  Я  ", "Оксана", "Оксана"},
		{"the Russian accusative the bot already knew", "мне", "Оксана", "Оксана"},
		{"the Ukrainian accusative", "Мене", "Оксана", "Оксана"},
		{"the reflexive", "собі", "Оксана", "Оксана"},

		{"somebody else is left alone", "Демид", "Оксана", "Демид"},
		{"a name that merely contains я", "Настя", "Оксана", "Настя"},
		{"an empty person stays empty", "", "Оксана", ""},

		// Attributing the row to the wrong person is worse than leaving a bad
		// value a person can see and fix.
		{"no author to resolve against", "Я", "", "Я"},
		{"only the surface placeholder", "Я", Unknown, "Я"},
		{"an author of pure whitespace", "Я", "   ", "Я"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.person, tc.by); got != tc.want {
				t.Fatalf("Resolve(%q, %q) = %q, want %q", tc.person, tc.by, got, tc.want)
			}
		})
	}
}

// The bot has resolved these against the message sender for far longer than
// the Mini App has existed. Both surfaces read the same list now, so a form
// and a captured message cannot disagree about who "мене" is.
func TestIsSelfCoversWhatTheBotAlwaysAccepted(t *testing.T) {
	for _, s := range []string{"я", "Я", "мене", "мне", "себе", "собі", " Я "} {
		if !IsSelf(s) {
			t.Errorf("IsSelf(%q) = false", s)
		}
	}
	// Empty is deliberately not self here: the bot treats a message that
	// names nobody as the sender, but an empty form field just means it was
	// not filled in.
	for _, s := range []string{"", "Демид", "Настя", "мама"} {
		if IsSelf(s) {
			t.Errorf("IsSelf(%q) = true", s)
		}
	}
}

func TestParseRoster(t *testing.T) {
	r := ParseRoster(" 111:Олег, 222:Аня ")
	if r.Name(111, "conformíst") != "Олег" {
		t.Errorf("111 = %q", r.Name(111, "conformíst"))
	}
	if r.Name(222, "x") != "Аня" {
		t.Errorf("222 = %q", r.Name(222, "x"))
	}
	// Anyone unlisted keeps whatever Telegram calls them: a guest in the
	// group is better named badly than not at all.
	if got := r.Name(999, "Гість"); got != "Гість" {
		t.Errorf("unlisted = %q, want the display name", got)
	}
}

// A typo in the roster must not stop the app from starting: it is a
// convenience over the display name, not a source of truth.
func TestParseRosterSkipsJunk(t *testing.T) {
	r := ParseRoster("notanid:X,,333:,:Y,444:Оля,555")
	if len(r) != 1 {
		t.Fatalf("roster = %v, want only the one good entry", r)
	}
	if r.Name(444, "") != "Оля" {
		t.Fatalf("444 = %q", r.Name(444, ""))
	}
}

func TestEmptyRosterNamesNobody(t *testing.T) {
	if got := ParseRoster("").Name(111, "conformíst"); got != "conformíst" {
		t.Fatalf("got %q, want the display name", got)
	}
}
