package cooking

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"familyhub/internal/mealie"
)

const twoRules = `{"items":[
 {"day":"unset","entryType":"lunch","queryFilterString":"tags.slug IN [\"obid\"]"},
 {"day":"unset","entryType":"dinner","queryFilterString":"tags.slug IN [\"vecheria\"]"}]}`

// fakePlanner serves the reads a pass makes and records what it planned.
// recipes answers the recipe query; it receives the decoded filter, so a test
// can make the rested query come back empty and the bare one not.
type fakePlanner struct {
	rules   string
	plan    string
	recipes func(filter string) string

	created []string // "date/slot/recipeId"
	filters []string
}

func (f *fakePlanner) planner(t *testing.T, cfg PlannerConfig) *Planner {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/households/mealplans/rules":
			_, _ = w.Write([]byte(f.rules))
		case r.URL.Path == "/api/households/mealplans" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(f.plan))
		case r.URL.Path == "/api/households/mealplans" && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.created = append(f.created, fmt.Sprintf("%v/%v/%v", in["date"], in["entryType"], in["recipeId"]))
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/api/recipes":
			filter, _ := url.QueryUnescape(r.URL.Query().Get("queryFilter"))
			f.filters = append(f.filters, filter)
			_, _ = w.Write([]byte(f.recipes(filter)))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	if cfg.Loc == nil {
		cfg.Loc = time.UTC
	}
	if len(cfg.Slots) == 0 {
		cfg.Slots = []string{"lunch"}
	}
	if cfg.Horizon == 0 {
		cfg.Horizon = 1
	}
	return NewPlanner(mealie.New(srv.URL, "t"), cfg)
}

func always(body string) func(string) string {
	return func(string) string { return body }
}

var sep10 = time.Date(2026, 9, 10, 5, 0, 0, 0, time.UTC)

func TestPlannerFillsAnEmptySlot(t *testing.T) {
	f := &fakePlanner{
		rules:   twoRules,
		plan:    `{"items":[]}`,
		recipes: always(`{"items":[{"id":"u1","slug":"borshch","name":"Борщ"}]}`),
	}
	added, err := f.planner(t, PlannerConfig{}).Run(context.Background(), sep10)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d", added)
	}
	// Tomorrow, not today: today is already being eaten, and a plan written
	// for a meal already cooked is a correction, not a plan.
	if len(f.created) != 1 || f.created[0] != "2026-09-11/lunch/u1" {
		t.Fatalf("created = %v", f.created)
	}
}

// The freshness window is computed per pass, because Mealie's filter language
// rejects a relative date and a literal one stored in a rule would rot.
func TestPlannerAsksForRestedDishesFirst(t *testing.T) {
	f := &fakePlanner{
		rules:   twoRules,
		plan:    `{"items":[]}`,
		recipes: always(`{"items":[{"id":"u1","slug":"borshch","name":"Борщ"}]}`),
	}
	if _, err := f.planner(t, PlannerConfig{RestDays: 14}).Run(context.Background(), sep10); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := `tags.slug IN ["obid"] AND (lastMade IS NONE OR lastMade < "2026-08-28")`
	if len(f.filters) != 1 || f.filters[0] != want {
		t.Fatalf("filters = %q,\nwant one: %q", f.filters, want)
	}
}

// Nothing rested enough is not a reason to leave the evening empty: a repeat
// is a worse plan, no plan is a silent digest.
func TestPlannerFallsBackWhenEverythingIsTooRecent(t *testing.T) {
	f := &fakePlanner{
		rules: twoRules,
		plan:  `{"items":[]}`,
		recipes: func(filter string) string {
			if strings.Contains(filter, "lastMade") {
				return `{"items":[]}`
			}
			return `{"items":[{"id":"u9","slug":"plov","name":"Плов"}]}`
		},
	}
	added, err := f.planner(t, PlannerConfig{}).Run(context.Background(), sep10)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want the fallback pick", added)
	}
	if len(f.filters) != 2 || strings.Contains(f.filters[1], "lastMade") {
		t.Fatalf("filters = %q, want the narrowed one then the bare rule", f.filters)
	}
}

func TestPlannerLeavesFilledSlotsAlone(t *testing.T) {
	f := &fakePlanner{
		rules:   twoRules,
		plan:    `{"items":[{"date":"2026-09-11","entryType":"lunch","recipe":{"id":"u1","slug":"borshch","name":"Борщ"}}]}`,
		recipes: always(`{"items":[{"id":"u2","slug":"plov","name":"Плов"}]}`),
	}
	added, err := f.planner(t, PlannerConfig{}).Run(context.Background(), sep10)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if added != 0 || len(f.created) != 0 {
		t.Fatalf("a planned slot must not be touched: %v", f.created)
	}
}

