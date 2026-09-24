package bot

import (
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/db"
	"familyhub/internal/dish"
	"familyhub/internal/menu"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// menuBot is a bot over a real, migrated SQLite with no Telegram behind it:
// buildMenu and applyMenuTap only touch the store, and menuView does not add
// the app button, so nothing reaches for telebot.
func menuBot(t *testing.T) *Bot {
	t.Helper()
	return menuBotAt(t, filepath.Join(t.TempDir(), "test.db"))
}

// menuBotAt opens a bot over the database at path — a second one over the
// same file is the bot after a restart.
func menuBotAt(t *testing.T, path string) *Bot {
	t.Helper()
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &Bot{
		cfg:    Config{Loc: time.UTC},
		store:  store.New(database),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func seedMenuDishes(t *testing.T, b *Bot, n int) []model.Dish {
	t.Helper()
	out := make([]model.Dish, 0, n)
	for i := range n {
		d, _, err := b.store.CreateDish(model.Dish{Name: fmt.Sprintf("Страва %02d", i+1)})
		if err != nil {
			t.Fatalf("create dish: %v", err)
		}
		out = append(out, d)
	}
	return out
}

func fixedRand() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

// A Thursday: no weekend dishes, and the day before is an ordinary day too.
var menuMorning = time.Date(2026, 9, 24, 7, 0, 0, 0, time.UTC)

const (
	menuToday     = "2026-09-24"
	menuYesterday = "2026-09-23"
)

func sampleMenu() menuState {
	return menuState{
		date:      menuToday,
		leftovers: []menuDish{{id: 1, name: "Борщ"}},
		offered: map[menu.Meal][]menuDish{
			menu.Lunch:  {{id: 2, name: "Плов"}, {id: 3, name: "Деруни"}, {id: 4, name: "Солянка", proposed: true}},
			menu.Dinner: {{id: 5, name: "Відбивні"}, {id: 6, name: "Вареники"}, {id: 7, name: "Удон"}},
		},
	}
}

func buttonTexts(m *tele.ReplyMarkup) [][]string {
	var out [][]string
	for _, row := range m.InlineKeyboard {
		var texts []string
		for _, btn := range row {
			texts = append(texts, btn.Text)
		}
		out = append(out, texts)
	}
	return out
}

func TestMenuViewLayout(t *testing.T) {
	text, markup := menuView(sampleMenu())

	for _, want := range []string{
		"Що приготувати сьогодні",
		"Доїдаємо: Борщ",
		"Обід: Плов · Деруни · 🆕 Солянка",
		"Вечеря: Відбивні · Вареники · Удон",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "✅") {
		t.Errorf("nothing is chosen yet, but the text marks something:\n%s", text)
	}

	want := [][]string{
		{"↩ Борщ · обід", "↩ Борщ · вечеря"},
		{"Плов", "Деруни", "🆕 Солянка"},
		{"🔀 обід"},
		{"Відбивні", "Вареники", "Удон"},
		{"🔀 вечеря"},
	}
	if got := buttonTexts(markup); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keyboard = %v\nwant       %v", got, want)
	}
}

func TestMenuViewWithoutLeftovers(t *testing.T) {
	s := sampleMenu()
	s.leftovers = nil
	text, markup := menuView(s)
	if strings.Contains(text, "Доїдаємо") {
		t.Errorf("no leftovers, yet the text offers some:\n%s", text)
	}
	if first := buttonTexts(markup)[0]; first[0] != "Плов" {
		t.Fatalf("first row = %v, want lunch", first)
	}
}

func TestMenuViewMarksTheChoice(t *testing.T) {
	s := sampleMenu()
	s.chosen = []model.MealEntry{
		{DishID: 3, Dish: "Деруни", Meal: model.MealLunch, Who: "Олег", Status: model.MealPlanned},
		{DishID: 1, Dish: "Борщ", Meal: model.MealDinner, Who: "Аня", Status: model.MealPlanned, Leftover: true},
	}
	text, markup := menuView(s)

	for _, want := range []string{"✅ Обід: Деруни — Олег", "✅ Вечеря: Борщ (доїдаємо) — Аня"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	rows := buttonTexts(markup)
	if rows[0][0] != "↩ Борщ · обід" || rows[0][1] != "✅ ↩ Борщ · вечеря" {
		t.Errorf("leftover row = %v, want only the dinner button marked", rows[0])
	}
	if rows[1][1] != "✅ Деруни" || rows[1][0] != "Плов" {
		t.Errorf("lunch row = %v, want only Деруни marked", rows[1])
	}
	for _, label := range rows[3] {
		if strings.HasPrefix(label, "✅") {
			t.Errorf("dinner row marks %q; the dinner choice is the leftover", label)
		}
	}
}

// A meal nothing fits leaves no label, no empty row and no 🔀 that could only
// ever answer "nothing more".
func TestMenuViewSkipsAnEmptyMeal(t *testing.T) {
	s := sampleMenu()
	s.leftovers = nil
	s.offered[menu.Lunch] = nil
	text, markup := menuView(s)
	if strings.Contains(text, "Обід:") {
		t.Errorf("text lists an empty lunch:\n%s", text)
	}
	want := [][]string{{"Відбивні", "Вареники", "Удон"}, {"🔀 вечеря"}}
	if got := buttonTexts(markup); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keyboard = %v, want %v", got, want)
	}
}

func TestMenuViewEscapesNames(t *testing.T) {
	s := sampleMenu()
	s.offered[menu.Lunch][0].name = "Мак & <сир>"
	text, _ := menuView(s)
	if !strings.Contains(text, "Мак &amp; &lt;сир&gt;") {
		t.Fatalf("name not escaped for HTML:\n%s", text)
	}
}

// The keyboard is the message's memory: what comes back from Telegram, with
// the app row under it, must read as the same menu.
func TestMenuKeyboardRoundTrips(t *testing.T) {
	_, markup := menuView(sampleMenu())
	back := asTelegramSentIt(markup)
	back.InlineKeyboard = append(back.InlineKeyboard,
		[]tele.InlineButton{{Text: appButtonLabel, URL: "https://example.test"}})

	refs := menuRefsFromMarkup(back)
	if refs.date != menuToday {
		t.Errorf("date = %q", refs.date)
	}
	if !slices.Equal(refs.leftovers, []int64{1}) {
		t.Errorf("leftovers = %v, want [1] once despite two buttons", refs.leftovers)
	}
	if !slices.Equal(refs.offered[menu.Lunch], []int64{2, 3, 4}) ||
		!slices.Equal(refs.offered[menu.Dinner], []int64{5, 6, 7}) {
		t.Errorf("offered = %v", refs.offered)
	}
	// Our own outgoing markup reads the same, so a redraw built from either
	// shape agrees.
	if own := menuRefsFromMarkup(markup); fmt.Sprint(own) != fmt.Sprint(refs) {
		t.Errorf("outgoing %v ≠ echoed %v", own, refs)
	}
	for _, row := range back.InlineKeyboard[:len(back.InlineKeyboard)-1] {
		for _, btn := range row {
			if len(btn.Data) > 64 {
				t.Errorf("callback data %q is over Telegram's 64 bytes", btn.Data)
			}
		}
	}
}

func TestBuildMenuOffersBothMeals(t *testing.T) {
	b := menuBot(t)
	seedMenuDishes(t, b, 12)

	st, ok, err := b.buildMenu(menuMorning, fixedRand())
	if err != nil || !ok {
		t.Fatalf("build: ok=%v err=%v", ok, err)
	}
	if st.date != menuToday {
		t.Errorf("date = %q", st.date)
	}
	lunch, dinner := st.offered[menu.Lunch], st.offered[menu.Dinner]
	if len(lunch) != menuOptions || len(dinner) != menuOptions {
		t.Fatalf("offered %d lunch, %d dinner", len(lunch), len(dinner))
	}
	for _, d := range dinner {
		if slices.ContainsFunc(lunch, func(l menuDish) bool { return l.id == d.id }) {
			t.Errorf("%s offered at both meals", d.name)
		}
	}
	if len(st.leftovers) != 0 {
		t.Errorf("leftovers %v with nothing eaten yesterday", st.leftovers)
	}
}

func TestBuildMenuLeftoversAndYesterdaysShown(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 15)
	borshch := dishes[0]
	for _, meal := range []string{model.MealLunch, model.MealDinner} {
		if _, _, err := b.store.RecordEaten(borshch.ID, menuYesterday, meal, "Олег", false); err != nil {
			t.Fatalf("eaten: %v", err)
		}
	}
	// Every dish is good for either meal, so what yesterday showed at lunch
	// has been seen by dinner as well, and the other way round.
	shownYesterday := []int64{dishes[1].ID, dishes[2].ID, dishes[3].ID, dishes[4].ID, dishes[5].ID}
	if err := b.store.SaveMenuMessage(model.MenuMessage{Date: menuYesterday, ChatID: -100, MessageID: 1,
		Shown: map[string][]int64{
			model.MealLunch:  shownYesterday[:3],
			model.MealDinner: shownYesterday[3:],
		}}); err != nil {
		t.Fatalf("save yesterday: %v", err)
	}

	for seed := range uint64(50) {
		st, ok, err := b.buildMenu(menuMorning, rand.New(rand.NewPCG(seed, 13)))
		if err != nil || !ok {
			t.Fatalf("seed %d: build: ok=%v err=%v", seed, ok, err)
		}
		if len(st.leftovers) != 1 || st.leftovers[0].id != borshch.ID {
			t.Fatalf("seed %d: leftovers = %v, want the borshch once", seed, st.leftovers)
		}
		for _, meal := range menuMeals {
			for _, d := range st.offered[meal] {
				if slices.Contains(shownYesterday, d.id) {
					t.Errorf("seed %d: %s was shown yesterday and offered again for %s", seed, d.name, meal)
				}
				// Yesterday's pot is on the message already, as a leftover.
				if d.id == borshch.ID {
					t.Errorf("seed %d: the leftover is also offered fresh for %s", seed, meal)
				}
			}
		}
	}
}

// A dish offered and passed over has been looked at: two days on, it does
// not outrank the dishes the menu has never offered.
func TestBuildMenuRanksAShownDishAsSeen(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 12)
	passedOver := []int64{dishes[0].ID, dishes[1].ID, dishes[2].ID}
	if err := b.store.SaveMenuMessage(model.MenuMessage{Date: "2026-09-22", ChatID: -100, MessageID: 1,
		Shown: map[string][]int64{model.MealLunch: passedOver}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Nine dishes never offered fill both meals' windows, so neither meal
	// should reach for the three passed over.
	for seed := range uint64(50) {
		st, ok, err := b.buildMenu(menuMorning, rand.New(rand.NewPCG(seed, 17)))
		if err != nil || !ok {
			t.Fatalf("seed %d: build: ok=%v err=%v", seed, ok, err)
		}
		for _, meal := range menuMeals {
			for _, d := range st.offered[meal] {
				if slices.Contains(passedOver, d.id) {
					t.Fatalf("seed %d: %s offers %s, passed over two days ago, ahead of dishes never offered", seed, meal, d.name)
				}
			}
		}
	}
}

func TestBuildMenuTwiceADayIsSilent(t *testing.T) {
	b := menuBot(t)
	seedMenuDishes(t, b, 6)
	if err := b.store.SaveMenuMessage(model.MenuMessage{Date: menuToday, ChatID: -100, MessageID: 1}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok, err := b.buildMenu(menuMorning, fixedRand()); ok || err != nil {
		t.Fatalf("second menu of the day: ok=%v err=%v, want silence", ok, err)
	}
}

func TestBuildMenuWithAnEmptyCatalogue(t *testing.T) {
	b := menuBot(t)
	if _, ok, err := b.buildMenu(menuMorning, fixedRand()); ok || err != nil {
		t.Fatalf("empty catalogue: ok=%v err=%v, want nothing to send", ok, err)
	}
	// Only rejected dishes is the same as none.
	if _, _, err := b.store.CreateDish(model.Dish{Name: "Печінка", Status: model.DishRejected}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := b.buildMenu(menuMorning, fixedRand()); ok {
		t.Fatal("built a menu out of a rejected dish")
	}
}

// sentMenu builds and "sends" today's menu the way sendMenu does, minus
// Telegram: the shown set is saved and the keyboard comes back echoed.
func sentMenu(t *testing.T, b *Bot) (menuState, *tele.ReplyMarkup) {
	t.Helper()
	st, ok, err := b.buildMenu(menuMorning, fixedRand())
	if err != nil || !ok {
		t.Fatalf("build: ok=%v err=%v", ok, err)
	}
	if err := b.store.SaveMenuMessage(model.MenuMessage{Date: menuToday, ChatID: -100, MessageID: 1, Shown: menuShown(st)}); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, markup := menuView(st)
	return st, asTelegramSentIt(markup)
}

func tapData(date string, meal menu.Meal, id int64) string {
	return fmt.Sprintf("%s|%s|%d", date, meal, id)
}

func TestMenuPickPlansAndReplaces(t *testing.T) {
	b := menuBot(t)
	seedMenuDishes(t, b, 12)
	st, kb := sentMenu(t, b)
	first, second := st.offered[menu.Lunch][0], st.offered[menu.Lunch][1]

	got, toast, redraw, err := b.applyMenuTap(menuMorning, menuPickUnique,
		tapData(menuToday, menu.Lunch, first.id), "Олег", kb, fixedRand())
	if err != nil || !redraw {
		t.Fatalf("pick: redraw=%v err=%v", redraw, err)
	}
	if toast != "Обід: "+first.name {
		t.Errorf("toast = %q", toast)
	}
	rows := dayMeals(t, b, menuToday)
	if len(rows) != 1 || rows[0].DishID != first.id || rows[0].Status != model.MealPlanned ||
		rows[0].Who != "Олег" || rows[0].Leftover {
		t.Fatalf("meals after pick = %+v", rows)
	}
	if !got.isChosen(menu.Lunch, first.id) {
		t.Error("redraw does not mark the pick")
	}
	// The rows on screen come from the keyboard, untouched by a pick.
	if fmt.Sprint(got.offered) != fmt.Sprint(st.offered) {
		t.Errorf("offered changed on a pick: %v → %v", st.offered, got.offered)
	}

	if _, _, _, err := b.applyMenuTap(menuMorning, menuPickUnique,
		tapData(menuToday, menu.Lunch, second.id), "Аня", kb, fixedRand()); err != nil {
		t.Fatalf("second pick: %v", err)
	}
	rows = dayMeals(t, b, menuToday)
	if len(rows) != 1 || rows[0].DishID != second.id || rows[0].Who != "Аня" {
		t.Fatalf("meals after a second pick = %+v, want it to replace the first", rows)
	}
}

func TestMenuLeftoverTapPlansALeftover(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 8)
	if _, _, err := b.store.RecordEaten(dishes[0].ID, menuYesterday, model.MealDinner, "", false); err != nil {
		t.Fatal(err)
	}
	_, kb := sentMenu(t, b)

	got, _, redraw, err := b.applyMenuTap(menuMorning, menuLeftUnique,
		tapData(menuToday, menu.Dinner, dishes[0].ID), "Олег", kb, fixedRand())
	if err != nil || !redraw {
		t.Fatalf("leftover tap: redraw=%v err=%v", redraw, err)
	}
	rows := dayMeals(t, b, menuToday)
	if len(rows) != 1 || !rows[0].Leftover || rows[0].Meal != model.MealDinner {
		t.Fatalf("meals = %+v, want a leftover dinner", rows)
	}
	if len(got.leftovers) != 1 {
		t.Errorf("leftover row lost on redraw: %v", got.leftovers)
	}
}

func TestMenuShuffleDoesNotRepeatWhatWasShown(t *testing.T) {
	b := menuBot(t)
	seedMenuDishes(t, b, 20)
	st, kb := sentMenu(t, b)

	seen := map[int64]bool{}
	for _, meal := range menuMeals {
		for _, d := range st.offered[meal] {
			seen[d.id] = true
		}
	}
	for round := range 2 {
		got, _, redraw, err := b.applyMenuTap(menuMorning, menuShufUnique,
			menuToday+"|lunch", "Олег", kb, rand.New(rand.NewPCG(uint64(round), 9)))
		if err != nil || !redraw {
			t.Fatalf("shuffle %d: redraw=%v err=%v", round, redraw, err)
		}
		lunch := got.offered[menu.Lunch]
		if len(lunch) != menuOptions {
			t.Fatalf("shuffle %d offered %d", round, len(lunch))
		}
		for _, d := range lunch {
			if seen[d.id] {
				t.Errorf("shuffle %d offered %s again", round, d.name)
			}
			seen[d.id] = true
		}
		if fmt.Sprint(got.offered[menu.Dinner]) != fmt.Sprint(st.offered[menu.Dinner]) {
			t.Errorf("a lunch shuffle changed dinner: %v", got.offered[menu.Dinner])
		}
		_, markup := menuView(got)
		kb = asTelegramSentIt(markup)
	}

	msg, err := b.store.MenuMessage(menuToday)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(msg.Shown[model.MealLunch]); n != 3*menuOptions {
		t.Fatalf("lunch shown = %v, want the first row and both shuffles", msg.Shown[model.MealLunch])
	}
	if rows := dayMeals(t, b, menuToday); len(rows) != 0 {
		t.Fatalf("a shuffle wrote meals: %+v", rows)
	}
}

func seedProposed(t *testing.T, b *Bot, n int) {
	t.Helper()
	for i := range n {
		if _, _, err := b.store.CreateDish(model.Dish{Name: fmt.Sprintf("Нова %02d", i+1), Status: model.DishProposed}); err != nil {
			t.Fatalf("create proposed: %v", err)
		}
	}
}

func proposedIn(dishes []menuDish) int {
	n := 0
	for _, d := range dishes {
		if d.proposed {
			n++
		}
	}
	return n
}

// The message carries at most one 🆕, on about a third of the mornings, at
// either meal.
func TestBuildMenuOffersAtMostOneNew(t *testing.T) {
	b := menuBot(t)
	seedMenuDishes(t, b, 12)
	seedProposed(t, b, 6)

	const mornings = 300
	sawLunch, sawDinner, withNew := false, false, 0
	for seed := range uint64(mornings) {
		st, ok, err := b.buildMenu(menuMorning, rand.New(rand.NewPCG(seed, 3)))
		if err != nil || !ok {
			t.Fatalf("seed %d: ok=%v err=%v", seed, ok, err)
		}
		lunch, dinner := proposedIn(st.offered[menu.Lunch]), proposedIn(st.offered[menu.Dinner])
		if lunch+dinner > 1 {
			t.Fatalf("seed %d: %d new dishes at lunch and %d at dinner, want at most one per message", seed, lunch, dinner)
		}
		sawLunch = sawLunch || lunch == 1
		sawDinner = sawDinner || dinner == 1
		withNew += lunch + dinner
	}
	if !sawLunch || !sawDinner {
		t.Errorf("new dish at lunch %v, at dinner %v — want either meal to carry it on some mornings", sawLunch, sawDinner)
	}
	// One draw per message: a draw per meal would land near five mornings
	// in nine.
	if withNew < mornings/5 || withNew > mornings*2/5 {
		t.Errorf("🆕 on %d of %d mornings, want roughly a third", withNew, mornings)
	}
}

// A shuffle keeps the row's 🆕 slot if it had one and never adds one: the
// message's one draw was made in the morning.
func TestMenuShuffleKeepsOneNewPerMessage(t *testing.T) {
	b := menuBot(t)
	active := seedMenuDishes(t, b, 12)
	seedProposed(t, b, 6)
	fresh, err := b.store.Dishes(model.DishProposed)
	if err != nil || len(fresh) == 0 {
		t.Fatalf("proposed: %v %v", fresh, err)
	}

	keyboard := func(lunch, dinner []int64) *tele.ReplyMarkup {
		st, err := b.menuStateFrom(menuRefs{date: menuToday, offered: map[menu.Meal][]int64{
			menu.Lunch: lunch, menu.Dinner: dinner,
		}})
		if err != nil {
			t.Fatal(err)
		}
		_, markup := menuView(st)
		return asTelegramSentIt(markup)
	}
	shuffleDinner := func(kb *tele.ReplyMarkup, seed uint64) int {
		got, _, redraw, err := b.applyMenuTap(menuMorning, menuShufUnique, menuToday+"|dinner", "",
			kb, rand.New(rand.NewPCG(seed, 5)))
		if err != nil || !redraw {
			t.Fatalf("seed %d: redraw=%v err=%v", seed, redraw, err)
		}
		return proposedIn(got.offered[menu.Dinner])
	}

	plain := []int64{active[3].ID, active[4].ID, active[5].ID}
	nextToLunchsNew := keyboard([]int64{active[0].ID, active[1].ID, fresh[0].ID}, plain)
	noNewAnywhere := keyboard([]int64{active[0].ID, active[1].ID, active[2].ID}, plain)
	dinnersOwnNew := keyboard([]int64{active[0].ID, active[1].ID, active[2].ID},
		[]int64{active[3].ID, active[4].ID, fresh[1].ID})
	for seed := range uint64(50) {
		if n := shuffleDinner(nextToLunchsNew, seed); n != 0 {
			t.Fatalf("seed %d: a dinner shuffle added a second 🆕 next to lunch's", seed)
		}
		if n := shuffleDinner(noNewAnywhere, seed); n != 0 {
			t.Fatalf("seed %d: a shuffle drew a 🆕 the morning did not", seed)
		}
		if n := shuffleDinner(dinnersOwnNew, seed); n != 1 {
			t.Fatalf("seed %d: the dinner row lost its 🆕 slot on a shuffle (%d)", seed, n)
		}
	}
}

// A catalogue smaller than a few rows goes round the circle rather than
// running dry, and a meal nothing fits any more says so.
func TestMenuShuffleOnASmallCatalogue(t *testing.T) {
	b := menuBot(t)
	var lunch []model.Dish
	for _, name := range []string{"Борщ", "Солянка", "Юшка", "Розсольник"} {
		d, _, err := b.store.CreateDish(model.Dish{Name: name, Meal: model.MealLunch})
		if err != nil {
			t.Fatal(err)
		}
		lunch = append(lunch, d)
	}
	if _, _, err := b.store.CreateDish(model.Dish{Name: "Вареники", Meal: model.MealDinner}); err != nil {
		t.Fatal(err)
	}
	_, kb := sentMenu(t, b)

	for round := range 4 {
		got, toast, redraw, err := b.applyMenuTap(menuMorning, menuShufUnique, menuToday+"|lunch", "",
			kb, rand.New(rand.NewPCG(uint64(round), 21)))
		if err != nil || !redraw {
			t.Fatalf("shuffle %d: toast=%q redraw=%v err=%v", round, toast, redraw, err)
		}
		if n := len(got.offered[menu.Lunch]); n == 0 || n > menuOptions {
			t.Fatalf("shuffle %d offered %d lunch dishes", round, n)
		}
		if n := len(got.offered[menu.Dinner]); n != 1 {
			t.Fatalf("shuffle %d: dinner row = %v, want it kept", round, got.offered[menu.Dinner])
		}
		_, markup := menuView(got)
		kb = asTelegramSentIt(markup)
	}
	msg, err := b.store.MenuMessage(menuToday)
	if err != nil {
		t.Fatal(err)
	}
	if shown := msg.Shown[model.MealLunch]; len(shown) != len(lunch) {
		t.Errorf("lunch shown = %v, want all four once each", shown)
	}

	for _, d := range lunch {
		if err := b.store.SetDishStatus(d.ID, model.DishRejected); err != nil {
			t.Fatal(err)
		}
	}
	_, toast, redraw, err := b.applyMenuTap(menuMorning, menuShufUnique, menuToday+"|lunch", "", kb, fixedRand())
	if err != nil || redraw || toast != "Більше нічого немає" {
		t.Fatalf("empty pool: toast=%q redraw=%v err=%v", toast, redraw, err)
	}
}

// A pick on a meal already reported eaten plans nothing: the plan would
// never be closed, and the menu would show two lunches for good.
func TestMenuPickAfterTheMealWasEaten(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 8)
	st, kb := sentMenu(t, b)
	pick := st.offered[menu.Lunch][0]
	var eaten model.Dish
	for _, d := range dishes {
		if !slices.ContainsFunc(st.offered[menu.Lunch], func(o menuDish) bool { return o.id == d.ID }) {
			eaten = d
			break
		}
	}
	if _, _, err := b.store.RecordEaten(eaten.ID, menuToday, model.MealLunch, "Аня", false); err != nil {
		t.Fatal(err)
	}

	got, toast, redraw, err := b.applyMenuTap(menuMorning, menuPickUnique,
		tapData(menuToday, menu.Lunch, pick.id), "Олег", kb, fixedRand())
	if err != nil || !redraw {
		t.Fatalf("pick: redraw=%v err=%v", redraw, err)
	}
	if toast != "Обід вже записано: "+eaten.Name {
		t.Errorf("toast = %q", toast)
	}
	if rows := mealRows(t, b, menuToday); rows != "lunch:"+eaten.Name+":eaten" {
		t.Fatalf("meals = %s, want only the eaten lunch", rows)
	}
	if got.isChosen(menu.Lunch, pick.id) {
		t.Error("the redraw marks a pick that was not written")
	}
}

func TestBuildMenuWeekendDishesFromFridayDinner(t *testing.T) {
	b := menuBot(t)
	for _, name := range []string{"Піца", "Суші", "Шаурма"} {
		if _, _, err := b.store.CreateDish(model.Dish{Name: name, Days: model.DishDaysWeekend}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name          string
		day           time.Time
		lunch, dinner bool
	}{
		{"thursday", menuMorning, false, false},
		{"friday", menuMorning.AddDate(0, 0, 1), false, true},
		{"saturday", menuMorning.AddDate(0, 0, 2), true, true},
		{"sunday", menuMorning.AddDate(0, 0, 3), true, true},
		{"monday", menuMorning.AddDate(0, 0, 4), false, false},
	} {
		st, ok, err := b.buildMenu(tc.day, fixedRand())
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		lunch, dinner := len(st.offered[menu.Lunch]) > 0, len(st.offered[menu.Dinner]) > 0
		if lunch != tc.lunch || dinner != tc.dinner || ok != (tc.lunch || tc.dinner) {
			t.Errorf("%s: lunch %v dinner %v ok %v, want lunch %v dinner %v", tc.name, lunch, dinner, ok, tc.lunch, tc.dinner)
		}
	}
}

// A plan nobody confirmed in the evening is neither yesterday's pot nor a
// dish "not seen for ages": it stays out of the leftovers and ranks as
// recent, so the next morning offers something else.
func TestBuildMenuAfterAnUnansweredEvening(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 10)
	plov := dishes[0]
	if _, err := b.store.PlanMeal(plov.ID, menuYesterday, model.MealLunch, "Олег", false); err != nil {
		t.Fatal(err)
	}

	for seed := range uint64(100) {
		st, ok, err := b.buildMenu(menuMorning, rand.New(rand.NewPCG(seed, 11)))
		if err != nil || !ok {
			t.Fatalf("seed %d: ok=%v err=%v", seed, ok, err)
		}
		if len(st.leftovers) != 0 {
			t.Fatalf("seed %d: leftovers %v from a plan nobody confirmed", seed, st.leftovers)
		}
		for _, meal := range menuMeals {
			for _, d := range st.offered[meal] {
				if d.id == plov.ID {
					t.Fatalf("seed %d: yesterday's unconfirmed plan offered again for %s", seed, meal)
				}
			}
		}
	}
	rows := dayMeals(t, b, menuYesterday)
	if len(rows) != 1 || rows[0].Status != model.MealPlanned {
		t.Fatalf("yesterday = %+v, want the plan left as it was", rows)
	}
}

// Nothing about a sent menu lives in the process: a bot started afresh over
// the same database handles taps on the message the previous one sent.
func TestMenuButtonsWorkAfterARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	before := menuBotAt(t, path)
	seedMenuDishes(t, before, 20)
	st, kb := sentMenu(t, before)

	after := menuBotAt(t, path)
	pick := st.offered[menu.Dinner][1]
	got, _, redraw, err := after.applyMenuTap(menuMorning, menuPickUnique,
		tapData(menuToday, menu.Dinner, pick.id), "Аня", kb, fixedRand())
	if err != nil || !redraw {
		t.Fatalf("pick after restart: redraw=%v err=%v", redraw, err)
	}
	if fmt.Sprint(got.offered) != fmt.Sprint(st.offered) || !got.isChosen(menu.Dinner, pick.id) {
		t.Fatalf("redraw after restart: offered %v chosen %v", got.offered, got.chosen)
	}
	_, markup := menuView(got)
	kb = asTelegramSentIt(markup)

	got, _, redraw, err = after.applyMenuTap(menuMorning, menuShufUnique, menuToday+"|lunch", "Аня", kb, fixedRand())
	if err != nil || !redraw {
		t.Fatalf("shuffle after restart: redraw=%v err=%v", redraw, err)
	}
	for _, d := range got.offered[menu.Lunch] {
		if slices.ContainsFunc(st.offered[menu.Lunch], func(o menuDish) bool { return o.id == d.id }) {
			t.Errorf("shuffle after restart repeated %s", d.name)
		}
	}
	if !got.isChosen(menu.Dinner, pick.id) {
		t.Error("shuffle after restart lost the dinner pick")
	}
	if _, ok, _ := after.buildMenu(menuMorning, fixedRand()); ok {
		t.Error("the restarted bot would post today's menu a second time")
	}
}

func TestMenuTapOnAnOldMenuWritesNothing(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 6)
	for _, tc := range []struct{ unique, data string }{
		{menuPickUnique, tapData(menuYesterday, menu.Lunch, dishes[0].ID)},
		{menuLeftUnique, tapData(menuYesterday, menu.Dinner, dishes[0].ID)},
		{menuShufUnique, menuYesterday + "|lunch"},
	} {
		_, toast, redraw, err := b.applyMenuTap(menuMorning, tc.unique, tc.data, "Олег", nil, fixedRand())
		if err != nil || redraw || toast != "Це меню вже минуло" {
			t.Errorf("%s: toast=%q redraw=%v err=%v", tc.unique, toast, redraw, err)
		}
	}
	for _, date := range []string{menuToday, menuYesterday} {
		if rows := dayMeals(t, b, date); len(rows) != 0 {
			t.Fatalf("an old menu wrote %s: %+v", date, rows)
		}
	}
}

