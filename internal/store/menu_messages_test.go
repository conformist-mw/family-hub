package store_test

import (
	"slices"
	"sync"
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

func TestMenuMessageReturnsWhatWasSaved(t *testing.T) {
	st := testStore(t)

	if _, err := st.MenuMessage(today); !store.IsNotFound(err) {
		t.Fatalf("menu before any save: err = %v, want not found", err)
	}

	want := model.MenuMessage{
		Date: today, ChatID: -100, MessageID: 42,
		Shown: map[string][]int64{model.MealLunch: {3, 1, 2}, model.MealDinner: {7}},
	}
	if err := st.SaveMenuMessage(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := st.MenuMessage(today)
	if err != nil {
		t.Fatalf("menu: %v", err)
	}
	if got.ChatID != want.ChatID || got.MessageID != want.MessageID {
		t.Fatalf("got chat %d message %d", got.ChatID, got.MessageID)
	}
	// The order is the order of offering; it is kept so the set reads back
	// the way it was written.
	if !slices.Equal(got.Shown[model.MealLunch], []int64{3, 1, 2}) ||
		!slices.Equal(got.Shown[model.MealDinner], []int64{7}) {
		t.Fatalf("shown = %v", got.Shown)
	}

	if _, err := st.MenuMessage("2026-09-23"); !store.IsNotFound(err) {
		t.Fatalf("another day: err = %v, want not found", err)
	}
}

// A re-send replaces the row: the newer message is the one whose buttons are
// live, and a date may only ever have one menu.
func TestSaveMenuMessageReplacesTheDay(t *testing.T) {
	st := testStore(t)
	if err := st.SaveMenuMessage(model.MenuMessage{Date: today, ChatID: -100, MessageID: 1,
		Shown: map[string][]int64{model.MealLunch: {1}}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := st.SaveMenuMessage(model.MenuMessage{Date: today, ChatID: -100, MessageID: 2}); err != nil {
		t.Fatalf("save again: %v", err)
	}
	got, err := st.MenuMessage(today)
	if err != nil {
		t.Fatalf("menu: %v", err)
	}
	if got.MessageID != 2 || len(got.Shown) != 0 {
		t.Fatalf("got %+v, want the second message with nothing shown", got)
	}
}

func TestSaveMenuMessageRejectsABadDate(t *testing.T) {
	st := testStore(t)
	if err := st.SaveMenuMessage(model.MenuMessage{Date: "24.09.2026"}); err == nil {
		t.Fatal("saved a menu under a date no day will ever match")
	}
}

func TestAppendShownAccumulates(t *testing.T) {
	st := testStore(t)
	if err := st.SaveMenuMessage(model.MenuMessage{Date: today, ChatID: -100, MessageID: 1,
		Shown: map[string][]int64{model.MealLunch: {1, 2, 3}, model.MealDinner: {9}}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := st.AppendShown(today, model.MealLunch, []int64{4, 5, 6}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// A shuffle that circles back offers some of the same dishes again; the
	// set keeps each once.
	if err := st.AppendShown(today, model.MealLunch, []int64{2, 7}); err != nil {
		t.Fatalf("append again: %v", err)
	}
	got, err := st.MenuMessage(today)
	if err != nil {
		t.Fatalf("menu: %v", err)
	}
	if want := []int64{1, 2, 3, 4, 5, 6, 7}; !slices.Equal(got.Shown[model.MealLunch], want) {
		t.Fatalf("lunch shown = %v, want %v", got.Shown[model.MealLunch], want)
	}
	if !slices.Equal(got.Shown[model.MealDinner], []int64{9}) {
		t.Fatalf("dinner shown = %v, a lunch shuffle touched dinner", got.Shown[model.MealDinner])
	}
}

// Two 🔀 taps at the same moment — one on lunch, one on dinner, or both on
// the same meal — must each keep what they added.
func TestConcurrentAppendShownLosesNothing(t *testing.T) {
	st := testStore(t)
	if err := st.SaveMenuMessage(model.MenuMessage{Date: today, ChatID: -100, MessageID: 1}); err != nil {
		t.Fatalf("save: %v", err)
	}
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			meal := model.MealLunch
			if i%2 == 1 {
				meal = model.MealDinner
			}
			errs <- st.AppendShown(today, meal, []int64{int64(100 + i)})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, err := st.MenuMessage(today)
	if err != nil {
		t.Fatalf("menu: %v", err)
	}
	if l, d := len(got.Shown[model.MealLunch]), len(got.Shown[model.MealDinner]); l+d != n {
		t.Fatalf("kept %d lunch + %d dinner of %d appends: %v", l, d, n, got.Shown)
	}
}

func TestAppendShownWithoutAMenu(t *testing.T) {
	st := testStore(t)
	if err := st.AppendShown(today, model.MealLunch, []int64{1}); !store.IsNotFound(err) {
		t.Fatalf("err = %v, want not found", err)
	}
	if err := st.AppendShown(today, "breakfast", []int64{1}); err == nil {
		t.Fatal("appended to a meal the menu does not have")
	}
}

func TestLastShownKeepsTheLatestDay(t *testing.T) {
	st := testStore(t)
	for _, m := range []model.MenuMessage{
		{Date: "2026-09-22", Shown: map[string][]int64{model.MealLunch: {1, 2}, model.MealDinner: {3}}},
		{Date: "2026-09-23", Shown: map[string][]int64{model.MealDinner: {2}}},
		{Date: "2026-09-24"},
	} {
		if err := st.SaveMenuMessage(m); err != nil {
			t.Fatalf("save %s: %v", m.Date, err)
		}
	}
	got, err := st.LastShown()
	if err != nil {
		t.Fatalf("last shown: %v", err)
	}
	want := map[int64]string{1: "2026-09-22", 2: "2026-09-23", 3: "2026-09-22"}
	if len(got) != len(want) {
		t.Fatalf("last shown = %v, want %v", got, want)
	}
	for id, date := range want {
		if got[id].Format(time.DateOnly) != date {
			t.Errorf("dish %d last shown %v, want %s", id, got[id], date)
		}
	}
}
