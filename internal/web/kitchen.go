package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"familyhub/internal/kitchen"
	"familyhub/internal/menu"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The Кухня world: what was eaten, and the menu for the days ahead. The bot
// and the Mini App have had both since the menu moved off Mealie; the desk had
// neither, so a week of meals could only be read by scrolling the group.
//
// Every write goes through kitchen.Recorder, the Mini App's own: a meal saved
// here redraws the bot's messages and announces the finished day the way one
// saved on a phone does. The pages are plain forms — a checkbox per dish — so
// the picker that needs a search box on a phone is a list with a filter here.

// journalDays is one page of the journal: two weeks, enough to see what has
// not been cooked in a while.
const journalDays = 14

// menuAhead is how far ahead a menu can be made. /menu plans tomorrow; a week
// is the furthest anybody here plans, and further would only be a typo.
const menuAhead = 7

// quickDishes caps the "usual" dishes a meal form puts first.
const quickDishes = 12

type journalMeal struct {
	Meal    string
	Title   string
	Eaten   []model.MealEntry
	Planned []model.MealEntry
	Away    bool // «Не вдома» on the evening check
}

type journalDay struct {
	Date  string
	Label string
	// Past is a day whose meals can be recorded as eaten (today included);
	// Plannable a day whose menu can still be made (today and ahead).
	Past, Plannable bool
	Meals           []journalMeal
}

type journalData struct {
	Days []journalDay
	// Older and Newer are the ?to= of the neighbouring pages; Newer is empty
	// on the first page, NewerFirst when the next page is the first.
	Older, Newer string
	NewerFirst   bool
	Today        string
}

func (a *App) handleKitchenJournal(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	today := dayStart(now)
	to := today
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := model.ParseDate(v)
		if err != nil {
			http.Error(w, "погана дата", http.StatusBadRequest)
			return
		}
		if t.Before(today) {
			to = t
		}
	}
	from := to.AddDate(0, 0, -(journalDays - 1))
	// The first page also carries the days ahead that already have a plan:
	// tomorrow's menu made tonight is what that page is opened to check.
	last := to
	if to.Equal(today) {
		last = today.AddDate(0, 0, menuAhead)
	}

	rows, err := a.Store.MealsBetween(from.Format(time.DateOnly), last.Format(time.DateOnly))
	if err != nil {
		a.serverError(w, err)
		return
	}
	type key struct{ date, meal string }
	byMeal := map[key][]model.MealEntry{}
	hasMeals := map[string]bool{}
	for _, m := range rows {
		k := key{m.Date, m.Meal}
		byMeal[k] = append(byMeal[k], m)
		hasMeals[m.Date] = true
	}

	data := journalData{Today: today.Format(time.DateOnly)}
	for d := last; !d.Before(from); d = d.AddDate(0, 0, -1) {
		date := d.Format(time.DateOnly)
		future := d.After(today)
		if future && !hasMeals[date] {
			continue
		}
		var away []string
		if !future {
			msg, err := a.Store.EveningMessage(date)
			if err != nil && !store.IsNotFound(err) {
				a.serverError(w, err)
				return
			}
			away = msg.Away
		}
		day := journalDay{
			Date: date, Label: model.DayLabel(d, now),
			Past: !future, Plannable: !d.Before(today),
		}
		for _, meal := range kitchen.Meals {
			jm := journalMeal{Meal: string(meal), Title: meal.Title(), Away: slices.Contains(away, string(meal))}
			for _, m := range byMeal[key{date, string(meal)}] {
				if m.Status == model.MealEaten {
					jm.Eaten = append(jm.Eaten, m)
				} else {
					jm.Planned = append(jm.Planned, m)
				}
			}
			day.Meals = append(day.Meals, jm)
		}
		data.Days = append(data.Days, day)
	}
	data.Older = from.AddDate(0, 0, -1).Format(time.DateOnly)
	if to.Before(today) {
		newer := to.AddDate(0, 0, journalDays)
		if newer.Before(today) {
			data.Newer = newer.Format(time.DateOnly)
		} else {
			data.NewerFirst = true
		}
	}
	a.render(w, "kitchen.html", "Кухня", "kitchen", data)
}

// dishOption is one checkbox of a meal's picker.
type dishOption struct {
	ID       int64
	Name     string
	Proposed bool // 🆕: suggested by the model, not cooked yet
	Leftover bool // ↩: eaten the day before
	Checked  bool
}

// dishPicker is one meal's checkboxes: the dishes that matter for it first,
// then the rest of the catalogue behind a filter.
type dishPicker struct {
	Field    string // the form field the ticked ids go under
	NewField string // the field typed names go under
	NewValue string
	Top      []dishOption
	Rest     []dishOption
}

