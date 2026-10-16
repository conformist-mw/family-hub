package mini

import (
	"net/http"
	"time"

	"familyhub/internal/kitchen"
	"familyhub/internal/menu"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The morning menu is made here. The bot posts the day's suggestions with a
// link; this screen opens with them ticked off by nobody, lets each meal take
// any number of dishes, asks for more suggestions, searches the catalogue,
// and on save writes the plan and has the bot redraw its message. A keyboard
// plans one dish per tap, which is the whole reason the menu moved.
//
// The suggestions are the ones the message showed: the day's shown set in
// menu_messages, which a request for more also appends to, so the next
// morning ages them the same way the 🔀 used to. A day without a message —
// the menu clock off, or the screen opened before seven — gets fresh picks
// that are not remembered.

var errPastMenu = &apiError{Code: "past_menu", Message: "Це меню вже минуло — відкрий Кухню", status: http.StatusConflict}

type plannerMealDTO struct {
	Meal  string `json:"meal"`
	Title string `json:"title"`
	// Eaten is set once the meal was reported eaten; then it is not planned.
	Eaten   []string `json:"eaten"`
	Planned []int64  `json:"planned"`
	// Offered are the suggestions, the morning's first and any asked for
	// since, in that order.
	Offered []int64 `json:"offered"`
}

type plannerDTO struct {
	Date      string           `json:"date"`
	DayLabel  string           `json:"dayLabel"`
	Leftovers []int64          `json:"leftovers"`
	Meals     []plannerMealDTO `json:"meals"`
	Dishes    []pickerDishDTO  `json:"dishes"`
}

// menuDay reads {date} and holds it to today: the menu is for the day it is
// made on, and a link from yesterday's message must not plan yesterday.
func (rt *Router) menuDay(r *http.Request) (string, time.Time, *apiError) {
	date := r.PathValue("date")
	now := rt.now().In(rt.loc)
	if _, err := time.ParseInLocation(time.DateOnly, date, rt.loc); err != nil {
		return "", now, errBadRequest
	}
	if date != now.Format(time.DateOnly) {
		return "", now, errPastMenu
	}
	return date, now, nil
}

func (rt *Router) handlePlanner(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	date, now, bad := rt.menuDay(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	dto, err := rt.planner(date, now)
	if err != nil {
		rt.log.Error("mini: menu planner", "err", err)
		rt.fail(w, errInternal)
		return
	}
	rt.writeJSON(w, http.StatusOK, dto)
}

func (rt *Router) planner(date string, now time.Time) (plannerDTO, error) {
	dto := plannerDTO{Date: date, DayLabel: dayLabel(now, now)}

	var offered map[menu.Meal][]int64
	msg, err := rt.store.MenuMessage(date)
	switch {
	case err == nil:
		offered = map[menu.Meal][]int64{}
		for _, meal := range kitchen.Meals {
			offered[meal] = msg.Shown[string(meal)]
		}
		if dto.Leftovers, err = rt.kitchen.Leftovers(now); err != nil {
			return dto, err
		}
	case store.IsNotFound(err):
		if dto.Leftovers, offered, err = rt.kitchen.FirstPicks(now, kitchen.Rand()); err != nil {
			return dto, err
		}
	default:
		return dto, err
	}

	var listed map[int64]bool
	if dto.Dishes, listed, err = rt.catalogue(); err != nil {
		return dto, err
	}
	rows, err := rt.store.MealsOn(date)
	if err != nil {
		return dto, err
	}
	// Anything referenced but outside the catalogue — a rejected dish still
	// in yesterday's pot or in a plan — is listed so the screen can name it.
	need := func(id int64, name, status string) {
		if !listed[id] {
			dto.Dishes = append(dto.Dishes, pickerDishDTO{ID: id, Name: name, Proposed: status == model.DishProposed})
			listed[id] = true
		}
	}
	for _, m := range rows {
		need(m.DishID, m.Dish, m.DishStatus)
	}
	for _, id := range dto.Leftovers {
		if !listed[id] {
			d, err := rt.store.Dish(id)
			if err != nil {
				return dto, err
			}
			need(d.ID, d.Name, d.Status)
		}
	}

	for _, meal := range kitchen.Meals {
		m := plannerMealDTO{Meal: string(meal), Title: meal.Title(), Eaten: []string{}, Planned: []int64{}, Offered: []int64{}}
		for _, row := range rows {
			if row.Meal != string(meal) {
				continue
			}
			if row.Status == model.MealEaten {
				m.Eaten = append(m.Eaten, row.Dish)
			} else {
				m.Planned = append(m.Planned, row.DishID)
			}
		}
		for _, id := range offered[meal] {
			if listed[id] {
				m.Offered = append(m.Offered, id)
			}
		}
		dto.Meals = append(dto.Meals, m)
	}
	if dto.Leftovers == nil {
		dto.Leftovers = []int64{}
	}
	return dto, nil
}

type moreRequest struct {
	// Offer is the meal's suggestions on screen; AlsoOut is everything else
	// on screen. Neither is offered again.
	Offer   []int64 `json:"offer"`
	AlsoOut []int64 `json:"alsoOut"`
}

func (rt *Router) handlePlannerMore(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	date, now, bad := rt.menuDay(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	meal := r.PathValue("meal")
	if !model.ValidMeal(meal) {
		rt.fail(w, errBadRequest)
		return
	}
	var body moreRequest
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	ids, err := rt.kitchen.More(now, date, menu.Meal(meal), body.Offer, body.AlsoOut, kitchen.Rand())
	if err != nil {
		rt.log.Error("mini: more suggestions", "err", err)
		rt.fail(w, errInternal)
		return
	}
	if ids == nil {
		ids = []int64{}
	}
	rt.writeJSON(w, http.StatusOK, map[string][]int64{"ids": ids})
}

func (rt *Router) handlePlannerSave(w http.ResponseWriter, r *http.Request) {
	who, bad := rt.v.authenticate(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	date, now, bad := rt.menuDay(r)
	if bad != nil {
		rt.fail(w, bad)
		return
	}
	var body map[string]pickerWrite
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	for meal, set := range body {
		if !model.ValidMeal(meal) {
			rt.fail(w, errBadRequest)
			return
		}
		if err := set.check(); err != nil {
			rt.writeError(w, err, "save menu")
			return
		}
	}

	leftovers, err := rt.kitchen.Leftovers(now)
	if err != nil {
		rt.log.Error("mini: leftovers", "err", err)
		rt.fail(w, errInternal)
		return
	}
	leftover := map[int64]bool{}
	for _, id := range leftovers {
		leftover[id] = true
	}

	saved := map[string]bool{}
	for _, meal := range kitchen.Meals {
		set, ok := body[string(meal)]
		if !ok {
			continue
		}
		ids, err := rt.dishIDs(set)
		if err != nil {
			rt.log.Error("mini: create dish", "err", err)
			rt.fail(w, errInternal)
			return
		}
		wrote, err := rt.store.SetPlan(date, string(meal), who.Name, ids, leftover)
		if store.IsNotFound(err) {
			rt.writeError(w, errDishGone, "save menu")
			return
		}
		if err != nil {
			rt.log.Error("mini: save plan", "err", err)
			rt.fail(w, errInternal)
			return
		}
		saved[string(meal)] = wrote
	}

	// Best-effort, like every group message: the plan is saved, and a
	// Telegram hiccup must not be reported as a failed save.
	if rt.menu != nil {
		if err := rt.menu.RedrawMenu(date); err != nil {
			rt.log.Error("mini: redraw menu", "err", err)
		}
	}
	rt.writeJSON(w, http.StatusOK, map[string]any{"saved": saved})
}
