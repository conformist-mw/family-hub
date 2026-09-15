package bot

import (
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/cooking"
	"familyhub/internal/dish"
	"familyhub/internal/mealie"
)

var kyiv = time.FixedZone("EEST", 3*3600)

func TestSlotFrom(t *testing.T) {
	tests := []struct {
		name      string
		modelSlot string
		hour      int
		want      cooking.Slot
	}{
		{"model read the hint", "vecheria", 12, cooking.SlotDinner},
		{"model read lunch", "obid", 20, cooking.SlotLunch},
		{"nothing said, midday", "", 12, cooking.SlotLunch},
		{"nothing said, evening", "", 19, cooking.SlotDinner},
		{"nothing said, late afternoon is dinner", "", 16, cooking.SlotDinner},
		{"a slot this house does not eat falls back to the clock", "snidanok", 9, cooking.SlotLunch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 9, 10, tt.hour, 0, 0, 0, kyiv)
			if got := slotFrom(tt.modelSlot, now); got != tt.want {
				t.Fatalf("slotFrom(%q, %d:00) = %q, want %q", tt.modelSlot, tt.hour, got, tt.want)
			}
		})
	}
}

func TestDateFrom(t *testing.T) {
	now := time.Date(2026, 9, 10, 13, 30, 0, 0, kyiv)
	today := time.Date(2026, 9, 10, 0, 0, 0, 0, kyiv)

	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{"empty means today", "", today},
		{"yesterday", "2026-09-09", today.AddDate(0, 0, -1)},
		{"garbage means today", "вчора", today},
		// A future date is always a misread: nobody photographs tomorrow's
		// dinner, and a wrong day entering the record silently is worse than
		// the cook tapping the day button.
		{"future falls back to today", "2026-09-11", today},
		{"a year off falls back to today", "2025-09-09", today},
		{"a week back is still allowed", "2026-09-03", today.AddDate(0, 0, -7)},
		{"older than a week is not", "2026-09-02", today},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dateFrom(tt.in, now, kyiv)
			if !got.Equal(tt.want) {
				t.Fatalf("dateFrom(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// The record answers "which day, which meal", so the hour is the meal's, not
// the moment the photo happened to be sent.
func TestCookedAtPinsCanonicalHour(t *testing.T) {
	day := time.Date(2026, 9, 10, 0, 0, 0, 0, kyiv)
	if got := cookedAt(day, cooking.SlotLunch); got.Hour() != 13 {
		t.Errorf("lunch at %d:00, want 13:00", got.Hour())
	}
	if got := cookedAt(day, cooking.SlotDinner); got.Hour() != 19 {
		t.Errorf("dinner at %d:00, want 19:00", got.Hour())
	}
	if got := cookedAt(day, cooking.SlotLunch); got.Location() != kyiv {
		t.Errorf("lost the location: %s", got.Location())
	}
}

func TestDayLabel(t *testing.T) {
	now := time.Date(2026, 9, 10, 21, 0, 0, 0, kyiv)
	day := func(d int) time.Time { return time.Date(2026, 9, 10+d, 0, 0, 0, 0, kyiv) }

	for _, tt := range []struct {
		date time.Time
		want string
	}{
		{day(0), "сьогодні"},
		{day(-1), "вчора"},
		{day(-2), "позавчора"},
		{day(-5), "05.09"},
	} {
		if got := dayLabel(tt.date, now); got != tt.want {
			t.Errorf("dayLabel(%s) = %q, want %q", tt.date.Format("02.01"), got, tt.want)
		}
	}
}

func TestSplitCommand(t *testing.T) {
	for _, tt := range []struct {
		in        string
		cmd, rest string
	}{
		{"/cooked драники на обед", "/cooked", "драники на обед"},
		{"/cooked@family_core_hub_bot борщ", "/cooked", "борщ"},
		{"/cooked", "/cooked", ""},
		{"просто підпис", "", "просто підпис"},
		{"", "", ""},
		{"/cooked\nборщ", "/cooked", "борщ"},
	} {
		cmd, rest := splitCommand(tt.in)
		if cmd != tt.cmd || rest != tt.rest {
			t.Errorf("splitCommand(%q) = (%q, %q), want (%q, %q)", tt.in, cmd, rest, tt.cmd, tt.rest)
		}
	}
}

// telebot joins callback arguments with "|", not the ":" the appointment
// buttons use — sharing the wrong splitter would silently mangle the key.
func TestSplitCookedData(t *testing.T) {
	key, arg := splitCookedData("2f|1")
	if key != "2f" || arg != "1" {
		t.Fatalf("got (%q, %q)", key, arg)
	}
	if key, arg := splitCookedData("2f"); key != "2f" || arg != "" {
		t.Fatalf("got (%q, %q)", key, arg)
	}
}

// Two taps on the same card must not write the meal down twice.
func TestCookedPendingClaimIsOnce(t *testing.T) {
	p := newCookedPending()
	now := time.Now()
	key := p.put(&cookedEntry{}, now)

	if _, ok := p.claim(key); !ok {
		t.Fatal("first claim must succeed")
	}
	if _, ok := p.claim(key); ok {
		t.Fatal("second claim must not")
	}
	// The entry survives the claim: the photo is still needed for the
	// "make it the main picture" button.
	if _, ok := p.get(key); !ok {
		t.Fatal("entry must outlive the claim")
	}
}

func TestCookedPendingEvictsStaleCards(t *testing.T) {
	p := newCookedPending()
	old := time.Now().Add(-2 * time.Hour)
	stale := p.put(&cookedEntry{}, old)
	p.put(&cookedEntry{}, time.Now()) // eviction runs on write

	if _, ok := p.get(stale); ok {
		t.Fatal("an hour-old card should be gone")
	}
}

// A failed write releases the card, so the retry button works instead of
// forcing the cook to send the photo again.
func TestCookedPendingReleaseAllowsRetry(t *testing.T) {
	p := newCookedPending()
	key := p.put(&cookedEntry{}, time.Now())

	if _, ok := p.claim(key); !ok {
		t.Fatal("first claim must succeed")
	}
	p.release(key)
	if _, ok := p.claim(key); !ok {
		t.Fatal("a released card must be claimable again")
	}
	if _, ok := p.claim(key); ok {
		t.Fatal("and only once after that")
	}
}

// plate builds a card the way recognise does: every dish the model read, in
// order, the first of them nominally the main one. A name with no slug is a
// dish the database does not have.
func plate(names ...string) *cookedEntry {
	e := &cookedEntry{main: -1, focus: -1, slot: cooking.SlotDinner, date: midnight(time.Now(), kyiv)}
	for _, n := range names {
		it := dish.Item{Name: n}
		if slug, ok := strings.CutPrefix(n, "!"); ok {
			it.Name = slug // "!Сирники" is a dish with no recipe behind it
		} else {
			it.Recipe = mealie.Recipe{ID: "id-" + n, Slug: n, Name: n}
		}
		e.items = append(e.items, plateItem{Item: it})
	}
	return e
}

func slugsOf(rs []mealie.Recipe) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Slug)
	}
	return out
}

