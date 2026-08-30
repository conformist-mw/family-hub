package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"familyhub/internal/db"
)

func migrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

// goose has to actually pick the file up; a migration that is present but not
// embedded fails silently as "table missing" much later.
func TestReminderTablesExistAfterMigration(t *testing.T) {
	database := migrated(t)
	for _, table := range []string{"reminders", "reminder_rules", "reminder_occurrences"} {
		var name string
		err := database.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %q missing after migrate: %v", table, err)
		}
	}
}

// The constraint is the race guard between the materialiser, the Mini App
// checkbox and the bot's inline button — it has to be enforced by the schema,
// not by whichever writer happens to check first.
func TestOneOccurrencePerReminderAndInstant(t *testing.T) {
	database := migrated(t)
	seedRule(t, database)

	insert := func(dueAt string) error {
		_, err := database.Exec(
			`INSERT INTO reminder_occurrences (reminder_id, rule_id, due_at, status)
			 VALUES (1, 1, ?, 'pending')`, dueAt)
		return err
	}

	if err := insert("2026-09-01T08:00"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert("2026-09-01T08:00"); err == nil {
		t.Fatal("duplicate (reminder_id, due_at) was accepted")
	}
}

// A full RRULE can put several occurrences on one date (BYHOUR=8,20). Keying
// on the date instead of the instant would collapse them into one.
func TestTwoInstantsOnOneDateAreTwoOccurrences(t *testing.T) {
	database := migrated(t)
	seedRule(t, database)

	for _, dueAt := range []string{"2026-09-01T08:00", "2026-09-01T20:00"} {
		if _, err := database.Exec(
			`INSERT INTO reminder_occurrences (reminder_id, rule_id, due_at, status)
			 VALUES (1, 1, ?, 'pending')`, dueAt); err != nil {
			t.Fatalf("insert %s: %v", dueAt, err)
		}
	}

	var n int
	if err := database.QueryRow(`SELECT count(*) FROM reminder_occurrences`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("got %d occurrences on one date, want 2", n)
	}
}

func TestUnknownOccurrenceStatusIsRejected(t *testing.T) {
	database := migrated(t)
	seedRule(t, database)

	_, err := database.Exec(
		`INSERT INTO reminder_occurrences (reminder_id, rule_id, due_at, status)
		 VALUES (1, 1, '2026-09-01T08:00', 'forgotten')`)
	if err == nil {
		t.Fatal("an unknown status passed the CHECK constraint")
	}
}

// One rule version per starting instant: two versions claiming the same moment
// would make "which rule was in force" ambiguous.
func TestOneRuleVersionPerStartingInstant(t *testing.T) {
	database := migrated(t)
	seedRule(t, database)

	_, err := database.Exec(
		`INSERT INTO reminder_rules (reminder_id, valid_from_at, dtstart, rrule)
		 VALUES (1, '2026-08-01T00:00', '2026-08-01T09:00', 'FREQ=MONTHLY;BYMONTHDAY=5')`)
	if err == nil {
		t.Fatal("duplicate (reminder_id, valid_from_at) was accepted")
	}
}

func seedRule(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO reminders (id, title) VALUES (1, 'Кешбек')`); err != nil {
		t.Fatalf("seed reminder: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO reminder_rules (id, reminder_id, valid_from_at, dtstart, rrule)
		 VALUES (1, 1, '2026-08-01T00:00', '2026-08-01T08:00', 'FREQ=MONTHLY;BYMONTHDAY=1')`); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
}
