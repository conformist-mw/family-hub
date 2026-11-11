package mini

import (
	"net/http"
	"time"

	"familyhub/internal/kitchen"
	"familyhub/internal/menu"
	"familyhub/internal/model"
)

// Кухня is what was eaten, a meal at a time, and the picker that records it.
//
// The picker exists because the evening check in the group is a keyboard, and
// a keyboard answers one dish: the first tap closes the meal, so "the mash and
// cutlets from yesterday, and a different salad" could not be said. Here any
// number of dishes are ticked, the whole catalogue can be searched, and a dish
// nobody has named yet is created by typing it. Writes go through
// kitchen.Recorder, shared with the web form.

// kitchenDays is how far back the Кухня screen reaches: a week, enough to fill
// in a dinner nobody answered and short enough to read on one screen.
const kitchenDays = 7

// maxQuickDishes caps the chips the picker opens with, before any search.
const maxQuickDishes = 12

type kitchenMealDTO struct {
	Meal    string   `json:"meal"`
	Title   string   `json:"title"`
	Eaten   []string `json:"eaten"`
	Planned []string `json:"planned"` // the morning's plan, shown while nothing is eaten
}

type kitchenDayDTO struct {
	Date  string           `json:"date"`
	Label string           `json:"label"`
	Meals []kitchenMealDTO `json:"meals"`
}

func (rt *Router) handleKitchen(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	now := rt.now().In(rt.loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, rt.loc)
	from := today.AddDate(0, 0, -(kitchenDays - 1))

	rows, err := rt.store.MealsBetween(from.Format(time.DateOnly), today.Format(time.DateOnly))
	if err != nil {
		rt.log.Error("mini: kitchen meals", "err", err)
		rt.fail(w, errInternal)
		return
	}
	type key struct{ date, meal string }
	byMeal := map[key][]model.MealEntry{}
	for _, m := range rows {
		k := key{m.Date, m.Meal}
		byMeal[k] = append(byMeal[k], m)
	}

	days := make([]kitchenDayDTO, 0, kitchenDays)
	for d := today; !d.Before(from); d = d.AddDate(0, 0, -1) {
		date := d.Format(time.DateOnly)
		day := kitchenDayDTO{Date: date, Label: model.DayLabel(d, now)}
		for _, meal := range []menu.Meal{menu.Lunch, menu.Dinner} {
			dto := kitchenMealDTO{Meal: string(meal), Title: meal.Title(), Eaten: []string{}, Planned: []string{}}
			for _, m := range byMeal[key{date, string(meal)}] {
				if m.Status == model.MealEaten {
					dto.Eaten = append(dto.Eaten, m.Dish)
				} else {
					dto.Planned = append(dto.Planned, m.Dish)
				}
			}
			day.Meals = append(day.Meals, dto)
		}
		days = append(days, day)
	}
	rt.writeJSON(w, http.StatusOK, map[string]any{"days": days})
}

type pickerDishDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Proposed bool   `json:"proposed"`
}

type pickerDTO struct {
	Date     string `json:"date"`
	Meal     string `json:"meal"`
	Title    string `json:"title"`
	DayLabel string `json:"dayLabel"`
	// Selected is what is eaten now; the picker opens with it ticked.
	Selected []int64 `json:"selected"`
	// Planned is the morning's plan.
	Planned []int64 `json:"planned"`
	// Quick are the chips shown before a search: the plan, yesterday's pot,
	// then what is eaten most. Selected dishes are shown anyway.
	Quick []int64 `json:"quick"`
	// Dishes is the whole searchable catalogue — everything but the rejected,
	// plus anything already on this meal whatever its status.
	Dishes []pickerDishDTO `json:"dishes"`
}

// mealKey reads {date}/{meal} from the path. A future date is refused: a meal
// is recorded after it is eaten, and a tap on tomorrow would only be a typo.
func (rt *Router) mealKey(r *http.Request) (string, menu.Meal, time.Time, *apiError) {
	date, meal := r.PathValue("date"), r.PathValue("meal")
	day, err := time.ParseInLocation(time.DateOnly, date, rt.loc)
	if err != nil || !model.ValidMeal(meal) {
		return "", "", time.Time{}, errBadRequest
	}
	if date > rt.now().In(rt.loc).Format(time.DateOnly) {
		return "", "", time.Time{}, errBadRequest
	}
	return date, menu.Meal(meal), day, nil
}

