package bot

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/dish"
	"familyhub/internal/model"
)

func sampleSuggestions() []suggestDish {
	return []suggestDish{
		{id: 11, name: "Солянка", note: "Густий суп з копченостями.", meal: "lunch", days: "any", status: model.DishProposed},
		{id: 12, name: "Курячі стегна з печеною картоплею", meal: "dinner", days: "any", status: model.DishProposed},
		{id: 13, name: "Голубці", note: "Довго, але на два дні.", meal: "any", days: "weekend", status: model.DishProposed},
	}
}

func TestSuggestViewLayout(t *testing.T) {
	text, markup := suggestView(sampleSuggestions())

	for _, want := range []string{
		"Може, спробуємо нове?",
		"<b>Солянка</b> · на обід",
		"Густий суп з копченостями.",
		"<b>Курячі стегна з печеною картоплею</b> · на вечерю",
		"<b>Голубці</b> · на обід чи вечерю, на вихідні",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	want := [][]string{
		{"➕ Солянка", "🤔 Подумаю", "✖ Ні"},
		{"➕ Курячі стегна…", "🤔 Подумаю", "✖ Ні"},
		{"➕ Голубці", "🤔 Подумаю", "✖ Ні"},
	}
	if got := buttonTexts(markup); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keyboard = %v\nwant       %v", got, want)
	}
}

func TestSuggestViewShowsTheAnswers(t *testing.T) {
	dishes := sampleSuggestions()
	dishes[0].status = model.DishActive
	dishes[1].maybe = true
	dishes[2].status = model.DishRejected
	text, markup := suggestView(dishes)

	for _, want := range []string{"➕ Додано в меню", "🤔 Подумаємо", "✖ Не пропонуватиму"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	want := [][]string{
		{"✓ ➕ Солянка", "🤔 Подумаю", "✖ Ні"},
		{"➕ Курячі стегна…", "✓ 🤔 Подумаю", "✖ Ні"},
		{"➕ Голубці", "🤔 Подумаю", "✓ ✖ Ні"},
	}
	if got := buttonTexts(markup); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("keyboard = %v\nwant       %v", got, want)
	}
}

// A maybe left over from before the dish was decided means nothing: the
// store's answer is the one on the card.
func TestSuggestViewDecisionOutranksMaybe(t *testing.T) {
	dishes := sampleSuggestions()[:1]
	dishes[0].status = model.DishActive
	dishes[0].maybe = true
	text, markup := suggestView(dishes)
	if strings.Contains(text, "Подумаємо") || strings.Contains(buttonTexts(markup)[0][1], chosenPrefix) {
		t.Fatalf("a decided dish still shows the maybe:\n%s\n%v", text, buttonTexts(markup))
	}
}

func TestSuggestViewEscapesNames(t *testing.T) {
	text, _ := suggestView([]suggestDish{{id: 1, name: "Тост <з> сиром", note: "a & b", status: model.DishProposed}})
	if !strings.Contains(text, "Тост &lt;з&gt; сиром") || !strings.Contains(text, "a &amp; b") {
		t.Fatalf("names must be escaped for HTML mode:\n%s", text)
	}
}

func TestSuggestKeyboardRoundTrips(t *testing.T) {
	dishes := sampleSuggestions()
	dishes[1].maybe = true
	_, markup := suggestView(dishes)
	sent := asTelegramSentIt(markup)
	sent.InlineKeyboard = append(sent.InlineKeyboard,
		[]tele.InlineButton{{Text: "Відкрити застосунок", URL: "https://example.test"}})

	for name, m := range map[string]*tele.ReplyMarkup{"as built": markup, "as sent": sent} {
		refs := suggestRefsFromMarkup(m)
		if fmt.Sprint(refs.ids) != "[11 12 13]" {
			t.Errorf("%s: ids = %v", name, refs.ids)
		}
		if len(refs.maybe) != 1 || !refs.maybe[12] {
			t.Errorf("%s: maybe = %v, want only 12", name, refs.maybe)
		}
	}
}