type mealFormData struct {
	Date, Meal, Title, DayLabel string
	Planned                     []string
	Picker                      dishPicker
	Error                       string
}

// mealKey reads {date}/{meal}. A future date is refused: a meal is recorded
// after it is eaten, and tomorrow's is a plan, made on the menu page.
func mealKey(r *http.Request) (string, menu.Meal, time.Time, bool) {
	date, meal := r.PathValue("date"), r.PathValue("meal")
	day, err := model.ParseDate(date)
	if err != nil || !model.ValidMeal(meal) || day.After(dayStart(time.Now())) {
		return "", "", time.Time{}, false
	}
	return date, menu.Meal(meal), day, true
}

func (a *App) handleMealEdit(w http.ResponseWriter, r *http.Request) {
	date, meal, day, ok := mealKey(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := a.mealForm(date, meal, day, nil, "")
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "kitchen_meal.html", "Що їли", "kitchen", data)
}

func (a *App) handleMealSave(w http.ResponseWriter, r *http.Request) {
	date, meal, day, ok := mealKey(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "погана форма", http.StatusBadRequest)
		return
	}
	set := formSet(r.PostForm, "dish", "new")
	if _, err := a.Meals.Eaten(date, meal, actorName(r), set); err != nil {
		if !kitchen.IsInvalid(err) {
			a.serverError(w, err)
			return
		}
		data, ferr := a.mealForm(date, meal, day, set.DishIDs, r.PostForm.Get("new"))
		if ferr != nil {
			a.serverError(w, ferr)
			return
		}
		data.Error = err.Error()
		a.render(w, "kitchen_meal.html", "Що їли", "kitchen", data)
		return
	}
	http.Redirect(w, r, journalURL(day)+"#d-"+date, http.StatusSeeOther)
}

// mealForm opens with what was eaten ticked — or, when the form comes back
// with an error, with what was ticked then — and puts first the plan,
// yesterday's pot and the dishes eaten most.
func (a *App) mealForm(date string, meal menu.Meal, day time.Time, checked []int64, newValue string) (mealFormData, error) {
	data := mealFormData{
		Date: date, Meal: string(meal), Title: meal.Title(),
		DayLabel: model.DayLabel(day, time.Now()),
	}
	rows, err := a.Store.MealsOn(date)
	if err != nil {
		return data, err
	}
	var eaten, planned []int64
	extra := map[int64]model.Dish{}
	for _, m := range rows {
		if m.Meal != string(meal) {
			continue
		}
		extra[m.DishID] = model.Dish{ID: m.DishID, Name: m.Dish, Status: m.DishStatus}
		if m.Status == model.MealEaten {
			eaten = append(eaten, m.DishID)
		} else {
			planned = append(planned, m.DishID)
			data.Planned = append(data.Planned, m.Dish)
		}
	}
	if checked == nil {
		checked = eaten
	}
	pot, err := a.Kitchen.Leftovers(day)
	if err != nil {
		return data, err
	}
	frequent, err := a.Store.FrequentDishes(quickDishes)
	if err != nil {
		return data, err
	}
	first := slices.Concat(checked, planned, pot)
	for _, d := range frequent {
		first = append(first, d.ID)
	}
	data.Picker, err = a.picker("dish", "new", newValue, first, checked, pot, extra)
	return data, err
}

// picker lays out one meal's checkboxes: first (in order, once each, the
// ticked ones always) on top, the rest of the catalogue below by name.
// extra names dishes outside the catalogue — a rejected dish still in a plan —
// so a tick on one is not lost.
func (a *App) picker(field, newField, newValue string, first, checked, pot []int64, extra map[int64]model.Dish) (dishPicker, error) {
	p := dishPicker{Field: field, NewField: newField, NewValue: newValue}
	dishes, err := a.Store.Dishes(model.DishActive, model.DishProposed)
	if err != nil {
		return p, err
	}
	byID := make(map[int64]model.Dish, len(dishes)+len(extra))
	for _, d := range dishes {
		byID[d.ID] = d
	}
	for id, d := range extra {
		if _, ok := byID[id]; !ok {
			byID[id] = d
			dishes = append(dishes, d)
		}
	}
	isChecked, isPot := idSet(checked), idSet(pot)
	option := func(d model.Dish) dishOption {
		return dishOption{
			ID: d.ID, Name: d.Name, Proposed: d.Status == model.DishProposed,
			Leftover: isPot[d.ID], Checked: isChecked[d.ID],
		}
	}
	onTop := map[int64]bool{}
	for _, id := range first {
		d, ok := byID[id]
		if !ok || onTop[id] || (len(p.Top) >= quickDishes && !isChecked[id]) {
			continue
		}
		onTop[id] = true
		p.Top = append(p.Top, option(d))
	}
	slices.SortFunc(dishes, func(x, y model.Dish) int {
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	for _, d := range dishes {
		if !onTop[d.ID] {
			p.Rest = append(p.Rest, option(d))
		}
	}
	return p, nil
}

// formSet reads one meal's dishes from a form: ticked ids under field, typed
// names under newField, separated by commas or lines. Typed names are created
// as typed — the desk has no model to read them through — which is still one
// dish per name, since the catalogue matches names case-insensitively.
func formSet(form url.Values, field, newField string) kitchen.Set {
	var set kitchen.Set
	for _, v := range form[field] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			set.DishIDs = append(set.DishIDs, id)
		}
	}
	for _, name := range strings.FieldsFunc(form.Get(newField), func(r rune) bool { return r == ',' || r == '\n' }) {
		if name = strings.TrimSpace(name); name != "" {
			set.NewDishes = append(set.NewDishes, kitchen.NewDish{Name: name})
		}
	}
	return set
}

