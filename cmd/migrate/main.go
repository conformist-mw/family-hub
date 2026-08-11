// Command migrate brings the database up to date and exits.
//
// The server migrates on boot too, and that stays: a container started by hand
// should not come up against a schema it does not understand. But a migration
// that fails on boot fails inside a container the deploy has already created,
// and Ansible reports the deploy as successful while the app restarts forever.
// Run as its own step first, a bad migration stops the play with the old
// container still serving.
package main

import (
	"flag"
	"log/slog"
	"os"

	"familyhub/internal/db"
)

func main() {
	dbPath := flag.String("db", "data/family-hub.db", "SQLite database path")
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

	version, err := db.Version(database)
	if err != nil {
		logger.Error("read schema version", "err", err)
		os.Exit(1)
	}
	logger.Info("migrated", "db", *dbPath, "version", version)
}
