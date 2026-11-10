package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"familyhub/internal/dish"
	"familyhub/internal/menu"
	"familyhub/internal/model"
)

// The evening before a Thursday: the menu is asked for tomorrow.
var (
	planEvening  = time.Date(2026, 9, 23, 20, 0, 0, 0, time.UTC)
	planTomorrow = time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
)

const planTomorrowDate = "2026-09-24"

func TestPlanDayFrom(t *testing.T) {
	for arg, want := range map[string]string{
		"":            "2026-09-24",
		"завтра":      "2026-09-24",
		" Сьогодні ":  "2026-09-23",
		"сегодня":     "2026-09-23",
		"післязавтра": "2026-09-25",
	} {
		got, ok := planDayFrom(arg, planEvening)
		if !ok || got.Format(time.DateOnly) != want || got.Hour() != 0 {
			t.Errorf("planDayFrom(%q) = %v, %v; want %s", arg, got, ok, want)
		}
	}
	if _, ok := planDayFrom("у п'ятницю", planEvening); ok {
		t.Error("an unknown day must be refused, not guessed")
	}
}

func samplePlan() dayPlan {
	return dayPlan{
		date:  planTomorrowDate,
		label: "Завтра, 24 вересня",
		variants: []planVariant{
			{lunch: []menuDish{{id: 1, name: "Борщ"}, {id: 2, name: "Салат"}}, dinner: []menuDish{{id: 3, name: "Плов"}}, note: "Борщу не було два тижні"},
			{lunch: []menuDish{{id: 4, name: "Солянка", proposed: true}}, dinner: []menuDish{{id: 5, name: "Деруни"}}},
		},
	}
}

func TestPlanViewLayout(t *testing.T) {
	text, markup := planView(samplePlan())
	for _, want := range []string{
		"Що готувати · Завтра, 24 вересня",
		"<b>1.</b> Обід: Борщ · Салат\nВечеря: Плов\n<i>Борщу не було два тижні</i>",
		"<b>2.</b> Обід: 🆕 Солянка\nВечеря: Деруни",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	want := [][]string{{"✅ 1", "✅ 2"}, {"🔀 Інші варіанти"}}
	if got := buttonTexts(markup); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keyboard = %v\nwant       %v", got, want)
	}

	got := planVariantsFromMarkup(asTelegramSentIt(markup))
	wantV := []dish.Variant{{Lunch: []int64{1, 2}, Dinner: []int64{3}}, {Lunch: []int64{4}, Dinner: []int64{5}}}
	if fmt.Sprint(got) != fmt.Sprint(wantV) {
		t.Fatalf("read back = %+v\nwant        %+v", got, wantV)
	}
}

// Telegram refuses callback data over 64 bytes, and the whole message with it.
func TestPlanPickDataFitsTelegram(t *testing.T) {
	v := planVariant{lunch: []menuDish{{id: 99999}, {id: 99998}}, dinner: []menuDish{{id: 99997}, {id: 99996}}}
	if data := "\f" + planPickUnique + "|" + planPickData(planTomorrowDate, v); len(data) > 64 {
		t.Fatalf("callback data is %d bytes: %q", len(data), data)
	}
}

func TestParsePlanPickRejectsBadData(t *testing.T) {
	for _, data := range []string{"", "2026-09-24", "2026-09-24||", "nope|1|2", "2026-09-24|x|2"} {
		if _, _, ok := parsePlanPick(data); ok {
			t.Errorf("parsePlanPick(%q) accepted", data)
		}
	}
}

func TestPlanViewDoneAndFull(t *testing.T) {
	text, markup := planView(dayPlan{label: "Завтра, 24 вересня", done: true, picked: []model.MealEntry{
		{Meal: "lunch", Dish: "Борщ", Who: "Олег"}, {Meal: "dinner", Dish: "Плов", Who: "Олег"},
	}})
	if !strings.Contains(text, "Меню · Завтра, 24 вересня") || !strings.Contains(text, "✅ Обід: Борщ — Олег\n✅ Вечеря: Плов — Олег") {
		t.Errorf("done text:\n%s", text)
	}
	if len(markup.InlineKeyboard) != 0 {
		t.Errorf("a picked menu keeps no buttons: %v", buttonTexts(markup))
	}

	text, _ = planView(dayPlan{label: "Сьогодні, 23 вересня", full: true, eaten: []model.MealEntry{
		{Meal: "lunch", Dish: "Борщ"}, {Meal: "dinner", Dish: "Плов"},
	}})
	if strings.Contains(text, "Немає з чого") || !strings.Contains(text, "✅ Вечеря: Плов") {
		t.Errorf("full day text:\n%s", text)
	}
}

