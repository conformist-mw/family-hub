package cooking

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"familyhub/internal/mealie"
)

// The planner fills empty meal slots a few days ahead, so that nobody has to
// press "generate" and the evening digest always has something to announce.
//
// Mealie can pick a random recipe itself, and this does not use that: its
// rules choose by tag alone, so the same dish comes back three times a week
// and the last-made date the cooking log so carefully records changes
// nothing. The rules *can* filter on lastMade, but only against a literal
// date — `lastMade < "now-14d"` is rejected — so a rule holding a freshness
// window would silently rot the day after it was written.
//
// So the tag half of the decision stays in the rules, read from the API and
// maintained in the UI where it belongs, and this appends the freshness
// window with today's date computed at each pass.

type Planner struct {
	c      *mealie.Client
	logger *slog.Logger
	loc    *time.Location
	rnd    *rand.Rand

	// slots are the entry types to fill, in the order they are filled.
	slots []string
	// horizon is how many days ahead of today to keep filled.
	horizon int
	// restDays is how long a dish rests before it may be planned again.
	restDays int
	// at is the wall-clock time of the daily pass, "HH:MM"; empty disables.
	at string
}

type PlannerConfig struct {
	Slots    []string
	Horizon  int
	RestDays int
	At       string
	Loc      *time.Location
	Logger   *slog.Logger
}

func NewPlanner(c *mealie.Client, cfg PlannerConfig) *Planner {
	if len(cfg.Slots) == 0 {
		cfg.Slots = []string{"lunch", "dinner"}
	}
	if cfg.Horizon <= 0 {
		cfg.Horizon = 7
	}
	if cfg.RestDays <= 0 {
		cfg.RestDays = 14
	}
	if cfg.Loc == nil {
		cfg.Loc = time.Local
	}
	return &Planner{
		c: c, logger: cfg.Logger, loc: cfg.Loc,
		rnd:      rand.New(rand.NewSource(time.Now().UnixNano())),
		slots:    cfg.Slots,
		horizon:  cfg.Horizon,
		restDays: cfg.RestDays,
		at:       cfg.At,
	}
}

// Run fills every empty slot from tomorrow to the horizon and reports how
// many it added. Today is left alone: it is being eaten, and a plan written
// for a meal already cooked is a correction, not a plan.
func (p *Planner) Run(ctx context.Context, now time.Time) (int, error) {
	from := midnight(now, p.loc).AddDate(0, 0, 1)
	to := from.AddDate(0, 0, p.horizon-1)

	rules, err := p.c.MealPlanRules(ctx)
	if err != nil {
		return 0, fmt.Errorf("meal-plan rules: %w", err)
	}
	planned, err := p.c.MealPlan(ctx, from, to)
	if err != nil {
		return 0, fmt.Errorf("meal plan: %w", err)
	}

	// taken is what is already planned in the window, so a fresh pick does
	// not put борщ on two days of the same week. Filled says which slots
	// already have something and must not be touched.
	taken := map[string]bool{}
	filled := map[string]bool{}
	for _, e := range planned {
		filled[e.Date+"/"+e.EntryType] = true
		if e.Recipe != nil {
			taken[e.Recipe.Slug] = true
		}
	}

	var added int
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		for _, slot := range p.slots {
			if filled[day.Format("2006-01-02")+"/"+slot] {
				continue
			}
			rec, ok, err := p.pick(ctx, rules, day, slot, taken)
			if err != nil {
				return added, err
			}
			if !ok {
				p.log("cooking: nothing to plan", "day", day.Format("2006-01-02"), "slot", slot)
				continue
			}
			if err := p.c.AddPlanEntry(ctx, day, slot, rec.ID); err != nil {
				return added, fmt.Errorf("plan %s %s: %w", day.Format("2006-01-02"), slot, err)
			}
			taken[rec.Slug] = true
			added++
			p.log("cooking: planned", "day", day.Format("2006-01-02"), "slot", slot, "recipe", rec.Name)
		}
	}
	return added, nil
}

// pick chooses a dish for one slot: the household's own rule for that day and
// meal, narrowed to dishes that have rested long enough and are not already
// planned this week.
//
// When the rested set is empty it falls back to the rule alone. A repeat is a
// worse plan; no plan at all is a silent evening digest, which is worse than
// a worse plan.
func (p *Planner) pick(ctx context.Context, rules []mealie.Rule, day time.Time, slot string, taken map[string]bool) (mealie.Recipe, bool, error) {
	rule, ok := matchRule(rules, day, slot)
	if !ok {
		return mealie.Recipe{}, false, nil
	}

	rested := fmt.Sprintf(`%s AND (lastMade IS NONE OR lastMade < %q)`,
		rule.QueryFilterString, day.AddDate(0, 0, -p.restDays).Format("2006-01-02"))

	for _, filter := range []string{rested, rule.QueryFilterString} {
		found, err := p.c.RecipesMatching(ctx, filter)
		if err != nil {
			return mealie.Recipe{}, false, fmt.Errorf("recipes for %s: %w", slot, err)
		}
		var free []mealie.Recipe
		for _, r := range found {
			if !taken[r.Slug] {
				free = append(free, r)
			}
		}
		if len(free) > 0 {
			return free[p.rnd.Intn(len(free))], true, nil
		}
	}
	return mealie.Recipe{}, false, nil
}

// matchRule finds the rule governing a slot on a day. A rule naming the
// weekday wins over one that applies to every day, which is how "Friday is
// pizza" would beat the general dinner rule.
func matchRule(rules []mealie.Rule, day time.Time, slot string) (mealie.Rule, bool) {
	weekday := strings.ToLower(day.Weekday().String())
	var general mealie.Rule
	var haveGeneral bool
	for _, r := range rules {
		if !strings.EqualFold(r.EntryType, slot) || r.QueryFilterString == "" {
			continue
		}
		switch strings.ToLower(r.Day) {
		case weekday:
			return r, true
		case "unset", "":
			general, haveGeneral = r, true
		}
	}
	return general, haveGeneral
}

// RunDaily ticks once a minute and runs a pass at the configured wall-clock
// time. It blocks; meant for its own goroutine.
//
// Like the reminders materialiser, it runs from main rather than from the
// bot: filling the plan is data, and hanging it off the bot's notification
// gates would stop the record being written whenever messages are switched
// off. It deliberately does not run a pass at startup — deploys are frequent,
// and a plan that reshuffles itself on every restart is not a plan.
func (p *Planner) RunDaily(ctx context.Context) {
	if p.at == "" {
		p.log("cooking: planner disabled (no MEALPLAN_FILL_TIME)")
		return
	}
	p.log("cooking: planner started", "at", p.at, "slots", strings.Join(p.slots, ","),
		"horizon_days", p.horizon, "rest_days", p.restDays)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// The date the pass last ran, so a minute-resolution match fires once a
	// day. In memory: a restart may repeat today's pass, which is harmless
	// because a pass only fills what is empty.
	var last string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().In(p.loc)
			today := now.Format("2006-01-02")
			if today == last || now.Format("15:04") != p.at {
				continue
			}
			last = today
			added, err := p.Run(ctx, now)
			if err != nil {
				p.logErr("cooking: planner", err)
				continue
			}
			p.log("cooking: planner pass done", "added", added)
		}
	}
}

func (p *Planner) log(msg string, args ...any) {
	if p.logger != nil {
		p.logger.Info(msg, args...)
	}
}

func (p *Planner) logErr(msg string, err error) {
	if p.logger != nil {
		p.logger.Error(msg, "err", err)
	}
}

func midnight(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}
