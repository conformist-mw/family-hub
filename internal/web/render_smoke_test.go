package web

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/reminders"
	"familyhub/internal/store"
)

// A template that only breaks when a particular page is opened breaks in
// production, because nothing else opens every page. This walks all of them
// against realistic data, so a bad field name or a renamed helper fails here
// instead of on somebody's phone.
//
// Ported from home-meters, which had the same gap and closed it this way.
func smokeRouter(t *testing.T) http.Handler {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(database)

	mustExec(t, database, `INSERT INTO persons (id, name) VALUES (1, 'Дьома')`)
	mustExec(t, database, `
		INSERT INTO enrollments (id, person_id, name, billing_type, current_price)
		VALUES (1, 1, 'Футбол', 'per_lesson', 300)`)
	mustExec(t, database, `INSERT INTO regular_slots (id, enrollment_id) VALUES (1, 1)`)
	mustExec(t, database, `
		INSERT INTO slot_versions (slot_id, valid_from_at, weekday, time, duration_min)
		VALUES (1, '2000-01-01T00:00', 2, '18:00', 60)`)
	mustExec(t, database, `
		INSERT INTO visits (enrollment_id, date, status) VALUES (1, '2026-08-04', 'done')`)
	mustExec(t, database, `
		INSERT INTO payments (enrollment_id, date, amount, lessons_paid)
		VALUES (1, '2026-08-01', 2400, 8)`)
	mustExec(t, database, `
		INSERT INTO appointments (person, title, starts_at, status)
		VALUES ('Дьома', 'Стоматолог', '2026-09-10T09:00', 'planned')`)
	mustExec(t, database, `INSERT INTO trainers (id, name) VALUES (1, 'Олена')`)

	svc := reminders.NewService(st, time.Local, logger, time.Now)
	return NewRouter(database, logger, "", nil, nil, svc)
}

func TestEveryPageRenders(t *testing.T) {
	router := smokeRouter(t)
	for _, path := range []string{
		"/",             // hub
		"/appointments", // shell
		"/appointments/new",
		"/reminders",
		"/reminders/new",
		"/reminders/history",
		"/lessons", // world: Заняття
		"/visits",
		"/visits/new",
		"/payments",
		"/payments/new",
		"/enrollments",
		"/enrollments/new",
		"/enrollments/1/edit",
		"/enrollments/1/audit",
		"/trainers",
		"/meters", // world: Дім — empty until the data copy
		"/meters/tariffs",
		"/meters/utilities",
		"/meters/addresses",
		"/stats", // world: Статистика
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d\n%s", path, rec.Code, rec.Body.String())
			}
			// A template that fails halfway leaves a 200 with a truncated body,
			// because the status is written before execution starts.
			if !strings.Contains(rec.Body.String(), "</html>") {
				t.Fatalf("GET %s stopped mid-template:\n%s", path, rec.Body.String())
			}
		})
	}
}

// The point of the two tiers: a page inside a world shows that world's
// navigation, and the hub shows none.
func TestAWorldPageCarriesItsOwnNavigation(t *testing.T) {
	router := smokeRouter(t)

	lessons := getBody(t, router, "/lessons")
	if !strings.Contains(lessons, `class="subnav"`) {
		t.Fatal("the lessons world renders without its second-tier nav")
	}
	if !strings.Contains(lessons, "Тренери") {
		t.Fatal("the lessons nav is missing Тренери")
	}

	hub := getBody(t, router, "/")
	if strings.Contains(hub, `class="subnav"`) {
		t.Fatal("the hub renders a world's navigation")
	}
}

// The utilities world is reachable and empty rather than hidden. Empty is a
// state these screens will keep showing for any month nobody has entered, so
// walking into one is not a dead end — and the navigation does not have to
// change again when the data arrives.
func TestTheUtilitiesWorldIsWalkableWhileEmpty(t *testing.T) {
	router := smokeRouter(t)

	if body := getBody(t, router, "/"); !strings.Contains(body, `href="/meters"`) {
		t.Fatal("the hub does not offer a way into the utilities world")
	}

	meters := getBody(t, router, "/meters")
	if !strings.Contains(meters, `class="subnav"`) || !strings.Contains(meters, "Тарифи") {
		t.Fatal("the utilities world renders without its own navigation")
	}
	if !strings.Contains(meters, "Даних ще немає") {
		t.Fatal("an empty utilities screen does not say it is empty")
	}
}

func mustExec(t *testing.T, database *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := database.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func getBody(t *testing.T, router http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	return rec.Body.String()
}
