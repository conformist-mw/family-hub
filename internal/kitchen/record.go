package kitchen

import (
	"errors"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strings"
	"time"

	"familyhub/internal/menu"
	"familyhub/internal/model"
	"familyhub/internal/store"
	"familyhub/internal/valid"
)

// The write side of the kitchen: a meal's plan and what was eaten at it, with
// everything that follows a save — the bot's two messages redrawn, the day
// announced once it is done. Shared by the Mini App and the web form, so a
// meal recorded from a laptop leaves the group exactly where one recorded from
// a phone does.

// MaxDishes is a fuse on one meal; a real dinner is two or three.
const MaxDishes = 20

// MaxDishName keeps a typed name a name, not a paragraph.
const MaxDishName = 80

// ErrDishGone answers a set naming a dish deleted since the screen loaded.
var ErrDishGone = valid.FieldError{Field: "dishes", Message: "однієї зі страв уже немає — онови сторінку"}

// Board is what a save needs from the bot: redrawing the day's morning menu
// and evening check from the store. Nil leaves them as they were.
type Board interface {
	RedrawMenu(date string) error
	RedrawEvening(date string) error
}

// Notifier posts to the family group. Nil posts nothing.
type Notifier interface {
	NotifyHTML(text string) error
}

// NewDish is a dish the catalogue does not have yet. Meal and Days decide when
// the morning menu may offer it, so a delivery marked weekend-only does not
// turn up on a Tuesday; empty means any.
type NewDish struct {
	Name string
	Meal string
	Days string
}

// Set is one meal's dishes as a screen sends them: ticked catalogue ids plus
// names typed that the catalogue lacks.
type Set struct {
	DishIDs   []int64
	NewDishes []NewDish
}

// Check keeps one meal's set within reason before anything is written.
func (s Set) Check() error {
	if len(s.DishIDs)+len(s.NewDishes) > MaxDishes {
		return valid.FieldError{Field: "dishes", Message: "забагато страв для одного разу"}
	}
	for _, d := range s.NewDishes {
		if n := len([]rune(strings.TrimSpace(d.Name))); n == 0 || n > MaxDishName {
			return valid.FieldError{Field: "dishes", Message: "назва страви порожня або задовга"}
		}
		if (d.Meal != "" && !model.ValidDishMeal(d.Meal)) || (d.Days != "" && !model.ValidDishDays(d.Days)) {
			return valid.FieldError{Field: "dishes", Message: "невідомий час для страви"}
		}
	}
	return nil
}

type Recorder struct {
	store  *store.Store
	board  Board
	notify Notifier
	log    *slog.Logger
	// now names the day in the announcement («Сьогодні», «Вчора»).
	now func() time.Time
}

func NewRecorder(st *store.Store, board Board, notify Notifier, logger *slog.Logger, now func() time.Time) *Recorder {
	return &Recorder{store: st, board: board, notify: notify, log: logger, now: now}
}

// Eaten makes the set exactly what was eaten at one meal, through
// store.SetEaten — the path the photo and the evening check take, so a plan is
// confirmed rather than doubled and a proposal eaten becomes the family's.
// Yesterday's dish eaten again is yesterday's pot. Then both of the day's
// messages are redrawn and, once every meal is answered, the day is announced.
// It returns how many dishes the meal now has.
func (r *Recorder) Eaten(date string, meal menu.Meal, who string, set Set) (int, error) {
	if err := set.Check(); err != nil {
		return 0, err
	}
	day, err := model.ParseDate(date)
	if err != nil {
		return 0, err
	}
	ids, err := r.ids(set)
	if err != nil {
		return 0, fmt.Errorf("create dish: %w", err)
	}
	leftover, err := r.leftovers(day)
	if err != nil {
		return 0, err
	}
	before, err := r.store.MealsOn(date)
	if err != nil {
		return 0, err
	}
	if err := r.store.SetEaten(date, string(meal), who, ids, leftover); err != nil {
		if store.IsNotFound(err) {
			return 0, ErrDishGone
		}
		return 0, err
	}
	// Both messages say what was eaten — the evening check asks it, the
	// morning menu shows it in its ✅ lines. Best-effort: the meal is saved,
	// and a Telegram hiccup must not be reported as a failed save.
	if r.board != nil {
		if err := r.board.RedrawEvening(date); err != nil {
			r.log.Error("kitchen: redraw evening check", "err", err)
		}
		if err := r.board.RedrawMenu(date); err != nil {
			r.log.Error("kitchen: redraw menu", "err", err)
		}
	}
	r.announceDay(date, meal, day, who, before)
	return len(ids), nil
}