func TestMainDishAndAlongside(t *testing.T) {
	t.Run("the first dish is the main one, the rest go with it", func(t *testing.T) {
		e := plate("guliash", "piure", "salat")
		if main, _ := e.mainDish(); main.Slug != "guliash" {
			t.Fatalf("main = %q", main.Slug)
		}
		if got := strings.Join(slugsOf(e.chosenAlongside()), ","); got != "piure,salat" {
			t.Fatalf("alongside = %q", got)
		}
	})

	// The meal is recorded against a recipe, and a dish nobody has added yet
	// is not one — so the entry lands on the first dish that is.
	t.Run("a dish that is not in the database cannot be the main one", func(t *testing.T) {
		e := plate("!Пшоняна каша", "guliash")
		if main, _ := e.mainDish(); main.Slug != "guliash" {
			t.Fatalf("main = %q", main.Slug)
		}
		if got := e.chosenAlongside(); len(got) != 0 {
			t.Fatalf("alongside = %v, want none", slugsOf(got))
		}
		if got := e.unknown(); len(got) != 1 || got[0] != 0 {
			t.Fatalf("unknown = %v, want the porridge", got)
		}
	})

	t.Run("the cook's pick wins", func(t *testing.T) {
		e := plate("guliash", "piure")
		e.main = 1
		if main, _ := e.mainDish(); main.Slug != "piure" {
			t.Fatalf("main = %q", main.Slug)
		}
		if got := strings.Join(slugsOf(e.chosenAlongside()), ","); got != "guliash" {
			t.Fatalf("alongside = %q", got)
		}
	})

	t.Run("a dish taken off the plate is not recorded at all", func(t *testing.T) {
		e := plate("guliash", "piure")
		e.items[1].dropped = true
		if got := e.chosenAlongside(); len(got) != 0 {
			t.Fatalf("alongside = %v, want none", slugsOf(got))
		}
		if got := e.unknown(); len(got) != 0 {
			t.Fatalf("unknown = %v, want none: it was taken off the plate", got)
		}
	})

	// Taking the chosen main off the plate must not leave the meal pointing at
	// a dish nobody ate.
	t.Run("the main falls back when it leaves the plate", func(t *testing.T) {
		e := plate("guliash", "piure")
		e.main = 0
		e.items[0].dropped = true
		if main, _ := e.mainDish(); main.Slug != "piure" {
			t.Fatalf("main = %q", main.Slug)
		}
	})

	t.Run("nothing in the database means nothing to confirm", func(t *testing.T) {
		e := plate("!Пшоняна каша")
		if _, ok := e.mainDish(); ok {
			t.Fatal("want no main dish")
		}
	})
}

