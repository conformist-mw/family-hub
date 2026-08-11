package db

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Migrate(db *sql.DB) error {
	if err := prepare(); err != nil {
		return err
	}
	return goose.Up(db, "migrations")
}

// Version is what the deploy step reports after migrating, so a run says which
// schema it left behind rather than only that it did not fail.
func Version(db *sql.DB) (int64, error) {
	if err := prepare(); err != nil {
		return 0, err
	}
	return goose.GetDBVersion(db)
}

func prepare() error {
	goose.SetBaseFS(migrationFS)
	return goose.SetDialect("sqlite3")
}