func TestMenuTapWithBadData(t *testing.T) {
	b := menuBot(t)
	for _, tc := range []struct{ unique, data string }{
		{menuPickUnique, menuToday + "|lunch"},
		{menuPickUnique, menuToday + "|breakfast|1"},
		{menuPickUnique, menuToday + "|lunch|x"},
		{menuPickUnique, "24.09|lunch|1"},
		{menuShufUnique, menuToday + "|lunch|1"},
	} {
		_, toast, redraw, err := b.applyMenuTap(menuMorning, tc.unique, tc.data, "", nil, fixedRand())
		if err != nil || redraw || toast != "Невірні дані" {
			t.Errorf("%s %q: toast=%q redraw=%v err=%v", tc.unique, tc.data, toast, redraw, err)
		}
	}
	// A dish deleted since the message went out is not an internal error.
	_, toast, redraw, err := b.applyMenuTap(menuMorning, menuPickUnique,
		tapData(menuToday, menu.Lunch, 999), "", nil, fixedRand())
	if err != nil || redraw || toast != "Цієї страви вже немає" {
		t.Errorf("missing dish: toast=%q redraw=%v err=%v", toast, redraw, err)
	}
}

// The evening check.

var menuEvening = time.Date(2026, 9, 24, 20, 0, 0, 0, time.UTC)

