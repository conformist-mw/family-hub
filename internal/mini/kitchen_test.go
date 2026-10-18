package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// testNow is 2026-08-06, so these are "yesterday" and "today".
const (
	kitchenYesterday = "2026-08-05"
	kitchenToday     = "2026-08-06"
)

func seedDish(t *testing.T, st *store.Store, name, status string) model.Dish {
	t.Helper()
	d, _, err := st.CreateDish(model.Dish{Name: name, Status: status})
	if err != nil {
		t.Fatalf("dish %q: %v", name, err)
	}
	return d
}

func getPicker(t *testing.T, h http.Handler, path string) pickerDTO {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, body = %s", path, rec.Code, rec.Body)
	}
	var dto pickerDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return dto
}

// The case the picker exists for: yesterday's mash and cutlets again, with a
// salad nobody had named before.
func TestPickerRecordsSeveralDishesAtOnce(t *testing.T) {
	st := testStore(t)
	h := testRouter(t, st, []int64{42}, 42)

	mash := seedDish(t, st, "Пюре з котлетами", model.DishActive)
	soup := seedDish(t, st, "Борщ", model.DishActive)
	seedDish(t, st, "Печінка", model.DishRejected)
	if _, _, err := st.RecordEaten(mash.ID, kitchenYesterday, model.MealDinner, "", false); err != nil {
		t.Fatalf("seed yesterday: %v", err)
	}
	if _, err := st.PlanMeal(soup.ID, kitchenToday, model.MealDinner, "", false); err != nil {
		t.Fatalf("seed plan: %v", err)
	}

	path := "/mini/api/meals/" + kitchenToday + "/dinner"
	before := getPicker(t, h, path)
	if before.Title != "Вечеря" || before.DayLabel != "Сьогодні, 6 серпня" || len(before.Selected) != 0 {
		t.Errorf("picker = %+v", before)
	}
	// The plan leads, yesterday's pot follows.
	if len(before.Quick) < 2 || before.Quick[0] != soup.ID || before.Quick[1] != mash.ID {
		t.Errorf("quick = %v, want plan then yesterday", before.Quick)
	}
	for _, d := range before.Dishes {
		if d.Name == "Печінка" {
			t.Errorf("rejected dish offered: %+v", d)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPut, path,
		`{"dishIds":[`+itoa(mash.ID)+`],"newDishes":[{"name":"Салат з редиски"}]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status = %d, body = %s", rec.Code, rec.Body)
	}

	var eaten []string
	for _, m := range mealsOnDay(t, st, kitchenToday) {
		if m.Meal == model.MealDinner && m.Status == model.MealEaten {
			eaten = append(eaten, m.Dish)
			if m.Dish == mash.Name && !m.Leftover {
				t.Errorf("yesterday's dish not marked leftover: %+v", m)
			}
		}
	}
	slices.Sort(eaten)
	if want := []string{"Пюре з котлетами", "Салат з редиски"}; !slices.Equal(eaten, want) {
		t.Errorf("eaten = %v, want %v", eaten, want)
	}

	after := getPicker(t, h, path)
	if len(after.Selected) != 2 {
		t.Errorf("reopened picker selected = %v, want both dishes ticked", after.Selected)
	}
}

func TestPickerRefusesTomorrowAndUnknownMeals(t *testing.T) {
	st := testStore(t)
	h := testRouter(t, st, []int64{42}, 42)
	for _, path := range []string{
		"/mini/api/meals/2026-08-07/dinner",
		"/mini/api/meals/" + kitchenToday + "/breakfast",
		"/mini/api/meals/not-a-date/lunch",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assertAPIError(t, rec, http.StatusBadRequest, "bad_request")
	}
}

func TestKitchenListsTheWeekNewestFirst(t *testing.T) {
	st := testStore(t)
	h := testRouter(t, st, []int64{42}, 42)

	soup := seedDish(t, st, "Борщ", model.DishActive)
	plov := seedDish(t, st, "Плов", model.DishActive)
	if _, _, err := st.RecordEaten(soup.ID, kitchenYesterday, model.MealLunch, "", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := st.PlanMeal(plov.ID, kitchenToday, model.MealDinner, "", false); err != nil {
		t.Fatalf("seed plan: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mini/api/kitchen", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var body struct {
		Days []kitchenDayDTO `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Days) != kitchenDays {
		t.Fatalf("days = %d, want %d", len(body.Days), kitchenDays)
	}
	today, yesterday := body.Days[0], body.Days[1]
	if today.Date != kitchenToday || len(today.Meals[1].Planned) != 1 || today.Meals[1].Planned[0] != "Плов" || len(today.Meals[1].Eaten) != 0 {
		t.Errorf("today = %+v", today)
	}
	if yesterday.Label != "Вчора, 5 серпня" || len(yesterday.Meals[0].Eaten) != 1 || yesterday.Meals[0].Eaten[0] != "Борщ" {
		t.Errorf("yesterday = %+v", yesterday)
	}
}

func mealsOnDay(t *testing.T, st *store.Store, date string) []model.MealEntry {
	t.Helper()
	got, err := st.MealsOn(date)
	if err != nil {
		t.Fatalf("meals on %s: %v", date, err)
	}
	return got
}

// A meal recorded here is news for both messages in the group: the evening
// check that asked about it, and the morning menu that shows it.
func TestPickerSaveRedrawsTheDaysMessages(t *testing.T) {
	st := testStore(t)
	b := &board{}
	h := menuRouter(t, st, b)
	soup := seedDish(t, st, "Борщ", model.DishActive)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPut, "/mini/api/meals/"+kitchenToday+"/lunch",
		`{"dishIds":[`+itoa(soup.ID)+`]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status = %d, body = %s", rec.Code, rec.Body)
	}
	if !slices.Equal(b.evenings, []string{kitchenToday}) || !slices.Equal(b.redrawn, []string{kitchenToday}) {
		t.Errorf("evenings = %v, menus = %v, want today's of each", b.evenings, b.redrawn)
	}
}

// The group hears about the day once, when its last meal is answered — not
// for every meal, and not again for a save that changed nothing.
func TestPickerAnnouncesTheFinishedDay(t *testing.T) {
	st := testStore(t)
	fam := &group{}
	h, err := NewRouter(st, discardLogger(), Config{
		BotToken: testToken, AllowedUsers: []int64{42}, DevUser: 0,
		Loc: time.UTC, Now: func() time.Time { return testNow }, Notifier: fam,
	})
	if err != nil {
		t.Fatal(err)
	}
	soup := seedDish(t, st, "Борщ", model.DishActive)
	steak := seedDish(t, st, "Стейк", model.DishActive)
	salad := seedDish(t, st, "Салат", model.DishActive)

	save := func(meal, ids string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, signedRequest(t, http.MethodPut, "/mini/api/meals/"+kitchenToday+"/"+meal, `{"dishIds":[`+ids+`]}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("save %s: status = %d, body = %s", meal, rec.Code, rec.Body)
		}
	}
	save("lunch", itoa(soup.ID))
	if len(fam.sent) != 0 {
		t.Fatalf("announced half a day: %q", fam.sent)
	}
	save("dinner", itoa(steak.ID)+","+itoa(salad.ID))
	save("dinner", itoa(salad.ID)+","+itoa(steak.ID))
	if len(fam.sent) != 1 {
		t.Fatalf("group got %d messages, want 1: %q", len(fam.sent), fam.sent)
	}
	for _, want := range []string{"Що їли · Сьогодні, 6 серпня", "(Тест)", "Обід: Борщ", "Вечеря: Стейк · Салат"} {
		if !strings.Contains(fam.sent[0], want) {
			t.Errorf("message lacks %q: %s", want, fam.sent[0])
		}
	}
}
