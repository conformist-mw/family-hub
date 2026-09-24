package bot

import (
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/dish"
	"familyhub/internal/menu"
	"familyhub/internal/model"
)

var kyiv = time.FixedZone("EEST", 3*3600)

func TestMealFrom(t *testing.T) {
	tests := []struct {
		name      string
		modelSlot string
		hour      int
		want      menu.Meal
	}{
		{"model read the hint", "dinner", 12, menu.Dinner},
		{"model read lunch", "lunch", 20, menu.Lunch},
		{"nothing said, midday", "", 12, menu.Lunch},
		{"nothing said, evening", "", 19, menu.Dinner},
		{"nothing said, late afternoon is dinner", "", 16, menu.Dinner},
		{"a meal this house does not eat falls back to the clock", "breakfast", 9, menu.Lunch},
		// The Mealie meal names are not meals of the journal.
		{"an old Mealie name falls back to the clock", "vecheria", 12, menu.Lunch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 9, 10, tt.hour, 0, 0, 0, kyiv)
			if got := mealFrom(tt.modelSlot, now); got != tt.want {
				t.Fatalf("mealFrom(%q, %d:00) = %q, want %q", tt.modelSlot, tt.hour, got, tt.want)
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
	// The entry survives the claim, so a late tap finds it recorded rather
	// than expired.
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
// order. A name prefixed with "!" is a dish the catalogue does not have; the
// rest get made-up ids, which is all a card that is only rendered needs.
func plate(names ...string) *cookedEntry {
	e := &cookedEntry{focus: -1, meal: menu.Dinner, date: midnight(time.Now(), kyiv)}
	for i, n := range names {
		it := dish.Item{Name: n, Meal: model.DishMealAny, Days: model.DishDaysAny}
		if name, ok := strings.CutPrefix(n, "!"); ok {
			it.Name = name
		} else {
			it.Dish = dish.DishRef{ID: int64(100 + i), Name: n}
		}
		e.items = append(e.items, plateItem{Item: it})
	}
	return e
}

func namesOf(items []plateItem) string {
	var out []string
	for _, it := range items {
		out = append(out, it.title())
	}
	return strings.Join(out, ",")
}

func TestChosenAndUnknown(t *testing.T) {
	t.Run("every known dish on the plate is written down", func(t *testing.T) {
		e := plate("Гуляш", "Пюре", "Салат")
		if got := namesOf(e.chosen()); got != "Гуляш,Пюре,Салат" {
			t.Fatalf("chosen = %q", got)
		}
	})

	t.Run("a dish that is not in the catalogue waits for a decision", func(t *testing.T) {
		e := plate("!Пшоняна каша", "Гуляш")
		if got := namesOf(e.chosen()); got != "Гуляш" {
			t.Fatalf("chosen = %q", got)
		}
		if got := e.unknown(); len(got) != 1 || got[0] != 0 {
			t.Fatalf("unknown = %v, want the porridge", got)
		}
	})

	t.Run("a dish taken off the plate is not recorded at all", func(t *testing.T) {
		e := plate("Гуляш", "!Пюре", "Салат")
		e.items[1].dropped = true
		e.items[2].dropped = true
		if got := namesOf(e.chosen()); got != "Гуляш" {
			t.Fatalf("chosen = %q", got)
		}
		if got := e.unknown(); len(got) != 0 {
			t.Fatalf("unknown = %v, want none: it was taken off the plate", got)
		}
	})

	t.Run("nothing in the catalogue means nothing to confirm", func(t *testing.T) {
		if got := plate("!Пшоняна каша").chosen(); len(got) != 0 {
			t.Fatalf("chosen = %q", namesOf(got))
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
		"1. Відбивні",
		"2. Пшоняна каша — <i>немає в базі</i>", // the one still waiting for a decision
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
	e.items[0].Alts = []dish.DishRef{{ID: 7, Name: "Гуляш"}}
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
	// Every dish is written down alike, so there is no main one to pick.
	if hasLabel(markup, "⭐") {
		t.Errorf("no main dish to choose: %v", labels(markup))
	}
}

// A dish the cook never added is a dish that was not written down. The card
// says so while it is still fresh enough to fix.
func TestCookedDoneNamesWhatWasLeftOut(t *testing.T) {
	b := cookBot()
	e := plate("Гуляш", "!Пшоняна каша")
	got := b.cookedDone(e, plateWrite{written: []string{"Гуляш"}})
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
	e.items[1].Alts = []dish.DishRef{{ID: 7, Name: "Гуляш"}}

	text, _ := b.cookedCard("k", e)
	if !strings.Contains(text, "1. Відбивні <i>(не точно)</i>") {
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

// storedPlate is a card whose known dishes are real rows of a migrated store,
// for the tests that write the plate down. "!" marks a dish the catalogue
// does not have, as in plate.
func storedPlate(t *testing.T, b *Bot, date time.Time, meal menu.Meal, names ...string) *cookedEntry {
	t.Helper()
	e := &cookedEntry{focus: -1, meal: meal, date: date, cook: "Олег"}
	for _, n := range names {
		it := dish.Item{Name: n, Meal: model.DishMealAny, Days: model.DishDaysAny}
		if name, ok := strings.CutPrefix(n, "!"); ok {
			it.Name = name
		} else {
			d := ensureDish(t, b, n)
			it.Dish = dish.DishRef{ID: d.ID, Name: d.Name}
		}
		e.items = append(e.items, plateItem{Item: it})
	}
	return e
}

func ensureDish(t *testing.T, b *Bot, name string) model.Dish {
	t.Helper()
	d, _, err := b.store.CreateDish(model.Dish{Name: name})
	if err != nil {
		t.Fatalf("create dish %q: %v", name, err)
	}
	return d
}

// mealRows flattens a day of the journal to "meal:dish:status" for comparing.
func mealRows(t *testing.T, b *Bot, date string) string {
	t.Helper()
	rows, err := b.store.MealsOn(date)
	if err != nil {
		t.Fatalf("meals on %s: %v", date, err)
	}
	var out []string
	for _, m := range rows {
		out = append(out, m.Meal+":"+m.Dish+":"+m.Status)
	}
	return strings.Join(out, ",")
}

var plateDay = time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

const plateDate = "2026-09-24"

func TestRecordPlateWritesEveryDish(t *testing.T) {
	b := menuBot(t)
	e := storedPlate(t, b, plateDay, menu.Lunch, "Гуляш", "Пюре", "!Пшоняна каша")

	w, err := b.recordPlate(e)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if strings.Join(w.written, ",") != "Гуляш,Пюре" || len(w.already) != 0 {
		t.Fatalf("write = %+v", w)
	}
	if got := mealRows(t, b, plateDate); got != "lunch:Гуляш:eaten,lunch:Пюре:eaten" {
		t.Fatalf("journal = %q", got)
	}
	rows, _ := b.store.MealsOn(plateDate)
	if rows[0].Who != "Олег" {
		t.Errorf("who = %q, want the cook", rows[0].Who)
	}
	// The dish nobody created is not in the journal, and the card says so.
	done := b.cookedDone(e, w)
	if !strings.Contains(done, "✓ <b>Гуляш + Пюре</b> · 24.09 · обід") ||
		!strings.Contains(done, "не записано (немає в базі): Пшоняна каша") {
		t.Errorf("done card = %q", done)
	}
}

// The photo is the answer to the morning's plan: the planned dish on the plate
// becomes eaten, and a plan that did not happen is dropped.
func TestRecordPlateClosesThePlan(t *testing.T) {
	b := menuBot(t)
	guliash := ensureDish(t, b, "Гуляш")
	plov := ensureDish(t, b, "Плов")

	t.Run("the planned dish on the plate", func(t *testing.T) {
		planned, err := b.store.PlanMeal(guliash.ID, plateDate, model.MealLunch, "Ліна", false)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if _, err := b.recordPlate(storedPlate(t, b, plateDay, menu.Lunch, "Гуляш", "Пюре")); err != nil {
			t.Fatalf("record: %v", err)
		}
		if got := mealRows(t, b, plateDate); got != "lunch:Гуляш:eaten,lunch:Пюре:eaten" {
			t.Fatalf("journal = %q", got)
		}
		rows, _ := b.store.MealsOn(plateDate)
		if rows[0].ID != planned.ID {
			t.Errorf("the plan row should turn eaten in place, got a new row")
		}
	})

	t.Run("a plan for a dish that is not on the plate", func(t *testing.T) {
		if _, err := b.store.PlanMeal(plov.ID, plateDate, model.MealDinner, "Ліна", false); err != nil {
			t.Fatalf("plan: %v", err)
		}
		if _, err := b.recordPlate(storedPlate(t, b, plateDay, menu.Dinner, "Гуляш")); err != nil {
			t.Fatalf("record: %v", err)
		}
		got := mealRows(t, b, plateDate)
		if strings.Contains(got, "Плов") || !strings.Contains(got, "dinner:Гуляш:eaten") {
			t.Fatalf("journal = %q", got)
		}
	})
}

// The same plate sent twice — a photo and then a /cooked, or the evening
// answer first — is written once, and the card says it was already there.
func TestRecordPlateTwiceIsAlreadyRecorded(t *testing.T) {
	b := menuBot(t)
	e := storedPlate(t, b, plateDay, menu.Dinner, "Борщ", "Пампушки")
	if _, err := b.recordPlate(e); err != nil {
		t.Fatalf("first: %v", err)
	}

	again := storedPlate(t, b, plateDay, menu.Dinner, "Борщ", "Пампушки")
	w, err := b.recordPlate(again)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(w.written) != 0 || strings.Join(w.already, ",") != "Борщ,Пампушки" {
		t.Fatalf("write = %+v", w)
	}
	if got := mealRows(t, b, plateDate); got != "dinner:Борщ:eaten,dinner:Пампушки:eaten" {
		t.Fatalf("journal = %q", got)
	}
	if done := b.cookedDone(again, w); !strings.Contains(done, "— вже було записано") {
		t.Errorf("done card = %q", done)
	}

	// Half new, half old: the new one is written and the old one named.
	mixed := storedPlate(t, b, plateDay, menu.Dinner, "Борщ", "Сало")
	w, err = b.recordPlate(mixed)
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	done := b.cookedDone(mixed, w)
	if !strings.Contains(done, "✓ <b>Сало</b>") || !strings.Contains(done, "вже було записано: Борщ") {
		t.Errorf("done card = %q", done)
	}
}

// Creating a dish from the card adds it to the catalogue with what the model
// read about it, and writes nothing to the journal: that is the confirmation's
// job.
func TestCreatePlateDishAddsToTheCatalogueOnly(t *testing.T) {
	b := menuBot(t)
	e := storedPlate(t, b, plateDay, menu.Dinner, "!Піца")
	e.items[0].Meal = model.MealDinner
	e.items[0].Days = model.DishDaysWeekend

	if err := b.createPlateDish(e, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	it := e.items[0]
	if !it.Known() || it.Dish.Name != "Піца" {
		t.Fatalf("item = %+v, want it known now", it)
	}
	d, err := b.store.Dish(it.Dish.ID)
	if err != nil {
		t.Fatalf("dish: %v", err)
	}
	if d.Status != model.DishActive || d.Meal != model.MealDinner || d.Days != model.DishDaysWeekend {
		t.Errorf("dish = %+v", d)
	}
	if got := mealRows(t, b, plateDate); got != "" {
		t.Errorf("journal = %q, want nothing before the confirmation", got)
	}
	// A second tap on the same button is a no-op, not a second dish.
	if err := b.createPlateDish(e, 0); err != nil {
		t.Fatalf("second create: %v", err)
	}
	if all, _ := b.store.Dishes(); len(all) != 1 {
		t.Errorf("dishes = %d, want 1", len(all))
	}
}

// A dish the family once turned down is not on the list the model sees; when
// it turns up on a plate anyway, creating it brings the same row back.
func TestCreatePlateDishRevivesARejectedDish(t *testing.T) {
	b := menuBot(t)
	rejected, _, err := b.store.CreateDish(model.Dish{Name: "Піца", Status: model.DishRejected})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	proposed, _, err := b.store.CreateDish(model.Dish{Name: "Рататуй", Status: model.DishProposed})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	catalogue, err := b.plateCatalogue()
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	if len(catalogue) != 1 || catalogue[0].ID != proposed.ID {
		t.Fatalf("catalogue = %+v, want only the proposal", catalogue)
	}

	e := storedPlate(t, b, plateDay, menu.Dinner, "!піца ")
	if err := b.createPlateDish(e, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	if e.items[0].Dish.ID != rejected.ID {
		t.Fatalf("got dish %d, want the rejected row %d back", e.items[0].Dish.ID, rejected.ID)
	}
	if d, _ := b.store.Dish(rejected.ID); d.Status != model.DishActive {
		t.Errorf("status = %q, want active", d.Status)
	}
}
