package dish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

var catalogue = []DishRef{
	{ID: 1, Name: "Деруни"},
	{ID: 2, Name: "Оладки"},
	{ID: 3, Name: "Млинці"},
	{ID: 4, Name: "Борщ"},
	{ID: 5, Name: "Пюре зі скумбрією"},
}

// item is how the tests say what they expect of one dish: the name shown, the
// catalogue id behind it (0 when there is none) and its alternatives.
type item struct {
	name  string
	id    int64
	alts  []int64
	isNew bool
}

func gotItems(g Guess) []item {
	var out []item
	for _, it := range g.Items {
		got := item{name: it.Name, id: it.Dish.ID, isNew: !it.Known()}
		for _, a := range it.Alts {
			got.alts = append(got.alts, a.ID)
		}
		out = append(out, got)
	}
	return out
}

func sameItems(a, b []item) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].name != b[i].name || a[i].id != b[i].id || a[i].isNew != b[i].isNew ||
			fmt.Sprint(a[i].alts) != fmt.Sprint(b[i].alts) {
			return false
		}
	}
	return true
}

// answer builds the model's JSON from compact item literals, so each case
// reads as the plate it describes rather than as a wall of braces.
func answer(slot, date string, items ...string) string {
	return `{"items":[` + strings.Join(items, ",") + `],"note":"","slot":"` + slot + `","date":"` + date + `"}`
}

func known(name string, id int64, alts ...int64) string {
	return fmt.Sprintf(`{"name":%q,"id":%d,"confidence":"high","alternatives":%s,"meal":"any","days":"any"}`, name, id, ints(alts))
}

func unknown(name string, alts ...int64) string {
	return fmt.Sprintf(`{"name":%q,"id":null,"confidence":"low","alternatives":%s,"meal":"any","days":"any"}`, name, ints(alts))
}

func ints(v []int64) string {
	b, _ := json.Marshal(append([]int64{}, v...))
	return string(b)
}

