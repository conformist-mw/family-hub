package dish

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"familyhub/internal/mealie"
)

var catalogue = []mealie.Recipe{
	{ID: "u1", Slug: "deruni", Name: "Деруни"},
	{ID: "u2", Slug: "oladki", Name: "Оладки"},
	{ID: "u3", Slug: "mlintsi", Name: "Млинці"},
	{ID: "u4", Slug: "borshch", Name: "Борщ"},
}

// item is how the tests say what they expect of one dish: the name shown, the
// recipe behind it (empty when the database has none) and its alternatives.
type item struct {
	name  string
	slug  string
	alts  []string
	isNew bool
}

func gotItems(g Guess) []item {
	var out []item
	for _, it := range g.Items {
		got := item{name: it.Name, slug: it.Recipe.Slug, isNew: !it.Known()}
		for _, a := range it.Alts {
			got.alts = append(got.alts, a.Slug)
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
		if a[i].name != b[i].name || a[i].slug != b[i].slug || a[i].isNew != b[i].isNew ||
			strings.Join(a[i].alts, ",") != strings.Join(b[i].alts, ",") {
			return false
		}
	}
	return true
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
			answer:   `{"items":[{"name":"Борщ","slug":"borshch","confidence":"high","alternatives":[],"category":null,"tags":[]},{"name":"Деруни","slug":"deruni","confidence":"medium","alternatives":[],"category":null,"tags":[]}],"note":"борщ і деруни","slot":"obid","date":"2026-09-09"}`,
			want:     []item{{name: "Борщ", slug: "borshch"}, {name: "Деруни", slug: "deruni"}},
			wantSlot: "obid",
			wantDate: "2026-09-09",
		},
		{
			// The model invents a slug for a dish it recognised but could not
			// find. The dish was still eaten, so it survives as one to create.
			name:   "a slug outside the catalogue leaves a dish to create",
			answer: `{"items":[{"name":"Пшоняна каша","slug":"pshonyana-kasha","confidence":"high","alternatives":[],"category":"Гарніри","tags":["швидко"]}],"note":"","slot":"","date":""}`,
			want:   []item{{name: "Пшоняна каша", isNew: true}},
		},
		{
			name:   "alternatives are other readings of the same dish",
			answer: `{"items":[{"name":"Деруни","slug":"deruni","confidence":"low","alternatives":["oladki","mlintsi","borshch"],"category":null,"tags":[]}],"note":"","slot":"","date":""}`,
			want:   []item{{name: "Деруни", slug: "deruni", alts: []string{"oladki", "mlintsi"}}},
		},
		{
			name:   "an invented alternative, and the dish itself, are not alternatives",
			answer: `{"items":[{"name":"Деруни","slug":"deruni","confidence":"low","alternatives":["pizza-hut","deruni","oladki"],"category":null,"tags":[]}],"note":"","slot":"","date":""}`,
			want:   []item{{name: "Деруни", slug: "deruni", alts: []string{"oladki"}}},
		},
		{
			name:   "one dish read twice is one dish",
			answer: `{"items":[{"name":"Деруни","slug":"deruni","confidence":"high","alternatives":[],"category":null,"tags":[]},{"name":"Деруни","slug":"deruni","confidence":"low","alternatives":[],"category":null,"tags":[]},{"name":"Сирники","slug":null,"confidence":"low","alternatives":[],"category":null,"tags":[]},{"name":"сирники","slug":null,"confidence":"low","alternatives":[],"category":null,"tags":[]}],"note":"","slot":"","date":""}`,
			want:   []item{{name: "Деруни", slug: "deruni"}, {name: "Сирники", isNew: true}},
		},
		{
			name:   "capped at four",
			answer: `{"items":[{"name":"Деруни","slug":"deruni","confidence":"high","alternatives":[],"category":null,"tags":[]},{"name":"Оладки","slug":"oladki","confidence":"low","alternatives":[],"category":null,"tags":[]},{"name":"Млинці","slug":"mlintsi","confidence":"low","alternatives":[],"category":null,"tags":[]},{"name":"Борщ","slug":"borshch","confidence":"low","alternatives":[],"category":null,"tags":[]},{"name":"Сирники","slug":null,"confidence":"low","alternatives":[],"category":null,"tags":[]}],"note":"","slot":"","date":""}`,
			want: []item{
				{name: "Деруни", slug: "deruni"}, {name: "Оладки", slug: "oladki"},
				{name: "Млинці", slug: "mlintsi"}, {name: "Борщ", slug: "borshch"},
			},
		},
		{
			// A nameless dish can neither be shown nor created.
			name:   "a dish with no name at all is dropped",
			answer: `{"items":[{"name":"","slug":null,"confidence":"low","alternatives":[],"category":null,"tags":[]}],"note":"","slot":"","date":""}`,
			want:   nil,
		},
		{
			name:   "a named recipe answers for a nameless item",
			answer: `{"items":[{"name":"","slug":"borshch","confidence":"high","alternatives":[],"category":null,"tags":[]}],"note":"","slot":"","date":""}`,
			want:   []item{{name: "Борщ", slug: "borshch"}},
		},
		{
			name:   "nonsense slot and date are cleared",
			answer: `{"items":[{"name":"Деруни","slug":"deruni","confidence":"high","alternatives":[],"category":null,"tags":[]}],"note":"","slot":"snidanok","date":"вчора"}`,
			want:   []item{{name: "Деруни", slug: "deruni"}},
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

// A dish to create carries where it would be filed: the card creates it
// without asking anything more.
func TestParseGuessKeepsFilingForANewDish(t *testing.T) {
	g, err := parseGuess([]byte(`{"items":[{"name":"Пшоняна каша","slug":null,"confidence":"high","alternatives":[],"category":"Гарніри","tags":["швидко","дитяче"]}],"note":"","slot":"","date":""}`), catalogue)
	if err != nil {
		t.Fatalf("parseGuess: %v", err)
	}
	it := g.Items[0]
	if it.Category != "Гарніри" || strings.Join(it.Tags, ",") != "швидко,дитяче" {
		t.Fatalf("filing lost: %+v", it)
	}
}

func TestParseGuessRejectsNonJSON(t *testing.T) {
	if _, err := parseGuess([]byte("вибач, не можу"), catalogue); err == nil {
		t.Fatal("want an error when the model answers prose")
	}
}

// An item carries the whole recipe, not just its slug: the caller needs the
// uuid for the timeline and the name for the button.
func TestParseGuessCarriesRecipeIdentity(t *testing.T) {
	g, err := parseGuess([]byte(`{"items":[{"name":"Деруни","slug":"deruni","confidence":"high","alternatives":[],"category":null,"tags":[]}],"note":"","slot":"","date":""}`), catalogue)
	if err != nil {
		t.Fatalf("parseGuess: %v", err)
	}
	if g.Items[0].Recipe.ID != "u1" || g.Items[0].Recipe.Name != "Деруни" {
		t.Fatalf("item lost its identity: %+v", g.Items[0].Recipe)
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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"items\":[{\"name\":\"Деруни\",\"slug\":\"deruni\",\"confidence\":\"high\",\"alternatives\":[],\"category\":null,\"tags\":[]}],\"note\":\"деруни зі сметаною\",\"slot\":\"obid\",\"date\":\"\"}"}}]}`))
	}))
	defer srv.Close()

	r := New(srv.URL, "k", "test-model")
	g, err := r.Identify(context.Background(), Input{
		Photo:      []byte("JPEG"),
		Mime:       "image/jpeg",
		Caption:    "драники на обед",
		Now:        time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC),
		Recipes:    catalogue,
		Categories: []string{"Основні страви"},
		Tags:       []string{"обід"},
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(g.Items) != 1 || g.Items[0].Recipe.Slug != "deruni" {
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
	for _, want := range []string{"deruni | Деруни", "драники на обед", "data:image/jpeg;base64,SlBFRw", "Дозволені теги"} {
		if !strings.Contains(text, want) {
			t.Errorf("request missing %q", want)
		}
	}
	if _, ok := body["response_format"]; !ok {
		t.Error("request must force the json schema")
	}
}

// The text-only path sends no image part at all.
func TestIdentifyWithoutPhoto(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"items\":[{\"name\":\"Сирники\",\"slug\":null,\"confidence\":\"high\",\"alternatives\":[],\"category\":null,\"tags\":[]}],\"note\":\"\",\"slot\":\"\",\"date\":\"\"}"}}]}`))
	}))
	defer srv.Close()

	g, err := New(srv.URL, "k", "m").Identify(context.Background(), Input{
		Caption: "сырники", Now: time.Now(), Recipes: catalogue,
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

	_, err := New(srv.URL, "k", "m").Identify(context.Background(), Input{Recipes: catalogue, Now: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err = %v, want the provider's message", err)
	}
}

func logGuess(t *testing.T, g Guess) {
	t.Helper()
	for i, it := range g.Items {
		where := "нова страва"
		if it.Known() {
			where = it.Recipe.Slug
		}
		var alts []string
		for _, a := range it.Alts {
			alts = append(alts, a.Name)
		}
		t.Logf("страва %d: %s [%s] (%s) alt: %s", i+1, it.Name, where, it.Confidence, strings.Join(alts, ", "))
	}
	t.Logf("slot=%q date=%q note=%q", g.Slot, g.Date, g.Note)
}

// liveCatalogue is the household's real recipe list when Mealie is reachable.
// With it the question is the real one; without it the fixture keeps the test
// runnable, but a dish outside those four can only come back as a new one.
func liveCatalogue(t *testing.T) []mealie.Recipe {
	t.Helper()
	url, tok := os.Getenv("MEALIE_URL"), os.Getenv("MEALIE_TOKEN")
	if url == "" || tok == "" {
		return catalogue
	}
	live, err := mealie.New(url, tok).Recipes(context.Background())
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	t.Logf("catalogue: %d recipes", len(live))
	return live
}

func liveModel() (base, model string) {
	base, model = os.Getenv("AI_BASE_URL"), os.Getenv("AI_MODEL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-5.6-luna"
	}
	return base, model
}

// TestIdentifyLive runs the real prompt against the real provider: skipped
// unless the key and a photo are given, so `go test ./...` stays offline.
//
//	AI_API_KEY=… AI_MODEL=gpt-5.6-luna DISH_TEST_PHOTO=plate.jpg go test ./internal/dish -run Live -v
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
		Recipes: liveCatalogue(t),
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
// asks, with the household's real catalogue. Skipped without the key.
//
//	AI_API_KEY=… MEALIE_URL=… MEALIE_TOKEN=… DISH_TEST_TEXT="макароны, гуляш, котлета куриная" \
//	  go test ./internal/dish -run LiveText -v
func TestIdentifyLiveText(t *testing.T) {
	key, text := os.Getenv("AI_API_KEY"), os.Getenv("DISH_TEST_TEXT")
	if key == "" || text == "" {
		t.Skip("AI_API_KEY or DISH_TEST_TEXT not set")
	}
	base, model := liveModel()

	g, err := New(base, key, model).Identify(context.Background(), Input{
		Caption: text, Now: time.Now(), Recipes: liveCatalogue(t),
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	logGuess(t, g)
}

// The near misses are the point of an unplaceable dish: the cook either
// creates it or takes the recipe the model was too unsure to pick.
func TestParseGuessKeepsAlternativesForANewDish(t *testing.T) {
	g, err := parseGuess([]byte(`{"items":[{"name":"Смажене м'ясо з цибулею","slug":null,"confidence":"low","alternatives":["borshch","deruni"],"category":null,"tags":[]}],"note":"","slot":"","date":""}`), catalogue)
	if err != nil {
		t.Fatalf("parseGuess: %v", err)
	}
	it := g.Items[0]
	if it.Known() || len(it.Alts) != 2 || it.Alts[0].Slug != "borshch" {
		t.Fatalf("item = %+v", it)
	}
}
