package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

type board struct{ redrawn []string }

func (b *board) RedrawMenu(date string) error {
	b.redrawn = append(b.redrawn, date)
	return nil
}

func menuRouter(t *testing.T, st *store.Store, b MenuBoard) http.Handler {
	t.Helper()
	h, err := NewRouter(st, discardLogger(), Config{
		BotToken: testToken, AllowedUsers: []int64{42}, DevUser: 42,
		Loc: time.UTC, Now: func() time.Time { return testNow }, Menu: b,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return h
}

func getPlanner(t *testing.T, h http.Handler) plannerDTO {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mini/api/menu/"+kitchenToday, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("planner: status = %d, body = %s", rec.Code, rec.Body)
	}
	var dto plannerDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return dto
}

// The screen opens with what the morning message offered, and saving a meal
// with several dishes writes them all as the plan and redraws the message.
func TestPlannerOffersTheMorningAndSavesSeveralDishes(t *testing.T) {
	st := testStore(t)
	b := &board{}
	h := menuRouter(t, st, b)

	soup := dish(t, st, "Борщ", model.DishActive)
	plov := dish(t, st, "Плов", model.DishActive)
	udon := dish(t, st, "Удон", model.DishActive)
	mash := dish(t, st, "Пюре з котлетами", model.DishActive)
	if _, _, err := st.RecordEaten(mash.ID, kitchenYesterday, model.MealDinner, "", false); err != nil {
		t.Fatalf("seed yesterday: %v", err)
	}
	if err := st.SaveMenuMessage(model.MenuMessage{
		Date: kitchenToday, ChatID: -1, MessageID: 7,
		Shown: map[string][]int64{"lunch": {soup.ID, plov.ID}, "dinner": {udon.ID}},
	}); err != nil {
		t.Fatalf("seed menu: %v", err)
	}

	dto := getPlanner(t, h)
	if !slices.Equal(dto.Leftovers, []int64{mash.ID}) {
		t.Errorf("leftovers = %v, want yesterday's mash", dto.Leftovers)
	}
	if len(dto.Meals) != 2 || !slices.Equal(dto.Meals[0].Offered, []int64{soup.ID, plov.ID}) ||
		!slices.Equal(dto.Meals[1].Offered, []int64{udon.ID}) {
		t.Fatalf("meals = %+v, want the morning's offer", dto.Meals)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPut, "/mini/api/menu/"+kitchenToday,
		`{"lunch":{"dishIds":[`+itoa(mash.ID)+`,`+itoa(soup.ID)+`],"newNames":["Салат з редиски"]}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status = %d, body = %s", rec.Code, rec.Body)
	}
	if !slices.Equal(b.redrawn, []string{kitchenToday}) {
		t.Errorf("redrawn = %v, want today's menu", b.redrawn)
	}

	var lunch []string
	for _, m := range mealsOnDay(t, st, kitchenToday) {
		if m.Meal != model.MealLunch || m.Status != model.MealPlanned {
			t.Errorf("unexpected row %+v", m)
			continue
		}
		lunch = append(lunch, m.Dish)
		if m.DishID == mash.ID && !m.Leftover {
			t.Errorf("yesterday's dish not marked leftover: %+v", m)
		}
	}
	slices.Sort(lunch)
	if want := []string{"Борщ", "Пюре з котлетами", "Салат з редиски"}; !slices.Equal(lunch, want) {
		t.Errorf("lunch plan = %v, want %v", lunch, want)
	}
	if got := getPlanner(t, h).Meals[0].Planned; len(got) != 3 {
		t.Errorf("reopened plan = %v, want the three dishes", got)
	}
}

// "More" never repeats what is on screen and is remembered as shown, so the
// next request moves on and tomorrow's menu ages these too.
func TestPlannerMoreMovesOn(t *testing.T) {
	st := testStore(t)
	h := menuRouter(t, st, nil)
	var ids []int64
	for _, n := range []string{"Борщ", "Плов", "Удон", "Деруни", "Вареники", "Омлет", "Гречка"} {
		ids = append(ids, dish(t, st, n, model.DishActive).ID)
	}
	if err := st.SaveMenuMessage(model.MenuMessage{
		Date: kitchenToday, ChatID: -1, MessageID: 7,
		Shown: map[string][]int64{"lunch": ids[:3]},
	}); err != nil {
		t.Fatalf("seed menu: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/menu/"+kitchenToday+"/lunch/more",
		`{"offer":[`+itoa(ids[0])+`,`+itoa(ids[1])+`,`+itoa(ids[2])+`],"alsoOut":[]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("more: status = %d, body = %s", rec.Code, rec.Body)
	}
	var got struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.IDs) == 0 {
		t.Fatal("no more suggestions with four dishes left")
	}
	for _, id := range got.IDs {
		if slices.Contains(ids[:3], id) {
			t.Errorf("offered %d again", id)
		}
	}
	if offered := getPlanner(t, h).Meals[0].Offered; len(offered) != 3+len(got.IDs) {
		t.Errorf("offered after more = %v, want the morning's three plus the new ones", offered)
	}
}

func TestPlannerKeepsItsHandsOffAnEatenMealAndThePast(t *testing.T) {
	st := testStore(t)
	h := menuRouter(t, st, nil)
	soup := dish(t, st, "Борщ", model.DishActive)
	plov := dish(t, st, "Плов", model.DishActive)
	if _, _, err := st.RecordEaten(soup.ID, kitchenToday, model.MealLunch, "", false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPut, "/mini/api/menu/"+kitchenToday,
		`{"lunch":{"dishIds":[`+itoa(plov.ID)+`]}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status = %d, body = %s", rec.Code, rec.Body)
	}
	if got := mealsOnDay(t, st, kitchenToday); len(got) != 1 || got[0].DishID != soup.ID {
		t.Errorf("rows = %+v, want the eaten lunch alone", got)
	}
	if eaten := getPlanner(t, h).Meals[0].Eaten; len(eaten) != 1 || eaten[0] != "Борщ" {
		t.Errorf("planner lunch eaten = %v", eaten)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mini/api/menu/"+kitchenYesterday, nil))
	assertAPIError(t, rec, http.StatusConflict, "past_menu")
}
