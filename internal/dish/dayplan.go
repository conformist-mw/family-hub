package dish

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"familyhub/internal/model"
)

// PlanInput is what the model is told when asked for a day's menu: which
// dishes each open meal may take, what the family ate lately, and what is
// left over from the day before. The pools are already filtered by the
// caller — meal and weekend rules are the menu's, not the model's — so the
// model only chooses, and an id outside a pool is dropped on the way back.
type PlanInput struct {
	Date time.Time
	// Lunch and Dinner are the dishes that meal may be planned with; a nil
	// pool is a meal not to plan — already eaten, or nothing fits it.
	Lunch, Dinner []PlanDish
	// History is the recent days, oldest first.
	History []PlanDay
	// Leftovers is the pot from the day before Date, by name.
	Leftovers []string
	// Avoid is the variants already on screen, when more are asked for.
	Avoid []Variant
}

// PlanDish is a dish of a pool: the id the model answers with, its name, and
// whether the family has yet to try it.
type PlanDish struct {
	ID       int64
	Name     string
	Proposed bool
}

// PlanDay is one day of history as the model reads it.
type PlanDay struct {
	Date          string // YYYY-MM-DD
	Lunch, Dinner []string
}

// Variant is one whole day's menu: the dishes of each meal, and a line about
// why this pairing.
type Variant struct {
	Lunch, Dinner []int64
	Note          string
}

const (
	// maxVariants is how many menus one message offers: a choice, not a
	// catalogue. planSystemPrompt says «три», so the two change together.
	maxVariants = 3
	// maxPerMeal is how many dishes a variant puts on one meal: a soup and a
	// salad, a main and a side — rarely more, and the button's callback data
	// has 64 bytes for both meals.
	maxPerMeal = 2
)

const planSystemPrompt = `Ти допомагаєш родині вирішити, що приготувати на конкретний день. Родина їсть удома двічі: обід і вечеря, домашня кухня.

Тобі дають:
- день, на який треба меню;
- що родина їла останні тижні (історія);
- що лишилося з попереднього дня;
- для кожного прийому їжі — список страв, з яких можна обирати, у форматі «id | назва».

Запропонуй три різні варіанти меню на цей день.

Правила:
- Обирай ЛИШЕ зі списку страв для відповідного прийому їжі й повертай їхні id. Не вигадуй страв.
- Для кожного прийому їжі зі списком — одна або дві страви (наприклад, суп і салат). Якщо для прийому їжі списку немає, повертай для нього порожній список.
- Зважай на історію: перевага стравам, яких давно не було; не повторюй те, що їли вчора чи позавчора (крім залишків); не став поруч дві схожі страви (два супи, двічі курка за день).
- Залишки з попереднього дня можна запропонувати доїсти в одному з варіантів, але не в усіх.
- Страви з позначкою «нова» родина ще не пробувала — можна запропонувати одну в одному варіанті, не більше.
- Три варіанти мають помітно відрізнятися між собою.
- Якщо дано варіанти, які вже показали, не повторюй їх.
- note — один короткий рядок українською: чому саме цей варіант (наприклад, «борщу не було два тижні, а на вечерю — легше»).`

var planSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"variants": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"lunch":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
					"dinner": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
					"note":   map[string]any{"type": "string"},
				},
				"required":             []string{"lunch", "dinner", "note"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"variants"},
	"additionalProperties": false,
}

// PlanDay asks the model for a few whole-day menus. What comes back is
// checked against the pools: an id the model invented or put on the wrong
// meal is dropped, a variant left without a dish for an open meal is dropped,
// and so is one that repeats another or one already avoided.
func (r *Recognizer) PlanDay(ctx context.Context, in PlanInput) ([]Variant, error) {
	body := map[string]any{
		"model": r.model,
		"messages": []map[string]any{
			{"role": "system", "content": planSystemPrompt},
			{"role": "user", "content": planUserPrompt(in)},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "day_menu", "strict": true, "schema": planSchema,
			},
		},
	}
	raw, err := r.post(ctx, body)
	if err != nil {
		return nil, err
	}
	return parseVariants(raw, in)
}

