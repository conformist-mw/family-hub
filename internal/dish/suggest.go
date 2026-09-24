package dish

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SuggestInput is what the model is told about the catalogue when it is asked
// for new dishes: every name it must not come back with. Rejected is the one
// that matters most — without it a dish turned down last week is suggested
// again this week — and Proposed keeps it from repeating what is still waiting
// for an answer.
type SuggestInput struct {
	Active   []string
	Proposed []string
	Rejected []string
}

// Suggestion is one new dish to try, as the model pitched it.
type Suggestion struct {
	Name string // Ukrainian, the dish's own short name
	Meal string // lunch | dinner | any
	Days string // any | weekend
	Note string // one line: what it is and why it would suit
}

// maxSuggestions is how many dishes one weekly message offers: enough to find
// one worth trying, few enough that the card is answered rather than skimmed.
const maxSuggestions = 3

// suggestSystemPrompt carries the household's standing tastes. They change
// rarely enough to live here rather than in configuration, and the catalogue
// in the user prompt already says the rest — what the family actually cooks.
const suggestSystemPrompt = `Ти допомагаєш родині урізноманітнити домашнє меню. Раз на тиждень ти пропонуєш три нові страви, яких родина ще не готує.

Про родину:
- Їдять двічі на день удома: обід і вечеря.
- Домашня українська кухня — те, що реально приготувати вдома у звичайний день з продуктів зі звичайного супермаркету.
- М'ясо — свинина і курка. Баранину не їдять.
- Старша дитина не їсть рибу; решта родини рибу їсть.

Правила:
- Рівно три страви, і всі три — різні за основою (не три супи і не три страви з курки).
- Не пропонуй страву, яка вже є в будь-якому зі списків нижче, і не пропонуй її варіант під іншою назвою. Список «Відхилені» — це страви, від яких родина відмовилась; не пропонуй їх і схожих на них.
- Страва — це те, що ставлять на стіл. Якщо її подають з гарніром, назви це однією стравою («Курячі стегна з печеною картоплею»), а не окремо головне і гарнір.
- name — коротка власна назва страви УКРАЇНСЬКОЮ.
- meal — "lunch", якщо це обід (суп, щось ситне), "dinner" — вечеря, "any" — годиться для обох.
- days — "weekend", якщо страва довга чи клопітна і годиться лише на вихідні; інакше "any".
- note — один рядок українською: що це за страва і чому вона може сподобатись саме цій родині.`

var suggestSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"dishes": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"meal": map[string]any{"type": "string", "enum": []string{"lunch", "dinner", "any"}},
					"days": map[string]any{"type": "string", "enum": []string{"any", "weekend"}},
					"note": map[string]any{"type": "string"},
				},
				"required":             []string{"name", "meal", "days", "note"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"dishes"},
	"additionalProperties": false,
}

// Suggest asks the model for new dishes to try. What comes back is only
// cleaned up, not checked against the catalogue: the model is told what not to
// repeat, but whether a name is really new is the caller's to decide, by the
// same key the store dedupes on.
func (r *Recognizer) Suggest(ctx context.Context, in SuggestInput) ([]Suggestion, error) {
	body := map[string]any{
		"model": r.model,
		"messages": []map[string]any{
			{"role": "system", "content": suggestSystemPrompt},
			{"role": "user", "content": suggestUserPrompt(in)},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "suggestions", "strict": true, "schema": suggestSchema,
			},
		},
	}
	raw, err := r.post(ctx, body)
	if err != nil {
		return nil, err
	}
	return parseSuggestions(raw)
}

func suggestUserPrompt(in SuggestInput) string {
	var sb strings.Builder
	list := func(title string, names []string) {
		fmt.Fprintf(&sb, "%s:\n", title)
		if len(names) == 0 {
			sb.WriteString("(немає)\n")
		}
		for _, n := range names {
			fmt.Fprintf(&sb, "- %s\n", n)
		}
		sb.WriteString("\n")
	}
	list("Страви, які родина вже готує", in.Active)
	list("Уже запропоновані, родина ще думає", in.Proposed)
	list("Відхилені — не пропонувати", in.Rejected)
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

type rawSuggestions struct {
	Dishes []struct {
		Name string `json:"name"`
		Meal string `json:"meal"`
		Days string `json:"days"`
		Note string `json:"note"`
	} `json:"dishes"`
}

// parseSuggestions keeps at most maxSuggestions named dishes, each name once.
// An empty list is not an error: the model found nothing new, and the caller
// already has to handle every suggestion turning out to be a known dish.
func parseSuggestions(raw []byte) ([]Suggestion, error) {
	var rs rawSuggestions
	if err := json.Unmarshal(raw, &rs); err != nil {
		return nil, fmt.Errorf("model answer is not the expected json: %.120s", raw)
	}
	var out []Suggestion
	seen := map[string]bool{}
	for _, d := range rs.Dishes {
		name := strings.Join(strings.Fields(d.Name), " ")
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Suggestion{
			Name: name,
			Meal: oneOf(d.Meal, "any", "lunch", "dinner"),
			Days: oneOf(d.Days, "any", "weekend"),
			Note: strings.TrimSpace(d.Note),
		})
		if len(out) == maxSuggestions {
			break
		}
	}
	return out, nil
}
