package db_test

import (
	"database/sql"
	"errors"
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

// Same reason as the reminder tables: a migration goose does not pick up fails
// silently as "table missing" much later, in a handler.
func TestUtilityTablesExistAfterMigration(t *testing.T) {
	database := migrated(t)
	for _, table := range []string{
		"addresses", "tariffs", "utilities", "readings"} {
		var name string
		err := database.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %q missing after migrate: %v", table, err)
		}
	}
}

// The month a meter is replaced carries two readings: the final one on the old
// tariff and the first on the new. The data carried over from the old app has
// three such months, so a two-column key would have made them unstorable.
func TestAMeterReplacementMonthTakesTwoReadings(t *testing.T) {
	database := migrated(t)
	seedUtility(t, database)

	insert := func(tariffID int, amount float64) error {
		_, err := database.Exec(
			`INSERT INTO readings (utility_id, tariff_id, period, amount)
			 VALUES (1, ?, '2026-05', ?)`, tariffID, amount)
		return err
	}

	if err := insert(1, 120.50); err != nil {
		t.Fatalf("final reading on the old tariff: %v", err)
	}
	if err := insert(2, 31.00); err != nil {
		t.Fatalf("first reading on the new tariff was rejected: %v", err)
	}
}

// Two readings for the same month on the SAME tariff are not a meter swap,
// they are a double submit — the paid flag would then depend on which row won.
func TestTwoReadingsOnOneTariffAndPeriodAreRejected(t *testing.T) {
	database := migrated(t)
	seedUtility(t, database)

	mustExec(t, database,
		`INSERT INTO readings (utility_id, tariff_id, period, amount)
		 VALUES (1, 1, '2026-05', 120.50)`)

	if _, err := database.Exec(
		`INSERT INTO readings (utility_id, tariff_id, period, amount)
		 VALUES (1, 1, '2026-05', 120.50)`); err == nil {
		t.Fatal("duplicate (utility_id, period, tariff_id) was accepted")
	}
}

// kind drives the arithmetic in model.ComputeAmount. A value it cannot mean
// would silently produce a zero bill rather than an error.
func TestUnknownTariffKindIsRejected(t *testing.T) {
	database := migrated(t)

	if _, err := database.Exec(
		`INSERT INTO tariffs (name, kind, rate1) VALUES ('Смітник', 'per_hour', 10)`); err == nil {
		t.Fatal("an unknown tariff kind passed the CHECK constraint")
	}
}

// The delivery log guarded automatic messages against repeating themselves.
// There are none left — one button on the month view replaced both, and
// pressing it is the decision — so the table went with them. Pinned here
// because a table nothing writes is easy to add back by reflex.
func TestTheDeliveryLogIsGone(t *testing.T) {
	database := migrated(t)

	var name string
	err := database.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='utility_deliveries'`).Scan(&name)
	if err == nil {
		t.Fatal("utility_deliveries is back; nothing writes it")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("checking for the table: %v", err)
	}
}

func seedUtility(t *testing.T, database *sql.DB) {
	t.Helper()
	mustExec(t, database, `INSERT INTO addresses (id, name) VALUES (1, 'Дім')`)
	mustExec(t, database,
		`INSERT INTO tariffs (id, name, kind, unit, rate1) VALUES (1, 'Газ до 05.26', 'meter', 'м3', 7.96)`)
	mustExec(t, database,
		`INSERT INTO tariffs (id, name, kind, unit, rate1) VALUES (2, 'Газ з 05.26', 'meter', 'м3', 8.42)`)
	mustExec(t, database,
		`INSERT INTO utilities (id, address_id, name, current_tariff_id) VALUES (1, 1, 'Газ', 2)`)
}

// Same reason as the reminder and utility tables: a migration goose does not
// pick up fails silently as "table missing" much later, in the Friday review.
func TestSchoolDetailTablesExistAfterMigration(t *testing.T) {
	database := migrated(t)
	for _, table := range []string{
		"school_lesson_details", "school_lesson_marks", "school_lesson_files"} {
		var name string
		err := database.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %q missing after migrate: %v", table, err)
		}
	}
}

// A detail row with no subject or no start is unrenderable and unselectable —
// it would sit in the table forever, invisible to the week query that keys on
// starts_at. Cheaper to refuse it at the schema than to filter it at read.
func TestSchoolDetailRejectsEmptyIdentityFields(t *testing.T) {
	database := migrated(t)

	if _, err := database.Exec(`
		INSERT INTO school_lesson_details (event_id, pupil_id, starts_at, subject)
		VALUES (1, 79311, '', 'Алгебра [9]')`); err == nil {
		t.Fatal("an empty starts_at was accepted")
	}
	if _, err := database.Exec(`
		INSERT INTO school_lesson_details (event_id, pupil_id, starts_at, subject)
		VALUES (2, 79311, '2026-09-03T09:50', '')`); err == nil {
		t.Fatal("an empty subject was accepted")
	}
}

// An empty mark is not "no mark" — it renders as a subject that was graded
// with nothing. Absence is expressed by having no row at all.
func TestSchoolMarkRejectsEmptyKindOrValue(t *testing.T) {
	database := migrated(t)

	if _, err := database.Exec(
		`INSERT INTO school_lesson_marks (event_id, kind, value) VALUES (1, '', '9,00')`); err == nil {
		t.Fatal("an empty mark kind was accepted")
	}
	if _, err := database.Exec(
		`INSERT INTO school_lesson_marks (event_id, kind, value) VALUES (1, 'Поточна', '')`); err == nil {
		t.Fatal("an empty mark value was accepted")
	}
}

// kind says which tab the file hung on, and the renderer will branch on it.
// A third value would silently render as neither.
func TestSchoolFileRejectsAnUnknownKind(t *testing.T) {
	database := migrated(t)

	if _, err := database.Exec(`
		INSERT INTO school_lesson_files (event_id, kind, url)
		VALUES (1, 'video', 'https://example.com/a.png')`); err == nil {
		t.Fatal("an unknown file kind passed the CHECK constraint")
	}
	if _, err := database.Exec(`
		INSERT INTO school_lesson_files (event_id, kind, url)
		VALUES (1, 'homework', '')`); err == nil {
		t.Fatal("an empty url was accepted")
	}
}

// fetched_at answers "how stale is this record" and nothing sets it explicitly
// — the collector inserts without it, so the default has to be there.
func TestSchoolDetailStampsFetchedAtByDefault(t *testing.T) {
	database := migrated(t)

	mustExec(t, database, `
		INSERT INTO school_lesson_details (event_id, pupil_id, starts_at, subject)
		VALUES (1, 79311, '2026-09-03T09:50', 'Українська мова [9]')`)

	var fetchedAt string
	if err := database.QueryRow(
		`SELECT fetched_at FROM school_lesson_details WHERE event_id = 1`).Scan(&fetchedAt); err != nil {
		t.Fatalf("read fetched_at: %v", err)
	}
	if len(fetchedAt) != len("2006-01-02T15:04:05") {
		t.Fatalf("fetched_at = %q, want a local datetime stamp", fetchedAt)
	}
}