// labels flattens a keyboard so a test can say what the card offers.
func labels(m *tele.ReplyMarkup) []string {
	var out []string
	for _, row := range m.InlineKeyboard {
		for _, btn := range row {
			out = append(out, btn.Text)
		}
	}
	return out
}

func hasLabel(m *tele.ReplyMarkup, want string) bool {
	for _, l := range labels(m) {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func cookBot() *Bot { return &Bot{cfg: Config{Loc: kyiv}} }

func TestCookedCardReadsThePlateAsAList(t *testing.T) {
	b := cookBot()
	e := plate("Відбивні", "!Пшоняна каша", "Салат з баклажанів")
	e.note = "На тарілці смажене м'ясо, каша та салат."

	text, markup := b.cookedCard("k", e)
	t.Log("\n" + text + "\n" + strings.Join(labels(markup), " | "))

	for _, want := range []string{
		"1. <b>Відбивні</b>",                    // the dish the meal is recorded against
		"2. Пшоняна каша — <i>немає в базі</i>", // and the one still waiting for a decision
		"3. Салат з баклажанів",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("card missing %q:\n%s", want, text)
		}
	}
	// The dish the database lacks is one tap from being added, and the
	// confirmation says out loud that it is not included yet.
	if !hasLabel(markup, "➕ Створити «Пшоняна каша»") {
		t.Errorf("no way to add the missing dish: %v", labels(markup))
	}
	if !hasLabel(markup, "✓ Зафіксувати без нових") {
		t.Errorf("confirmation must admit what it leaves out: %v", labels(markup))
	}
	// One button per dish: every correction is about one dish at a time.
	for _, want := range []string{"1. Відбивні", "2. Пшоняна каша", "3. Салат"} {
		if !hasLabel(markup, want) {
			t.Errorf("no button for %q: %v", want, labels(markup))
		}
	}
}

func TestCookedCardConfirmsPlainlyWhenEverythingIsKnown(t *testing.T) {
	b := cookBot()
	text, markup := b.cookedCard("k", plate("Гуляш", "Пюре"))
	if !hasLabel(markup, "✓ Зафіксувати") || hasLabel(markup, "без нових") {
		t.Errorf("labels = %v", labels(markup))
	}
	if hasLabel(markup, "➕ Створити") {
		t.Errorf("nothing to create: %v", labels(markup))
	}
	if strings.Contains(text, "немає в базі") {
		t.Errorf("card = %q", text)
	}
}

// With nothing the database knows, there is nothing to confirm yet — only the
// dish to add.
func TestCookedCardWithoutAKnownDishOffersOnlyTheCreate(t *testing.T) {
	b := cookBot()
	_, markup := b.cookedCard("k", plate("!Сирники"))
	if hasLabel(markup, "Зафіксувати") {
		t.Errorf("nothing to record against: %v", labels(markup))
	}
	if !hasLabel(markup, "➕ Створити «Сирники»") {
		t.Errorf("labels = %v", labels(markup))
	}
}

func TestCookedItemCardAsksAboutOneDish(t *testing.T) {
	b := cookBot()
	e := plate("Відбивні", "Салат")
	e.items[0].Alts = []mealie.Recipe{{Slug: "guliash", Name: "Гуляш"}}
	e.focus = 0

	text, markup := b.cookedCard("k", e)
	t.Log("\n" + text + "\n" + strings.Join(labels(markup), " | "))
	if !strings.Contains(text, "Страва 1 з 2") {
		t.Errorf("card = %q", text)
	}
	// The alternative belongs to this dish, not to the meal: it is offered
	// here and nowhere else.
	if !hasLabel(markup, "↔ Це Гуляш") {
		t.Errorf("labels = %v", labels(markup))
	}
	if !hasLabel(markup, "🗑") || !hasLabel(markup, "← Назад") {
		t.Errorf("labels = %v", labels(markup))
	}
	// It is already the main dish, so there is nothing to promote.
	if hasLabel(markup, "⭐") {
		t.Errorf("the main dish cannot be made the main dish: %v", labels(markup))
	}

	e.focus = 1
	_, markup = b.cookedCard("k", e)
	if !hasLabel(markup, "⭐ Зробити головною") {
		t.Errorf("labels = %v", labels(markup))
	}
}

// A dish the cook never added is a dish that was not written down. The card
// says so while it is still fresh enough to fix.
func TestCookedDoneNamesWhatWasLeftOut(t *testing.T) {
	b := cookBot()
	e := plate("Гуляш", "!Пшоняна каша")
	e.recipe = e.items[0].Recipe
	got := b.cookedDone(e, cooking.Result{})
	if !strings.Contains(got, "не записано (немає в базі): Пшоняна каша") {
		t.Errorf("done card = %q", got)
	}
}

func TestShorten(t *testing.T) {
	if got := shorten("Салат", 18); got != "Салат" {
		t.Errorf("got %q", got)
	}
	got := shorten("Салат із смажених баклажанів", 18)
	if len([]rune(got)) != 18 || !strings.HasSuffix(got, "…") {
		t.Errorf("got %q (%d runes)", got, len([]rune(got)))
	}
}

// A match the model was unsure of is marked, and the dish it could not place
// at all is offered as a new one with the near misses behind it.
func TestCookedCardShowsDoubt(t *testing.T) {
	b := cookBot()
	e := plate("Відбивні", "!Смажене м'ясо з цибулею")
	e.items[0].Confidence = "low"
	e.items[1].Alts = []mealie.Recipe{{Slug: "guliash", Name: "Гуляш"}}

	text, _ := b.cookedCard("k", e)
	if !strings.Contains(text, "1. <b>Відбивні</b> <i>(не точно)</i>") {
		t.Errorf("an uncertain match must say so:\n%s", text)
	}

	e.focus = 1
	_, markup := b.cookedCard("k", e)
	if !hasLabel(markup, "➕ Створити «Смажене м'ясо з цибулею»") || !hasLabel(markup, "↔ Це Гуляш") {
		t.Errorf("labels = %v", labels(markup))
	}
}

// A confident match carries no marker: the note would stop meaning anything if
// every line had it.
func TestCookedCardKeepsAConfidentMatchPlain(t *testing.T) {
	b := cookBot()
	e := plate("Борщ")
	e.items[0].Confidence = "high"
	if text, _ := b.cookedCard("k", e); strings.Contains(text, "не точно") {
		t.Errorf("card = %q", text)
	}
}
