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

// The picker sends the whole set: what is ticked is eaten, what was unticked
// is not any more, and the morning's plan for a dish that was eaten turns
// eaten rather than gaining a twin.
func TestSetEatenMakesTheSetExact(t *testing.T) {
	st := testStore(t)
	pure := seedDish(t, st, "Пюре з котлетами", model.DishActive)
	salad := seedDish(t, st, "Салат з капусти", model.DishActive)
	borsch := seedDish(t, st, "Борщ", model.DishActive)
	novel := seedDish(t, st, "Шакшука", model.DishProposed)

	if _, err := st.PlanMeal(pure.ID, today, model.MealDinner, "Оля", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, _, err := st.RecordEaten(borsch.ID, today, model.MealDinner, "Олег", false); err != nil {
		t.Fatalf("eat: %v", err)
	}
	// Lunch is another meal and must not be touched.
	if _, _, err := st.RecordEaten(borsch.ID, today, model.MealLunch, "Олег", false); err != nil {
		t.Fatalf("eat lunch: %v", err)
	}

	ids := []int64{pure.ID, salad.ID, novel.ID}
	if err := st.SetEaten(today, model.MealDinner, "Олег", ids, map[int64]bool{pure.ID: true}); err != nil {
		t.Fatalf("set eaten: %v", err)
	}

	got := map[int64]model.MealEntry{}
	for _, m := range mealsOn(t, st, today) {
		if m.Meal == model.MealDinner {
			got[m.DishID] = m
		}
	}
	if len(got) != 3 {
		t.Fatalf("dinner = %+v, want exactly the three ticked dishes", got)
	}
	if p := got[pure.ID]; p.ID == 0 || p.Status != model.MealEaten || !p.Leftover {
		t.Errorf("planned dish = %+v, want the plan confirmed as a leftover", p)
	}
	if got[salad.ID].Status != model.MealEaten || got[novel.ID].Status != model.MealEaten {
		t.Errorf("dinner = %+v", got)
	}
	if d, _ := st.Dish(novel.ID); d.Status != model.DishActive {
		t.Errorf("eaten proposal status = %q, want active", d.Status)
	}
	lunch := 0
	for _, m := range mealsOn(t, st, today) {
		if m.Meal == model.MealLunch {
			lunch++
		}
	}
	if lunch != 1 {
		t.Errorf("lunch rows = %d, want the borscht untouched", lunch)
	}

	// Unticking everything leaves the meal unanswered.
	if err := st.SetEaten(today, model.MealDinner, "Олег", nil, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	for _, m := range mealsOn(t, st, today) {
		if m.Meal == model.MealDinner {
			t.Errorf("dinner row left after clearing: %+v", m)
		}
	}
}

func TestSetEatenIsAllOrNothing(t *testing.T) {
	st := testStore(t)
	borsch := seedDish(t, st, "Борщ", model.DishActive)
	if _, _, err := st.RecordEaten(borsch.ID, today, model.MealDinner, "Олег", false); err != nil {
		t.Fatalf("eat: %v", err)
	}
	err := st.SetEaten(today, model.MealDinner, "Олег", []int64{9999}, nil)
	if !store.IsNotFound(err) {
		t.Fatalf("missing dish err = %v, want not found", err)
	}
	if got := mealsOn(t, st, today); len(got) != 1 || got[0].DishID != borsch.ID {
		t.Errorf("after failed set = %+v, want the borscht kept", got)
	}
}

func TestFrequentDishesRankByUseAndSkipRejected(t *testing.T) {
	st := testStore(t)
	plov := seedDish(t, st, "Плов", model.DishActive)
	udon := seedDish(t, st, "Удон", model.DishActive)
	gone := seedDish(t, st, "Печінка", model.DishActive)
	seedDish(t, st, "Ніколи не їли", model.DishActive)

	eat := func(id int64, date string) {
		t.Helper()
		if _, _, err := st.RecordEaten(id, date, model.MealDinner, "", false); err != nil {
			t.Fatalf("eat: %v", err)
		}
	}
	eat(udon.ID, "2026-09-20")
	eat(plov.ID, "2026-09-21")
	eat(plov.ID, "2026-09-22")
	eat(gone.ID, "2026-09-23")
	eat(gone.ID, "2026-09-24")
	eat(gone.ID, "2026-09-25")
	if err := st.SetDishStatus(gone.ID, model.DishRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	got, err := st.FrequentDishes(10)
	if err != nil {
		t.Fatalf("frequent: %v", err)
	}
	if len(got) != 2 || got[0].ID != plov.ID || got[1].ID != udon.ID {
		t.Errorf("frequent = %+v, want Плов then Удон", got)
	}
}

func TestSetPlanMakesThePlanExact(t *testing.T) {
	st := testStore(t)
	soup := seedDish(t, st, "Борщ", model.DishActive)
	salad := seedDish(t, st, "Салат", model.DishActive)
	plov := seedDish(t, st, "Плов", model.DishActive)

	if _, err := st.PlanMeal(plov.ID, today, model.MealLunch, "Оля", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := st.PlanMeal(soup.ID, today, model.MealDinner, "Оля", false); err != nil {
		t.Fatalf("plan: %v", err)
	}
	ok, err := st.SetPlan(today, model.MealLunch, "Олег", []int64{soup.ID, salad.ID}, map[int64]bool{soup.ID: true})
	if err != nil || !ok {
		t.Fatalf("set plan: ok=%v err=%v", ok, err)
	}
	var lunch []string
	for _, m := range mealsOn(t, st, today) {
		if m.Meal == model.MealLunch {
			if m.Status != model.MealPlanned {
				t.Errorf("lunch row not a plan: %+v", m)
			}
			if m.DishID == soup.ID && !m.Leftover {
				t.Errorf("leftover mark lost: %+v", m)
			}
			lunch = append(lunch, m.Dish)
		} else if m.DishID != soup.ID {
			t.Errorf("dinner touched: %+v", m)
		}
	}
	if len(lunch) != 2 || lunch[0] != "Борщ" || lunch[1] != "Салат" {
		t.Errorf("lunch = %v, want Борщ and Салат, the Плов plan gone", lunch)
	}
}

func TestSetPlanLeavesAnEatenMealAlone(t *testing.T) {
	st := testStore(t)
	soup := seedDish(t, st, "Борщ", model.DishActive)
	plov := seedDish(t, st, "Плов", model.DishActive)
	if _, _, err := st.RecordEaten(soup.ID, today, model.MealLunch, "", false); err != nil {
		t.Fatalf("eat: %v", err)
	}
	ok, err := st.SetPlan(today, model.MealLunch, "Олег", []int64{plov.ID}, nil)
	if err != nil || ok {
		t.Fatalf("set plan on an eaten meal: ok=%v err=%v, want nothing written", ok, err)
	}
	if got := mealsOn(t, st, today); len(got) != 1 || got[0].DishID != soup.ID {
		t.Errorf("rows = %+v", got)
	}

	if _, err := st.SetPlan(today, model.MealDinner, "", []int64{9999}, nil); !store.IsNotFound(err) {
		t.Errorf("missing dish err = %v", err)
	}
}