func (rt *Router) handlePicker(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	date, meal, day, bad := rt.mealKey(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	dto, err := rt.picker(date, meal, day)
	if err != nil {
		rt.log.Error("mini: meal picker", "err", err)
		rt.fail(w, errInternal)
		return
	}
	rt.writeJSON(w, http.StatusOK, dto)
}

func (rt *Router) picker(date string, meal menu.Meal, day time.Time) (pickerDTO, error) {
	dto := pickerDTO{
		Date: date, Meal: string(meal), Title: meal.Title(),
		DayLabel: model.DayLabel(day, rt.now().In(rt.loc)),
		Selected: []int64{}, Planned: []int64{}, Quick: []int64{},
	}

	var listed map[int64]bool
	var err error
	if dto.Dishes, listed, err = rt.catalogue(); err != nil {
		return dto, err
	}

	rows, err := rt.store.MealsOn(date)
	if err != nil {
		return dto, err
	}
	for _, m := range rows {
		if m.Meal != string(meal) {
			continue
		}
		if !listed[m.DishID] {
			dto.Dishes = append(dto.Dishes, pickerDishDTO{ID: m.DishID, Name: m.Dish, Proposed: m.DishStatus == model.DishProposed})
			listed[m.DishID] = true
		}
		if m.Status == model.MealEaten {
			dto.Selected = append(dto.Selected, m.DishID)
		} else {
			dto.Planned = append(dto.Planned, m.DishID)
		}
	}

	seen := map[int64]bool{}
	add := func(id int64) {
		if id != 0 && listed[id] && !seen[id] && len(dto.Quick) < maxQuickDishes {
			seen[id] = true
			dto.Quick = append(dto.Quick, id)
		}
	}
	for _, id := range dto.Planned {
		add(id)
	}
	yesterday, err := rt.store.EatenOn(day.AddDate(0, 0, -1).Format(time.DateOnly))
	if err != nil {
		return dto, err
	}
	for _, id := range menu.Leftovers(yesterday) {
		add(id)
	}
	frequent, err := rt.store.FrequentDishes(maxQuickDishes)
	if err != nil {
		return dto, err
	}
	for _, d := range frequent {
		add(d.ID)
	}
	return dto, nil
}

type pickerWrite struct {
	DishIDs []int64 `json:"dishIds"`
	// NewDishes are dishes the catalogue does not have, named the way
	// /mini/api/dishes/resolve named them (or as typed, without a model).
	// They are created on save, active: the family just ate or chose them.
	NewDishes []newDish `json:"newDishes"`
}

// newDish carries what a dish is created with; see kitchen.NewDish.
type newDish struct {
	Name string `json:"name"`
	Meal string `json:"meal"`
	Days string `json:"days"`
}

func (p pickerWrite) set() kitchen.Set {
	s := kitchen.Set{DishIDs: p.DishIDs}
	for _, d := range p.NewDishes {
		s.NewDishes = append(s.NewDishes, kitchen.NewDish{Name: d.Name, Meal: d.Meal, Days: d.Days})
	}
	return s
}

func (rt *Router) handlePickerSave(w http.ResponseWriter, r *http.Request) {
	who, bad := rt.v.authenticate(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	date, meal, _, bad := rt.mealKey(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	var body pickerWrite
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	n, err := rt.meals.Eaten(date, meal, who.Name, body.set())
	if err != nil {
		rt.writeError(w, err, "save meal")
		return
	}
	rt.writeJSON(w, http.StatusOK, map[string]int{"saved": n})
}

// catalogue is every dish a picker may search — all but the rejected — and
// which ids it holds.
func (rt *Router) catalogue() ([]pickerDishDTO, map[int64]bool, error) {
	dishes, err := rt.store.Dishes(model.DishActive, model.DishProposed)
	if err != nil {
		return nil, nil, err
	}
	out := make([]pickerDishDTO, 0, len(dishes))
	listed := make(map[int64]bool, len(dishes))
	for _, d := range dishes {
		out = append(out, pickerDishDTO{ID: d.ID, Name: d.Name, Proposed: d.Status == model.DishProposed})
		listed[d.ID] = true
	}
	return out, listed, nil
}
