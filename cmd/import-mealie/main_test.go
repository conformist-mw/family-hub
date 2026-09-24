package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"familyhub/internal/db"
	"familyhub/internal/mealie"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

func tag(slug, name string) mealie.Organizer { return mealie.Organizer{Slug: slug, Name: name} }

func cat(name string) mealie.Organizer { return mealie.Organizer{Name: name} }

var (
	obid     = tag("obid", "обід")
	vecheria = tag("vecheria", "вечеря")
	dostavka = tag("dostavka", "доставка")
	pokupne  = tag("pokupne", "покупне")
	svynyna  = tag("svinina", "свинина")
)

func TestMapRecipeTags(t *testing.T) {
	cases := []struct {
		name       string
		r          mealie.Recipe
		meal, days string
	}{
		{"lunch tag", mealie.Recipe{Name: "Борщ", Tags: []mealie.Organizer{obid, svynyna}}, model.MealLunch, model.DishDaysAny},
		{"dinner tag", mealie.Recipe{Name: "Відбивні", Tags: []mealie.Organizer{vecheria}}, model.MealDinner, model.DishDaysAny},
		{"both meal tags", mealie.Recipe{Name: "Плов", Tags: []mealie.Organizer{obid, vecheria}}, model.DishMealAny, model.DishDaysAny},
		{"no meal tag", mealie.Recipe{Name: "Деруни", Tags: []mealie.Organizer{svynyna}}, model.DishMealAny, model.DishDaysAny},
		{"delivery is weekend", mealie.Recipe{Name: "Піца", Tags: []mealie.Organizer{dostavka, vecheria}}, model.MealDinner, model.DishDaysWeekend},
		{"bought is weekend", mealie.Recipe{Name: "Вареники покупні", Tags: []mealie.Organizer{pokupne}}, model.DishMealAny, model.DishDaysWeekend},
		// A tag renamed in the UI keeps its slug.
		{"slug outlives rename", mealie.Recipe{Name: "Солянка", Tags: []mealie.Organizer{tag("obid", "Обідня")}}, model.MealLunch, model.DishDaysAny},
		// A slug that differs still matches by name, case and spacing aside.
		{"name without slug", mealie.Recipe{Name: "Удон", Tags: []mealie.Organizer{tag("", " Вечеря ")}}, model.MealDinner, model.DishDaysAny},
		{"salad without meal tag", mealie.Recipe{Name: "Олівʼє", RecipeCategory: []mealie.Organizer{cat("Салати")}}, model.DishMealAny, model.DishDaysAny},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, reason := mapRecipe(c.r)
			if reason != "" {
				t.Fatalf("skipped: %s", reason)
			}
			if d.Meal != c.meal || d.Days != c.days {
				t.Errorf("meal/days = %s/%s, want %s/%s", d.Meal, d.Days, c.meal, c.days)
			}
			if d.Status != model.DishActive {
				t.Errorf("status = %s, want active — imported dishes are not 🆕", d.Status)
			}
			if d.Name != c.r.Name {
				t.Errorf("name = %q, want %q", d.Name, c.r.Name)
			}
		})
	}
}

func TestMapRecipeSkipsCategories(t *testing.T) {
	for _, c := range []string{"Гарніри", "Заготовки", "Соуси та заправки", "Напої"} {
		t.Run(c, func(t *testing.T) {
			r := mealie.Recipe{Name: "Щось", Tags: []mealie.Organizer{obid},
				RecipeCategory: []mealie.Organizer{cat("Основні страви"), cat(c)}}
			d, reason := mapRecipe(r)
			if reason == "" {
				t.Fatalf("imported %+v, want skipped", d)
			}
			if want := `category "` + c + `"`; reason != want {
				t.Errorf("reason = %q, want %q", reason, want)
			}
		})
	}
}

func TestMapRecipeSkipsEmptyName(t *testing.T) {
	if _, reason := mapRecipe(mealie.Recipe{Name: "  "}); reason == "" {
		t.Fatal("a recipe with no name was imported")
	}
}

func TestMapRecipeCollapsesWhitespace(t *testing.T) {
	d, _ := mapRecipe(mealie.Recipe{Name: "  Пюре   зі скумбрією "})
	if d.Name != "Пюре зі скумбрією" {
		t.Errorf("name = %q", d.Name)
	}
}

