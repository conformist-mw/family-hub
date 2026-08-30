package mini

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"familyhub/internal/model"
	"familyhub/internal/reminders"
	"familyhub/internal/store"
)

// reminderRouter mounts the Mini App with the reminder service wired, the way
// main.go does once the feature is on.
func reminderRouter(t *testing.T, st *store.Store) http.Handler {
	t.Helper()
	svc := reminders.NewService(st, time.UTC, discardLogger(), func() time.Time { return testNow })
	h, err := NewRouter(st, discardLogger(), Config{
		BotToken:     testToken,
		AllowedUsers: []int64{42},
		DevUser:      42,
		Loc:          time.UTC,
		Now:          func() time.Time { return testNow },
		Reminders:    svc,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return h
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
}

// createChore is the happy path every other test starts from.
func createChore(t *testing.T, h http.Handler, title, rrule, date, clock string) int64 {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/mini/api/reminders", map[string]any{
		"title": title, "person": "Олег", "durationMin": 15, "note": "",
		"rrule": rrule, "date": date, "time": clock,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %q = %d: %s", title, rec.Code, rec.Body.String())
	}
	var out struct {
		ID int64 `json:"id"`
	}
	decodeBody(t, rec, &out)
	return out.ID
}

// seedExistingChore makes a chore that has been running for a while, rather
// than one created just now. It matters: a chore created at noon must not
// retroactively claim you forgot it at 08:00 this morning, so anything about
// the recorded past needs a backfill floor that predates it.
func seedExistingChore(t *testing.T, st *store.Store, title, rrule, dtstart, since string) int64 {
	t.Helper()
	r, err := st.CreateReminder(
		model.Reminder{Title: title, Person: "Олег", Active: true, ActiveSince: since},
		model.ReminderRule{ValidFromAt: "2020-01-01T00:00", DTStart: dtstart, RRule: rrule})
	if err != nil {
		t.Fatalf("seed %q: %v", title, err)
	}
	// The rows the chore has already accumulated. In production the
	// materialiser writes them as each moment comes due, and backfills the
	// rest at boot; a read repairs only the last two ticks, deliberately —
	// /calendar.ics is public, and a 30-day pass per GET is a write
	// amplifier. So a chore that has "been running for a while" is one the
	// ticker has passed over, and the seed has to say so rather than lean on
	// a read to invent its past.
	svc := reminders.NewService(st, time.UTC, discardLogger(), func() time.Time { return testNow })
	if err := svc.Materialise(testNow); err != nil {
		t.Fatalf("seed %q: materialise: %v", title, err)
	}
	return r.ID
}

type listResponse struct {
	Reminders []struct {
		ID          int64  `json:"id"`
		Title       string `json:"title"`
		Person      string `json:"person"`
		Active      bool   `json:"active"`
		DurationMin int    `json:"durationMin"`
		Rule        struct {
			ID    int64  `json:"id"`
			RRule string `json:"rrule"`
			Text  string `json:"text"`
			Date  string `json:"date"`
			Time  string `json:"time"`
		} `json:"rule"`
	} `json:"reminders"`
	Occurrences []struct {
		ReminderID int64  `json:"reminderId"`
		Title      string `json:"title"`
		DueAt      string `json:"dueAt"`
		Status     string `json:"status"`
		DoneBy     string `json:"doneBy"`
		CanMark    bool   `json:"canMark"`
	} `json:"occurrences"`
}

func list(t *testing.T, h http.Handler) listResponse {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/mini/api/reminders", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var out listResponse
	decodeBody(t, rec, &out)
	return out
}

func TestCreatingAChoreAndReadingItBack(t *testing.T) {
	h := reminderRouter(t, testStore(t))
	id := createChore(t, h, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1", "2026-08-01", "08:00")

	got := list(t, h)
	if len(got.Reminders) != 1 {
		t.Fatalf("got %d reminders, want 1", len(got.Reminders))
	}
	r := got.Reminders[0]
	if r.ID != id || r.Title != "Кешбек" || r.Person != "Олег" || !r.Active {
		t.Fatalf("reminder = %+v", r)
	}
	if r.Rule.RRule != "FREQ=MONTHLY;BYMONTHDAY=1" || r.Rule.Date != "2026-08-01" || r.Rule.Time != "08:00" {
		t.Fatalf("rule = %+v", r.Rule)
	}
	// The screen prints this rather than parsing the rrule itself; the Go
	// renderer is the only implementation now, and the web reads the same one.
	if r.Rule.Text != "щомісяця, 1-го" {
		t.Fatalf("rule text = %q", r.Rule.Text)
	}
}

// The browser must not decide what can be closed: an occurrence that has not
// come due yet is not markable, and that rule lives in the service.
func TestTheListSaysWhichOccurrencesCanBeClosed(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	// testNow is 2026-08-06 12:00 UTC, and the chore has been running since
	// the 1st — so this morning's 08:00 has already come and gone.
	seedExistingChore(t, st, "Ліки", "FREQ=DAILY;BYHOUR=8,20;BYMINUTE=0;BYSECOND=0",
		"2026-08-06T08:00", "2026-08-01T00:00")

	got := list(t, h)
	var morning, evening bool
	for _, o := range got.Occurrences {
		switch o.DueAt {
		case "2026-08-06T08:00":
			morning = true
			if !o.CanMark {
				t.Fatal("this morning's occurrence is not markable")
			}
		case "2026-08-06T20:00":
			evening = true
			if o.CanMark {
				t.Fatal("tonight's occurrence is markable before it has happened")
			}
		}
	}
	if !morning || !evening {
		var today []string
		for _, o := range got.Occurrences {
			if o.DueAt[:10] == "2026-08-06" {
				today = append(today, o.DueAt)
			}
		}
		t.Fatalf("today holds %v, want both 08:00 and 20:00", today)
	}
}

// Creating a chore at noon must not immediately report that you forgot this
// morning's occurrence: before it existed, nothing was due.
func TestANewChoreHasNoPast(t *testing.T) {
	h := reminderRouter(t, testStore(t))
	createChore(t, h, "Кактус", "FREQ=DAILY", "2026-08-01", "08:00")

	for _, o := range list(t, h).Occurrences {
		if o.DueAt < "2026-08-06T12:00" {
			t.Fatalf("a brand-new chore claims %s already came due", o.DueAt)
		}
	}
}

func TestCreateRejectsWhatAPersonCanFix(t *testing.T) {
	h := reminderRouter(t, testStore(t))
	for _, tc := range []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"no title", map[string]any{"title": "", "rrule": "FREQ=DAILY", "date": "2026-08-01", "time": "08:00"}, "title"},
		{"no date", map[string]any{"title": "X", "rrule": "FREQ=DAILY", "date": "", "time": "08:00"}, "date"},
		{"no time", map[string]any{"title": "X", "rrule": "FREQ=DAILY", "date": "2026-08-01", "time": ""}, "time"},
		{"broken rule", map[string]any{"title": "X", "rrule": "FREQ=NONSENSE", "date": "2026-08-01", "time": "08:00"}, "rrule"},
		{"empty rule", map[string]any{"title": "X", "rrule": "", "date": "2026-08-01", "time": "08:00"}, "rrule"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/mini/api/reminders", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("code = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			var out struct {
				Error struct {
					Field string `json:"field"`
				} `json:"error"`
			}
			decodeBody(t, rec, &out)
			if out.Error.Field != tc.field {
				t.Fatalf("field = %q, want %q", out.Error.Field, tc.field)
			}
		})
	}
}

