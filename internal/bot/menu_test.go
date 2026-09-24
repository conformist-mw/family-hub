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
	"familyhub/internal/menu"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// menuBot is a bot over a real, migrated SQLite with no Telegram behind it:
// buildMenu and applyMenuTap only touch the store, and menuView does not add
// the app button, so nothing reaches for telebot.
func menuBot(t *testing.T) *Bot {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
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
	shownYesterday := []int64{dishes[1].ID, dishes[2].ID, dishes[3].ID}
	if err := b.store.SaveMenuMessage(model.MenuMessage{Date: menuYesterday, ChatID: -100, MessageID: 1,
		Shown: map[string][]int64{model.MealLunch: shownYesterday}}); err != nil {
		t.Fatalf("save yesterday: %v", err)
	}

	st, ok, err := b.buildMenu(menuMorning, fixedRand())
	if err != nil || !ok {
		t.Fatalf("build: ok=%v err=%v", ok, err)
	}
	if len(st.leftovers) != 1 || st.leftovers[0].id != borshch.ID {
		t.Fatalf("leftovers = %v, want the borshch once", st.leftovers)
	}
	for _, d := range st.offered[menu.Lunch] {
		if slices.Contains(shownYesterday, d.id) {
			t.Errorf("%s was offered for lunch yesterday and again today", d.name)
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
	shown := map[string][]int64{}
	for _, meal := range menuMeals {
		for _, d := range st.offered[meal] {
			shown[string(meal)] = append(shown[string(meal)], d.id)
		}
	}
	if err := b.store.SaveMenuMessage(model.MenuMessage{Date: menuToday, ChatID: -100, MessageID: 1, Shown: shown}); err != nil {
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
	rows, _ := b.store.MealsOn(menuToday)
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
	rows, _ = b.store.MealsOn(menuToday)
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
	rows, _ := b.store.MealsOn(menuToday)
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
	if rows, _ := b.store.MealsOn(menuToday); len(rows) != 0 {
		t.Fatalf("a shuffle wrote meals: %+v", rows)
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
		if rows, _ := b.store.MealsOn(date); len(rows) != 0 {
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