func planDishes(t *testing.T, b *Bot) (lunch, dinner, either model.Dish) {
	t.Helper()
	mk := func(name, meal string) model.Dish {
		d, _, err := b.store.CreateDish(model.Dish{Name: name, Meal: meal})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	return mk("Борщ", "lunch"), mk("Плов", "dinner"), mk("Деруни", "any")
}

func TestBuildPlanWithoutModelUsesTheRotation(t *testing.T) {
	b := menuBot(t)
	seedMenuDishes(t, b, 12)
	p, err := b.buildPlan(t.Context(), planEvening, planTomorrow, nil, fixedRand())
	if err != nil {
		t.Fatal(err)
	}
	if p.label != "Завтра, 24 вересня" || len(p.variants) != 3 {
		t.Fatalf("plan = %+v", p)
	}
	seen := map[int64]bool{}
	for _, v := range p.variants {
		if len(v.lunch) != 1 || len(v.dinner) != 1 {
			t.Fatalf("a rotation variant is one dish per meal: %+v", v)
		}
		for _, d := range append(v.lunch, v.dinner...) {
			if seen[d.id] {
				t.Fatalf("dish %d offered twice: %+v", d.id, p.variants)
			}
			seen[d.id] = true
		}
	}

	// 🔀 moves on: what is on screen is not offered again while there is
	// anything else.
	_, markup := planView(p)
	more, err := b.buildPlan(t.Context(), planEvening, planTomorrow, planVariantsFromMarkup(markup), fixedRand())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range more.variants {
		for _, d := range append(v.lunch, v.dinner...) {
			if seen[d.id] {
				t.Fatalf("dish %d came back on 🔀 with %d dishes left", d.id, 12-len(seen))
			}
		}
	}
}

func TestBuildPlanLeavesAnEatenMealOut(t *testing.T) {
	b := menuBot(t)
	borsch, _, _ := planDishes(t, b)
	seedMenuDishes(t, b, 4)
	if _, _, err := b.store.RecordEaten(borsch.ID, planTomorrowDate, "lunch", "", false); err != nil {
		t.Fatal(err)
	}
	p, err := b.buildPlan(t.Context(), planEvening, planTomorrow, nil, fixedRand())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.eaten) != 1 || len(p.variants) == 0 {
		t.Fatalf("plan = %+v", p)
	}
	for _, v := range p.variants {
		if len(v.lunch) != 0 {
			t.Fatalf("lunch is eaten, yet planned: %+v", v)
		}
	}

	if _, _, err := b.store.RecordEaten(borsch.ID, planTomorrowDate, "dinner", "", false); err != nil {
		t.Fatal(err)
	}
	p, err = b.buildPlan(t.Context(), planEvening, planTomorrow, nil, fixedRand())
	if err != nil || !p.full || len(p.variants) != 0 {
		t.Fatalf("a day fully eaten has nothing to plan: %+v, %v", p, err)
	}
}

// planModel answers with content and keeps the user prompt it was sent.
func planModel(t *testing.T, content string, prompt *string) *dish.Recognizer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		for _, m := range body.Messages {
			if m.Role == "user" && prompt != nil {
				*prompt = m.Content
			}
		}
		if content == "" {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		out, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		})
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return dish.New(srv.URL, "k", "test-model")
}

func TestBuildPlanAsksTheModelWithTheHistory(t *testing.T) {
	b := menuBot(t)
	borsch, plov, deruny := planDishes(t, b)
	if _, _, err := b.store.RecordEaten(plov.ID, "2026-09-23", "dinner", "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.store.RecordEaten(borsch.ID, "2026-09-01", "lunch", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.store.SetPlan("2026-09-22", "lunch", "", []int64{deruny.ID}, nil); err != nil {
		t.Fatal(err)
	}

	var prompt string
	b.cfg.Dish = planModel(t, fmt.Sprintf(`{"variants":[{"lunch":[%d],"dinner":[%d],"note":"Борщу давно не було"}]}`, borsch.ID, deruny.ID), &prompt)
	p, err := b.buildPlan(t.Context(), planEvening, planTomorrow, nil, fixedRand())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.variants) != 1 || p.variants[0].lunch[0].name != "Борщ" || p.variants[0].dinner[0].name != "Деруни" ||
		p.variants[0].note != "Борщу давно не було" {
		t.Fatalf("plan = %+v", p)
	}
	for _, want := range []string{
		"- 2026-09-22: обід — Деруни; вечеря — —",
		"- 2026-09-23: обід — —; вечеря — Плов",
		"Лишилося з попереднього дня: Плов",
		fmt.Sprintf("%d | Борщ", borsch.ID),
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "2026-09-01") {
		t.Errorf("history reaches past %d days:\n%s", planHistoryDays, prompt)
	}
	lunchPool := prompt[strings.Index(prompt, "Страви на обід"):strings.Index(prompt, "Страви на вечерю")]
	if strings.Contains(lunchPool, "Плов") {
		t.Errorf("a dinner dish is in the lunch pool:\n%s", lunchPool)
	}
}

