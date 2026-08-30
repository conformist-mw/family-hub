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

func mustExec(t *testing.T, database *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := database.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
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

// The slot schedule moved out of regular_slots into slot_versions, and every
// existing row had to survive the move. A migration that silently dropped the
// timetable would empty the calendar and the evening reminder at once.
func TestSlotVersionsCarryTheExistingScheduleForward(t *testing.T) {
	database := migrated(t)

	// A course with a slot, written the way 0001..0005 left it.
	mustExec(t, database, `INSERT INTO persons (id, name) VALUES (1, 'Маша')`)
	mustExec(t, database, `
		INSERT INTO enrollments (id, person_id, name, billing_type, current_price)
		VALUES (1, 1, 'Балет', 'per_lesson', 300)`)
	mustExec(t, database, `INSERT INTO regular_slots (id, enrollment_id) VALUES (7, 1)`)
	mustExec(t, database, `
		INSERT INTO slot_versions (slot_id, valid_from_at, weekday, time, duration_min)
		VALUES (7, '2000-01-01T00:00', 2, '13:35', 45)`)

	var weekday, duration int
	var clock, from string
	err := database.QueryRow(`
		SELECT weekday, time, duration_min, valid_from_at
		FROM slot_versions WHERE slot_id = 7`).Scan(&weekday, &clock, &duration, &from)
	if err != nil {
		t.Fatalf("read slot_versions: %v", err)
	}
	if weekday != 2 || clock != "13:35" || duration != 45 {
		t.Fatalf("version = %d %s %d", weekday, clock, duration)
	}

	// The schedule columns are gone from the identity row: two homes for the
	// same fact is how the schedule started lying in the first place.
	if _, err := database.Exec(`SELECT weekday FROM regular_slots`); err == nil {
		t.Fatal("regular_slots still carries a weekday")
	}
}

// A slot cannot have two versions starting at the same instant — which one was
// in force would have no answer.
func TestOneSlotVersionPerStartingInstant(t *testing.T) {
	database := migrated(t)
	mustExec(t, database, `INSERT INTO persons (id, name) VALUES (1, 'Маша')`)
	mustExec(t, database, `
		INSERT INTO enrollments (id, person_id, name, billing_type, current_price)
		VALUES (1, 1, 'Балет', 'per_lesson', 300)`)
	mustExec(t, database, `INSERT INTO regular_slots (id, enrollment_id) VALUES (7, 1)`)
	mustExec(t, database, `
		INSERT INTO slot_versions (slot_id, valid_from_at, weekday, time)
		VALUES (7, '2026-09-01T00:00', 2, '13:35')`)

	_, err := database.Exec(`
		INSERT INTO slot_versions (slot_id, valid_from_at, weekday, time)
		VALUES (7, '2026-09-01T00:00', 4, '17:00')`)
	if err == nil {
		t.Fatal("two versions were accepted for the same instant")
	}
}

// A weekday the schema cannot mean must not reach the expansion, which would
// skip it and render the course as never happening.
func TestSlotVersionRejectsAnImpossibleWeekday(t *testing.T) {
	database := migrated(t)
	mustExec(t, database, `INSERT INTO persons (id, name) VALUES (1, 'Маша')`)
	mustExec(t, database, `
		INSERT INTO enrollments (id, person_id, name, billing_type, current_price)
		VALUES (1, 1, 'Балет', 'per_lesson', 300)`)
	mustExec(t, database, `INSERT INTO regular_slots (id, enrollment_id) VALUES (7, 1)`)

	if _, err := database.Exec(`
		INSERT INTO slot_versions (slot_id, valid_from_at, weekday, time)
		VALUES (7, '2026-09-01T00:00', 9, '13:35')`); err == nil {
		t.Fatal("weekday 9 was accepted")
	}
}
