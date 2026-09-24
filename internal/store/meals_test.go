package store_test

import (
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

func seedDish(t *testing.T, st *store.Store, name, status string) model.Dish {
	t.Helper()
	d, _, err := st.CreateDish(model.Dish{Name: name, Status: status})
	if err != nil {
		t.Fatalf("create dish %q: %v", name, err)
	}
	return d
}

func mealsOn(t *testing.T, st *store.Store, date string) []model.MealEntry {
	t.Helper()
	got, err := st.MealsOn(date)
	if err != nil {
		t.Fatalf("meals on %s: %v", date, err)
	}
	return got
}

const today = "2026-09-24"

func TestPlanMealReplacesPlan(t *testing.T) {
	st := testStore(t)
	plov := seedDish(t, st, "Плов", model.DishActive)
	deruny := seedDish(t, st, "Деруни", model.DishActive)
	udon := seedDish(t, st, "Удон", model.DishActive)

	first, err := st.PlanMeal(plov.ID, today, model.MealLunch, "Олег", false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if first.Status != model.MealPlanned || first.Dish != "Плов" || first.Who != "Олег" {
		t.Errorf("plan row = %+v", first)
	}
	if _, err := st.PlanMeal(udon.ID, today, model.MealDinner, "Олег", false); err != nil {
		t.Fatalf("plan dinner: %v", err)
	}
	if _, err := st.PlanMeal(deruny.ID, today, model.MealLunch, "Оля", false); err != nil {
		t.Fatalf("replan: %v", err)
	}

	got := mealsOn(t, st, today)
	if len(got) != 2 || got[0].DishID != deruny.ID || got[0].Who != "Оля" || got[1].DishID != udon.ID {
		t.Fatalf("after replan = %+v, want Деруни for lunch and Удон for dinner", got)
	}

	// Tapping the same dish again keeps the row, only its who changes.
	again, err := st.PlanMeal(deruny.ID, today, model.MealLunch, "Олег", true)
	if err != nil {
		t.Fatalf("same plan: %v", err)
	}
	if again.ID != got[0].ID || again.Who != "Олег" || !again.Leftover {
		t.Errorf("same plan = %+v, want row %d updated in place", again, got[0].ID)
	}
}

func TestPlanMealLeavesEaten(t *testing.T) {
	st := testStore(t)
	borshch := seedDish(t, st, "Борщ", model.DishActive)
	plov := seedDish(t, st, "Плов", model.DishActive)

	eaten, _, err := st.RecordEaten(borshch.ID, today, model.MealLunch, "Олег", false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := st.PlanMeal(plov.ID, today, model.MealLunch, "Оля", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Planning the dish that was already eaten does not demote it.
	same, err := st.PlanMeal(borshch.ID, today, model.MealLunch, "Оля", false)
	if err != nil {
		t.Fatalf("plan eaten dish: %v", err)
	}
	if same.ID != eaten.ID || same.Status != model.MealEaten || same.Who != "Олег" {
		t.Errorf("plan over eaten = %+v, want the eaten row untouched", same)
	}

	got := mealsOn(t, st, today)
	if len(got) != 1 || got[0].ID != eaten.ID || got[0].Status != model.MealEaten {
		t.Errorf("meals = %+v, want only the eaten Борщ (re-planning it dropped Плов)", got)
	}
}

func TestRecordEatenClosesPlanOfSameDish(t *testing.T) {
	st := testStore(t)
	borshch := seedDish(t, st, "Борщ", model.DishActive)

	plan, err := st.PlanMeal(borshch.ID, today, model.MealLunch, "Оля", true)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got, already, err := st.RecordEaten(borshch.ID, today, model.MealLunch, "", false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if already {
		t.Error("already = true for a planned row")
	}
	if got.ID != plan.ID || got.Status != model.MealEaten {
		t.Errorf("record = %+v, want plan row %d turned eaten", got, plan.ID)
	}
	if got.Who != "Оля" || !got.Leftover {
		t.Errorf("record = %+v, want planner's who and leftover kept", got)
	}
	if n := len(mealsOn(t, st, today)); n != 1 {
		t.Errorf("%d rows, want 1", n)
	}
}

func TestRecordEatenOtherDishDropsPlan(t *testing.T) {
	st := testStore(t)
	plov := seedDish(t, st, "Плов", model.DishActive)
	deruny := seedDish(t, st, "Деруни", model.DishActive)
	udon := seedDish(t, st, "Удон", model.DishActive)

	if _, err := st.PlanMeal(plov.ID, today, model.MealLunch, "Оля", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	dinner, err := st.PlanMeal(udon.ID, today, model.MealDinner, "Оля", false)
	if err != nil {
		t.Fatalf("plan dinner: %v", err)
	}
	if _, _, err := st.RecordEaten(deruny.ID, today, model.MealLunch, "Олег", false); err != nil {
		t.Fatalf("record: %v", err)
	}

	got := mealsOn(t, st, today)
	if len(got) != 2 {
		t.Fatalf("meals = %+v, want eaten Деруни + planned Удон", got)
	}
	if got[0].DishID != deruny.ID || got[0].Status != model.MealEaten || got[0].Who != "Олег" {
		t.Errorf("lunch = %+v, want eaten Деруни", got[0])
	}
	if got[1].ID != dinner.ID || got[1].Status != model.MealPlanned {
		t.Errorf("dinner = %+v, want the dinner plan untouched", got[1])
	}
}

func TestRecordEatenTwiceIsAlready(t *testing.T) {
	st := testStore(t)
	borshch := seedDish(t, st, "Борщ", model.DishActive)

	first, already, err := st.RecordEaten(borshch.ID, today, model.MealDinner, "Олег", false)
	if err != nil || already {
		t.Fatalf("first record: already=%v err=%v", already, err)
	}
	second, already, err := st.RecordEaten(borshch.ID, today, model.MealDinner, "Оля", true)
	if err != nil {
		t.Fatalf("second record: %v", err)
	}
	if !already {
		t.Error("already = false for a repeat")
	}
	if second != first {
		t.Errorf("repeat = %+v, want untouched %+v", second, first)
	}
	// The same dish at the other meal is a different meal, not a repeat.
	if _, already, err := st.RecordEaten(borshch.ID, today, model.MealLunch, "Олег", false); err != nil || already {
		t.Errorf("lunch record: already=%v err=%v", already, err)
	}
}

func TestRecordEatenActivatesProposed(t *testing.T) {
	st := testStore(t)
	solianka := seedDish(t, st, "Солянка", model.DishProposed)

	got, _, err := st.RecordEaten(solianka.ID, today, model.MealLunch, "Олег", false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if got.DishStatus != model.DishActive {
		t.Errorf("row dish status = %q, want active", got.DishStatus)
	}
	d, err := st.Dish(solianka.ID)
	if err != nil {
		t.Fatalf("dish: %v", err)
	}
	if d.Status != model.DishActive {
		t.Errorf("dish status = %q, want active", d.Status)
	}

	// A plan alone is not eating it: the dish stays a proposal.
	udon := seedDish(t, st, "Удон", model.DishProposed)
	if _, err := st.PlanMeal(udon.ID, today, model.MealDinner, "Олег", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if d, _ := st.Dish(udon.ID); d.Status != model.DishProposed {
		t.Errorf("planned proposal status = %q, want proposed", d.Status)
	}
}

func TestConfirmMeal(t *testing.T) {
	st := testStore(t)
	plov := seedDish(t, st, "Плов", model.DishProposed)
	plan, err := st.PlanMeal(plov.ID, today, model.MealDinner, "Оля", false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got, already, err := st.ConfirmMeal(plan.ID, "Олег")
	if err != nil || already {
		t.Fatalf("confirm: already=%v err=%v", already, err)
	}
	if got.ID != plan.ID || got.Status != model.MealEaten || got.Who != "Олег" || got.DishStatus != model.DishActive {
		t.Errorf("confirm = %+v", got)
	}
	if _, already, err := st.ConfirmMeal(plan.ID, "Олег"); err != nil || !already {
		t.Errorf("repeat confirm: already=%v err=%v", already, err)
	}
	if _, _, err := st.ConfirmMeal(999, "Олег"); !store.IsNotFound(err) {
		t.Errorf("confirm missing err = %v, want not found", err)
	}
}

func TestDeleteMealKeepsOtherDays(t *testing.T) {
	st := testStore(t)
	borshch := seedDish(t, st, "Борщ", model.DishActive)
	const yesterday = "2026-09-23"

	old, _, err := st.RecordEaten(borshch.ID, yesterday, model.MealLunch, "Олег", false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	plan, err := st.PlanMeal(borshch.ID, today, model.MealLunch, "Олег", true)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := st.DeleteMeal(plan.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := mealsOn(t, st, today); len(got) != 0 {
		t.Errorf("today = %+v, want empty", got)
	}
	if got := mealsOn(t, st, yesterday); len(got) != 1 || got[0].ID != old.ID {
		t.Errorf("yesterday = %+v, want the eaten row kept", got)
	}
	if err := st.DeleteMeal(plan.ID); !store.IsNotFound(err) {
		t.Errorf("repeat delete err = %v, want not found", err)
	}
}

func TestEatenOnSkipsPlans(t *testing.T) {
	st := testStore(t)
	borshch := seedDish(t, st, "Борщ", model.DishActive)
	plov := seedDish(t, st, "Плов", model.DishActive)

	if _, err := st.PlanMeal(plov.ID, today, model.MealDinner, "", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, _, err := st.RecordEaten(borshch.ID, today, model.MealLunch, "", false); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := st.EatenOn(today)
	if err != nil {
		t.Fatalf("eaten on: %v", err)
	}
	if len(got) != 1 || got[0].DishID != borshch.ID {
		t.Errorf("eaten = %+v, want only Борщ", got)
	}
}

func TestLastSeenCountsPlans(t *testing.T) {
	st := testStore(t)
	borshch := seedDish(t, st, "Борщ", model.DishActive)
	plov := seedDish(t, st, "Плов", model.DishActive)
	never := seedDish(t, st, "Удон", model.DishActive)

	if _, _, err := st.RecordEaten(borshch.ID, "2026-09-20", model.MealLunch, "", false); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, _, err := st.RecordEaten(borshch.ID, "2026-09-22", model.MealDinner, "", false); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := st.PlanMeal(plov.ID, "2026-09-23", model.MealLunch, "", false); err != nil {
		t.Fatalf("plan: %v", err)
	}

	got, err := st.LastSeen()
	if err != nil {
		t.Fatalf("last seen: %v", err)
	}
	want := map[int64]string{borshch.ID: "2026-09-22", plov.ID: "2026-09-23"}
	if len(got) != len(want) {
		t.Errorf("last seen = %v, want %v", got, want)
	}
	for id, date := range want {
		if got[id].Format(time.DateOnly) != date {
			t.Errorf("dish %d last seen %v, want %s", id, got[id], date)
		}
	}
	if _, ok := got[never.ID]; ok {
		t.Error("a dish never planned or eaten has a last-seen date")
	}
}

func TestMealWritesRejectBadInput(t *testing.T) {
	st := testStore(t)
	plov := seedDish(t, st, "Плов", model.DishActive)

	if _, err := st.PlanMeal(999, today, model.MealLunch, "", false); !store.IsNotFound(err) {
		t.Errorf("plan missing dish err = %v, want not found", err)
	}
	if _, _, err := st.RecordEaten(999, today, model.MealLunch, "", false); !store.IsNotFound(err) {
		t.Errorf("record missing dish err = %v, want not found", err)
	}
	if _, err := st.PlanMeal(plov.ID, today, "breakfast", "", false); err == nil {
		t.Error("PlanMeal accepted an unknown meal")
	}
	if _, _, err := st.RecordEaten(plov.ID, "24.09.2026", model.MealLunch, "", false); err == nil {
		t.Error("RecordEaten accepted a malformed date")
	}
}
