package mini

import (
	"fmt"
	"html"
	"net/http"
	"slices"
	"strings"
	"time"

	"familyhub/internal/menu"
	"familyhub/internal/model"
	"familyhub/internal/store"
	"familyhub/internal/valid"
)

// Кухня is what was eaten, a meal at a time, and the picker that records it.
//
// The picker exists because the evening check in the group is a keyboard, and
// a keyboard answers one dish: the first tap closes the meal, so "the mash and
// cutlets from yesterday, and a different salad" could not be said. Here any
// number of dishes are ticked, the whole catalogue can be searched, and a dish
// nobody has named yet is created by typing it. Writes go through
// store.SetEaten, the same RecordEaten path the photo and the evening check
// take, so a plan is confirmed rather than doubled and a proposal that was
// eaten becomes the family's.

// kitchenDays is how far back the Кухня screen reaches: a week, enough to fill
// in a dinner nobody answered and short enough to read on one screen.
const kitchenDays = 7

// maxQuickDishes caps the chips the picker opens with, before any search.
const maxQuickDishes = 12

// maxPickedDishes is a fuse on one meal; a real dinner is two or three.
const maxPickedDishes = 20

// maxDishName keeps a typed name a name, not a paragraph.
const maxDishName = 80

// errDishGone answers a set naming a dish deleted since the screen loaded.
var errDishGone = valid.FieldError{Field: "dishes", Message: "однієї зі страв уже немає — онови сторінку"}

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
		day := kitchenDayDTO{Date: date, Label: dayLabel(d, now)}
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
		DayLabel: dayLabel(day, rt.now().In(rt.loc)),
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
	// They are created here, active: the family just ate or chose them.
	NewDishes []newDish `json:"newDishes"`
}

// newDish carries what a dish is created with. Meal and Days decide when the
// morning menu may offer it, so a delivery the model marked weekend-only does
// not turn up on a Tuesday; empty means any.
type newDish struct {
	Name string `json:"name"`
	Meal string `json:"meal"`
	Days string `json:"days"`
}