func TestPlanSplitsAndSorts(t *testing.T) {
	dishes, skipped := plan([]mealie.Recipe{
		{Name: "Плов"},
		{Name: "Гречка", RecipeCategory: []mealie.Organizer{cat("Гарніри")}},
		{Name: "Борщ"},
		{Name: "Аджика", RecipeCategory: []mealie.Organizer{cat("Заготовки")}},
	})
	if len(dishes) != 2 || dishes[0].Name != "Борщ" || dishes[1].Name != "Плов" {
		t.Errorf("dishes = %+v", dishes)
	}
	if len(skipped) != 2 || skipped[0].name != "Аджика" || skipped[1].name != "Гречка" {
		t.Errorf("skipped = %+v", skipped)
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(database)
}

// A second run, or one after the menu went live, must not duplicate a dish
// or undo a decision the family has made on it since.
func TestImportDishesIsIdempotent(t *testing.T) {
	st := testStore(t)
	dishes, _ := plan([]mealie.Recipe{
		{Name: "Борщ", Tags: []mealie.Organizer{obid}},
		{Name: "Плов"},
	})

	created, existed, err := importDishes(st, dishes, io.Discard)
	if err != nil || created != 2 || existed != 0 {
		t.Fatalf("first run: created=%d existed=%d err=%v", created, existed, err)
	}

	all, err := st.Dishes()
	if err != nil {
		t.Fatal(err)
	}
	var plov model.Dish
	for _, d := range all {
		if d.Name == "Плов" {
			plov = d
		}
	}
	if err := st.SetDishStatus(plov.ID, model.DishRejected); err != nil {
		t.Fatal(err)
	}

	created, existed, err = importDishes(st, dishes, io.Discard)
	if err != nil || created != 0 || existed != 2 {
		t.Fatalf("second run: created=%d existed=%d err=%v", created, existed, err)
	}
	all, err = st.Dishes()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("dishes after two runs = %d, want 2", len(all))
	}
	if got, err := st.Dish(plov.ID); err != nil || got.Status != model.DishRejected {
		t.Errorf("re-import changed a rejected dish to %s (%v)", got.Status, err)
	}
	for _, d := range all {
		if d.Name == "Борщ" && d.Meal != model.MealLunch {
			t.Errorf("Борщ meal = %s, want lunch", d.Meal)
		}
	}
}

// The dry run is read by a person before anything is written, so its shape is
// the interface: both lists, their counts, and the reason next to each skip.
func TestReportListsBothSides(t *testing.T) {
	dishes, skipped := plan([]mealie.Recipe{
		{Name: "Піца", Tags: []mealie.Organizer{dostavka, vecheria}},
		{Name: "Гречка", RecipeCategory: []mealie.Organizer{cat("Гарніри")}},
		{Name: "Борщ", Tags: []mealie.Organizer{obid}},
	})
	var out bytes.Buffer
	report(&out, dishes, skipped)
	want := strings.Join([]string{
		"import (2):",
		"  Борщ                                     meal=lunch  days=any",
		"  Піца                                     meal=dinner days=weekend",
		"",
		"skip (1):",
		`  Гречка                                   category "Гарніри"`,
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("report =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestAddDish(t *testing.T) {
	st := testStore(t)
	var out bytes.Buffer
	if err := addDish(st, " Пюре  зі скумбрією ", model.MealLunch, model.DishDaysAny, &out); err != nil {
		t.Fatalf("add: %v", err)
	}
	all, err := st.Dishes()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "Пюре зі скумбрією" || all[0].Meal != model.MealLunch ||
		all[0].Status != model.DishActive {
		t.Fatalf("dishes = %+v", all)
	}
	if !strings.HasPrefix(out.String(), "created Пюре зі скумбрією") {
		t.Errorf("output = %q", out.String())
	}

	// The same name in another case is the same dish, reported and left alone.
	out.Reset()
	if err := addDish(st, "пюре зі скумбрією", model.MealDinner, model.DishDaysWeekend, &out); err != nil {
		t.Fatalf("second add: %v", err)
	}
	if all, err := st.Dishes(); err != nil || len(all) != 1 || all[0].Meal != model.MealLunch {
		t.Fatalf("dishes after a repeat = %+v, %v", all, err)
	}
	if !strings.HasPrefix(out.String(), "exists") {
		t.Errorf("output = %q", out.String())
	}

	for _, tc := range []struct{ meal, days string }{{"breakfast", model.DishDaysAny}, {model.MealLunch, "weekday"}} {
		if err := addDish(st, "Інше", tc.meal, tc.days, io.Discard); err == nil {
			t.Errorf("meal %q days %q accepted", tc.meal, tc.days)
		}
	}
}

func TestFlagConflict(t *testing.T) {
	for _, tc := range []struct {
		set  []string
		fail bool
	}{
		{nil, false},
		{[]string{"apply"}, false},
		{[]string{"add", "meal", "days"}, false},
		{[]string{"add", "apply"}, true},
		{[]string{"meal"}, true},
		{[]string{"apply", "days"}, true},
	} {
		set := map[string]bool{}
		for _, f := range tc.set {
			set[f] = true
		}
		if err := flagConflict(set); (err != nil) != tc.fail {
			t.Errorf("flags %v: err = %v, want failure %v", tc.set, err, tc.fail)
		}
	}
}
