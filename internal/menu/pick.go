// Package menu decides which dishes the morning message offers. It is pure:
// the bot reads the catalogue, the journal and what was already shown, and
// hands them here together with the random source, so the choice can be
// tested without a database or a clock.
package menu

import (
	"math/rand/v2"
	"slices"
	"time"

	"familyhub/internal/model"
)

// Meal is which meal of the day a dish is offered or eaten at. The values are
// the ones stored in meals.meal and carried in callback data.
type Meal string

const (
	Lunch  Meal = model.MealLunch
	Dinner Meal = model.MealDinner
)

// Title is the human label of a meal, shared by the menu, the evening check
// and the plate card so the three cannot disagree.
func (m Meal) Title() string {
	if m == Dinner {
		return "Вечеря"
	}
	return "Обід"
}

// IsWeekend reports whether weekend-only dishes (delivery, bought ready-made)
// may be offered. Friday dinner counts: the week is over by then, and that is
// when the family actually orders in.
func IsWeekend(date time.Time, meal Meal) bool {
	switch date.Weekday() {
	case time.Saturday, time.Sunday:
		return true
	case time.Friday:
		return meal == Dinner
	}
	return false
}

// Candidate is a dish together with the last day it was planned or eaten.
// A zero LastSeen means never, which sorts first.
type Candidate struct {
	Dish     model.Dish
	LastSeen time.Time
}

// newChance is how often a proposed dish takes a slot in the morning menu:
// one morning in three. More often and the menu becomes the suggestion
// channel; less and nobody ever gets to try what they said "maybe" to.
const newChance = 3

// RollNew is the morning's one draw for a 🆕. It is made once per message by
// the caller rather than inside Pick: a draw per meal would put a proposal on
// well over a third of the mornings, and a shuffle that drew again would add
// one on every few taps.
func RollNew(rnd *rand.Rand) bool {
	return rnd.IntN(newChance) == 0
}

// Pick chooses up to n dishes for one meal on date. shownToday and
// shownYesterday are the dishes the morning message already offered; they are
// kept out so a shuffle brings something new and a dish nobody picks
// yesterday does not sit in the window forever. When they exhaust the pool,
// yesterday's are let back in first, then today's — the shuffle has gone
// round the circle.
//
// Among what is left, the dishes seen longest ago win, but not strictly: the
// pool is shuffled before the stable sort, so dishes with the same LastSeen
// (on the first day, all of them) do not come out in id order every morning,
// and n are drawn at random from the oldest few rather than taken off the top.
//
// With withNew, one proposed dish (the menu's 🆕) replaces the last regular
// one, if a proposal fits; without it proposals are never offered.
func Pick(cands []Candidate, date time.Time, meal Meal, shownToday, shownYesterday map[int64]bool, n int, withNew bool, rnd *rand.Rand) []model.Dish {
	if n <= 0 {
		return nil
	}
	var active, proposed []Candidate
	for _, c := range cands {
		if !fits(c.Dish, date, meal) {
			continue
		}
		switch c.Dish.Status {
		case model.DishActive:
			active = append(active, c)
		case model.DishProposed:
			proposed = append(proposed, c)
		}
	}

	pool := fresh(active, shownToday, shownYesterday)
	rnd.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	slices.SortStableFunc(pool, func(a, b Candidate) int { return a.LastSeen.Compare(b.LastSeen) })
	window := pool[:min(len(pool), max(2*n, n+3))]
	rnd.Shuffle(len(window), func(i, j int) { window[i], window[j] = window[j], window[i] })

	out := make([]model.Dish, 0, n)
	for _, c := range window[:min(len(window), n)] {
		out = append(out, c.Dish)
	}

	if !withNew {
		return out
	}
	newPool := without(proposed, shownToday, shownYesterday)
	if len(newPool) == 0 {
		return out
	}
	pick := newPool[rnd.IntN(len(newPool))].Dish
	if len(out) < n {
		return append(out, pick)
	}
	out[len(out)-1] = pick
	return out
}

// fits is the part of the pool rule that does not change with what was shown:
// the dish is for this meal (or either), and weekend-only dishes stay out on
// weekdays.
func fits(d model.Dish, date time.Time, meal Meal) bool {
	if d.Meal != string(meal) && d.Meal != model.DishMealAny {
		return false
	}
	return d.Days != model.DishDaysWeekend || IsWeekend(date, meal)
}

// fresh drops what was shown, relaxing the exclusion in two steps when it
// would leave nothing: yesterday's first, then today's.
func fresh(pool []Candidate, today, yesterday map[int64]bool) []Candidate {
	if out := without(pool, today, yesterday); len(out) > 0 {
		return out
	}
	if out := without(pool, today); len(out) > 0 {
		return out
	}
	return slices.Clone(pool)
}

// without returns a copy of pool minus every dish in any of the sets; a copy
// so that shuffling it never reorders the caller's slice.
func without(pool []Candidate, sets ...map[int64]bool) []Candidate {
	out := make([]Candidate, 0, len(pool))
next:
	for _, c := range pool {
		for _, s := range sets {
			if s[c.Dish.ID] {
				continue next
			}
		}
		out = append(out, c)
	}
	return out
}

// Leftovers are the dishes offered as "finishing yesterday's": everything
// confirmed eaten yesterday, once each. Finishing a pot is itself recorded as
// eaten, so a big borshch carries over for two or three days on its own and
// drops out as soon as nobody picks it.
func Leftovers(eatenYesterday []model.MealEntry) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, m := range eatenYesterday {
		if seen[m.DishID] {
			continue
		}
		seen[m.DishID] = true
		out = append(out, m.DishID)
	}
	return out
}