func TestUpdatingAChore(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	id := createChore(t, h, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1", "2026-08-01", "08:00")

	rec := do(t, h, http.MethodPut, "/mini/api/reminders/"+itoa(id), map[string]any{
		"title": "Кешбек у банку", "person": "Оксана", "durationMin": 30, "note": "обидві картки",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	got := list(t, h)
	r := got.Reminders[0]
	if r.Title != "Кешбек у банку" || r.Person != "Оксана" || r.DurationMin != 30 {
		t.Fatalf("after update = %+v", r)
	}
}

// Pausing goes through the switch, which also stamps the backfill floor. The
// chore has to be seeded with a genuinely OLD floor: created at testNow, the
// expected value after a resume equals the creation stamp, and the assertion
// cannot tell "stamped correctly" from "never moved".
func TestResumingAChoreMovesTheBackfillFloor(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	id := seedExistingChore(t, st, "Кактус", "FREQ=DAILY", "2026-01-01T08:00", "2026-01-01T00:00")

	off, on := false, true
	if rec := do(t, h, http.MethodPut, "/mini/api/reminders/"+itoa(id), map[string]any{
		"title": "Кактус", "durationMin": 15, "active": &off,
	}); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rec.Code, rec.Body.String())
	}
	rem, _ := st.GetReminder(id)
	if rem.Active {
		t.Fatal("the chore is still on")
	}
	if rem.ActiveSince != "2026-01-01T00:00" {
		t.Fatalf("pausing moved the floor to %q", rem.ActiveSince)
	}

	if rec := do(t, h, http.MethodPut, "/mini/api/reminders/"+itoa(id), map[string]any{
		"title": "Кактус", "durationMin": 15, "active": &on,
	}); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body.String())
	}
	rem, _ = st.GetReminder(id)
	if !rem.Active {
		t.Fatal("the chore did not come back on")
	}
	if rem.ActiveSince != "2026-08-06T12:00" {
		t.Fatalf("active_since = %q, want the service clock at resume", rem.ActiveSince)
	}
}