func planUserPrompt(in PlanInput) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "День: %s, %s\n\n", in.Date.Format(time.DateOnly), model.WeekdayFull[int(in.Date.Weekday())])

	sb.WriteString("Що їли останнім часом:\n")
	if len(in.History) == 0 {
		sb.WriteString("(нічого не записано)\n")
	}
	for _, d := range in.History {
		fmt.Fprintf(&sb, "- %s: обід — %s; вечеря — %s\n", d.Date, namesOrDash(d.Lunch), namesOrDash(d.Dinner))
	}

	sb.WriteString("\nЛишилося з попереднього дня: ")
	if len(in.Leftovers) == 0 {
		sb.WriteString("нічого\n")
	} else {
		sb.WriteString(strings.Join(in.Leftovers, ", ") + "\n")
	}

	pool := func(title string, dishes []PlanDish) {
		fmt.Fprintf(&sb, "\n%s:\n", title)
		if dishes == nil {
			sb.WriteString("(не планувати)\n")
			return
		}
		for _, d := range dishes {
			mark := ""
			if d.Proposed {
				mark = " (нова)"
			}
			fmt.Fprintf(&sb, "%d | %s%s\n", d.ID, d.Name, mark)
		}
	}
	pool("Страви на обід", in.Lunch)
	pool("Страви на вечерю", in.Dinner)

	if len(in.Avoid) > 0 {
		sb.WriteString("\nВже показані варіанти — не повторювати:\n")
		for _, v := range in.Avoid {
			fmt.Fprintf(&sb, "- обід %s; вечеря %s\n", idList(v.Lunch), idList(v.Dinner))
		}
	}
	return sb.String()
}

func namesOrDash(names []string) string {
	if len(names) == 0 {
		return "—"
	}
	return strings.Join(names, ", ")
}

func idList(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

type rawVariants struct {
	Variants []struct {
		Lunch  []int64 `json:"lunch"`
		Dinner []int64 `json:"dinner"`
		Note   string  `json:"note"`
	} `json:"variants"`
}

func parseVariants(raw []byte, in PlanInput) ([]Variant, error) {
	var rv rawVariants
	if err := json.Unmarshal(raw, &rv); err != nil {
		return nil, fmt.Errorf("model answer is not the expected json: %.120s", raw)
	}
	seen := map[string]bool{}
	for _, v := range in.Avoid {
		seen[VariantKey(v)] = true
	}
	var out []Variant
	for _, r := range rv.Variants {
		v := Variant{
			Lunch:  keepFrom(r.Lunch, in.Lunch),
			Dinner: keepFrom(r.Dinner, in.Dinner),
			Note:   strings.TrimSpace(r.Note),
		}
		if len(v.Lunch)+len(v.Dinner) == 0 ||
			(in.Lunch != nil && len(v.Lunch) == 0) || (in.Dinner != nil && len(v.Dinner) == 0) {
			continue
		}
		key := VariantKey(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
		if len(out) == maxVariants {
			break
		}
	}
	return out, nil
}

// keepFrom is ids that are in the pool, each once, at most maxPerMeal.
func keepFrom(ids []int64, pool []PlanDish) []int64 {
	var out []int64
	for _, id := range ids {
		if slices.Contains(out, id) || !slices.ContainsFunc(pool, func(d PlanDish) bool { return d.ID == id }) {
			continue
		}
		out = append(out, id)
		if len(out) == maxPerMeal {
			break
		}
	}
	return out
}

// VariantKey identifies a variant by its dishes, whatever their order: two
// menus of the same dishes are the same menu.
func VariantKey(v Variant) string {
	l, d := slices.Clone(v.Lunch), slices.Clone(v.Dinner)
	slices.Sort(l)
	slices.Sort(d)
	return idList(l) + idList(d)
}