// The menu page ------------------------------------------------------------

type menuMeal struct {
	Meal, Title string
	// Eaten is set once the meal is eaten; then it is shown, not planned.
	Eaten   []string
	Picker  dishPicker
	Offered []int64 // the suggestions on screen, carried through a 🔀
}

type menuDayLink struct {
	Date, Label string
	Active      bool
}

type menuData struct {
	Date, DayLabel string
	Days           []menuDayLink
	Meals          []menuMeal
	Error          string
}

// menuDay reads {date} and holds it to today through menuAhead days.
func menuDay(r *http.Request) (string, time.Time, bool) {
	date := r.PathValue("date")
	day, err := model.ParseDate(date)
	if err != nil {
		return "", time.Time{}, false
	}
	today := dayStart(time.Now())
	if day.Before(today) || day.After(today.AddDate(0, 0, menuAhead)) {
		return "", time.Time{}, false
	}
	return date, day, true
}

func (a *App) handleMenuToday(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/kitchen/menu/"+today(), http.StatusSeeOther)
}

func (a *App) handleMenu(w http.ResponseWriter, r *http.Request) {
	date, day, ok := menuDay(r)
	if !ok {
		// A past day's menu is its journal entry now; a far one is a typo.
		http.Redirect(w, r, "/kitchen", http.StatusSeeOther)
		return
	}
	offered, pot, err := a.firstOffer(date, day)
	if err != nil {
		a.serverError(w, err)
		return
	}
	data, err := a.menuForm(date, day, offered, pot, nil)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "kitchen_menu.html", "Меню", "kitchen_menu", data)
}

// firstOffer is the day's opening suggestions. Today's are the ones the
// morning message showed, so the page and the group offer the same dishes;
// another day, or today before the message, gets fresh picks that are not
// remembered — as the Mini App does.
func (a *App) firstOffer(date string, day time.Time) (map[menu.Meal][]int64, []int64, error) {
	msg, err := a.Store.MenuMessage(date)
	switch {
	case err == nil:
		offered := map[menu.Meal][]int64{}
		for _, meal := range kitchen.Meals {
			offered[meal] = msg.Shown[string(meal)]
		}
		pot, err := a.Kitchen.Leftovers(day)
		return offered, pot, err
	case store.IsNotFound(err):
		pot, offered, err := a.Kitchen.FirstPicks(day, kitchen.Rand())
		return offered, pot, err
	default:
		return nil, nil, err
	}
}