func eveningSample() eveningState {
	return eveningState{
		date: menuToday,
		open: map[menu.Meal]bool{menu.Lunch: true, menu.Dinner: true},
		meals: []model.MealEntry{
			{ID: 1, DishID: 10, Dish: "Борщ", DishStatus: model.DishActive, Date: menuToday,
				Meal: model.MealLunch, Status: model.MealPlanned},
		},
		yesterday: []menuDish{{id: 20, name: "Плов"}, {id: 21, name: "Деруни"}},
		other:     true,
	}
}

func TestEveningViewAsksAboutThePlan(t *testing.T) {
	text, markup, ok := eveningView(eveningSample())
	if !ok {
		t.Fatal("two open meals, but nothing to ask")
	}
	for _, want := range []string{"Обід: <b>Борщ</b> — так?", "Вечеря: що їли?"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	want := [][]string{
		{"✓ Так · обід", "Інше · обід"},
		{"Не вдома · обід"},
		{"↩ Плов", "↩ Деруни"},
		{"Інше · вечеря", "Не вдома · вечеря"},
	}
	if got := buttonTexts(markup); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keyboard = %v\nwant       %v", got, want)
	}
	if strings.Contains(text, "не наше") || hasLabel(markup, "✖ Ні, не наше") {
		t.Error("an active dish offered for rejection")
	}
}