func TestFilterSuggestions(t *testing.T) {
	existing := []model.Dish{
		{Name: "Борщ", Status: model.DishActive},
		{Name: "Шакшука", Status: model.DishProposed},
		{Name: "Плов з бараниною", Status: model.DishRejected},
	}
	sugg := []dish.Suggestion{
		{Name: " борщ "},
		{Name: "Солянка"},
		{Name: "плов  з бараниною"},
		{Name: "ШАКШУКА"},
		{Name: "солянка"},
		{Name: "Голубці"},
	}
	var names []string
	for _, s := range filterSuggestions(sugg, existing) {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "Солянка,Голубці" {
		t.Fatalf("survivors = %v, want Солянка and Голубці", names)
	}
}

func TestSuggestInputSortsByStatus(t *testing.T) {
	in := suggestInput([]model.Dish{
		{Name: "Борщ", Status: model.DishActive},
		{Name: "Шакшука", Status: model.DishProposed},
		{Name: "Плов з бараниною", Status: model.DishRejected},
		{Name: "Деруни", Status: model.DishActive},
	})
	if fmt.Sprint(in.Active) != "[Борщ Деруни]" || fmt.Sprint(in.Proposed) != "[Шакшука]" ||
		fmt.Sprint(in.Rejected) != "[Плов з бараниною]" {
		t.Fatalf("input = %+v", in)
	}
}

func TestProposeDishesWritesProposed(t *testing.T) {
	b := menuBot(t)
	if _, _, err := b.store.CreateDish(model.Dish{Name: "Борщ"}); err != nil {
		t.Fatal(err)
	}
	existing, err := b.store.Dishes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.proposeDishes([]dish.Suggestion{
		{Name: "Борщ", Meal: "lunch", Days: "any"},
		{Name: "Голубці", Meal: "any", Days: "weekend", Note: "Довго, але на два дні."},
	}, existing)
	if err != nil {
		t.Fatalf("proposeDishes: %v", err)
	}
	if len(got) != 1 || got[0].name != "Голубці" || got[0].status != model.DishProposed ||
		got[0].days != model.DishDaysWeekend || got[0].note != "Довго, але на два дні." {
		t.Fatalf("proposed = %+v", got)
	}
	d, err := b.store.Dish(got[0].id)
	if err != nil || d.Status != model.DishProposed || d.Note != "Довго, але на два дні." {
		t.Fatalf("stored = %+v, %v", d, err)
	}
	borsch, err := b.store.Dishes(model.DishActive)
	if err != nil || len(borsch) != 1 || borsch[0].Name != "Борщ" {
		t.Fatalf("the family's own dish must stay as it was: %+v, %v", borsch, err)
	}
}

// A name that appeared between reading the catalogue and writing — the stale
// list handed in here — is not offered as new.
func TestProposeDishesSkipsANameThatAppearedMeanwhile(t *testing.T) {
	b := menuBot(t)
	if _, _, err := b.store.CreateDish(model.Dish{Name: "Голубці", Status: model.DishRejected}); err != nil {
		t.Fatal(err)
	}
	got, err := b.proposeDishes([]dish.Suggestion{{Name: "Голубці"}}, nil)
	if err != nil {
		t.Fatalf("proposeDishes: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("proposed = %+v, want nothing", got)
	}
	d, _ := b.store.Dishes(model.DishRejected)
	if len(d) != 1 {
		t.Fatalf("the rejected dish must stay rejected: %+v", d)
	}
}

// sentSuggestions writes two proposals and returns their card as Telegram
// hands it back on a tap.
func sentSuggestions(t *testing.T, b *Bot) ([]suggestDish, *tele.ReplyMarkup) {
	t.Helper()
	dishes, err := b.proposeDishes([]dish.Suggestion{
		{Name: "Солянка", Meal: "lunch", Days: "any"},
		{Name: "Голубці", Meal: "any", Days: "weekend"},
	}, nil)
	if err != nil || len(dishes) != 2 {
		t.Fatalf("proposeDishes: %+v, %v", dishes, err)
	}
	_, markup := suggestView(dishes)
	return dishes, asTelegramSentIt(markup)
}

func dishStatus(t *testing.T, b *Bot, id int64) string {
	t.Helper()
	d, err := b.store.Dish(id)
	if err != nil {
		t.Fatalf("dish %d: %v", id, err)
	}
	return d.Status
}

func TestSuggestTapChangesStatuses(t *testing.T) {
	b := menuBot(t)
	dishes, current := sentSuggestions(t, b)
	soup, rolls := dishes[0].id, dishes[1].id

	got, toast, redraw, err := b.applySuggestTap(suggAddUnique, strconv.FormatInt(soup, 10), current)
	if err != nil || !redraw {
		t.Fatalf("add: redraw=%v err=%v", redraw, err)
	}
	if dishStatus(t, b, soup) != model.DishActive || !strings.Contains(toast, "Солянка") {
		t.Fatalf("add: status %q, toast %q", dishStatus(t, b, soup), toast)
	}
	if len(got) != 2 || got[0].status != model.DishActive || got[1].status != model.DishProposed {
		t.Fatalf("add: the card must keep both dishes, the other untouched: %+v", got)
	}

	_, current = suggestView(got)
	got, _, _, err = b.applySuggestTap(suggNoUnique, strconv.FormatInt(rolls, 10), asTelegramSentIt(current))
	if err != nil {
		t.Fatalf("no: %v", err)
	}
	if dishStatus(t, b, rolls) != model.DishRejected {
		t.Fatalf("no: status %q", dishStatus(t, b, rolls))
	}
	if got[0].status != model.DishActive || got[1].status != model.DishRejected {
		t.Fatalf("no: card = %+v", got)
	}

	// A changed mind is one more tap.
	if _, _, _, err := b.applySuggestTap(suggAddUnique, strconv.FormatInt(rolls, 10), current); err != nil {
		t.Fatal(err)
	}
	if dishStatus(t, b, rolls) != model.DishActive {
		t.Fatalf("add after no: status %q", dishStatus(t, b, rolls))
	}
}

func TestSuggestMaybeWritesNothingAndStaysOnTheCard(t *testing.T) {
	b := menuBot(t)
	dishes, current := sentSuggestions(t, b)
	soup, rolls := dishes[0].id, dishes[1].id

	got, _, redraw, err := b.applySuggestTap(suggMaybeUnique, strconv.FormatInt(soup, 10), current)
	if err != nil || !redraw {
		t.Fatalf("maybe: redraw=%v err=%v", redraw, err)
	}
	if dishStatus(t, b, soup) != model.DishProposed {
		t.Fatalf("maybe changed the status to %q", dishStatus(t, b, soup))
	}
	if !got[0].maybe || got[1].maybe {
		t.Fatalf("maybe: card = %+v", got)
	}

	// The maybe survives a tap on another dish: it is read back from the card.
	_, markup := suggestView(got)
	got, _, _, err = b.applySuggestTap(suggNoUnique, strconv.FormatInt(rolls, 10), asTelegramSentIt(markup))
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].maybe || got[1].status != model.DishRejected {
		t.Fatalf("after another tap: card = %+v", got)
	}
	text, _ := suggestView(got)
	if !strings.Contains(text, "🤔 Подумаємо") {
		t.Fatalf("the maybe was lost from the card:\n%s", text)
	}
}

func TestSuggestTapWithBadData(t *testing.T) {
	b := menuBot(t)
	_, current := sentSuggestions(t, b)
	for _, tc := range []struct{ unique, data, toast string }{
		{suggAddUnique, "x", "Невірні дані"},
		{suggAddUnique, "", "Невірні дані"},
		{"sugg_other", "1", "Невірні дані"},
		{suggNoUnique, "999", "Цієї страви вже немає"},
	} {
		_, toast, redraw, err := b.applySuggestTap(tc.unique, tc.data, current)
		if err != nil || redraw || toast != tc.toast {
			t.Errorf("%s %q: toast=%q redraw=%v err=%v", tc.unique, tc.data, toast, redraw, err)
		}
	}
}

// Without a recognizer there is nobody to ask, and the run must not reach for
// the nil one.
func TestSendDishSuggestionsWithoutRecognizerIsANoop(t *testing.T) {
	b := menuBot(t)
	b.sendDishSuggestions(t.Context(), menuMorning)
	if b.suggestRunning.Load() {
		t.Fatal("a run was started without a recognizer")
	}
}