// Editing a title must not touch the switch. Re-stamping the floor on every
// edit would erase a chore's pending history each time someone fixed a typo.
func TestEditingAChoreLeavesTheBackfillFloorAlone(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	id := seedExistingChore(t, st, "Кактус", "FREQ=DAILY", "2026-01-01T08:00", "2026-01-01T00:00")

	on := true
	if rec := do(t, h, http.MethodPut, "/mini/api/reminders/"+itoa(id), map[string]any{
		"title": "Полити кактус", "durationMin": 15, "active": &on, // already active
	}); rec.Code != http.StatusOK {
		t.Fatalf("edit = %d: %s", rec.Code, rec.Body.String())
	}
	rem, _ := st.GetReminder(id)
	if rem.Title != "Полити кактус" {
		t.Fatalf("title = %q", rem.Title)
	}
	if rem.ActiveSince != "2026-01-01T00:00" {
		t.Fatalf("editing an active chore re-stamped the floor to %q", rem.ActiveSince)
	}
}

func TestDeletingAChoreHidesIt(t *testing.T) {
	h := reminderRouter(t, testStore(t))
	id := createChore(t, h, "Зникає", "FREQ=DAILY", "2026-08-01", "08:00")

	if rec := do(t, h, http.MethodDelete, "/mini/api/reminders/"+itoa(id), nil); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if got := list(t, h); len(got.Reminders) != 0 {
		t.Fatalf("deleted chore still listed: %+v", got.Reminders)
	}
	if rec := do(t, h, http.MethodDelete, "/mini/api/reminders/"+itoa(id), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}
}