func TestEveningViewOffersToTurnDownAProposal(t *testing.T) {
	s := eveningSample()
	s.meals[0].DishStatus = model.DishProposed
	text, markup, _ := eveningView(s)
	if !strings.Contains(text, "🆕 <b>Борщ</b>") {
		t.Errorf("proposal not marked:\n%s", text)
	}
	if !hasLabel(markup, "✖ Ні, не наше") {
		t.Fatalf("no rejection button: %v", buttonTexts(markup))
	}
}

func TestEveningViewWithoutTheRecognizerHasNoOther(t *testing.T) {
	s := eveningSample()
	s.other = false
	_, markup, _ := eveningView(s)
	for _, l := range labels(markup) {
		if strings.HasPrefix(l, "Інше") {
			t.Fatalf("%q offered without a recognizer", l)
		}
	}
}

func TestEveningViewIsSilentWhenEverythingIsAnswered(t *testing.T) {
	s := eveningSample()
	s.meals = []model.MealEntry{
		{DishID: 10, Dish: "Борщ", Meal: model.MealLunch, Status: model.MealEaten},
		{DishID: 11, Dish: "Вареники", Meal: model.MealDinner, Status: model.MealEaten},
	}
	text, markup, ok := eveningView(s)
	if ok || len(markup.InlineKeyboard) != 0 {
		t.Fatalf("ok=%v keyboard=%v with both meals eaten", ok, buttonTexts(markup))
	}
	if !strings.Contains(text, "✅ Обід: Борщ") || !strings.Contains(text, "✅ Вечеря: Вареники") {
		t.Errorf("summary lacks the meals:\n%s", text)
	}

	// "Not at home" closes a meal just as well, with nothing eaten.
	s.meals = s.meals[:1]
	s.open[menu.Dinner] = false
	text, _, ok = eveningView(s)
	if ok || !strings.Contains(text, "Вечеря: не їли вдома") {
		t.Fatalf("ok=%v:\n%s", ok, text)
	}
}