// Plan makes each listed meal's set its plan for the day, through
// store.SetPlan: an eaten meal is left alone, and a dish eaten the day before
// is marked a leftover. Every set is checked before anything is written. It
// reports, per meal, whether the plan was written. A plan for today redraws
// the morning menu.
func (r *Recorder) Plan(date, who string, sets map[menu.Meal]Set) (map[string]bool, error) {
	for _, set := range sets {
		if err := set.Check(); err != nil {
			return nil, err
		}
	}
	day, err := model.ParseDate(date)
	if err != nil {
		return nil, err
	}
	leftover, err := r.leftovers(day)
	if err != nil {
		return nil, err
	}
	saved := map[string]bool{}
	for _, meal := range Meals {
		set, ok := sets[meal]
		if !ok {
			continue
		}
		ids, err := r.ids(set)
		if err != nil {
			return nil, fmt.Errorf("create dish: %w", err)
		}
		wrote, err := r.store.SetPlan(date, string(meal), who, ids, leftover)
		if store.IsNotFound(err) {
			return nil, ErrDishGone
		}
		if err != nil {
			return nil, err
		}
		saved[string(meal)] = wrote
	}
	// Only today has a morning message; RedrawMenu finds none for another day
	// and does nothing, but there is no reason to ask.
	if r.board != nil && date == r.now().Format(time.DateOnly) {
		if err := r.board.RedrawMenu(date); err != nil {
			r.log.Error("kitchen: redraw menu", "err", err)
		}
	}
	return saved, nil
}

// leftovers is the day before's pot, as the set store writes take.
func (r *Recorder) leftovers(day time.Time) (map[int64]bool, error) {
	eaten, err := r.store.EatenOn(day.AddDate(0, 0, -1).Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, m := range eaten {
		out[m.DishID] = true
	}
	return out, nil
}

// ids is the set as ids, the typed names created first. They are created
// outside the meal's transaction: a dish that then fails to be recorded is
// still a dish the family named, and saving again finds it by name instead of
// creating a second one.
func (r *Recorder) ids(s Set) ([]int64, error) {
	ids := make([]int64, 0, len(s.DishIDs)+len(s.NewDishes))
	seen := map[int64]bool{}
	pick := func(id int64) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range s.DishIDs {
		pick(id)
	}
	for _, nd := range s.NewDishes {
		d, _, err := r.store.EnsureDish(model.Dish{Name: strings.TrimSpace(nd.Name), Meal: nd.Meal, Days: nd.Days})
		if err != nil {
			return nil, err
		}
		pick(d.ID)
	}
	return ids, nil
}

// announceDay tells the group what the day was, once every meal is answered:
// eaten, or «Не вдома» on the evening check. The redraws are edits, and an
// edit in Telegram is silent — filling the day in before the evening check
// meant the group heard nothing at all, the check then having nothing to ask.
// A save that changed nothing says nothing. Best-effort, like every group
// message.
func (r *Recorder) announceDay(date string, meal menu.Meal, day time.Time, by string, before []model.MealEntry) {
	if r.notify == nil {
		return
	}
	after, err := r.store.MealsOn(date)
	if err != nil {
		r.log.Error("kitchen: meals after save", "err", err)
		return
	}
	if slices.Equal(eatenIDs(before, meal), eatenIDs(after, meal)) {
		return
	}
	var away []string
	if msg, err := r.store.EveningMessage(date); err == nil {
		away = msg.Away
	} else if !store.IsNotFound(err) {
		r.log.Error("kitchen: evening message", "err", err)
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🍽 <b>Що їли · %s</b>", html.EscapeString(model.DayLabel(day, r.now())))
	if by != "" {
		fmt.Fprintf(&sb, " (%s)", html.EscapeString(by))
	}
	for _, m := range Meals {
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
	if err := r.notify.NotifyHTML(sb.String()); err != nil {
		r.log.Error("kitchen: announce day", "err", err)
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

// IsInvalid reports whether err is the caller's to fix rather than the
// server's: a set out of bounds or a dish gone since the screen loaded.
func IsInvalid(err error) bool {
	var invalid valid.FieldError
	return errors.As(err, &invalid)
}
