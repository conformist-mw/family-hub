// Command add-dish writes one dish into the catalogue from the command line:
//
//	add-dish -db /data/family-hub.db -meal lunch -days any "Пюре зі скумбрією"
//
// It exists for combinations. The bot creates a dish from a plate, but it reads
// a plate of two dishes as two, so «Пюре зі скумбрією» has to come in some other
// way, and the catalogue has no screen of its own. Hand-written SQL would not
// do: the dedup key is computed in Go, because SQLite's lower() does not fold
// Cyrillic. Writing through CreateDish keeps that key right, and a name already
// in the catalogue — in any spelling NameKey folds together — is reported, not
// doubled or changed.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

func main() {
	dbPath := flag.String("db", "data/family-hub.db", "SQLite database path")
	meal := flag.String("meal", model.DishMealAny, "lunch, dinner or any")
	days := flag.String("days", model.DishDaysAny, "any or weekend")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), `usage: add-dish [-db path] [-meal lunch|dinner|any] [-days any|weekend] "name"`)
		flag.PrintDefaults()
	}
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	name := strings.Join(flag.Args(), " ")
	if strings.TrimSpace(name) == "" {
		flag.Usage()
		os.Exit(2)
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		logger.Error("open db", "err", err)
		os.Exit(1)
	}
	defer database.Close()
	if err := addDish(store.New(database), name, *meal, *days, os.Stdout); err != nil {
		logger.Error("add", "err", err)
		os.Exit(1)
	}
}

// addDish writes one active dish through CreateDish.
func addDish(st *store.Store, name, meal, days string, w io.Writer) error {
	if !model.ValidDishMeal(meal) {
		return fmt.Errorf("-meal %q: want lunch, dinner or any", meal)
	}
	if !model.ValidDishDays(days) {
		return fmt.Errorf("-days %q: want any or weekend", days)
	}
	got, existed, err := st.CreateDish(model.Dish{Name: name, Meal: meal, Days: days, Status: model.DishActive})
	if err != nil {
		return err
	}
	if existed {
		fmt.Fprintf(w, "exists  %s (%s, meal=%s days=%s) — left as it is\n", got.Name, got.Status, got.Meal, got.Days)
		return nil
	}
	fmt.Fprintf(w, "created %s (meal=%s days=%s)\n", got.Name, got.Meal, got.Days)
	return nil
}