func TestEveningKeyboardRoundTrips(t *testing.T) {
	s := eveningSample()
	s.meals = append(s.meals, model.MealEntry{DishID: 11, Dish: "Вареники", Meal: model.MealDinner, Status: model.MealEaten})
	_, markup, _ := eveningView(s)
	back := asTelegramSentIt(markup)
	back.InlineKeyboard = append(back.InlineKeyboard,
		[]tele.InlineButton{{Text: appButtonLabel, URL: "https://example.test"}})

	open := eveningOpenFromMarkup(back)
	if !open[menu.Lunch] || open[menu.Dinner] {
		t.Fatalf("open = %v, want lunch only", open)
	}
	for _, row := range back.InlineKeyboard {
		for _, btn := range row {
			if len(btn.Data) > 64 {
				t.Errorf("callback data %q is over Telegram's 64 bytes", btn.Data)
			}
		}
	}
}

// Without a plan the check offers what was eaten yesterday, once each even
// when it was eaten at both meals, three buttons to a row.
func TestBuildEveningOffersYesterdaysDishes(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 4)
	for _, meal := range []string{model.MealLunch, model.MealDinner} {
		if _, _, err := b.store.RecordEaten(dishes[0].ID, menuYesterday, meal, "", false); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range dishes[1:] {
		if _, _, err := b.store.RecordEaten(d.ID, menuYesterday, model.MealDinner, "", false); err != nil {
			t.Fatal(err)
		}
	}
	st, ok, err := b.buildEvening(menuEvening)
	if err != nil || !ok {
		t.Fatalf("build: ok=%v err=%v", ok, err)
	}
	if len(st.yesterday) != 4 || st.yesterday[0].id != dishes[0].ID {
		t.Fatalf("yesterday = %v, want four dishes, the one eaten twice once", st.yesterday)
	}
	_, markup, _ := eveningView(st)
	rows := buttonTexts(markup)
	want := [][]string{
		{"↩ Страва 01", "↩ Страва 02", "↩ Страва 03"},
		{"↩ Страва 04"},
		{"Не вдома · обід"},
	}
	if fmt.Sprint(rows[:3]) != fmt.Sprint(want) {
		t.Fatalf("lunch rows = %v\nwant        %v", rows[:3], want)
	}
}

