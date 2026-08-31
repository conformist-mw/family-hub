package web

import (
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"familyhub/internal/db"
	"familyhub/internal/model"
	"familyhub/internal/reminders"
	"familyhub/internal/store"
)

func choreApp(t *testing.T, wired bool) (http.Handler, *store.Store, *reminders.Service) {
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
	var svc *reminders.Service
	if wired {
		svc = reminders.NewService(st, time.Local, logger, time.Now)
	}
	return NewRouter(database, logger, "", nil, nil, svc), st, svc
}

// seedExistingChore makes a chore that has been running for a while, rather
// than one created just now. A chore created at noon deliberately has no past —
// its backfill floor is the moment it was switched on — so anything about a
// recorded occurrence needs a floor that predates it, plus the materialiser
// pass the ticker would have run in production.
func seedExistingChore(t *testing.T, st *store.Store, svc *reminders.Service,
	title, rrule string, dtstart time.Time) int64 {
	t.Helper()
	rem, err := st.CreateReminder(
		model.Reminder{
			Title: title, Person: "Оксана", DurationMin: 15, Active: true,
			ActiveSince: dtstart.Add(-24 * time.Hour).Format(model.LocalDatetime),
		},
		model.ReminderRule{
			ValidFromAt: dtstart.Add(-24 * time.Hour).Format(model.LocalDatetime),
			DTStart:     dtstart.Format(model.LocalDatetime),
			RRule:       rrule,
		})
	if err != nil {
		t.Fatalf("seed %q: %v", title, err)
	}
	if err := svc.Materialise(time.Now()); err != nil {
		t.Fatalf("seed %q: materialise: %v", title, err)
	}
	return rem.ID
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func post(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// The whole point of #54: a chore can be made with a real keyboard.
func TestAChoreCanBeCreatedFromTheDesk(t *testing.T) {
	h, st, _ := choreApp(t, true)

	rec := post(t, h, "/reminders", url.Values{
		"title": {"Кешбек"}, "person": {"Оксана"},
		"rrule": {"FREQ=MONTHLY;BYMONTHDAY=1"},
		"date":  {"2026-09-01"}, "time": {"08:00"},
		"durationMin": {"15"}, "active": {"1"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	all, err := st.Reminders()
	if err != nil {
		t.Fatalf("Reminders: %v", err)
	}
	if len(all) != 1 || all[0].Title != "Кешбек" || all[0].Person != "Оксана" {
		t.Fatalf("stored = %+v", all)
	}
	rules, _ := st.RulesFor(all[0].ID)
	if len(rules) != 1 || rules[0].RRule != "FREQ=MONTHLY;BYMONTHDAY=1" {
		t.Fatalf("rules = %+v", rules)
	}
	if rules[0].DTStart != "2026-09-01T08:00" {
		t.Fatalf("dtstart = %q", rules[0].DTStart)
	}
}

// A rule the library cannot expand must come back as something to fix, not as
// a 500 and not as a stored row that breaks every later read.
func TestABadRuleIsRejectedWithAMessage(t *testing.T) {
	h, st, _ := choreApp(t, true)

	rec := post(t, h, "/reminders", url.Values{
		"title": {"Зламане"}, "rrule": {"FREQ=NONSENSE"},
		"date": {"2026-09-01"}, "time": {"08:00"}, "durationMin": {"15"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the form back, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "правило") {
		t.Fatalf("no message about the rule:\n%s", rec.Body.String())
	}
	if all, _ := st.Reminders(); len(all) != 0 {
		t.Fatalf("an unexpandable rule was stored: %+v", all)
	}
}

func TestAChoreWithoutATitleIsRejected(t *testing.T) {
	h, st, _ := choreApp(t, true)
	rec := post(t, h, "/reminders", url.Values{
		"title": {"  "}, "rrule": {"FREQ=DAILY"},
		"date": {"2026-09-01"}, "time": {"08:00"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the form back, got %d", rec.Code)
	}
	if all, _ := st.Reminders(); len(all) != 0 {
		t.Fatal("a nameless chore was stored")
	}
}

// The list has to say the rule in words. That renderer moved to Go precisely
// so this page and the Mini App cannot disagree about it.
func TestTheListSaysTheRuleInUkrainian(t *testing.T) {
	h, _, svc := choreApp(t, true)
	if _, err := svc.Create(model.Reminder{Title: "Прибирання"},
		"FREQ=WEEKLY;INTERVAL=2;BYDAY=SA", time.Now().Add(-time.Hour), "Оксана"); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := get(t, h, "/reminders").Body.String()
	if !strings.Contains(body, "раз на 2 тижні, сб") {
		t.Fatalf("the rule is not in words:\n%s", body)
	}
}

// Editing without touching the rule must not stamp a version. Otherwise fixing
// a typo in the title fills the history with versions that say nothing.
func TestSavingWithoutChangingTheRuleAddsNoVersion(t *testing.T) {
	h, st, svc := choreApp(t, true)
	rem, err := svc.Create(model.Reminder{Title: "Кактус", DurationMin: 15},
		"FREQ=DAILY", time.Now().Add(-time.Hour), "Оксана")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rules, _ := st.RulesFor(rem.ID)
	date, clock := splitLocalDatetime(rules[0].DTStart)

	rec := post(t, h, "/reminders/"+itoa(rem.ID), url.Values{
		"title": {"Кактус на кухні"}, "rrule": {rules[0].RRule},
		"date": {date}, "time": {clock}, "durationMin": {"15"},
		"active": {"1"}, "ruleId": {itoa(rules[0].ID)}, "ruleChange": {"new"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	after, _ := st.RulesFor(rem.ID)
	if len(after) != 1 {
		t.Fatalf("got %d versions, want the original only", len(after))
	}
	got, _ := st.GetReminder(rem.ID)
	if got.Title != "Кактус на кухні" {
		t.Fatalf("title = %q", got.Title)
	}
}

// "Відсьогодні" is the ordinary edit: the old rule stays on the record.
func TestChangingTheRuleFromTodayKeepsTheOldVersion(t *testing.T) {
	h, st, svc := choreApp(t, true)
	rem, err := svc.Create(model.Reminder{Title: "Кешбек", DurationMin: 15},
		"FREQ=MONTHLY;BYMONTHDAY=1", time.Now().Add(-24*time.Hour), "Оксана")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rules, _ := st.RulesFor(rem.ID)
	date, clock := splitLocalDatetime(rules[0].DTStart)

	rec := post(t, h, "/reminders/"+itoa(rem.ID), url.Values{
		"title": {"Кешбек"}, "rrule": {"FREQ=MONTHLY;BYMONTHDAY=5"},
		"date": {date}, "time": {clock}, "durationMin": {"15"},
		"active": {"1"}, "ruleId": {itoa(rules[0].ID)}, "ruleChange": {"new"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	after, _ := st.RulesFor(rem.ID)
	if len(after) != 2 {
		t.Fatalf("got %d versions, want the old one and the new", len(after))
	}
	if after[0].RRule != "FREQ=MONTHLY;BYMONTHDAY=1" {
		t.Fatalf("the old rule was rewritten: %+v", after[0])
	}
	if after[1].RRule != "FREQ=MONTHLY;BYMONTHDAY=5" {
		t.Fatalf("the new rule did not land: %+v", after[1])
	}
}

// "Виправити" rewrites the record on purpose — the rule was mistyped.
func TestAmendingCorrectsTheVersionInPlace(t *testing.T) {
	h, st, svc := choreApp(t, true)
	rem, err := svc.Create(model.Reminder{Title: "Кешбек", DurationMin: 15},
		"FREQ=MONTHLY;BYMONTHDAY=1", time.Now().Add(-24*time.Hour), "Оксана")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rules, _ := st.RulesFor(rem.ID)
	date, clock := splitLocalDatetime(rules[0].DTStart)

	rec := post(t, h, "/reminders/"+itoa(rem.ID), url.Values{
		"title": {"Кешбек"}, "rrule": {"FREQ=MONTHLY;BYMONTHDAY=5"},
		"date": {date}, "time": {clock}, "durationMin": {"15"},
		"active": {"1"}, "ruleId": {itoa(rules[0].ID)}, "ruleChange": {"amend"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	after, _ := st.RulesFor(rem.ID)
	if len(after) != 1 {
		t.Fatalf("an amend created a version: %d", len(after))
	}
	if after[0].RRule != "FREQ=MONTHLY;BYMONTHDAY=5" {
		t.Fatalf("the correction did not stick: %+v", after[0])
	}
}

// Closing a chore from the desk, which is the daily gesture the web could not
// do at all.
func TestAnOccurrenceCanBeClosedFromTheWeb(t *testing.T) {
	h, st, svc := choreApp(t, true)
	// Due an hour ago, so it has come due and can be answered.
	due := time.Now().Add(-time.Hour).Truncate(time.Minute)
	id := seedExistingChore(t, st, svc, "Ліки", "FREQ=DAILY", due)

	rec := post(t, h, "/reminders/"+itoa(id)+"/mark", url.Values{
		"dueAt": {due.Format(model.LocalDatetime)}, "status": {"done"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("mark = %d: %s", rec.Code, rec.Body.String())
	}
	row, err := st.GetOccurrence(id, due.Format(model.LocalDatetime))
	if err != nil {
		t.Fatalf("GetOccurrence: %v", err)
	}
	if row.Status != model.OccDone {
		t.Fatalf("status = %q, want done", row.Status)
	}
	if row.DoneBy == "" {
		t.Fatal("nobody was recorded as having closed it")
	}
}

// Closing something that has not happened is refused by the service, and the
// page has to say so rather than 500.
func TestClosingSomethingNotYetDueIsRefusedPolitely(t *testing.T) {
	h, _, svc := choreApp(t, true)
	future := time.Now().Add(6 * time.Hour).Truncate(time.Minute)
	rem, err := svc.Create(model.Reminder{Title: "Ліки", DurationMin: 15},
		"FREQ=DAILY", future, "Оксана")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec := post(t, h, "/reminders/"+itoa(rem.ID)+"/mark", url.Values{
		"dueAt": {future.Format(model.LocalDatetime)}, "status": {"done"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("mark = %d, want the page back with a message", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ще не настала") {
		t.Fatalf("no explanation shown:\n%s", rec.Body.String())
	}
}

// The dashboard's whole complaint in #54: opening the app at midday said
// nothing about what was forgotten at 08:00.
func TestTheDashboardShowsWhatIsStillOpenToday(t *testing.T) {
	h, st, svc := choreApp(t, true)
	due := time.Now().Add(-2 * time.Hour).Truncate(time.Minute)
	if due.Day() != time.Now().Day() {
		t.Skip("run crosses midnight; today's window would not hold the occurrence")
	}
	seedExistingChore(t, st, svc, "Кешбек", "FREQ=DAILY", due)

	body := get(t, h, "/").Body.String()
	if !strings.Contains(body, "Сьогодні не закрито") || !strings.Contains(body, "Кешбек") {
		t.Fatalf("the dashboard is silent about the open chore:\n%s", body)
	}
}

// A nil service is a deployment state — the feature is off — not an error.
func TestWithoutTheChoresServiceThePagesAre404(t *testing.T) {
	h, _, _ := choreApp(t, false)
	for _, path := range []string{"/reminders", "/reminders/new"} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	// And the dashboard still renders, without a chores section.
	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Сьогодні не закрито") {
		t.Fatal("the dashboard shows a chores section with no chores service")
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// #55: the record exists and there was nowhere to look at it. The overview
// ranks by what keeps being forgotten, which is what makes it actionable.
func TestTheHistoryOverviewRanksTheWorstKeptFirst(t *testing.T) {
	h, st, svc := choreApp(t, true)
	start := time.Now().Add(-5 * 24 * time.Hour).Truncate(time.Minute)
	kept := seedExistingChore(t, st, svc, "Кактус", "FREQ=DAILY", start)
	seedExistingChore(t, st, svc, "Кешбек", "FREQ=DAILY", start)

	// Answer every occurrence of Кактус and none of Кешбек.
	occ, err := svc.Upcoming(start.Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatalf("Upcoming: %v", err)
	}
	for _, o := range occ {
		if o.ReminderID != kept {
			continue
		}
		if err := svc.Mark(kept, o.Due, model.OccDone, "Оксана"); err != nil {
			t.Fatalf("mark: %v", err)
		}
	}

	body := get(t, h, "/reminders/history").Body.String()
	cashback := strings.Index(body, "Кешбек")
	cactus := strings.Index(body, "Кактус")
	if cashback < 0 || cactus < 0 {
		t.Fatalf("a chore is missing from the overview:\n%s", body)
	}
	if cashback > cactus {
		t.Fatal("the well-kept chore is listed above the one that keeps being forgotten")
	}
}

// The drill-down is the per-moment record — the whole reason occurrences are
// stored rather than recomputed.
func TestTheDrillDownShowsEachOccurrence(t *testing.T) {
	h, st, svc := choreApp(t, true)
	start := time.Now().Add(-3 * 24 * time.Hour).Truncate(time.Minute)
	id := seedExistingChore(t, st, svc, "Кешбек", "FREQ=DAILY", start)
	if err := svc.Mark(id, start, model.OccDone, "Оксана"); err != nil {
		t.Fatalf("mark: %v", err)
	}

	body := get(t, h, "/reminders/"+itoa(id)+"/history").Body.String()
	for _, want := range []string{"Кешбек", "закрито", "Оксана", "не закрито"} {
		if !strings.Contains(body, want) {
			t.Errorf("the ledger is missing %q:\n%s", want, body)
		}
	}
}

// A period reaching past the 30-day floor has to say so. A silent hole there
// reads as "nothing came due", which is not what it means.
func TestAPeriodPastTheFloorSaysTheRecordIsShort(t *testing.T) {
	h, _, _ := choreApp(t, true)
	// Three months back reaches well past the backfill window.
	from := time.Now().AddDate(0, -3, 0).Format("2006-01-02")
	to := time.Now().Format("2006-01-02")
	body := get(t, h, "/reminders/history?range=custom&from="+from+"&to="+to).Body.String()
	if !strings.Contains(body, "Записи ведуться лише з") {
		t.Fatalf("the short record is not admitted:\n%s", body)
	}
}

// A bad custom period falls back with a message rather than a blank screen.
func TestABadCustomPeriodFallsBack(t *testing.T) {
	h, _, _ := choreApp(t, true)
	rec := get(t, h, "/reminders/history?range=custom&from=не-дата&to=теж-ні")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want the page with a message", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "не вдалося зрозуміти період") {
		t.Fatalf("no explanation:\n%s", body)
	}
	if !strings.Contains(body, "останні 30 днів") {
		t.Fatalf("no fallback period shown:\n%s", body)
	}
}

// Tonight's open chore must not be counted with yesterday's forgotten one.
func TestTheLedgerSeparatesForgottenFromNotYet(t *testing.T) {
	h, st, svc := choreApp(t, true)
	now := time.Now()
	if now.Hour() < 1 || now.Hour() > 22 {
		t.Skip("needs an hour with room either side inside today")
	}
	// One an hour ago (forgotten), one in an hour (not yet).
	past := now.Add(-time.Hour).Truncate(time.Minute)
	id := seedExistingChore(t, st, svc, "Ліки", "FREQ=HOURLY", past)

	body := get(t, h, "/reminders/"+itoa(id)+"/history").Body.String()
	if !strings.Contains(body, "не закрито") {
		t.Errorf("the forgotten one is not marked:\n%s", body)
	}
	if !strings.Contains(body, "ще не настало") {
		t.Errorf("what has not come due is counted as forgotten:\n%s", body)
	}
}

func TestHistoryIs404WithoutTheChoresService(t *testing.T) {
	h, _, _ := choreApp(t, false)
	if rec := get(t, h, "/reminders/history"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /reminders/history = %d, want 404", rec.Code)
	}
}

// A number input rejects anything that is not min + n*step, so a careless step
// can make a field refuse its own default — which is exactly what `min="1"
// step="5"` did to the 15 this form starts with. The rule is easy to
// reintroduce and invisible until somebody tries to save, so it is pinned
// against every number input on both chore forms rather than against the one
// that broke.
func TestNoChoreFormFieldRejectsItsOwnValue(t *testing.T) {
	h, st, svc := choreApp(t, true)
	rem, err := svc.Create(model.Reminder{Title: "Кешбек", DurationMin: 15},
		"FREQ=DAILY", time.Now().Add(-time.Hour), "Оксана")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = st

	for _, path := range []string{"/reminders/new", "/reminders/" + itoa(rem.ID) + "/edit"} {
		body := get(t, h, path).Body.String()
		for _, in := range numberInputs(body) {
			if in.step <= 0 {
				continue // no step: every value is valid
			}
			if in.value < in.min {
				t.Errorf("%s: %s defaults to %g, below its own min %g",
					path, in.name, in.value, in.min)
				continue
			}
			// The check the browser makes before it will submit.
			if steps := (in.value - in.min) / in.step; steps != math.Trunc(steps) {
				t.Errorf("%s: %s defaults to %g, which min=%g step=%g refuses — "+
					"the browser will not save this form untouched",
					path, in.name, in.value, in.min, in.step)
			}
		}
	}
}

type numberInput struct {
	name             string
	value, min, step float64
}

// numberInputs pulls every <input type="number"> out of a rendered page. A
// crude parse on purpose: it reads what the browser reads, so a field it
// cannot understand is a field a browser could not either.
func numberInputs(body string) []numberInput {
	var out []numberInput
	for _, tag := range regexp.MustCompile(`<input[^>]*type="number"[^>]*>`).FindAllString(body, -1) {
		attr := func(name string) (float64, bool) {
			m := regexp.MustCompile(name + `="([^"]*)"`).FindStringSubmatch(tag)
			if len(m) != 2 {
				return 0, false
			}
			v, err := strconv.ParseFloat(m[1], 64)
			return v, err == nil
		}
		in := numberInput{}
		if m := regexp.MustCompile(`name="([^"]*)"`).FindStringSubmatch(tag); len(m) == 2 {
			in.name = m[1]
		}
		v, hasValue := attr("value")
		if !hasValue {
			continue // an empty field is not yet an answer to check
		}
		in.value = v
		in.min, _ = attr("min")
		in.step, _ = attr("step")
		out = append(out, in)
	}
	return out
}
