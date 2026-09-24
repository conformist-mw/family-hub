package menu

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"familyhub/internal/model"
)

// 2026-09-24 is a Thursday; the rest of the week follows from it.
var (
	thursday = time.Date(2026, 9, 24, 7, 0, 0, 0, time.Local)
	friday   = thursday.AddDate(0, 0, 1)
	saturday = thursday.AddDate(0, 0, 2)
	sunday   = thursday.AddDate(0, 0, 3)
	monday   = thursday.AddDate(0, 0, 4)
)

func seeded(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed+1)) }

func dish(id int64, meal, days, status string) Candidate {
	return Candidate{Dish: model.Dish{ID: id, Name: "d", Meal: meal, Days: days, Status: status}}
}

func active(id int64) Candidate {
	return dish(id, model.DishMealAny, model.DishDaysAny, model.DishActive)
}

func ids(ds []model.Dish) []int64 {
	out := make([]int64, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

func set(xs ...int64) map[int64]bool {
	m := map[int64]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func TestMealTitle(t *testing.T) {
	cases := map[Meal]string{Lunch: "Обід", Dinner: "Вечеря"}
	for m, want := range cases {
		if got := m.Title(); got != want {
			t.Errorf("Meal(%q).Title() = %q, want %q", m, got, want)
		}
	}
}

func TestIsWeekend(t *testing.T) {
	cases := []struct {
		name string
		date time.Time
		meal Meal
		want bool
	}{
		{"thursday dinner", thursday, Dinner, false},
		{"friday lunch", friday, Lunch, false},
		{"friday dinner", friday, Dinner, true},
		{"saturday lunch", saturday, Lunch, true},
		{"sunday dinner", sunday, Dinner, true},
		{"monday lunch", monday, Lunch, false},
	}
	for _, tc := range cases {
		if got := IsWeekend(tc.date, tc.meal); got != tc.want {
			t.Errorf("%s: IsWeekend = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPickPool checks which dishes are eligible at all: n is larger than the
// catalogue so everything eligible comes back and the draw does not matter.
func TestPickPool(t *testing.T) {
	cands := []Candidate{
		dish(1, model.MealLunch, model.DishDaysAny, model.DishActive),
		dish(2, model.MealDinner, model.DishDaysAny, model.DishActive),
		dish(3, model.DishMealAny, model.DishDaysAny, model.DishActive),
		dish(4, model.DishMealAny, model.DishDaysWeekend, model.DishActive),
		dish(5, model.MealDinner, model.DishDaysWeekend, model.DishActive),
		dish(6, model.DishMealAny, model.DishDaysAny, model.DishRejected),
	}
	cases := []struct {
		name string
		date time.Time
		meal Meal
		want []int64
	}{
		{"weekday lunch skips weekend dishes", thursday, Lunch, []int64{1, 3}},
		{"weekday dinner skips weekend dishes", thursday, Dinner, []int64{2, 3}},
		{"friday lunch is still a weekday", friday, Lunch, []int64{1, 3}},
		{"friday dinner lets weekend dishes in", friday, Dinner, []int64{2, 3, 4, 5}},
		{"saturday lunch", saturday, Lunch, []int64{1, 3, 4}},
		{"sunday dinner", sunday, Dinner, []int64{2, 3, 4, 5}},
	}
	for _, tc := range cases {
		for seed := range uint64(20) {
			got := ids(Pick(cands, tc.date, tc.meal, nil, nil, 10, false, seeded(seed)))
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("%s (seed %d): got %v, want %v", tc.name, seed, got, tc.want)
			}
		}
	}
}

// TestPickPrefersNeverSeen: with n=1 the window is the four oldest, so the
// four never-seen dishes are the only possible answers whatever the seed.
func TestPickPrefersNeverSeen(t *testing.T) {
	var cands []Candidate
	for id := int64(1); id <= 10; id++ {
		c := active(id)
		if id > 4 {
			c.LastSeen = thursday.AddDate(0, 0, -int(id))
		}
		cands = append(cands, c)
	}
	hit := map[int64]bool{}
	for seed := range uint64(50) {
		got := ids(Pick(cands, thursday, Lunch, nil, nil, 1, false, seeded(seed)))
		if len(got) != 1 || got[0] > 4 {
			t.Fatalf("seed %d: got %v, want one of the never-seen 1..4", seed, got)
		}
		hit[got[0]] = true
	}
	if len(hit) < 2 {
		t.Errorf("never-seen dishes all tie; expected the draw to vary, got only %v", hit)
	}
}

// TestPickPrefersOldest: among seen dishes the window holds the oldest ones.
func TestPickPrefersOldest(t *testing.T) {
	var cands []Candidate
	for id := int64(1); id <= 10; id++ {
		c := active(id)
		// Dish 10 was seen longest ago, dish 1 yesterday.
		c.LastSeen = thursday.AddDate(0, 0, -int(id))
		cands = append(cands, c)
	}
	for seed := range uint64(50) {
		for _, id := range ids(Pick(cands, thursday, Lunch, nil, nil, 1, false, seeded(seed))) {
			if id < 7 {
				t.Fatalf("seed %d: picked %d, want one of the four oldest 7..10", seed, id)
			}
		}
	}
}

func TestPickExcludeAndCircle(t *testing.T) {
	cands := []Candidate{active(1), active(2), active(3), active(4)}
	cases := []struct {
		name             string
		today, yesterday map[int64]bool
		want             []int64
	}{
		{"shown today and yesterday are kept out", set(1), set(2, 3), []int64{4}},
		{"yesterday comes back first", set(1, 2), set(3, 4), []int64{3, 4}},
		{"today comes back last", set(1, 2, 3, 4), set(), []int64{1, 2, 3, 4}},
		{"both exhausted, full circle", set(1, 2), set(3, 4, 1, 2), []int64{3, 4}},
	}
	for _, tc := range cases {
		got := ids(Pick(cands, thursday, Lunch, tc.today, tc.yesterday, 10, false, seeded(1)))
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPickAtMostOneNew(t *testing.T) {
	var cands []Candidate
	for id := int64(1); id <= 8; id++ {
		cands = append(cands, active(id))
	}
	for id := int64(100); id < 105; id++ {
		cands = append(cands, dish(id, model.DishMealAny, model.DishDaysAny, model.DishProposed))
	}
	for seed := range uint64(100) {
		for _, withNew := range []bool{false, true} {
			got := Pick(cands, thursday, Lunch, nil, nil, 3, withNew, seeded(seed))
			if len(got) != 3 {
				t.Fatalf("seed %d: got %d dishes, want 3", seed, len(got))
			}
			n := 0
			for i, d := range got {
				if d.Status == model.DishProposed {
					n++
					if i != len(got)-1 {
						t.Errorf("seed %d: proposed dish at %d, want it in the last slot", seed, i)
					}
				}
			}
			if want := map[bool]int{false: 0, true: 1}[withNew]; n != want {
				t.Fatalf("seed %d, withNew %v: %d proposed dishes, want %d", seed, withNew, n, want)
			}
		}
	}
}

func TestRollNewIsAboutOneMorningInThree(t *testing.T) {
	const mornings = 300
	hits := 0
	for seed := range uint64(mornings) {
		if RollNew(seeded(seed)) {
			hits++
		}
	}
	// Loosely: the point is "sometimes, not always".
	if hits < mornings/5 || hits > mornings/2 {
		t.Errorf("🆕 on %d of %d mornings, want roughly a third", hits, mornings)
	}
}

func TestPickNewRespectsPoolRules(t *testing.T) {
	cands := []Candidate{
		active(1),
		dish(100, model.DishMealAny, model.DishDaysWeekend, model.DishProposed),
		dish(101, model.MealDinner, model.DishDaysAny, model.DishProposed),
		dish(102, model.DishMealAny, model.DishDaysAny, model.DishProposed),
	}
	for seed := range uint64(100) {
		for _, d := range Pick(cands, thursday, Lunch, set(102), nil, 3, true, seeded(seed)) {
			if d.Status == model.DishProposed {
				t.Fatalf("seed %d: offered proposed %d, but weekend-only, dinner-only and already shown should all be out", seed, d.ID)
			}
		}
	}
}

// TestPickNewTopsUpShortMenu: with fewer regular dishes than n the proposed
// one is added rather than replacing the only real choice.
func TestPickNewTopsUpShortMenu(t *testing.T) {
	cands := []Candidate{
		active(1),
		dish(100, model.DishMealAny, model.DishDaysAny, model.DishProposed),
	}
	for seed := range uint64(50) {
		got := ids(Pick(cands, thursday, Lunch, nil, nil, 3, true, seeded(seed)))
		if len(got) != 2 || got[0] != 1 || got[1] != 100 {
			t.Fatalf("seed %d: got %v, want the only active dish and the proposal after it", seed, got)
		}
	}
}

// TestPickRotatesOverDays simulates the first mornings after the import: no
// history at all, only "not what was shown yesterday". Consecutive menus must
// not overlap and, between them, reach the whole pool rather than cycling
// through the first few ids.
func TestPickRotatesOverDays(t *testing.T) {
	var cands []Candidate
	for id := int64(1); id <= 12; id++ {
		cands = append(cands, active(id))
	}
	rnd := seeded(7)
	covered := map[int64]bool{}
	var yesterday map[int64]bool
	var prev []int64
	for day := range 10 {
		date := monday.AddDate(0, 0, day)
		got := ids(Pick(cands, date, Lunch, nil, yesterday, 3, false, rnd))
		if len(got) != 3 {
			t.Fatalf("day %d: got %v, want 3 dishes", day, got)
		}
		for _, id := range got {
			if yesterday[id] {
				t.Errorf("day %d: %d was shown yesterday", day, id)
			}
			covered[id] = true
		}
		sorted := slices.Sorted(slices.Values(got))
		if slices.Equal(sorted, prev) {
			t.Errorf("day %d: same menu as the day before: %v", day, got)
		}
		prev = sorted
		yesterday = set(got...)
	}
	if len(covered) != len(cands) {
		t.Errorf("10 mornings covered %d of %d dishes", len(covered), len(cands))
	}
}

func TestPickEdges(t *testing.T) {
	if got := Pick(nil, thursday, Lunch, nil, nil, 3, false, seeded(1)); len(got) != 0 {
		t.Errorf("empty catalogue: got %v, want nothing", ids(got))
	}
	onlyDinner := []Candidate{dish(1, model.MealDinner, model.DishDaysAny, model.DishActive)}
	if got := Pick(onlyDinner, thursday, Lunch, nil, nil, 3, false, seeded(1)); len(got) != 0 {
		t.Errorf("nothing for lunch: got %v, want nothing", ids(got))
	}
	two := []Candidate{active(1), active(2)}
	got := ids(Pick(two, thursday, Lunch, nil, nil, 5, false, seeded(1)))
	slices.Sort(got)
	if !slices.Equal(got, []int64{1, 2}) {
		t.Errorf("n larger than the pool: got %v, want the whole pool", got)
	}
	if got := Pick(two, thursday, Lunch, nil, nil, 0, false, seeded(1)); got != nil {
		t.Errorf("n=0: got %v, want nil", ids(got))
	}
}

func TestPickDoesNotReorderInput(t *testing.T) {
	cands := []Candidate{active(1), active(2), active(3), active(4), active(5)}
	before := slices.Clone(cands)
	Pick(cands, thursday, Lunch, nil, nil, 3, false, seeded(3))
	if !slices.Equal(cands, before) {
		t.Error("Pick reordered the caller's candidates")
	}
}

func TestLeftovers(t *testing.T) {
	eaten := []model.MealEntry{
		{DishID: 7, Meal: model.MealLunch},
		{DishID: 3, Meal: model.MealLunch},
		{DishID: 7, Meal: model.MealDinner},
	}
	if got := Leftovers(eaten); !slices.Equal(got, []int64{7, 3}) {
		t.Errorf("Leftovers = %v, want [7 3]", got)
	}
	if got := Leftovers(nil); len(got) != 0 {
		t.Errorf("Leftovers(nil) = %v, want empty", got)
	}
}