// A check that could only be answered "not at home" is not sent: no plan,
// nothing from yesterday, and no recognizer behind "Інше".
func TestBuildEveningWithNothingToAnswerIsSilent(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 1)
	if _, ok, err := b.buildEvening(menuEvening); ok || err != nil {
		t.Fatalf("nothing to ask: ok=%v err=%v, want silence", ok, err)
	}
	// "Інше" is an answer.
	b.cfg.Dish = dish.New("http://127.0.0.1:0", "k", "m")
	if _, ok, err := b.buildEvening(menuEvening); !ok || err != nil {
		t.Fatalf("with a recognizer: ok=%v err=%v, want the check", ok, err)
	}
	b.cfg.Dish = nil
	// So is a plan, for either meal.
	if _, err := b.store.PlanMeal(dishes[0].ID, menuToday, model.MealDinner, "", false); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := b.buildEvening(menuEvening); !ok || err != nil {
		t.Fatalf("with a plan: ok=%v err=%v, want the check", ok, err)
	}
}

func TestBuildEvening(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 3)
	if _, err := b.store.PlanMeal(dishes[0].ID, menuToday, model.MealLunch, "Олег", false); err != nil {
		t.Fatal(err)
	}
	st, ok, err := b.buildEvening(menuEvening)
	if err != nil || !ok {
		t.Fatalf("build: ok=%v err=%v", ok, err)
	}
	if !st.open[menu.Lunch] || !st.open[menu.Dinner] {
		t.Errorf("open = %v, want both", st.open)
	}

	for i, meal := range []string{model.MealLunch, model.MealDinner} {
		if _, _, err := b.store.RecordEaten(dishes[i+1].ID, menuToday, meal, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := b.buildEvening(menuEvening); ok || err != nil {
		t.Fatalf("both meals eaten: ok=%v err=%v, want silence", ok, err)
	}
}

// sentEvening builds tonight's check and returns its keyboard as it comes
// back from Telegram.
func sentEvening(t *testing.T, b *Bot) *tele.ReplyMarkup {
	t.Helper()
	st, ok, err := b.buildEvening(menuEvening)
	if err != nil || !ok {
		t.Fatalf("build: ok=%v err=%v", ok, err)
	}
	_, markup, _ := eveningView(st)
	return asTelegramSentIt(markup)
}

func TestEveningYesRecordsThePlan(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 2)
	if _, err := b.store.PlanMeal(dishes[0].ID, menuToday, model.MealLunch, "Олег", false); err != nil {
		t.Fatal(err)
	}
	kb := sentEvening(t, b)

	st, toast, redraw, err := b.applyEveningTap(menuEvening, eveYesUnique,
		tapData(menuToday, menu.Lunch, dishes[0].ID), "Аня", kb)
	if err != nil || !redraw {
		t.Fatalf("yes: redraw=%v err=%v", redraw, err)
	}
	if toast != "Обід: "+dishes[0].Name+" ✓" {
		t.Errorf("toast = %q", toast)
	}
	if got := mealRows(t, b, menuToday); got != "lunch:"+dishes[0].Name+":eaten" {
		t.Fatalf("meals = %s", got)
	}
	// Dinner is still asked about; lunch is now a line of the summary.
	text, _, ok := eveningView(st)
	if !ok || !strings.Contains(text, "✅ Обід: "+dishes[0].Name) || !strings.Contains(text, "Вечеря: що їли?") {
		t.Fatalf("redraw ok=%v:\n%s", ok, text)
	}

	_, toast, _, _ = b.applyEveningTap(menuEvening, eveYesUnique,
		tapData(menuToday, menu.Lunch, dishes[0].ID), "Аня", kb)
	if toast != "Вже записано" {
		t.Errorf("second yes: toast = %q", toast)
	}
}

