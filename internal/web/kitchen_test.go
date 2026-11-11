package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// fakeBoard stands in for the bot's two kitchen messages.
type fakeBoard struct{ menus, evenings []string }

func (b *fakeBoard) RedrawMenu(date string) error { b.menus = append(b.menus, date); return nil }
func (b *fakeBoard) RedrawEvening(date string) error {
	b.evenings = append(b.evenings, date)
	return nil
}

type kitchenFixture struct {
	h      http.Handler
	st     *store.Store
	board  *fakeBoard
	notify *fakeNotifier
	dish   map[string]int64
}

func kitchenApp(t *testing.T) kitchenFixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(database)
	f := kitchenFixture{st: st, board: &fakeBoard{}, notify: &fakeNotifier{}, dish: map[string]int64{}}
	for _, name := range []string{"Борщ", "Плов", "Деруни", "Вареники", "Гречка з печінкою", "Омлет", "Курячий суп"} {
		d, _, err := st.CreateDish(model.Dish{Name: name})
		if err != nil {
			t.Fatalf("create dish: %v", err)
		}
		f.dish[name] = d.ID
	}
	f.h = NewRouter(database, slog.New(slog.NewTextHandler(io.Discard, nil)), "", nil, f.notify, nil, f.board)
	return f
}

func dayOff(offset int) string { return time.Now().AddDate(0, 0, offset).Format(time.DateOnly) }

func (f kitchenFixture) meals(t *testing.T, date, meal, status string) []string {
	t.Helper()
	rows, err := f.st.MealsOn(date)
	if err != nil {
		t.Fatalf("meals: %v", err)
	}
	var out []string
	for _, m := range rows {
		if m.Meal == meal && m.Status == status {
			out = append(out, m.Dish)
		}
	}
	return out
}

func dishID(n int64) string { return strconv.FormatInt(n, 10) }