// A dish already planned later in the window must not also fill an earlier
// day of it.
func TestPlannerDoesNotRepeatWithinTheWindow(t *testing.T) {
	f := &fakePlanner{
		rules:   twoRules,
		plan:    `{"items":[{"date":"2026-09-12","entryType":"lunch","recipe":{"id":"u1","slug":"borshch","name":"Борщ"}}]}`,
		recipes: always(`{"items":[{"id":"u1","slug":"borshch","name":"Борщ"}]}`),
	}
	added, err := f.planner(t, PlannerConfig{Horizon: 3}).Run(context.Background(), sep10)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if added != 0 {
		t.Fatalf("борщ is already on the 12th, it must not fill the 11th too: %v", f.created)
	}
}

// Two slots on one day are two different rules and two different dishes.
func TestPlannerFillsEverySlot(t *testing.T) {
	f := &fakePlanner{
		rules: twoRules,
		plan:  `{"items":[]}`,
		recipes: func(filter string) string {
			if strings.Contains(filter, "vecheria") {
				return `{"items":[{"id":"u2","slug":"plov","name":"Плов"}]}`
			}
			return `{"items":[{"id":"u1","slug":"borshch","name":"Борщ"}]}`
		},
	}
	added, err := f.planner(t, PlannerConfig{Slots: []string{"lunch", "dinner"}}).Run(context.Background(), sep10)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if added != 2 {
		t.Fatalf("added = %d (%v)", added, f.created)
	}
	if f.created[0] != "2026-09-11/lunch/u1" || f.created[1] != "2026-09-11/dinner/u2" {
		t.Fatalf("created = %v", f.created)
	}
}

// A slot with no rule is not this planner's business — somebody deliberately
// did not describe it.
func TestPlannerSkipsSlotsWithNoRule(t *testing.T) {
	f := &fakePlanner{
		rules:   `{"items":[{"day":"unset","entryType":"lunch","queryFilterString":"tags.slug IN [\"obid\"]"}]}`,
		plan:    `{"items":[]}`,
		recipes: always(`{"items":[{"id":"u1","slug":"borshch","name":"Борщ"}]}`),
	}
	added, err := f.planner(t, PlannerConfig{Slots: []string{"breakfast"}}).Run(context.Background(), sep10)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if added != 0 || len(f.filters) != 0 {
		t.Fatalf("no rule means no query and no entry: %v %v", f.created, f.filters)
	}
}

func TestMatchRulePrefersTheWeekday(t *testing.T) {
	rules := []mealie.Rule{
		{Day: "unset", EntryType: "dinner", QueryFilterString: "any"},
		{Day: "friday", EntryType: "dinner", QueryFilterString: "pizza"},
	}
	friday := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

	if got, ok := matchRule(rules, friday, "dinner"); !ok || got.QueryFilterString != "pizza" {
		t.Fatalf("got %+v, want the Friday rule", got)
	}
	if got, ok := matchRule(rules, friday.AddDate(0, 0, -1), "dinner"); !ok || got.QueryFilterString != "any" {
		t.Fatalf("got %+v, want the general rule", got)
	}
	if _, ok := matchRule(rules, friday, "lunch"); ok {
		t.Fatal("no lunch rule exists; want no match")
	}
}

// An empty filter is a rule that selects everything; treat it as no rule
// rather than planning a random dish from the whole database.
func TestMatchRuleIgnoresAnEmptyFilter(t *testing.T) {
	rules := []mealie.Rule{{Day: "unset", EntryType: "lunch", QueryFilterString: ""}}
	if _, ok := matchRule(rules, sep10, "lunch"); ok {
		t.Fatal("want no match")
	}
}

// TestPlannerLive runs one real pass against the household's own Mealie. It
// writes: gated on PLANNER_LIVE=1 as well as the credentials, so neither
// `go test ./...` nor a stray environment can fill somebody's week.
//
//	PLANNER_LIVE=1 MEALIE_URL=… MEALIE_TOKEN=… go test ./internal/cooking -run Live -v
func TestPlannerLive(t *testing.T) {
	url, tok := os.Getenv("MEALIE_URL"), os.Getenv("MEALIE_TOKEN")
	if os.Getenv("PLANNER_LIVE") != "1" || url == "" || tok == "" {
		t.Skip("PLANNER_LIVE not set")
	}
	p := NewPlanner(mealie.New(url, tok), PlannerConfig{
		Slots:    []string{"lunch", "dinner"},
		Horizon:  7,
		RestDays: 14,
		Loc:      time.Local,
		Logger:   slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	added, err := p.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("filled %d slots", added)
}
