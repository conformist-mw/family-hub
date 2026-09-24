package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/db"
	"familyhub/internal/dish"
	"familyhub/internal/model"
)

func sampleSuggestions() []suggestDish {
	return []suggestDish{
		{Dish: model.Dish{ID: 11, Name: "Солянка", Note: "Густий суп з копченостями.", Meal: "lunch", Days: "any", Status: model.DishProposed}},
		{Dish: model.Dish{ID: 12, Name: "Курячі стегна з печеною картоплею", Meal: "dinner", Days: "any", Status: model.DishProposed}},
		{Dish: model.Dish{ID: 13, Name: "Голубці", Note: "Довго, але на два дні.", Meal: "any", Days: "weekend", Status: model.DishProposed}},
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
	dishes[0].Status = model.DishActive
	dishes[1].maybe = true
	dishes[2].Status = model.DishRejected
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
	dishes[0].Status = model.DishActive
	dishes[0].maybe = true
	text, markup := suggestView(dishes)
	if strings.Contains(text, "Подумаємо") || strings.Contains(buttonTexts(markup)[0][1], answeredPrefix) {
		t.Fatalf("a decided dish still shows the maybe:\n%s\n%v", text, buttonTexts(markup))
	}
}

func TestSuggestViewEscapesNames(t *testing.T) {
	text, _ := suggestView([]suggestDish{{Dish: model.Dish{ID: 1, Name: "Тост <з> сиром", Note: "a & b", Status: model.DishProposed}}})
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
	if len(got) != 1 || got[0].Name != "Голубці" || got[0].Status != model.DishProposed ||
		got[0].Days != model.DishDaysWeekend || got[0].Note != "Довго, але на два дні." {
		t.Fatalf("proposed = %+v", got)
	}
	d, err := b.store.Dish(got[0].ID)
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
	d, err := b.store.Dishes(model.DishRejected)
	if err != nil || len(d) != 1 {
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
	soup, rolls := dishes[0].ID, dishes[1].ID

	got, toast, redraw, err := b.applySuggestTap(suggAddUnique, strconv.FormatInt(soup, 10), current)
	if err != nil || !redraw {
		t.Fatalf("add: redraw=%v err=%v", redraw, err)
	}
	if dishStatus(t, b, soup) != model.DishActive || !strings.Contains(toast, "Солянка") {
		t.Fatalf("add: status %q, toast %q", dishStatus(t, b, soup), toast)
	}
	if len(got) != 2 || got[0].Status != model.DishActive || got[1].Status != model.DishProposed {
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
	if got[0].Status != model.DishActive || got[1].Status != model.DishRejected {
		t.Fatalf("no: card = %+v", got)
	}
	// A maybe on a dish already decided writes nothing and says so.
	if _, toast, _, err := b.applySuggestTap(suggMaybeUnique, strconv.FormatInt(soup, 10), current); err != nil ||
		toast != "Вже вирішено" || dishStatus(t, b, soup) != model.DishActive {
		t.Fatalf("maybe after add: toast=%q status=%q err=%v", toast, dishStatus(t, b, soup), err)
	}

	// A changed mind is one more tap.
	if _, _, _, err := b.applySuggestTap(suggAddUnique, strconv.FormatInt(rolls, 10), current); err != nil {
		t.Fatal(err)
	}
	if dishStatus(t, b, rolls) != model.DishActive {
		t.Fatalf("add after no: status %q", dishStatus(t, b, rolls))
	}
}

// Once the family has eaten an added dish it is its own: a "no" from the
// old card does not reject it. Before that, "no" after "add" is a changed
// mind like any other.
func TestSuggestNoLeavesAnEatenDish(t *testing.T) {
	b := menuBot(t)
	dishes, current := sentSuggestions(t, b)
	soup, rolls := dishes[0].ID, dishes[1].ID
	for _, id := range []int64{soup, rolls} {
		if _, _, _, err := b.applySuggestTap(suggAddUnique, strconv.FormatInt(id, 10), current); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := b.store.RecordEaten(soup, "2026-09-24", model.MealLunch, "", false); err != nil {
		t.Fatal(err)
	}

	_, toast, redraw, err := b.applySuggestTap(suggNoUnique, strconv.FormatInt(soup, 10), current)
	if err != nil || !redraw || toast != "Вже вирішено" {
		t.Fatalf("no on an eaten dish: toast=%q redraw=%v err=%v", toast, redraw, err)
	}
	if got := dishStatus(t, b, soup); got != model.DishActive {
		t.Errorf("eaten dish status = %q, want active", got)
	}
	if _, _, _, err := b.applySuggestTap(suggNoUnique, strconv.FormatInt(rolls, 10), current); err != nil {
		t.Fatal(err)
	}
	if got := dishStatus(t, b, rolls); got != model.DishRejected {
		t.Errorf("added, never eaten, then no: status = %q, want rejected", got)
	}
}

func TestSuggestMaybeWritesNothingAndStaysOnTheCard(t *testing.T) {
	b := menuBot(t)
	dishes, current := sentSuggestions(t, b)
	soup, rolls := dishes[0].ID, dishes[1].ID

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
	if !got[0].maybe || got[1].Status != model.DishRejected {
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

// modelServer answers every chat-completion request with content, or with a
// server error when content is empty; before answering it waits for release,
// when one is given.
func modelServer(t *testing.T, content string, release <-chan struct{}, hits *atomic.Int32) *dish.Recognizer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if release != nil {
			<-release
		}
		if content == "" {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		out, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		})
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return dish.New(srv.URL, "k", "test-model")
}

const threeSuggestions = `{"dishes":[` +
	`{"name":"Солянка","meal":"lunch","days":"any","note":"Густий суп."},` +
	`{"name":"Голубці","meal":"any","days":"weekend","note":"На два дні."},` +
	`{"name":"Юшка","meal":"lunch","days":"any","note":"Легкий суп."}]}`

func TestCollectSuggestions(t *testing.T) {
	t.Run("the model's dishes become proposals on the card", func(t *testing.T) {
		b := menuBot(t)
		b.cfg.Dish = modelServer(t, threeSuggestions, nil, nil)
		got := b.collectSuggestions(t.Context(), menuMorning)
		if len(got) != 3 || got[0].Name != "Солянка" || got[1].Days != model.DishDaysWeekend {
			t.Fatalf("card = %+v", got)
		}
		if all, err := b.store.Dishes(model.DishProposed); err != nil || len(all) != 3 {
			t.Fatalf("proposed = %+v, %v", all, err)
		}
	})

	t.Run("a failed call writes nothing and sends nothing", func(t *testing.T) {
		b := menuBot(t)
		b.cfg.Dish = modelServer(t, "", nil, nil)
		if got := b.collectSuggestions(t.Context(), menuMorning); len(got) != 0 {
			t.Fatalf("card = %+v, want none", got)
		}
		if all, err := b.store.Dishes(); err != nil || len(all) != 0 {
			t.Fatalf("dishes = %+v, %v", all, err)
		}
	})

	// A write that fails half-way still sends the card for what was written:
	// those dishes are proposed already and will turn up in the menu anyway.
	t.Run("a failed write keeps what came before it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.db")
		b := menuBotAt(t, path)
		b.cfg.Dish = modelServer(t, threeSuggestions, nil, nil)
		raw, err := db.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { raw.Close() })
		if _, err := raw.Exec(`CREATE TRIGGER no_holubtsi BEFORE INSERT ON dishes
			WHEN NEW.name = 'Голубці' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
			t.Fatal(err)
		}
		got := b.collectSuggestions(t.Context(), menuMorning)
		if len(got) != 1 || got[0].Name != "Солянка" {
			t.Fatalf("card = %+v, want the dish written before the failure", got)
		}
	})
}

// A run still waiting on the model is not joined by a second one: the group
// would get two cards of different dishes.
func TestSendDishSuggestionsRunsOneAtATime(t *testing.T) {
	b := menuBot(t)
	release := make(chan struct{})
	var hits atomic.Int32
	b.cfg.Dish = modelServer(t, threeSuggestions, release, &hits)

	b.sendDishSuggestions(t.Context(), menuMorning)
	waitFor(t, "the first run to reach the model", func() bool { return hits.Load() == 1 })
	b.sendDishSuggestions(t.Context(), menuMorning)
	close(release)
	waitFor(t, "the run to finish", func() bool { return !b.suggestRunning.Load() })

	if n := hits.Load(); n != 1 {
		t.Fatalf("the model was asked %d times, want once", n)
	}
	if all, err := b.store.Dishes(model.DishProposed); err != nil || len(all) != 3 {
		t.Fatalf("proposed = %d, %v; want one run's three", len(all), err)
	}
}