func TestBuildPlanFallsBackWhenTheModelFails(t *testing.T) {
	b := menuBot(t)
	seedMenuDishes(t, b, 6)
	b.cfg.Dish = planModel(t, "", nil)
	p, err := b.buildPlan(t.Context(), planEvening, planTomorrow, nil, fixedRand())
	if err != nil || len(p.variants) != 3 {
		t.Fatalf("plan = %+v, %v", p, err)
	}
}

func TestApplyPlanPickWritesTheDaysPlan(t *testing.T) {
	b := menuBot(t)
	borsch, plov, deruny := planDishes(t, b)
	// Borshch eaten today is tomorrow's leftover.
	if _, _, err := b.store.RecordEaten(borsch.ID, "2026-09-23", "lunch", "", false); err != nil {
		t.Fatal(err)
	}
	// An older plan for tomorrow's dinner is replaced.
	if _, err := b.store.SetPlan(planTomorrowDate, "dinner", "Аня", []int64{deruny.ID}, nil); err != nil {
		t.Fatal(err)
	}
	data := planPickData(planTomorrowDate, planVariant{
		lunch:  []menuDish{{id: borsch.ID}, {id: deruny.ID}},
		dinner: []menuDish{{id: plov.ID}},
	})
	p, toast, redraw, err := b.applyPlanPick(planEvening, data, "Олег")
	if err != nil || !redraw || toast != "Меню записано" || !p.done {
		t.Fatalf("pick = %+v, %q, %v, %v", p, toast, redraw, err)
	}
	rows, err := b.store.MealsOn(planTomorrowDate)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range rows {
		got = append(got, fmt.Sprintf("%s:%s:%s:%v:%s", m.Meal, m.Dish, m.Status, m.Leftover, m.Who))
	}
	slices.Sort(got)
	want := []string{"dinner:Плов:planned:false:Олег", "lunch:Борщ:planned:true:Олег", "lunch:Деруни:planned:false:Олег"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("meals = %v\nwant    %v", got, want)
	}
	text, _ := planView(p)
	if !strings.Contains(text, "✅ Обід: Борщ (доїдаємо) · Деруни — Олег") || !strings.Contains(text, "✅ Вечеря: Плов — Олег") {
		t.Errorf("done text:\n%s", text)
	}
}

func TestApplyPlanPickRefusesAPastDay(t *testing.T) {
	b := menuBot(t)
	borsch, _, _ := planDishes(t, b)
	data := planPickData("2026-09-22", planVariant{lunch: []menuDish{{id: borsch.ID}}})
	if _, toast, redraw, err := b.applyPlanPick(planEvening, data, "Олег"); err != nil || redraw || toast != "Цей день уже минув" {
		t.Fatalf("pick = %q, %v, %v", toast, redraw, err)
	}
	if rows, _ := b.store.MealsOn("2026-09-22"); len(rows) != 0 {
		t.Fatalf("a past day was planned: %+v", rows)
	}
}

func TestApplyPlanPickMissingDish(t *testing.T) {
	b := menuBot(t)
	data := planPickData(planTomorrowDate, planVariant{lunch: []menuDish{{id: 404}}})
	if _, toast, redraw, err := b.applyPlanPick(planEvening, data, "Олег"); err != nil || redraw || toast != "Цієї страви вже немає" {
		t.Fatalf("pick = %q, %v, %v", toast, redraw, err)
	}
}

// The morning menu of a day planned the evening before opens with the plan.
func TestAPlanFromMenuShowsInTheMorning(t *testing.T) {
	b := menuBot(t)
	borsch, plov, _ := planDishes(t, b)
	seedMenuDishes(t, b, 4)
	data := planPickData(planTomorrowDate, planVariant{lunch: []menuDish{{id: borsch.ID}}, dinner: []menuDish{{id: plov.ID}}})
	if _, _, _, err := b.applyPlanPick(planEvening, data, "Олег"); err != nil {
		t.Fatal(err)
	}
	st, ok, err := b.buildMenu(menuMorning, fixedRand())
	if err != nil || !ok {
		t.Fatalf("buildMenu: %v, %v", ok, err)
	}
	if !st.decided(menu.Lunch) || !st.decided(menu.Dinner) {
		t.Fatalf("morning menu does not see the plan: %+v", st.chosen)
	}
	text, markup := menuView(st)
	if !strings.Contains(text, "✅ Обід: Борщ — Олег") || len(markup.InlineKeyboard) != 0 {
		t.Errorf("morning menu:\n%s\n%v", text, buttonTexts(markup))
	}
}
