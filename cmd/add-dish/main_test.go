package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

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
