package main

import (
	"flag"
	"log/slog"
	"os"

	"lessons/internal/db"
	"lessons/internal/importer"
)

func main() {
	src := flag.String("src", "Доп. занятия.xlsx", "source xlsx file")
	dbPath := flag.String("db", "data/lessons.db", "SQLite database path")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	database, err := db.Open(*dbPath)
	if err != nil {
		logger.Error("open db", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		logger.Error("migrate", "err", err)
		os.Exit(1)
	}

	stats, err := importer.Import(database, *src, logger)
	if err != nil {
		logger.Error("import", "err", err)
		os.Exit(1)
	}
	logger.Info("import done",
		"persons", stats.Persons,
		"enrollments", stats.Enrollments,
		"visits", stats.Visits,
		"payments", stats.Payments,
		"date_fixed", stats.DateFixed,
		"status_fixed", stats.StatusFixed,
		"skipped", stats.Skipped,
	)
}