func (a *App) handleMenuSave(w http.ResponseWriter, r *http.Request) {
	date, day, ok := menuDay(r)
	if !ok {
		http.Redirect(w, r, "/kitchen", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "погана форма", http.StatusBadRequest)
		return
	}
	form := r.PostForm
	pot, err := a.Kitchen.Leftovers(day)
	if err != nil {
		a.serverError(w, err)
		return
	}
	offered := map[menu.Meal][]int64{}
	sets := map[menu.Meal]kitchen.Set{}
	for _, meal := range kitchen.Meals {
		offered[meal] = formIDs(form["offered_"+string(meal)])
		sets[meal] = formSet(form, "dish_"+string(meal), "new_"+string(meal))
	}

	// 🔀 asks for a fresh row for one meal and keeps every tick as it was.
	if m := form.Get("more"); model.ValidMeal(m) {
		meal := menu.Meal(m)
		var alsoOut []int64
		for _, other := range kitchen.Meals {
			alsoOut = append(alsoOut, sets[other].DishIDs...)
			if other != meal {
				alsoOut = append(alsoOut, offered[other]...)
			}
		}
		alsoOut = append(alsoOut, pot...)
		more, err := a.Kitchen.More(day, date, meal, offered[meal], alsoOut, kitchen.Rand())
		if err != nil {
			a.serverError(w, err)
			return
		}
		offered[meal] = append(offered[meal], more...)
		data, err := a.menuForm(date, day, offered, pot, sets)
		if err != nil {
			a.serverError(w, err)
			return
		}
		if len(more) == 0 {
			data.Error = "Більше нічого запропонувати — усе вже на екрані."
		}
		a.render(w, "kitchen_menu.html", "Меню", "kitchen_menu", data)
		return
	}

	if _, err := a.Meals.Plan(date, actorName(r), sets); err != nil {
		if !kitchen.IsInvalid(err) {
			a.serverError(w, err)
			return
		}
		data, ferr := a.menuForm(date, day, offered, pot, sets)
		if ferr != nil {
			a.serverError(w, ferr)
			return
		}
		data.Error = err.Error()
		a.render(w, "kitchen_menu.html", "Меню", "kitchen_menu", data)
		return
	}
	http.Redirect(w, r, "/kitchen#d-"+date, http.StatusSeeOther)
}

// menuForm lays out the day: per meal, what is eaten (then nothing to plan),
// or the plan ticked with the suggestions and yesterday's pot beside it.
// posted, when set, is what the form came back with and replaces the plan.
func (a *App) menuForm(date string, day time.Time, offered map[menu.Meal][]int64, pot []int64, posted map[menu.Meal]kitchen.Set) (menuData, error) {
	now := time.Now()
	data := menuData{Date: date, DayLabel: model.DayLabel(day, now)}
	today := dayStart(now)
	for i := 0; i <= 3; i++ {
		d := today.AddDate(0, 0, i)
		label := model.WeekdayLabels[int(d.Weekday())] + " " + strconv.Itoa(d.Day())
		switch i {
		case 0:
			label = "сьогодні"
		case 1:
			label = "завтра"
		}
		data.Days = append(data.Days, menuDayLink{Date: d.Format(time.DateOnly), Label: label, Active: d.Equal(day)})
	}

	rows, err := a.Store.MealsOn(date)
	if err != nil {
		return data, err
	}
	for _, meal := range kitchen.Meals {
		mm := menuMeal{Meal: string(meal), Title: meal.Title(), Offered: offered[meal]}
		var planned []int64
		extra := map[int64]model.Dish{}
		for _, row := range rows {
			if row.Meal != string(meal) {
				continue
			}
			extra[row.DishID] = model.Dish{ID: row.DishID, Name: row.Dish, Status: row.DishStatus}
			if row.Status == model.MealEaten {
				mm.Eaten = append(mm.Eaten, row.Dish)
			} else {
				planned = append(planned, row.DishID)
			}
		}
		if len(mm.Eaten) == 0 {
			checked, newValue := planned, ""
			if set, ok := posted[meal]; ok {
				checked = set.DishIDs
				newValue = joinNames(set.NewDishes)
			}
			if checked == nil {
				checked = []int64{}
			}
			first := slices.Concat(checked, planned, pot, offered[meal])
			mm.Picker, err = a.picker("dish_"+string(meal), "new_"+string(meal), newValue, first, checked, pot, extra)
			if err != nil {
				return data, err
			}
			// Everything offered stays on top, past the usual cap: it is what
			// 🔀 was pressed for.
			mm.Picker = liftOffered(mm.Picker, offered[meal])
		}
		data.Meals = append(data.Meals, mm)
	}
	return data, nil
}

// liftOffered moves any offered dish the cap left below up into Top.
func liftOffered(p dishPicker, offered []int64) dishPicker {
	want := idSet(offered)
	rest := p.Rest[:0:0]
	for _, o := range p.Rest {
		if want[o.ID] {
			p.Top = append(p.Top, o)
		} else {
			rest = append(rest, o)
		}
	}
	p.Rest = rest
	return p
}

func joinNames(ds []kitchen.NewDish) string {
	names := make([]string, 0, len(ds))
	for _, d := range ds {
		names = append(names, d.Name)
	}
	return strings.Join(names, ", ")
}

func formIDs(vs []string) []int64 {
	var out []int64
	for _, v := range vs {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func idSet(ids []int64) map[int64]bool {
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// journalURL is the journal page a day is on: the first page for the last
// two weeks, otherwise the page ending on that day.
func journalURL(day time.Time) string {
	if day.After(dayStart(time.Now()).AddDate(0, 0, -journalDays)) {
		return "/kitchen"
	}
	return "/kitchen?to=" + day.Format(time.DateOnly)
}