func TestEveningYesAcceptsAProposal(t *testing.T) {
	b := menuBot(t)
	d, _, err := b.store.CreateDish(model.Dish{Name: "Солянка", Status: model.DishProposed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.store.PlanMeal(d.ID, menuToday, model.MealDinner, "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := b.applyEveningTap(menuEvening, eveYesUnique,
		tapData(menuToday, menu.Dinner, d.ID), "Олег", sentEvening(t, b)); err != nil {
		t.Fatal(err)
	}
	if got := dishStatus(t, b, d.ID); got != model.DishActive {
		t.Fatalf("status = %q, want the eaten proposal active", got)
	}
}

func TestEveningRejectTurnsDownAProposal(t *testing.T) {
	b := menuBot(t)
	d, _, err := b.store.CreateDish(model.Dish{Name: "Солянка", Status: model.DishProposed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.store.PlanMeal(d.ID, menuToday, model.MealLunch, "", false); err != nil {
		t.Fatal(err)
	}
	other := ensureDish(t, b, "Плов")
	if _, err := b.store.PlanMeal(other.ID, menuToday, model.MealDinner, "", false); err != nil {
		t.Fatal(err)
	}
	st, _, redraw, err := b.applyEveningTap(menuEvening, eveRejUnique,
		tapData(menuToday, menu.Lunch, d.ID), "Олег", sentEvening(t, b))
	if err != nil || !redraw {
		t.Fatalf("reject: redraw=%v err=%v", redraw, err)
	}
	if got := dishStatus(t, b, d.ID); got != model.DishRejected {
		t.Errorf("status = %q, want rejected", got)
	}
	if got := mealRows(t, b, menuToday); got != "dinner:Плов:planned" {
		t.Errorf("meals = %s, want the lunch plan gone and dinner's kept", got)
	}
	// What they ate instead is still a question.
	text, _, ok := eveningView(st)
	if !ok || !strings.Contains(text, "Обід: що їли?") {
		t.Fatalf("redraw ok=%v:\n%s", ok, text)
	}
}

// A stale "✖ Ні" on a dish the family has eaten since does not reject it;
// the plan it was asked about still goes.
func TestEveningRejectLeavesAnEatenDishActive(t *testing.T) {
	b := menuBot(t)
	d, _, err := b.store.CreateDish(model.Dish{Name: "Солянка", Status: model.DishProposed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.store.PlanMeal(d.ID, menuToday, model.MealDinner, "", false); err != nil {
		t.Fatal(err)
	}
	kb := sentEvening(t, b)
	// Eaten at lunch in the meantime: the proposal is the family's now.
	if _, _, err := b.store.RecordEaten(d.ID, menuToday, model.MealLunch, "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, redraw, err := b.applyEveningTap(menuEvening, eveRejUnique,
		tapData(menuToday, menu.Dinner, d.ID), "Олег", kb); err != nil || !redraw {
		t.Fatalf("reject: redraw=%v err=%v", redraw, err)
	}
	if got := dishStatus(t, b, d.ID); got != model.DishActive {
		t.Errorf("status = %q, want the eaten dish left active", got)
	}
	if got := mealRows(t, b, menuToday); got != "lunch:Солянка:eaten" {
		t.Errorf("meals = %s, want the dinner plan gone and lunch kept", got)
	}
}

// A "✓ Так" left on screen after the meal was answered with another dish —
// through "Інше" or a photo — must not add the planned dish as a second one.
func TestEveningYesAfterAnotherDishWasRecorded(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 2)
	planned, instead := dishes[0], dishes[1]
	if _, err := b.store.PlanMeal(planned.ID, menuToday, model.MealLunch, "", false); err != nil {
		t.Fatal(err)
	}
	kb := sentEvening(t, b)
	if _, _, err := b.store.RecordEaten(instead.ID, menuToday, model.MealLunch, "Аня", false); err != nil {
		t.Fatal(err)
	}

	st, toast, redraw, err := b.applyEveningTap(menuEvening, eveYesUnique,
		tapData(menuToday, menu.Lunch, planned.ID), "Олег", kb)
	if err != nil || !redraw {
		t.Fatalf("stale yes: redraw=%v err=%v", redraw, err)
	}
	if toast != "Вже записано" {
		t.Errorf("toast = %q", toast)
	}
	if got := mealRows(t, b, menuToday); got != "lunch:"+instead.Name+":eaten" {
		t.Fatalf("meals = %s, want only what was reported", got)
	}
	if text, _, _ := eveningView(st); !strings.Contains(text, "✅ Обід: "+instead.Name) {
		t.Errorf("redraw does not show the answer:\n%s", text)
	}
}

// "Not at home" at one meal drops that meal's plan and nothing else: the
// other meal's plan and anything eaten stay.
func TestEveningNotAtHomeTouchesOnlyItsMeal(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 3)
	if _, err := b.store.PlanMeal(dishes[0].ID, menuToday, model.MealLunch, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.store.PlanMeal(dishes[1].ID, menuToday, model.MealDinner, "", false); err != nil {
		t.Fatal(err)
	}
	kb := sentEvening(t, b)
	if _, _, redraw, err := b.applyEveningTap(menuEvening, eveHomeUnique, menuToday+"|dinner", "Олег", kb); err != nil || !redraw {
		t.Fatalf("home: redraw=%v err=%v", redraw, err)
	}
	if got := mealRows(t, b, menuToday); got != "lunch:"+dishes[0].Name+":planned" {
		t.Fatalf("meals = %s, want the lunch plan kept", got)
	}

	// An eaten row at the meal is not touched either.
	if _, _, err := b.store.RecordEaten(dishes[2].ID, menuToday, model.MealLunch, "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := b.applyEveningTap(menuEvening, eveHomeUnique, menuToday+"|lunch", "Олег", kb); err != nil {
		t.Fatal(err)
	}
	if got := mealRows(t, b, menuToday); got != "lunch:"+dishes[2].Name+":eaten" {
		t.Fatalf("meals = %s, want the eaten lunch kept", got)
	}
}

func TestEveningNotAtHomeWritesNothing(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 2)
	if _, err := b.store.PlanMeal(dishes[0].ID, menuToday, model.MealDinner, "", false); err != nil {
		t.Fatal(err)
	}
	kb := sentEvening(t, b)
	st, _, redraw, err := b.applyEveningTap(menuEvening, eveHomeUnique, menuToday+"|dinner", "Олег", kb)
	if err != nil || !redraw {
		t.Fatalf("home: redraw=%v err=%v", redraw, err)
	}
	if got := mealRows(t, b, menuToday); got != "" {
		t.Fatalf("meals = %s, want nothing", got)
	}
	text, markup, ok := eveningView(st)
	if !ok || !strings.Contains(text, "Вечеря: не їли вдома") {
		t.Fatalf("redraw ok=%v:\n%s", ok, text)
	}

	// The answer lives in the keyboard: the next tap, on lunch, keeps it.
	st, _, _, err = b.applyEveningTap(menuEvening, eveHomeUnique, menuToday+"|lunch", "Олег", asTelegramSentIt(markup))
	if err != nil {
		t.Fatal(err)
	}
	text, markup, ok = eveningView(st)
	if ok || len(markup.InlineKeyboard) != 0 || !strings.Contains(text, "Вечеря: не їли вдома") ||
		!strings.Contains(text, "Обід: не їли вдома") {
		t.Fatalf("after both: ok=%v keyboard=%v\n%s", ok, buttonTexts(markup), text)
	}
}

func TestEveningPickRecordsYesterdaysDishAsLeftover(t *testing.T) {
	b := menuBot(t)
	dishes := seedMenuDishes(t, b, 2)
	if _, _, err := b.store.RecordEaten(dishes[1].ID, menuYesterday, model.MealDinner, "", false); err != nil {
		t.Fatal(err)
	}
	kb := sentEvening(t, b)
	if _, _, redraw, err := b.applyEveningTap(menuEvening, evePickUnique,
		tapData(menuToday, menu.Lunch, dishes[1].ID), "Олег", kb); err != nil || !redraw {
		t.Fatalf("pick: redraw=%v err=%v", redraw, err)
	}
	rows := dayMeals(t, b, menuToday)
	if len(rows) != 1 || rows[0].Status != model.MealEaten || !rows[0].Leftover || rows[0].Meal != model.MealLunch {
		t.Fatalf("meals = %+v, want an eaten leftover lunch", rows)
	}
}

func TestEveningTapWithBadData(t *testing.T) {
	b := menuBot(t)
	for _, tc := range []struct{ unique, data string }{
		{eveYesUnique, menuToday + "|lunch"},
		{eveYesUnique, menuToday + "|breakfast|1"},
		{eveYesUnique, menuToday + "|lunch|x"},
		{eveHomeUnique, menuToday + "|lunch|1"},
		{eveHomeUnique, "2026-09-25|lunch"},
		{eveOtherUnique, menuToday + "|lunch"},
	} {
		_, toast, redraw, err := b.applyEveningTap(menuEvening, tc.unique, tc.data, "", nil)
		if err != nil || redraw || toast != "Невірні дані" {
			t.Errorf("%s %q: toast=%q redraw=%v err=%v", tc.unique, tc.data, toast, redraw, err)
		}
	}
	_, toast, redraw, err := b.applyEveningTap(menuEvening, eveYesUnique,
		tapData(menuToday, menu.Lunch, 999), "", nil)
	if err != nil || redraw || toast != "Цієї страви вже немає" {
		t.Errorf("missing dish: toast=%q redraw=%v err=%v", toast, redraw, err)
	}
	// An answer tapped after midnight is about the evening it was asked.
	dishes := seedMenuDishes(t, b, 1)
	if _, _, redraw, err := b.applyEveningTap(menuEvening.Add(5*time.Hour), eveYesUnique,
		tapData(menuToday, menu.Dinner, dishes[0].ID), "", nil); err != nil || !redraw {
		t.Fatalf("after midnight: redraw=%v err=%v", redraw, err)
	}
	if got := mealRows(t, b, menuToday); got != "dinner:"+dishes[0].Name+":eaten" {
		t.Errorf("the evening it was asked = %q, want the dinner there", got)
	}
	if got := mealRows(t, b, "2026-09-25"); got != "" {
		t.Errorf("the day it was tapped = %q, want nothing", got)
	}
}