func TestUnknownChoreIsNotFound(t *testing.T) {
	h := reminderRouter(t, testStore(t))
	for _, tc := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodDelete, "/mini/api/reminders/999", nil},
		{http.MethodPut, "/mini/api/reminders/999",
			map[string]any{"title": "X", "durationMin": 15}},
		{http.MethodPost, "/mini/api/reminders/999/rules",
			map[string]any{"rrule": "FREQ=DAILY", "date": "2026-08-01", "time": "08:00"}},
	} {
		rec := do(t, h, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

// Adding a version is a POST, not a PUT: it appends: what already came due
// keeps the schedule it actually ran on.
func TestANewRuleVersionLeavesTheRecordedPastAlone(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	id := seedExistingChore(t, st, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1",
		"2026-08-01T08:00", "2026-07-01T00:00")

	// August 1st has already come due at testNow (the 6th).
	before := list(t, h)
	var augustSeen bool
	for _, o := range before.Occurrences {
		if o.DueAt == "2026-08-01T08:00" {
			augustSeen = true
		}
	}
	if !augustSeen {
		t.Fatalf("August's occurrence missing before the change: %+v", before.Occurrences)
	}

	rec := do(t, h, http.MethodPost, "/mini/api/reminders/"+itoa(id)+"/rules", map[string]any{
		"rrule": "FREQ=MONTHLY;BYMONTHDAY=5", "date": "2026-09-05", "time": "08:00",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add rule = %d: %s", rec.Code, rec.Body.String())
	}

	after := list(t, h)
	augustSeen = false
	for _, o := range after.Occurrences {
		if o.DueAt == "2026-08-01T08:00" {
			augustSeen = true
		}
		if o.DueAt == "2026-09-01T08:00" {
			t.Fatal("the old rule still projects into the future")
		}
	}
	if !augustSeen {
		t.Fatalf("changing the rule rewrote the past: %+v", after.Occurrences)
	}
	rules, err := st.RulesFor(id)
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules = %+v err = %v, want two versions", rules, err)
	}
}

func TestAmendingAVersionRewritesItInPlace(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	id := createChore(t, h, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1", "2026-08-01", "08:00")
	rules, _ := st.RulesFor(id)

	rec := do(t, h, http.MethodPut,
		"/mini/api/reminders/"+itoa(id)+"/rules/"+itoa(rules[0].ID), map[string]any{
			"rrule": "FREQ=MONTHLY;BYMONTHDAY=5", "date": "2026-08-05", "time": "08:00",
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("amend = %d: %s", rec.Code, rec.Body.String())
	}
	after, _ := st.RulesFor(id)
	if len(after) != 1 {
		t.Fatalf("amend created a version: %d total", len(after))
	}
	if after[0].RRule != "FREQ=MONTHLY;BYMONTHDAY=5" {
		t.Fatalf("rule = %q", after[0].RRule)
	}
}

func TestAmendingAnUnknownVersionIsNotFound(t *testing.T) {
	h := reminderRouter(t, testStore(t))
	id := createChore(t, h, "Кешбек", "FREQ=DAILY", "2026-08-01", "08:00")
	rec := do(t, h, http.MethodPut, "/mini/api/reminders/"+itoa(id)+"/rules/999", map[string]any{
		"rrule": "FREQ=DAILY", "date": "2026-08-01", "time": "08:00",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

// --- marking ---

func TestClosingAnOccurrence(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	// A chore that has been running a while: one created just now has no past,
	// and closing a moment from before it existed is refused on purpose.
	id := seedExistingChore(t, st, "Кактус", "FREQ=DAILY", "2026-08-01T08:00", "2026-07-01T00:00")

	rec := do(t, h, http.MethodPost, "/mini/api/reminders/"+itoa(id)+"/occurrences", map[string]any{
		"dueAt": "2026-08-05T08:00", "status": model.OccDone,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("mark = %d: %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetOccurrence(id, "2026-08-05T08:00")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != model.OccDone {
		t.Fatalf("status = %q", got.Status)
	}
}

// A row in the future would be invisible to the calendar, which projects that
// half from the rules.
func TestClosingSomethingThatHasNotHappenedIsRefused(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	id := seedExistingChore(t, st, "Кактус", "FREQ=DAILY", "2026-08-01T08:00", "2026-07-01T00:00")

	rec := do(t, h, http.MethodPost, "/mini/api/reminders/"+itoa(id)+"/occurrences", map[string]any{
		"dueAt": "2026-08-10T08:00", "status": model.OccDone,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

func TestClosingAnInstantTheRulesNeverScheduledIsNotFound(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	id := seedExistingChore(t, st, "Кешбек", "FREQ=MONTHLY;BYMONTHDAY=1", "2026-08-01T08:00", "2026-07-01T00:00")

	rec := do(t, h, http.MethodPost, "/mini/api/reminders/"+itoa(id)+"/occurrences", map[string]any{
		"dueAt": "2026-08-03T08:00", "status": model.OccDone,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestAnUnknownStatusIsRefused(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	id := seedExistingChore(t, st, "Кактус", "FREQ=DAILY", "2026-08-01T08:00", "2026-07-01T00:00")

	rec := do(t, h, http.MethodPost, "/mini/api/reminders/"+itoa(id)+"/occurrences", map[string]any{
		"dueAt": "2026-08-05T08:00", "status": "forgotten",
	})
	if rec.Code == http.StatusOK {
		t.Fatal("an unknown status was accepted")
	}
}

// --- preview ---

func TestPreviewShowsWhatARuleWouldDo(t *testing.T) {
	h := reminderRouter(t, testStore(t))
	rec := do(t, h, http.MethodPost, "/mini/api/reminders/preview", map[string]any{
		"rrule": "FREQ=MONTHLY;BYMONTHDAY=1", "date": "2026-08-01", "time": "08:00",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Next []struct {
			DueAt string `json:"dueAt"`
			Date  string `json:"date"`
			Time  string `json:"time"`
		} `json:"next"`
	}
	decodeBody(t, rec, &out)
	if len(out.Next) != 5 {
		t.Fatalf("got %d dates, want 5", len(out.Next))
	}
	if out.Next[0].DueAt != "2026-09-01T08:00" {
		t.Fatalf("first date = %q, want the next one after today", out.Next[0].DueAt)
	}
	if out.Next[0].Time != "08:00" {
		t.Fatalf("time = %q", out.Next[0].Time)
	}
}

// Preview must reject exactly what Create would, using the same library — so a
// rule that previews cannot fail to save.
func TestPreviewRefusesWhatCreateWouldRefuse(t *testing.T) {
	h := reminderRouter(t, testStore(t))
	rec := do(t, h, http.MethodPost, "/mini/api/reminders/preview", map[string]any{
		"rrule": "FREQ=NONSENSE", "date": "2026-08-01", "time": "08:00",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// --- mounting and auth ---

// Without the service the routes are absent rather than present-and-failing,
// the same shape as the Mini App itself without a bot token.
func TestReminderRoutesAreAbsentWithoutTheService(t *testing.T) {
	h := testRouter(t, testStore(t), []int64{42}, 42)
	if rec := do(t, h, http.MethodGet, "/mini/api/reminders", nil); rec.Code == http.StatusOK {
		t.Fatalf("reminders answered %d with no service wired", rec.Code)
	}
}

func TestReminderRoutesRequireAuthentication(t *testing.T) {
	st := testStore(t)
	svc := reminders.NewService(st, time.UTC, discardLogger(), func() time.Time { return testNow })
	h, err := NewRouter(st, discardLogger(), Config{
		BotToken: testToken, AllowedUsers: []int64{42},
		Loc: time.UTC, Now: func() time.Time { return testNow },
		Reminders: svc,
		// No DevUser: unsigned requests must be rejected.
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	for _, path := range []string{"/mini/api/reminders"} {
		rec := do(t, h, http.MethodGet, path, nil)
		if rec.Code == http.StatusOK {
			t.Fatalf("GET %s answered 200 unauthenticated", path)
		}
	}
}

// Anything the materialiser recorded must be reachable on the screen. With a
// shorter list window than the backfill window, an unclosed chore slid out of
// sight while its row stayed pending forever — a miss nobody was ever given
// the chance to close.
func TestNothingRecordedFallsOutOfTheList(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	// Running since well before the backfill window opened, so the oldest rows
	// the materialiser will write sit right at its edge.
	seedExistingChore(t, st, "Кактус", "FREQ=DAILY", "2026-06-01T11:00", "2026-01-01T00:00")

	got := list(t, h)
	var oldest string
	for _, o := range got.Occurrences {
		if o.CanMark && (oldest == "" || o.DueAt < oldest) {
			oldest = o.DueAt
		}
	}
	if oldest == "" {
		t.Fatal("nothing came back at all")
	}

	// Whatever the store holds as pending has to be on the screen.
	rows, err := st.PendingOccurrencesIn("2000-01-01T00:00", "2026-08-06T12:00")
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	for _, r := range rows {
		if r.DueAt < oldest {
			t.Fatalf("occurrence %s is pending in the store but before the list's oldest (%s)",
				r.DueAt, oldest)
		}
	}
}

// The floor the service enforces has to be visible through the API too: a
// decision about a moment from before the chore existed is invented history.
func TestClosingAMomentFromBeforeTheChoreExistedIsRefused(t *testing.T) {
	st := testStore(t)
	h := reminderRouter(t, st)
	id := seedExistingChore(t, st, "Кактус", "FREQ=DAILY", "2026-08-01T08:00", "2026-08-04T00:00")

	rec := do(t, h, http.MethodPost, "/mini/api/reminders/"+itoa(id)+"/occurrences", map[string]any{
		"dueAt": "2026-08-02T08:00", "status": model.OccDone,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
