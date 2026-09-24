package main

import (
	"io"
	"path/filepath"
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

	all, _ := st.Dishes()
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
	all, _ = st.Dishes()
	if len(all) != 2 {
		t.Fatalf("dishes after two runs = %d, want 2", len(all))
	}
	if got, _ := st.Dish(plov.ID); got.Status != model.DishRejected {
		t.Errorf("re-import changed a rejected dish to %s", got.Status)
	}
	for _, d := range all {
		if d.Name == "Борщ" && d.Meal != model.MealLunch {
			t.Errorf("Борщ meal = %s, want lunch", d.Meal)
		}
	}
}
