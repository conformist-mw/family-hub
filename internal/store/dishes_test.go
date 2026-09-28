package store_test

import (
	"strings"
	"testing"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

func TestNameKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Борщ", "борщ"},
		{"  борщ  ", "борщ"},
		{"Пюре   зі\tскумбрією", "пюре зі скумбрією"},
		{"Мʼясо по-французьки", "м'ясо по-французьки"},
		{"М’ясо по-французьки", "м'ясо по-французьки"},
		{"м'ясо по-французьки", "м'ясо по-французьки"},
		{"ЇЖАКИ", "їжаки"},
	}
	for _, c := range cases {
		if got := store.NameKey(c.in); got != c.want {
			t.Errorf("NameKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCreateDishStoresDefaults(t *testing.T) {
	st := testStore(t)
	d, existed, err := st.CreateDish(model.Dish{Name: "  Пюре   зі скумбрією "})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if existed {
		t.Error("existed = true for a new dish")
	}
	if d.ID == 0 || d.CreatedAt == "" {
		t.Errorf("dish not read back from the row: %+v", d)
	}
	if d.Name != "Пюре зі скумбрією" {
		t.Errorf("name = %q, want whitespace collapsed", d.Name)
	}
	if d.Meal != model.DishMealAny || d.Days != model.DishDaysAny || d.Status != model.DishActive {
		t.Errorf("defaults = %q/%q/%q, want any/any/active", d.Meal, d.Days, d.Status)
	}

	p, _, err := st.CreateDish(model.Dish{Name: "Солянка", Meal: "lunch", Days: model.DishDaysWeekend,
		Status: model.DishProposed, Note: "густий суп з копченостями"})
	if err != nil {
		t.Fatalf("create proposed: %v", err)
	}
	got, err := st.Dish(p.ID)
	if err != nil {
		t.Fatalf("dish: %v", err)
	}
	if got != p {
		t.Errorf("Dish(%d) = %+v, want %+v", p.ID, got, p)
	}
}

// The same dish typed by different people on different keyboards must land on
// one row, and the one already there keeps its name and status: a suggestion
// that repeats a family dish must not turn it into a proposal.
func TestCreateDishDeduplicatesByNameKey(t *testing.T) {
	st := testStore(t)
	borshch, _, err := st.CreateDish(model.Dish{Name: "Борщ"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	meat, _, err := st.CreateDish(model.Dish{Name: "Мʼясо по-французьки"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, c := range []struct {
		name string
		want model.Dish
	}{
		{" борщ ", borshch},
		{"БОРЩ", borshch},
		{"М’ясо  по-французьки", meat},
		{"м'ясо по-французьки", meat},
	} {
		got, existed, err := st.CreateDish(model.Dish{Name: c.name, Status: model.DishProposed, Note: "x"})
		if err != nil {
			t.Fatalf("create %q: %v", c.name, err)
		}
		if !existed {
			t.Errorf("%q: existed = false, want the existing dish", c.name)
		}
		if got != c.want {
			t.Errorf("%q: got %+v, want untouched %+v", c.name, got, c.want)
		}
	}

	all, err := st.Dishes()
	if err != nil {
		t.Fatalf("dishes: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("catalogue has %d dishes, want 2: %+v", len(all), all)
	}
}

// A dish on the plate was eaten, so whatever the family said to the
// suggestion, it is now one of theirs.
func TestEnsureDishReactivates(t *testing.T) {
	st := testStore(t)
	for _, status := range []string{model.DishRejected, model.DishProposed} {
		name := "Удон " + status
		d, _, err := st.CreateDish(model.Dish{Name: name, Status: status})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		got, existed, err := st.EnsureDish(model.Dish{Name: strings.ToUpper(name)})
		if err != nil {
			t.Fatalf("ensure: %v", err)
		}
		if !existed || got.ID != d.ID {
			t.Errorf("%s: ensure made a new dish %+v instead of reusing %d", status, got, d.ID)
		}
		if got.Status != model.DishActive {
			t.Errorf("%s: returned status %q, want active", status, got.Status)
		}
		stored, err := st.Dish(d.ID)
		if err != nil {
			t.Fatalf("dish: %v", err)
		}
		if stored.Status != model.DishActive {
			t.Errorf("%s: stored status %q, want active", status, stored.Status)
		}
	}
}

func TestEnsureDishCreatesActive(t *testing.T) {
	st := testStore(t)
	d, existed, err := st.EnsureDish(model.Dish{Name: "Деруни", Meal: "dinner", Status: model.DishProposed})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if existed {
		t.Error("existed = true for a new dish")
	}
	if d.Status != model.DishActive || d.Meal != "dinner" {
		t.Errorf("got %+v, want an active dinner dish", d)
	}
}

func TestDishesFiltersByStatus(t *testing.T) {
	st := testStore(t)
	mk := func(name, status string) int64 {
		d, _, err := st.CreateDish(model.Dish{Name: name, Status: status})
		if err != nil {
			t.Fatalf("create %q: %v", name, err)
		}
		return d.ID
	}
	plov := mk("Плов", model.DishActive)
	solianka := mk("Солянка", model.DishProposed)
	mk("Баранина", model.DishRejected)

	got, err := st.Dishes(model.DishActive, model.DishProposed)
	if err != nil {
		t.Fatalf("dishes: %v", err)
	}
	if len(got) != 2 || got[0].ID != plov || got[1].ID != solianka {
		t.Errorf("active+proposed = %+v, want Плов, Солянка", got)
	}

	all, err := st.Dishes()
	if err != nil {
		t.Fatalf("dishes: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("all = %d dishes, want 3", len(all))
	}
}

func TestSetDishStatus(t *testing.T) {
	st := testStore(t)
	d, _, err := st.CreateDish(model.Dish{Name: "Солянка", Status: model.DishProposed})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.SetDishStatus(d.ID, model.DishRejected); err != nil {
		t.Fatalf("set status: %v", err)
	}
	got, err := st.Dish(d.ID)
	if err != nil {
		t.Fatalf("dish: %v", err)
	}
	if got.Status != model.DishRejected {
		t.Errorf("status = %q, want rejected", got.Status)
	}
	// Setting the status it already has is still a success, not a miss.
	if err := st.SetDishStatus(d.ID, model.DishRejected); err != nil {
		t.Errorf("repeat set status: %v", err)
	}
}

func TestDishUnknownIDAndInvalidStatus(t *testing.T) {
	st := testStore(t)
	if _, err := st.Dish(999); !store.IsNotFound(err) {
		t.Errorf("Dish(999) err = %v, want not found", err)
	}
	if err := st.SetDishStatus(999, model.DishActive); !store.IsNotFound(err) {
		t.Errorf("SetDishStatus(999) err = %v, want not found", err)
	}

	d, _, err := st.CreateDish(model.Dish{Name: "Плов"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.SetDishStatus(d.ID, "eaten"); err == nil {
		t.Error("SetDishStatus accepted an unknown status")
	}
	if _, _, err := st.CreateDish(model.Dish{Name: "Вареники", Status: "maybe"}); err == nil {
		t.Error("CreateDish accepted an unknown status")
	}
	if _, _, err := st.CreateDish(model.Dish{Name: "Вареники", Meal: "breakfast"}); err == nil {
		t.Error("CreateDish accepted an unknown meal")
	}
	if _, _, err := st.CreateDish(model.Dish{Name: "   "}); err == nil {
		t.Error("CreateDish accepted an empty name")
	}
	if _, err := st.Dishes("maybe"); err == nil {
		t.Error("Dishes accepted an unknown status")
	}
}