func (rt *Router) handlePickerSave(w http.ResponseWriter, r *http.Request) {
	who, bad := rt.v.authenticate(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	date, meal, day, bad := rt.mealKey(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	var body pickerWrite
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	if err := body.check(); err != nil {
		rt.writeError(w, err, "save meal")
		return
	}
	ids, err := rt.dishIDs(body)
	if err != nil {
		rt.log.Error("mini: create dish", "err", err)
		rt.fail(w, errInternal)
		return
	}

	// Yesterday's dish eaten again is yesterday's pot — the same mark the
	// evening check's "↩" buttons and the morning's «Доїдаємо» leave.
	yesterday, err := rt.store.EatenOn(day.AddDate(0, 0, -1).Format(time.DateOnly))
	if err != nil {
		rt.log.Error("mini: yesterday's meals", "err", err)
		rt.fail(w, errInternal)
		return
	}
	leftover := map[int64]bool{}
	for _, m := range yesterday {
		leftover[m.DishID] = true
	}

	before, err := rt.store.MealsOn(date)
	if err != nil {
		rt.log.Error("mini: meals before save", "err", err)
		rt.fail(w, errInternal)
		return
	}
	if err := rt.store.SetEaten(date, string(meal), who.Name, ids, leftover); err != nil {
		if store.IsNotFound(err) {
			rt.writeError(w, errDishGone, "save meal")
			return
		}
		rt.log.Error("mini: save meal", "err", err)
		rt.fail(w, errInternal)
		return
	}
	// Both messages say what was eaten — the evening check asks it, the
	// morning menu shows it in its ✅ lines. Best-effort: the meal is saved,
	// and a Telegram hiccup must not be reported as a failed save.
	if rt.menu != nil {
		if err := rt.menu.RedrawEvening(date); err != nil {
			rt.log.Error("mini: redraw evening check", "err", err)
		}
		if err := rt.menu.RedrawMenu(date); err != nil {
			rt.log.Error("mini: redraw menu", "err", err)
		}
	}
	rt.announceDay(date, meal, day, who.Name, before)
	rt.writeJSON(w, http.StatusOK, map[string]int{"saved": len(ids)})
}

// check keeps one meal's set within reason before anything is written.
func (p pickerWrite) check() error {
	if len(p.DishIDs)+len(p.NewDishes) > maxPickedDishes {
		return valid.FieldError{Field: "dishes", Message: "забагато страв для одного разу"}
	}
	for _, d := range p.NewDishes {
		if n := len([]rune(strings.TrimSpace(d.Name))); n == 0 || n > maxDishName {
			return valid.FieldError{Field: "dishes", Message: "назва страви порожня або задовга"}
		}
		if (d.Meal != "" && !model.ValidDishMeal(d.Meal)) || (d.Days != "" && !model.ValidDishDays(d.Days)) {
			return valid.FieldError{Field: "dishes", Message: "невідомий час для страви"}
		}
	}
	return nil
}

// dishIDs is the set as ids, the typed names created first. They are created
// outside the meal's transaction: a dish that then fails to be recorded is
// still a dish the family named, and saving again finds it by name instead of
// creating a second one.
func (rt *Router) dishIDs(p pickerWrite) ([]int64, error) {
	ids := make([]int64, 0, len(p.DishIDs)+len(p.NewDishes))
	seen := map[int64]bool{}
	pick := func(id int64) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range p.DishIDs {
		pick(id)
	}
	for _, nd := range p.NewDishes {
		d, _, err := rt.store.EnsureDish(model.Dish{Name: nd.Name, Meal: nd.Meal, Days: nd.Days})
		if err != nil {
			return nil, err
		}
		pick(d.ID)
	}
	return ids, nil
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

// announceDay tells the group what the day was, once every meal is answered:
// eaten, or «Не вдома» on the evening check. The redraws above are edits, and
// an edit in Telegram is silent — filling the day in before the evening check
// meant the group heard nothing at all, the check then having nothing to ask.
// A save that changed nothing says nothing. Best-effort, like every group
// message.
func (rt *Router) announceDay(date string, meal menu.Meal, day time.Time, by string, before []model.MealEntry) {
	if rt.notify == nil {
		return
	}
	after, err := rt.store.MealsOn(date)
	if err != nil {
		rt.log.Error("mini: meals after save", "err", err)
		return
	}
	if slices.Equal(eatenIDs(before, meal), eatenIDs(after, meal)) {
		return
	}
	var away []string
	if msg, err := rt.store.EveningMessage(date); err == nil {
		away = msg.Away
	} else if !store.IsNotFound(err) {
		rt.log.Error("mini: evening message", "err", err)
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🍽 <b>Що їли · %s</b>", html.EscapeString(dayLabel(day, rt.now().In(rt.loc))))
	if by != "" {
		fmt.Fprintf(&sb, " (%s)", html.EscapeString(by))
	}
	for _, m := range []menu.Meal{menu.Lunch, menu.Dinner} {
		var names []string
		for _, row := range after {
			if row.Meal == string(m) && row.Status == model.MealEaten {
				names = append(names, html.EscapeString(row.Dish))
			}
		}
		switch {
		case len(names) > 0:
			fmt.Fprintf(&sb, "\n%s: %s", m.Title(), strings.Join(names, " · "))
		case slices.Contains(away, string(m)):
			fmt.Fprintf(&sb, "\n%s: не вдома", m.Title())
		default:
			return // a meal still open: the day is not done
		}
	}
	if err := rt.notify.NotifyHTML(sb.String()); err != nil {
		rt.log.Error("mini: announce day", "err", err)
	}
}

func eatenIDs(rows []model.MealEntry, meal menu.Meal) []int64 {
	var ids []int64
	for _, m := range rows {
		if m.Meal == string(meal) && m.Status == model.MealEaten {
			ids = append(ids, m.DishID)
		}
	}
	slices.Sort(ids)
	return ids
}