// The journal is the point of the page: a day reads as what was eaten, and a
// meal nobody confirmed reads as a plan, not as a meal.
func TestTheJournalShowsWhatWasEatenAndWhatWasOnlyPlanned(t *testing.T) {
	f := kitchenApp(t)
	if _, _, err := f.st.RecordEaten(f.dish["Борщ"], dayOff(-1), model.MealLunch, "Олег", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.PlanMeal(f.dish["Плов"], dayOff(-1), model.MealDinner, "Олег", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.PlanMeal(f.dish["Деруни"], dayOff(1), model.MealLunch, "Олег", false); err != nil {
		t.Fatal(err)
	}

	body := getBody(t, f.h, "/kitchen")
	for _, want := range []string{"Борщ", "план: Плов", "план: Деруни", `id="d-` + dayOff(1) + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("journal is missing %q", want)
		}
	}
	// A day ahead with nothing planned is not a journal entry.
	if strings.Contains(body, `id="d-`+dayOff(2)+`"`) {
		t.Error("an empty day ahead is listed")
	}
	// The day ahead is planned on the menu page, not recorded as eaten.
	if strings.Contains(body, "/kitchen/meals/"+dayOff(1)+"/") {
		t.Error("a day ahead offers to record what was eaten")
	}
}

func TestTheJournalPagesBack(t *testing.T) {
	f := kitchenApp(t)
	old := dayOff(-20)
	if _, _, err := f.st.RecordEaten(f.dish["Вареники"], old, model.MealDinner, "", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(getBody(t, f.h, "/kitchen"), "Вареники") {
		t.Fatal("the first page reaches back three weeks")
	}
	body := getBody(t, f.h, "/kitchen?to="+dayOff(-14))
	if !strings.Contains(body, "Вареники") {
		t.Fatal("the second page does not show the meal from three weeks ago")
	}
	if !strings.Contains(body, `href="/kitchen"`) {
		t.Fatal("the second page has no way back to the first")
	}
}

// A meal is recorded the way the Mini App records one: the whole set, a
// typed name becoming a dish, yesterday's dish a leftover — and the bot's
// messages redrawn and the finished day announced.
func TestAMealIsRecordedFromTheDesk(t *testing.T) {
	f := kitchenApp(t)
	today := dayOff(0)
	if _, _, err := f.st.RecordEaten(f.dish["Борщ"], dayOff(-1), model.MealDinner, "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.RecordEaten(f.dish["Омлет"], today, model.MealLunch, "", false); err != nil {
		t.Fatal(err)
	}

	rec := post(t, f.h, "/kitchen/meals/"+today+"/dinner", url.Values{
		"dish": {dishID(f.dish["Борщ"]), dishID(f.dish["Деруни"])},
		"new":  {"Салат з редиски"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d\n%s", rec.Code, rec.Body.String())
	}
	got := f.meals(t, today, model.MealDinner, model.MealEaten)
	if strings.Join(got, ",") != "Борщ,Деруни,Салат з редиски" {
		t.Fatalf("dinner = %v", got)
	}
	rows, _ := f.st.MealsOn(today)
	for _, m := range rows {
		if m.Dish == "Борщ" && !m.Leftover {
			t.Error("yesterday's borscht is not marked a leftover")
		}
	}
	if len(f.board.evenings) != 1 || len(f.board.menus) != 1 {
		t.Errorf("redraws: evening %v, menu %v", f.board.evenings, f.board.menus)
	}
	if len(f.notify.html) != 1 || !strings.Contains(f.notify.html[0], "Що їли") {
		t.Fatalf("the finished day was not announced: %v", f.notify.html)
	}

	// Unticking everything takes the meal back.
	if rec := post(t, f.h, "/kitchen/meals/"+today+"/dinner", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("clear = %d", rec.Code)
	}
	if got := f.meals(t, today, model.MealDinner, model.MealEaten); len(got) != 0 {
		t.Fatalf("dinner after clearing = %v", got)
	}
}

func TestTomorrowsMealCannotBeRecordedAsEaten(t *testing.T) {
	f := kitchenApp(t)
	rec := post(t, f.h, "/kitchen/meals/"+dayOff(1)+"/lunch", url.Values{"dish": {dishID(f.dish["Борщ"])}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("save for tomorrow = %d", rec.Code)
	}
	if got := f.meals(t, dayOff(1), model.MealLunch, model.MealEaten); len(got) != 0 {
		t.Fatalf("tomorrow's lunch was written: %v", got)
	}
}

func TestTheMealFormOpensWithWhatWasEatenTicked(t *testing.T) {
	f := kitchenApp(t)
	if _, _, err := f.st.RecordEaten(f.dish["Плов"], dayOff(-2), model.MealLunch, "", false); err != nil {
		t.Fatal(err)
	}
	body := getBody(t, f.h, "/kitchen/meals/"+dayOff(-2)+"/lunch")
	ticked := regexp.MustCompile(`value="(\d+)" checked`).FindAllStringSubmatch(body, -1)
	if len(ticked) != 1 || ticked[0][1] != dishID(f.dish["Плов"]) {
		t.Fatalf("ticked = %v, want only Плов", ticked)
	}
}

// The menu plans both meals in one save, for a day ahead too, and leaves an
// eaten meal alone.
func TestTheMenuIsMadeForTomorrow(t *testing.T) {
	f := kitchenApp(t)
	tomorrow := dayOff(1)
	if rec := getBody(t, f.h, "/kitchen/menu/"+tomorrow); !strings.Contains(rec, "Ще варіанти") {
		t.Fatal("tomorrow's menu page has no suggestions button")
	}
	rec := post(t, f.h, "/kitchen/menu/"+tomorrow, url.Values{
		"dish_lunch":  {dishID(f.dish["Борщ"])},
		"dish_dinner": {dishID(f.dish["Плов"]), dishID(f.dish["Омлет"])},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d\n%s", rec.Code, rec.Body.String())
	}
	if got := f.meals(t, tomorrow, model.MealLunch, model.MealPlanned); strings.Join(got, ",") != "Борщ" {
		t.Errorf("lunch plan = %v", got)
	}
	if got := f.meals(t, tomorrow, model.MealDinner, model.MealPlanned); strings.Join(got, ",") != "Плов,Омлет" {
		t.Errorf("dinner plan = %v", got)
	}
	// Tomorrow has no morning message yet; there is nothing to redraw.
	if len(f.board.menus) != 0 {
		t.Errorf("redrew the menu for a day ahead: %v", f.board.menus)
	}
}

func TestTodaysMenuRedrawsTheMorningMessageAndSparesAnEatenMeal(t *testing.T) {
	f := kitchenApp(t)
	today := dayOff(0)
	if _, _, err := f.st.RecordEaten(f.dish["Омлет"], today, model.MealLunch, "", false); err != nil {
		t.Fatal(err)
	}
	body := getBody(t, f.h, "/kitchen/menu/"+today)
	if !strings.Contains(body, "уже з'їли") {
		t.Fatal("an eaten lunch is offered for planning")
	}
	rec := post(t, f.h, "/kitchen/menu/"+today, url.Values{"dish_dinner": {dishID(f.dish["Вареники"])}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d", rec.Code)
	}
	if got := f.meals(t, today, model.MealLunch, model.MealEaten); strings.Join(got, ",") != "Омлет" {
		t.Errorf("lunch = %v", got)
	}
	if got := f.meals(t, today, model.MealDinner, model.MealPlanned); strings.Join(got, ",") != "Вареники" {
		t.Errorf("dinner plan = %v", got)
	}
	if len(f.board.menus) != 1 || f.board.menus[0] != today {
		t.Errorf("menu redraws = %v", f.board.menus)
	}
}

// 🔀 adds fresh suggestions and keeps every tick; nothing is saved by it.
func TestMoreSuggestionsKeepWhatWasTicked(t *testing.T) {
	f := kitchenApp(t)
	tomorrow := dayOff(1)
	rec := post(t, f.h, "/kitchen/menu/"+tomorrow, url.Values{
		"more":           {"lunch"},
		"offered_lunch":  {dishID(f.dish["Борщ"])},
		"dish_dinner":    {dishID(f.dish["Плов"])},
		"offered_dinner": {dishID(f.dish["Плов"])},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("more = %d", rec.Code)
	}
	body := rec.Body.String()
	lunchOffered := regexp.MustCompile(`name="offered_lunch" value="(\d+)"`).FindAllStringSubmatch(body, -1)
	if len(lunchOffered) < 2 {
		t.Fatalf("lunch suggestions after 🔀 = %v, want the old one and more", lunchOffered)
	}
	for _, m := range lunchOffered[1:] {
		if m[1] == dishID(f.dish["Плов"]) {
			t.Error("🔀 offered the dish ticked for dinner")
		}
	}
	if !strings.Contains(body, `name="dish_dinner" value="`+dishID(f.dish["Плов"])+`" checked`) {
		t.Error("dinner's tick was lost")
	}
	if got := f.meals(t, tomorrow, model.MealDinner, model.MealPlanned); len(got) != 0 {
		t.Errorf("🔀 saved a plan: %v", got)
	}
}

func TestAPastMenuGoesToTheJournal(t *testing.T) {
	f := kitchenApp(t)
	rec := get(t, f.h, "/kitchen/menu/"+dayOff(-1))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/kitchen" {
		t.Fatalf("past menu = %d → %q", rec.Code, rec.Header().Get("Location"))
	}
}
