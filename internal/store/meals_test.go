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
	// A plan tapped after the photo of the plate writes nothing — it would
	// be a plan nobody ever closes — and says what was eaten instead.
	other, err := st.PlanMeal(plov.ID, today, model.MealLunch, "Оля", false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if other.ID != eaten.ID || other.Status != model.MealEaten {
		t.Errorf("plan over an eaten meal = %+v, want the eaten Борщ back", other)
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
		t.Errorf("meals = %+v, want only the eaten Борщ", got)
	}
	// The other meal of the day is still free to plan.
	if dinner, err := st.PlanMeal(plov.ID, today, model.MealDinner, "Оля", false); err != nil || dinner.Status != model.MealPlanned {
		t.Errorf("dinner plan = %+v, %v", dinner, err)
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

// The morning's pick stays next to what was really eaten: the history is
// what later shows which picks hold.
func TestRecordEatenOtherDishKeepsThePlan(t *testing.T) {
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
	if len(got) != 3 {
		t.Fatalf("meals = %+v, want planned Плов + eaten Деруни + planned Удон", got)
	}
	if got[0].DishID != plov.ID || got[0].Status != model.MealPlanned || got[0].Who != "Оля" {
		t.Errorf("lunch plan = %+v, want the pick kept", got[0])
	}
	if got[1].DishID != deruny.ID || got[1].Status != model.MealEaten || got[1].Who != "Олег" {
		t.Errorf("lunch = %+v, want eaten Деруни", got[1])
	}
	if got[2].ID != dinner.ID || got[2].Status != model.MealPlanned {
		t.Errorf("dinner = %+v, want the dinner plan untouched", got[2])
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

	// A rejected dish eaten anyway stays rejected: only the cooking log's
	// "create" (EnsureDish) overrules a "no", not a row in the journal.
	liver := seedDish(t, st, "Печінка", model.DishRejected)
	if _, _, err := st.RecordEaten(liver.ID, today, model.MealDinner, "Олег", false); err != nil {
		t.Fatalf("record rejected: %v", err)
	}
	if d, err := st.Dish(liver.ID); err != nil || d.Status != model.DishRejected {
		t.Errorf("eaten rejected dish status = %q (err %v), want rejected", d.Status, err)
	}

	// A plan alone is not eating it: the dish stays a proposal.
	udon := seedDish(t, st, "Удон", model.DishProposed)
	if _, err := st.PlanMeal(udon.ID, today, model.MealDinner, "Олег", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if d, err := st.Dish(udon.ID); err != nil || d.Status != model.DishProposed {
		t.Errorf("planned proposal status = %q (err %v), want proposed", d.Status, err)
	}
}

func TestDropPlans(t *testing.T) {
	st := testStore(t)
	borshch := seedDish(t, st, "Борщ", model.DishActive)
	plov := seedDish(t, st, "Плов", model.DishActive)
	const yesterday = "2026-09-23"

	old, err := st.PlanMeal(borshch.ID, yesterday, model.MealLunch, "Олег", false)
	if err != nil {
		t.Fatalf("plan yesterday: %v", err)
	}
	if _, err := st.PlanMeal(borshch.ID, today, model.MealLunch, "Олег", true); err != nil {
		t.Fatalf("plan: %v", err)
	}
	eaten, _, err := st.RecordEaten(plov.ID, today, model.MealDinner, "Олег", false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	// A dish that is not the planned one drops nothing.
	if err := st.DropPlans(today, model.MealLunch, plov.ID); err != nil {
		t.Fatalf("drop other dish: %v", err)
	}
	if got := mealsOn(t, st, today); len(got) != 2 {
		t.Fatalf("today = %+v, want the lunch plan and the eaten dinner", got)
	}
	// Any dish of the meal: the lunch plan goes, the eaten dinner and
	// yesterday's plan stay.
	if err := st.DropPlans(today, model.MealLunch, 0); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if got := mealsOn(t, st, today); len(got) != 1 || got[0].ID != eaten.ID {
		t.Errorf("today = %+v, want only the eaten dinner", got)
	}
	if err := st.DropPlans(today, model.MealDinner, 0); err != nil {
		t.Fatalf("drop dinner: %v", err)
	}
	if got := mealsOn(t, st, today); len(got) != 1 || got[0].ID != eaten.ID {
		t.Errorf("today = %+v, an eaten row must never be dropped", got)
	}
	if got := mealsOn(t, st, yesterday); len(got) != 1 || got[0].ID != old.ID {
		t.Errorf("yesterday = %+v, want its plan kept", got)
	}
	if err := st.DropPlans(today, "breakfast", 0); err == nil {
		t.Error("DropPlans accepted an unknown meal")
	}
}

func TestTurnDown(t *testing.T) {
	st := testStore(t)
	solianka := seedDish(t, st, "Солянка", model.DishProposed)
	plov := seedDish(t, st, "Плов", model.DishActive)

	if _, err := st.PlanMeal(solianka.ID, today, model.MealLunch, "Оля", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := st.PlanMeal(plov.ID, today, model.MealDinner, "Оля", false); err != nil {
		t.Fatalf("plan dinner: %v", err)
	}
	d, err := st.TurnDown(solianka.ID, today, model.MealLunch)
	if err != nil {
		t.Fatalf("turn down: %v", err)
	}
	if d.Status != model.DishRejected {
		t.Errorf("returned status = %q, want rejected", d.Status)
	}
	if got, err := st.Dish(solianka.ID); err != nil || got.Status != model.DishRejected {
		t.Errorf("stored status = %q (err %v), want rejected", got.Status, err)
	}
	if got := mealsOn(t, st, today); len(got) != 1 || got[0].DishID != plov.ID {
		t.Errorf("today = %+v, want only the dinner plan", got)
	}

	// A dish that is the family's own by now keeps its status; its plan
	// still goes.
	if _, err := st.PlanMeal(plov.ID, today, model.MealLunch, "Оля", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if d, err := st.TurnDown(plov.ID, today, model.MealLunch); err != nil || d.Status != model.DishActive {
		t.Errorf("turn down active = %+v, %v; want it left active", d, err)
	}
	if got := mealsOn(t, st, today); len(got) != 1 || got[0].Meal != model.MealDinner {
		t.Errorf("today = %+v, want the lunch plan gone and dinner kept", got)
	}

	if _, err := st.TurnDown(999, today, model.MealLunch); !store.IsNotFound(err) {
		t.Errorf("missing dish err = %v, want not found", err)
	}
}

func TestDishEaten(t *testing.T) {
	st := testStore(t)
	plov := seedDish(t, st, "Плов", model.DishActive)

	if _, err := st.PlanMeal(plov.ID, today, model.MealLunch, "", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if eaten, err := st.DishEaten(plov.ID); err != nil || eaten {
		t.Errorf("planned only: eaten=%v err=%v, want false", eaten, err)
	}
	if _, _, err := st.RecordEaten(plov.ID, today, model.MealLunch, "", false); err != nil {
		t.Fatalf("record: %v", err)
	}
	if eaten, err := st.DishEaten(plov.ID); err != nil || !eaten {
		t.Errorf("after eating: eaten=%v err=%v, want true", eaten, err)
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