func TestParseGuess(t *testing.T) {
	tests := []struct {
		name     string
		answer   string
		want     []item
		wantSlot string
		wantDate string
	}{
		{
			name:     "the plate is a list, main dish first",
			answer:   answer("lunch", "2026-09-09", known("Борщ", 4), known("Деруни", 1)),
			want:     []item{{name: "Борщ", id: 4}, {name: "Деруни", id: 1}},
			wantSlot: "lunch",
			wantDate: "2026-09-09",
		},
		{
			// The model makes up an id for a dish it recognised but could
			// not find. The dish was still eaten, so it survives as one to
			// create rather than landing on whatever dish owns that number.
			name:   "an id outside the catalogue leaves a dish to create",
			answer: answer("", "", known("Пшоняна каша", 99)),
			want:   []item{{name: "Пшоняна каша", isNew: true}},
		},
		{
			name:   "alternatives are other readings of the same dish",
			answer: answer("", "", known("Деруни", 1, 2, 3, 4)),
			want:   []item{{name: "Деруни", id: 1, alts: []int64{2, 3}}},
		},
		{
			name:   "an invented alternative, and the dish itself, are not alternatives",
			answer: answer("", "", known("Деруни", 1, 77, 1, 2)),
			want:   []item{{name: "Деруни", id: 1, alts: []int64{2}}},
		},
		{
			name:   "one dish read twice is one dish",
			answer: answer("", "", known("Деруни", 1), known("Деруни", 1), unknown("Сирники"), unknown("сирники")),
			want:   []item{{name: "Деруни", id: 1}, {name: "Сирники", isNew: true}},
		},
		{
			// A combination the family names as one dish comes back as one
			// item: the mash is not a second dish of the meal.
			name:     "a combination on the list is one dish",
			answer:   answer("dinner", "", known("Пюре зі скумбрією", 5)),
			want:     []item{{name: "Пюре зі скумбрією", id: 5}},
			wantSlot: "dinner",
		},
		{
			name: "capped at four",
			answer: answer("", "", known("Деруни", 1), known("Оладки", 2), known("Млинці", 3),
				known("Борщ", 4), unknown("Сирники")),
			want: []item{
				{name: "Деруни", id: 1}, {name: "Оладки", id: 2},
				{name: "Млинці", id: 3}, {name: "Борщ", id: 4},
			},
		},
		{
			// A nameless dish can neither be shown nor created.
			name:   "a dish with no name at all is dropped",
			answer: answer("", "", unknown("")),
			want:   nil,
		},
		{
			name:   "a catalogue dish answers for a nameless item",
			answer: answer("", "", known("", 4)),
			want:   []item{{name: "Борщ", id: 4}},
		},
		{
			// The old Mealie meal names are not this schema's: a model still
			// answering them gets the clock instead.
			name:   "nonsense slot and date are cleared",
			answer: answer("obid", "вчора", known("Деруни", 1)),
			want:   []item{{name: "Деруни", id: 1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := parseGuess([]byte(tt.answer), catalogue)
			if err != nil {
				t.Fatalf("parseGuess: %v", err)
			}
			if got := gotItems(g); !sameItems(got, tt.want) {
				t.Errorf("items = %+v, want %+v", got, tt.want)
			}
			if g.Slot != tt.wantSlot {
				t.Errorf("slot = %q, want %q", g.Slot, tt.wantSlot)
			}
			if g.Date != tt.wantDate {
				t.Errorf("date = %q, want %q", g.Date, tt.wantDate)
			}
		})
	}
}

// A dish to create carries which meal and which days it is for: the card
// creates it without asking anything more.
func TestParseGuessKeepsMealAndDaysForANewDish(t *testing.T) {
	g, err := parseGuess([]byte(answer("", "",
		`{"name":"Піца","id":null,"confidence":"high","alternatives":[],"meal":"dinner","days":"weekend"}`,
	)), catalogue)
	if err != nil {
		t.Fatalf("parseGuess: %v", err)
	}
	if it := g.Items[0]; it.Meal != "dinner" || it.Days != "weekend" {
		t.Fatalf("meal/days lost: %+v", it)
	}
}

// A provider that ignores the enum does not get to write "обід" into a column
// whose CHECK would then refuse the whole dish.
func TestParseGuessDefaultsUnknownMealAndDays(t *testing.T) {
	g, err := parseGuess([]byte(answer("", "",
		`{"name":"Піца","id":null,"confidence":"high","alternatives":[],"meal":"обід","days":""}`,
	)), catalogue)
	if err != nil {
		t.Fatalf("parseGuess: %v", err)
	}
	if it := g.Items[0]; it.Meal != "any" || it.Days != "any" {
		t.Fatalf("want any/any, got %+v", it)
	}
}

func TestParseGuessRejectsNonJSON(t *testing.T) {
	if _, err := parseGuess([]byte("вибач, не можу"), catalogue); err == nil {
		t.Fatal("want an error when the model answers prose")
	}
}

// An item carries the catalogue's own name, not the model's spelling of it:
// the card shows the dish the family knows.
func TestParseGuessCarriesDishIdentity(t *testing.T) {
	g, err := parseGuess([]byte(answer("", "", known("деруни зі сметаною", 1))), catalogue)
	if err != nil {
		t.Fatalf("parseGuess: %v", err)
	}
	if g.Items[0].Dish != (DishRef{ID: 1, Name: "Деруни"}) {
		t.Fatalf("item lost its identity: %+v", g.Items[0].Dish)
	}
}

func TestIdentifyRequest(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"items\":[{\"name\":\"Деруни\",\"id\":1,\"confidence\":\"high\",\"alternatives\":[],\"meal\":\"any\",\"days\":\"any\"}],\"note\":\"деруни зі сметаною\",\"slot\":\"lunch\",\"date\":\"\"}"}}]}`))
	}))
	defer srv.Close()

	r := New(srv.URL, "k", "test-model")
	g, err := r.Identify(context.Background(), Input{
		Photo:   []byte("JPEG"),
		Mime:    "image/jpeg",
		Caption: "драники на обед",
		Now:     time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC),
		Dishes:  catalogue,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(g.Items) != 1 || g.Items[0].Dish.ID != 1 {
		t.Fatalf("guess = %+v", g)
	}
	if g.Note != "деруни зі сметаною" {
		t.Fatalf("note = %q", g.Note)
	}

	if body["model"] != "test-model" {
		t.Errorf("model = %v", body["model"])
	}
	msgs, _ := json.Marshal(body["messages"])
	text := string(msgs)
	// The catalogue, the hint and the picture all have to reach the model:
	// without the first it cannot answer from the list, without the picture
	// there is nothing to look at.
	for _, want := range []string{"1 | Деруни", "5 | Пюре зі скумбрією", "драники на обед", "data:image/jpeg;base64,SlBFRw"} {
		if !strings.Contains(text, want) {
			t.Errorf("request missing %q", want)
		}
	}
	if _, ok := body["response_format"]; !ok {
		t.Error("request must force the json schema")
	}
	// A served-together combination is one dish only when the family names
	// it so; the prompt has to say both halves of that.
	if !strings.Contains(text, "пюре зі скумбрією") || !strings.Contains(text, "якщо вона є такою в списку") {
		t.Error("prompt lost the combination rule")
	}
	schema, _ := json.Marshal(body["response_format"])
	for _, want := range []string{`"id":{"type":["integer","null"]}`, `"enum":["lunch","dinner",""]`} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("schema missing %s", want)
		}
	}
}

// The text-only path sends no image part at all.
func TestIdentifyWithoutPhoto(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"items\":[{\"name\":\"Сирники\",\"id\":null,\"confidence\":\"high\",\"alternatives\":[],\"meal\":\"any\",\"days\":\"any\"}],\"note\":\"\",\"slot\":\"\",\"date\":\"\"}"}}]}`))
	}))
	defer srv.Close()

	g, err := New(srv.URL, "k", "m").Identify(context.Background(), Input{
		Caption: "сырники", Now: time.Now(), Dishes: catalogue,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(g.Items) != 1 || g.Items[0].Known() || g.Items[0].Name != "Сирники" {
		t.Fatalf("guess = %+v", g)
	}
	msgs, _ := json.Marshal(body["messages"])
	if strings.Contains(string(msgs), "image_url") {
		t.Error("no photo means no image part")
	}
}

func TestIdentifySurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"overloaded"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "k", "m").Identify(context.Background(), Input{Dishes: catalogue, Now: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err = %v, want the provider's message", err)
	}
}

