// Command import-mealie copies the Mealie recipe catalogue into the local dish
// table, once, as the menu moves off Mealie.
//
// Only the catalogue comes across, not the history: nearly every "made this"
// entry in Mealie is its own nightly auto-mark of whatever stood in the meal
// plan, so importing it would seed the menu's rotation with meals nobody ate.
// The dishes arrive as plain active ones with no last-seen date.
//
// Without -apply it only prints what would be imported and, separately, what
// would be skipped and why — the skipped list is meant to be read by a person
// before anything is written, since some of it (a side dish that is always
// served with the same main) is better re-added by hand as a combination.
// With -apply it writes through CreateDish, so a second run changes nothing.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/mealie"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

func main() {
	dbPath := flag.String("db", "data/family-hub.db", "SQLite database path")
	apply := flag.Bool("apply", false, "write the dishes; without it only print what would be imported")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	url, token := os.Getenv("MEALIE_URL"), os.Getenv("MEALIE_TOKEN")
	if url == "" || token == "" {
		logger.Error("MEALIE_URL and MEALIE_TOKEN must be set")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	recipes, err := mealie.New(url, token).Recipes(ctx)
	if err != nil {
		logger.Error("read mealie catalogue", "err", err)
		os.Exit(1)
	}

	dishes, skipped := plan(recipes)
	report(os.Stdout, dishes, skipped)
	if !*apply {
		fmt.Println("\ndry run: nothing written; re-run with -apply to import")
		return
	}

	// No migration here: the server and cmd/migrate own the schema, and an
	// import run against a database that is not up to date should fail on the
	// missing table rather than quietly move it forward.
	database, err := db.Open(*dbPath)
	if err != nil {
		logger.Error("open db", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	created, existed, err := importDishes(store.New(database), dishes, os.Stdout)
	if err != nil {
		logger.Error("import", "err", err)
		os.Exit(1)
	}
	logger.Info("imported", "db", *dbPath, "created", created, "already_there", existed, "skipped", len(skipped))
}

// Tag keys are matched against the slug and against the name, so a tag
// renamed in the UI still counts as long as its slug — which Mealie keeps —
// has not changed.
var (
	lunchTags   = []string{"obid", "обід"}
	dinnerTags  = []string{"vecheria", "вечеря"}
	weekendTags = []string{"dostavka", "доставка", "pokupne", "покупне"}
)

// skipCategories are the kinds of recipe that are not something put on the
// table by itself: a side is served with a main, a preserve or a sauce goes
// into other food, a drink is not a meal. In the new menu a dish is the whole
// plate, so these would be noise as buttons ("Гречка" for lunch).
var skipCategories = []string{"гарніри", "заготовки", "соуси та заправки", "напої"}

type skip struct {
	name, reason string
}

// mapRecipe turns one Mealie recipe into a dish, or says why it is left out.
// A recipe tagged for both meals, or for neither, is "any": the tags were
// only ever there to feed the meal-plan rules, and an untagged salad is as
// good at lunch as at dinner.
func mapRecipe(r mealie.Recipe) (model.Dish, string) {
	name := strings.Join(strings.Fields(r.Name), " ")
	if name == "" {
		return model.Dish{}, "no name"
	}
	for _, c := range r.RecipeCategory {
		if matches(c, skipCategories...) {
			return model.Dish{}, fmt.Sprintf("category %q", c.Name)
		}
	}

	d := model.Dish{Name: name, Meal: model.DishMealAny, Days: model.DishDaysAny, Status: model.DishActive}
	lunch, dinner := hasTag(r.Tags, lunchTags), hasTag(r.Tags, dinnerTags)
	switch {
	case lunch && !dinner:
		d.Meal = model.MealLunch
	case dinner && !lunch:
		d.Meal = model.MealDinner
	}
	if hasTag(r.Tags, weekendTags) {
		d.Days = model.DishDaysWeekend
	}
	return d, ""
}

func hasTag(tags []mealie.Organizer, keys []string) bool {
	for _, t := range tags {
		if matches(t, keys...) {
			return true
		}
	}
	return false
}

func matches(o mealie.Organizer, keys ...string) bool {
	name := store.NameKey(o.Name)
	for _, k := range keys {
		if o.Slug == k || name == k {
			return true
		}
	}
	return false
}

// plan splits the catalogue into what would be imported and what would not,
// both sorted by name so the dry-run listing is easy to read through.
func plan(recipes []mealie.Recipe) ([]model.Dish, []skip) {
	var dishes []model.Dish
	var skipped []skip
	for _, r := range recipes {
		d, reason := mapRecipe(r)
		if reason != "" {
			skipped = append(skipped, skip{name: r.Name, reason: reason})
			continue
		}
		dishes = append(dishes, d)
	}
	sort.SliceStable(dishes, func(i, j int) bool { return dishes[i].Name < dishes[j].Name })
	sort.SliceStable(skipped, func(i, j int) bool { return skipped[i].name < skipped[j].name })
	return dishes, skipped
}

func report(w io.Writer, dishes []model.Dish, skipped []skip) {
	fmt.Fprintf(w, "import (%d):\n", len(dishes))
	for _, d := range dishes {
		fmt.Fprintf(w, "  %-40s meal=%-6s days=%s\n", d.Name, d.Meal, d.Days)
	}
	fmt.Fprintf(w, "\nskip (%d):\n", len(skipped))
	for _, s := range skipped {
		fmt.Fprintf(w, "  %-40s %s\n", s.name, s.reason)
	}
}

// importDishes writes each dish through CreateDish. A name already in the
// table is left exactly as it is — including a dish the family has since
// rejected, or one the model proposed under the same name — which is what
// makes a second run, or a run after the menu has been live, harmless.
func importDishes(st *store.Store, dishes []model.Dish, w io.Writer) (created, existed int, err error) {
	for _, d := range dishes {
		got, was, err := st.CreateDish(d)
		if err != nil {
			return created, existed, fmt.Errorf("%s: %w", d.Name, err)
		}
		if was {
			existed++
			fmt.Fprintf(w, "exists  %s (%s)\n", got.Name, got.Status)
			continue
		}
		created++
		fmt.Fprintf(w, "created %s\n", got.Name)
	}
	return created, existed, nil
}
