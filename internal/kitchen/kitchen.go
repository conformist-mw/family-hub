// Package kitchen is what the morning menu offers, against the store: the
// dishes it may pick from, what was already shown, the first picks of a day
// and a fresh row on request. It is shared by the bot, which posts the menu,
// and the Mini App, where the menu is actually made, so the two cannot drift
// on what "a few dishes nobody has had in a while" means. The choosing itself
// stays in internal/menu, which knows nothing about storage.
package kitchen

import (
	"math/rand/v2"
	"slices"
	"time"

	"familyhub/internal/menu"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// Options is how many dishes one meal is offered at a time.
const Options = 3

// Meals are the meals the menu covers, lunch first.
var Meals = []menu.Meal{menu.Lunch, menu.Dinner}

type Service struct {
	store *store.Store
}

func New(st *store.Store) *Service { return &Service{store: st} }

// Candidates ranks each dish by the later of the last day it was planned or
// eaten and the last day the menu offered it: a dish offered and passed over
// has been looked at, and letting it keep the rank of "never seen" would bring
// it back every other morning for good.
func (s *Service) Candidates() ([]menu.Candidate, error) {
	dishes, err := s.store.Dishes(model.DishActive, model.DishProposed)
	if err != nil {
		return nil, err
	}
	seen, err := s.store.LastSeen()
	if err != nil {
		return nil, err
	}
	shown, err := s.store.LastShown()
	if err != nil {
		return nil, err
	}
	cands := make([]menu.Candidate, 0, len(dishes))
	for _, d := range dishes {
		last := seen[d.ID]
		if shown[d.ID].After(last) {
			last = shown[d.ID]
		}
		cands = append(cands, menu.Candidate{Dish: d, LastSeen: last})
	}
	return cands, nil
}

// ShownOn is every dish the day's menu offered, at either meal; a day without
// a menu showed nothing. The meals are not told apart: a dish good for either
// that was offered at yesterday's lunch has been seen just the same when
// today's dinner is picked.
func (s *Service) ShownOn(date string) (map[int64]bool, error) {
	out := map[int64]bool{}
	msg, err := s.store.MenuMessage(date)
	if store.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, ids := range msg.Shown {
		for _, id := range ids {
			out[id] = true
		}
	}
	return out, nil
}

// Leftovers is yesterday's pot: every dish eaten the day before, once.
func (s *Service) Leftovers(day time.Time) ([]int64, error) {
	eaten, err := s.store.EatenOn(day.AddDate(0, 0, -1).Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	return menu.Leftovers(eaten), nil
}

// FirstPicks decides a day's opening offer: yesterday's pot, and a few dishes
// per meal.
//
// The meals are picked one after the other, the second with the first's
// dishes kept out, so a dish good for either meal is not offered twice unless
// there is nothing else; yesterday's pot is kept out of both, being offered
// already. What was shown yesterday is kept out of both meals.
//
// The 🆕 is drawn once for the day and offered first to a meal picked at
// random, so it is not always lunch's; the other meal gets it only when the
// first had no proposal that fits.
func (s *Service) FirstPicks(now time.Time, rnd *rand.Rand) (leftovers []int64, offered map[menu.Meal][]int64, err error) {
	shownYesterday, err := s.ShownOn(now.AddDate(0, 0, -1).Format(time.DateOnly))
	if err != nil {
		return nil, nil, err
	}
	cands, err := s.Candidates()
	if err != nil {
		return nil, nil, err
	}
	if leftovers, err = s.Leftovers(now); err != nil {
		return nil, nil, err
	}

	offered = map[menu.Meal][]int64{}
	onOffer := idSet(leftovers)
	withNew := menu.RollNew(rnd)
	order := Meals
	if withNew && rnd.IntN(2) == 1 {
		order = []menu.Meal{menu.Dinner, menu.Lunch}
	}
	for _, meal := range order {
		picked := menu.Pick(cands, now, meal, menu.Exclude{Today: onOffer, Yesterday: shownYesterday}, Options, withNew, rnd)
		for _, d := range picked {
			offered[meal] = append(offered[meal], d.ID)
			onOffer[d.ID] = true
			if d.Status == model.DishProposed {
				withNew = false
			}
		}
	}
	return leftovers, offered, nil
}

// More offers a fresh row for one meal: nothing shown today at either meal,
// nothing in offer (the meal's current dishes) or alsoOut (the rest of what is
// on screen), nothing shown yesterday — Pick lets those back in, in
// reverse order, once the pool runs out. The row is added to the day's shown
// set when the day has a menu message, so the next request moves on again.
//
// The row keeps a 🆕 slot only if the meal's offer already had one. The day's
// one draw was made in the morning; drawing again on every request would add
// a proposal every few taps.
func (s *Service) More(now time.Time, date string, meal menu.Meal, offer, alsoOut []int64, rnd *rand.Rand) ([]int64, error) {
	exclude, err := s.ShownOn(date)
	if err != nil {
		return nil, err
	}
	for _, id := range append(slices.Clone(offer), alsoOut...) {
		exclude[id] = true
	}
	day, err := model.ParseDate(date)
	if err != nil {
		return nil, err
	}
	yesterday, err := s.ShownOn(day.AddDate(0, 0, -1).Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	cands, err := s.Candidates()
	if err != nil {
		return nil, err
	}
	proposed := map[int64]bool{}
	for _, c := range cands {
		if c.Dish.Status == model.DishProposed {
			proposed[c.Dish.ID] = true
		}
	}
	withNew := slices.ContainsFunc(offer, func(id int64) bool { return proposed[id] })

	var ids []int64
	for _, d := range menu.Pick(cands, now, meal, menu.Exclude{Today: exclude, Yesterday: yesterday}, Options, withNew, rnd) {
		ids = append(ids, d.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := s.store.AppendShown(date, string(meal), ids); err != nil && !store.IsNotFound(err) {
		return nil, err
	}
	return ids, nil
}

// Rand is a fresh source per request; only the tests need a fixed one.
func Rand() *rand.Rand {
	return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
}

func idSet(ids []int64) map[int64]bool {
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}
