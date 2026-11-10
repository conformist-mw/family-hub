package dish

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func planPools() PlanInput {
	return PlanInput{
		Date:   time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
		Lunch:  []PlanDish{{ID: 1, Name: "Борщ"}, {ID: 2, Name: "Солянка", Proposed: true}, {ID: 3, Name: "Салат"}},
		Dinner: []PlanDish{{ID: 4, Name: "Плов"}, {ID: 5, Name: "Деруни"}, {ID: 3, Name: "Салат"}},
	}
}

func TestParseVariantsKeepsOnlyWhatThePoolsAllow(t *testing.T) {
	raw := `{"variants":[
		{"lunch":[1,3,2],"dinner":[4],"note":" борщу давно не було "},
		{"lunch":[4],"dinner":[5],"note":"плов не обідній"},
		{"lunch":[3,1],"dinner":[4],"note":"той самий, що перший"},
		{"lunch":[99],"dinner":[5],"note":"вигадана страва"},
		{"lunch":[2],"dinner":[5,5],"note":"нова"},
		{"lunch":[3],"dinner":[3],"note":"салат двічі"},
		{"lunch":[1],"dinner":[5],"note":"зайвий четвертий"}
	]}`
	got, err := parseVariants([]byte(raw), planPools())
	if err != nil {
		t.Fatal(err)
	}
	want := []Variant{
		{Lunch: []int64{1, 3}, Dinner: []int64{4}, Note: "борщу давно не було"},
		{Lunch: []int64{2}, Dinner: []int64{5}, Note: "нова"},
		{Lunch: []int64{3}, Dinner: []int64{3}, Note: "салат двічі"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("variants = %+v\nwant       %+v", got, want)
	}
}

// A meal that is not planned (nil pool) takes nothing, and a variant is
// still kept for the other one; a variant already on screen is not offered
// again.
func TestParseVariantsClosedMealAndAvoid(t *testing.T) {
	in := planPools()
	in.Lunch = nil
	in.Avoid = []Variant{{Dinner: []int64{4}}}
	raw := `{"variants":[
		{"lunch":[1],"dinner":[4],"note":"вже показаний"},
		{"lunch":[1],"dinner":[5],"note":"обід уже з'їли"},
		{"lunch":[],"dinner":[],"note":"порожній"}
	]}`
	got, err := parseVariants([]byte(raw), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Lunch) != 0 || fmt.Sprint(got[0].Dinner) != "[5]" {
		t.Fatalf("variants = %+v", got)
	}
}

func TestParseVariantsRejectsGarbage(t *testing.T) {
	if _, err := parseVariants([]byte("not json"), planPools()); err == nil {
		t.Fatal("want an error")
	}
}

func TestPlanUserPrompt(t *testing.T) {
	in := planPools()
	in.Dinner = nil
	in.History = []PlanDay{{Date: "2026-11-09", Lunch: []string{"Борщ"}}, {Date: "2026-11-10", Dinner: []string{"Плов", "Салат"}}}
	in.Leftovers = []string{"Плов", "Салат"}
	in.Avoid = []Variant{{Lunch: []int64{3, 1}}}
	got := planUserPrompt(in)
	for _, want := range []string{
		"День: 2026-11-11, Середа",
		"- 2026-11-09: обід — Борщ; вечеря — —",
		"- 2026-11-10: обід — —; вечеря — Плов, Салат",
		"Лишилося з попереднього дня: Плов, Салат",
		"1 | Борщ\n2 | Солянка (нова)\n3 | Салат",
		"Страви на вечерю:\n(не планувати)",
		"- обід [3, 1]; вечеря []",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}

func TestPlanPromptAsksForMaxVariants(t *testing.T) {
	if maxVariants != 3 || !strings.Contains(planSystemPrompt, "три різні варіанти") {
		t.Fatal("planSystemPrompt and maxVariants disagree on how many variants")
	}
}

func TestVariantKeyIgnoresOrder(t *testing.T) {
	a := VariantKey(Variant{Lunch: []int64{3, 1}, Dinner: []int64{4}})
	b := VariantKey(Variant{Lunch: []int64{1, 3}, Dinner: []int64{4}})
	c := VariantKey(Variant{Lunch: []int64{4}, Dinner: []int64{1, 3}})
	if a != b || a == c {
		t.Fatalf("keys %q %q %q", a, b, c)
	}
}