func logGuess(t *testing.T, g Guess) {
	t.Helper()
	for i, it := range g.Items {
		where := "нова страва (" + it.Meal + "/" + it.Days + ")"
		if it.Known() {
			where = fmt.Sprintf("id %d", it.Dish.ID)
		}
		var alts []string
		for _, a := range it.Alts {
			alts = append(alts, a.Name)
		}
		t.Logf("страва %d: %s [%s] (%s) alt: %s", i+1, it.Name, where, it.Confidence, strings.Join(alts, ", "))
	}
	t.Logf("slot=%q date=%q note=%q", g.Slot, g.Date, g.Note)
}

func liveModel() (base, model string) {
	base, model = os.Getenv("AI_BASE_URL"), os.Getenv("AI_MODEL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-6-luna"
	}
	return base, model
}

// TestIdentifyLive runs the real prompt against the real provider: skipped
// unless the key and a photo are given, so `go test ./...` stays offline.
//
//	AI_API_KEY=… AI_MODEL=gpt-6-luna DISH_TEST_PHOTO=plate.jpg go test ./internal/dish -run Live -v
func TestIdentifyLive(t *testing.T) {
	key, photoPath := os.Getenv("AI_API_KEY"), os.Getenv("DISH_TEST_PHOTO")
	if key == "" || photoPath == "" {
		t.Skip("AI_API_KEY or DISH_TEST_PHOTO not set")
	}
	photo, err := os.ReadFile(photoPath)
	if err != nil {
		t.Fatalf("read photo: %v", err)
	}
	base, model := liveModel()

	g, err := New(base, key, model).Identify(context.Background(), Input{
		Photo:   photo,
		Mime:    "image/jpeg",
		Caption: os.Getenv("DISH_TEST_CAPTION"),
		Now:     time.Now(),
		Dishes:  catalogue,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	logGuess(t, g)
	if len(g.Items) == 0 {
		t.Fatal("no dish at all from a photo of a plate")
	}
}

// TestIdentifyLiveText asks the real model the question a text-only /cooked
// asks, against the fixture catalogue. Skipped without the key.
//
//	AI_API_KEY=… DISH_TEST_TEXT="макароны, гуляш, котлета куриная" \
//	  go test ./internal/dish -run LiveText -v
func TestIdentifyLiveText(t *testing.T) {
	key, text := os.Getenv("AI_API_KEY"), os.Getenv("DISH_TEST_TEXT")
	if key == "" || text == "" {
		t.Skip("AI_API_KEY or DISH_TEST_TEXT not set")
	}
	base, model := liveModel()

	g, err := New(base, key, model).Identify(context.Background(), Input{
		Caption: text, Now: time.Now(), Dishes: catalogue,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	logGuess(t, g)
}

// The near misses are the point of an unplaceable dish: the cook either
// creates it or takes the dish the model was too unsure to pick.
func TestParseGuessKeepsAlternativesForANewDish(t *testing.T) {
	g, err := parseGuess([]byte(answer("", "", unknown("Смажене м'ясо з цибулею", 4, 1))), catalogue)
	if err != nil {
		t.Fatalf("parseGuess: %v", err)
	}
	it := g.Items[0]
	if it.Known() || len(it.Alts) != 2 || it.Alts[0].ID != 4 {
		t.Fatalf("item = %+v", it)
	}
}
