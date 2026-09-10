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

func TestParseGuess(t *testing.T) {
	tests := []struct {
		name     string
		answer   string
		wantSlug []string
		wantNew  string
		wantSlot string
		wantDate string
	}{
		{
			name:     "candidates in order",
			answer:   `{"candidates":[{"slug":"deruni","confidence":"high"},{"slug":"oladki","confidence":"low"}],"new_name":null,"category":null,"tags":[],"note":"деруни","slot":"obid","date":"2026-09-09"}`,
			wantSlug: []string{"deruni", "oladki"},
			wantSlot: "obid",
			wantDate: "2026-09-09",
		},
		{
			// A slug outside the catalogue is the model inventing a dish; it
			// must not survive into a card offering to record it.
			name:     "invented slug dropped",
			answer:   `{"candidates":[{"slug":"pizza-hut","confidence":"high"},{"slug":"borshch","confidence":"medium"}],"new_name":null,"category":null,"tags":[],"note":"","slot":"","date":""}`,
			wantSlug: []string{"borshch"},
		},
		{
			name:     "every slug invented leaves nothing",
			answer:   `{"candidates":[{"slug":"nope","confidence":"high"}],"new_name":"Сирники","category":"Сніданки","tags":["швидко"],"note":"","slot":"","date":""}`,
			wantSlug: nil,
			wantNew:  "Сирники",
		},
		{
			name:     "duplicates collapse",
			answer:   `{"candidates":[{"slug":"deruni","confidence":"high"},{"slug":"deruni","confidence":"low"}],"new_name":null,"category":null,"tags":[],"note":"","slot":"","date":""}`,
			wantSlug: []string{"deruni"},
		},
		{
			name:     "capped at three",
			answer:   `{"candidates":[{"slug":"deruni","confidence":"high"},{"slug":"oladki","confidence":"medium"},{"slug":"mlintsi","confidence":"low"},{"slug":"borshch","confidence":"low"}],"new_name":null,"category":null,"tags":[],"note":"","slot":"","date":""}`,
			wantSlug: []string{"deruni", "oladki", "mlintsi"},
		},
		{
			name:     "nonsense slot and date are cleared",
			answer:   `{"candidates":[{"slug":"deruni","confidence":"high"}],"new_name":null,"category":null,"tags":[],"note":"","slot":"snidanok","date":"вчора"}`,
			wantSlug: []string{"deruni"},
			wantSlot: "",
			wantDate: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := parseGuess([]byte(tt.answer), catalogue)
			if err != nil {
				t.Fatalf("parseGuess: %v", err)
			}
			var got []string
			for _, c := range g.Candidates {
				got = append(got, c.Recipe.Slug)
			}
			if strings.Join(got, ",") != strings.Join(tt.wantSlug, ",") {
				t.Errorf("candidates = %v, want %v", got, tt.wantSlug)
			}
			if g.NewName != tt.wantNew {
				t.Errorf("new_name = %q, want %q", g.NewName, tt.wantNew)
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

func TestParseGuessRejectsNonJSON(t *testing.T) {
	if _, err := parseGuess([]byte("вибач, не можу"), catalogue); err == nil {
		t.Fatal("want an error when the model answers prose")
	}
}

// A candidate carries the whole recipe, not just its slug: the caller needs
// the uuid for the timeline and the name for the button.
func TestParseGuessCarriesRecipeIdentity(t *testing.T) {
	g, err := parseGuess([]byte(`{"candidates":[{"slug":"deruni","confidence":"high"}],"new_name":null,"category":null,"tags":[],"note":"","slot":"","date":""}`), catalogue)
	if err != nil {
		t.Fatalf("parseGuess: %v", err)
	}
	if g.Candidates[0].Recipe.ID != "u1" || g.Candidates[0].Recipe.Name != "Деруни" {
		t.Fatalf("candidate lost its identity: %+v", g.Candidates[0].Recipe)
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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"candidates\":[{\"slug\":\"deruni\",\"confidence\":\"high\"}],\"new_name\":null,\"category\":null,\"tags\":[],\"note\":\"деруни зі сметаною\",\"slot\":\"obid\",\"date\":\"\"}"}}]}`))
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
	if len(g.Candidates) != 1 || g.Candidates[0].Recipe.Slug != "deruni" {
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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"candidates\":[],\"new_name\":\"Сирники\",\"category\":null,\"tags\":[],\"note\":\"\",\"slot\":\"\",\"date\":\"\"}"}}]}`))
	}))
	defer srv.Close()

	g, err := New(srv.URL, "k", "m").Identify(context.Background(), Input{
		Caption: "сырники", Now: time.Now(), Recipes: catalogue,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if g.NewName != "Сирники" {
		t.Fatalf("new_name = %q", g.NewName)
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

// TestIdentifyLive runs the real prompt against the real provider, the way
// parse's own live test does: skipped unless the key and a photo are given,
// so `go test ./...` stays offline.
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
	base, model := os.Getenv("AI_BASE_URL"), os.Getenv("AI_MODEL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-5.6-luna"
	}

	// With a Mealie to ask, the question is the real one: the whole catalogue,
	// which is what makes the answer meaningful. Without it the fixture keeps
	// the test runnable, but a dish outside those four can only come back as
	// "not in the database".
	recipes := catalogue
	if url, tok := os.Getenv("MEALIE_URL"), os.Getenv("MEALIE_TOKEN"); url != "" && tok != "" {
		live, err := mealie.New(url, tok).Recipes(context.Background())
		if err != nil {
			t.Fatalf("catalogue: %v", err)
		}
		recipes = live
		t.Logf("catalogue: %d recipes", len(recipes))
	}

	g, err := New(base, key, model).Identify(context.Background(), Input{
		Photo:   photo,
		Mime:    "image/jpeg",
		Caption: os.Getenv("DISH_TEST_CAPTION"),
		Now:     time.Now(),
		Recipes: recipes,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	for i, c := range g.Candidates {
		t.Logf("candidate %d: %s (%s)", i+1, c.Recipe.Name, c.Confidence)
	}
	for _, c := range g.Alongside {
		t.Logf("side: %s (%s)", c.Recipe.Name, c.Confidence)
	}
	t.Logf("new=%q slot=%q date=%q note=%q", g.NewName, g.Slot, g.Date, g.Note)
	if len(g.Candidates) == 0 {
		t.Fatal("no candidate from a photo of a known dish")
	}
}

func TestParseGuessAlongside(t *testing.T) {
	tests := []struct {
		name          string
		answer        string
		wantMain      []string
		wantAlongside []string
	}{
		{
			name:          "main plus its side",
			answer:        `{"candidates":[{"slug":"borshch","confidence":"high"}],"alongside":[{"slug":"oladki","confidence":"medium"}],"new_name":null,"category":null,"tags":[],"note":"","slot":"","date":""}`,
			wantMain:      []string{"borshch"},
			wantAlongside: []string{"oladki"},
		},
		{
			name:          "invented side dropped",
			answer:        `{"candidates":[{"slug":"borshch","confidence":"high"}],"alongside":[{"slug":"kimchi","confidence":"low"}],"new_name":null,"category":null,"tags":[],"note":"","slot":"","date":""}`,
			wantMain:      []string{"borshch"},
			wantAlongside: nil,
		},
		{
			name:          "sides capped at three",
			answer:        `{"candidates":[{"slug":"borshch","confidence":"high"}],"alongside":[{"slug":"oladki","confidence":"low"},{"slug":"mlintsi","confidence":"low"},{"slug":"deruni","confidence":"low"},{"slug":"oladki","confidence":"low"}],"new_name":null,"category":null,"tags":[],"note":"","slot":"","date":""}`,
			wantMain:      []string{"borshch"},
			wantAlongside: []string{"oladki", "mlintsi", "deruni"},
		},
		{
			// The same dish may be both an alternative reading of the plate
			// and the side it actually is; which one it becomes is settled by
			// the main dish the cook confirms, not by the parser.
			name:          "a dish may be both an alternative and a side",
			answer:        `{"candidates":[{"slug":"borshch","confidence":"high"},{"slug":"oladki","confidence":"low"}],"alongside":[{"slug":"oladki","confidence":"medium"}],"new_name":null,"category":null,"tags":[],"note":"","slot":"","date":""}`,
			wantMain:      []string{"borshch", "oladki"},
			wantAlongside: []string{"oladki"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := parseGuess([]byte(tt.answer), catalogue)
			if err != nil {
				t.Fatalf("parseGuess: %v", err)
			}
			var main, sides []string
			for _, c := range g.Candidates {
				main = append(main, c.Recipe.Slug)
			}
			for _, c := range g.Alongside {
				sides = append(sides, c.Recipe.Slug)
			}
			if strings.Join(main, ",") != strings.Join(tt.wantMain, ",") {
				t.Errorf("candidates = %v, want %v", main, tt.wantMain)
			}
			if strings.Join(sides, ",") != strings.Join(tt.wantAlongside, ",") {
				t.Errorf("sides = %v, want %v", sides, tt.wantAlongside)
			}
		})
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
	recipes := catalogue
	if url, tok := os.Getenv("MEALIE_URL"), os.Getenv("MEALIE_TOKEN"); url != "" && tok != "" {
		live, err := mealie.New(url, tok).Recipes(context.Background())
		if err != nil {
			t.Fatalf("catalogue: %v", err)
		}
		recipes = live
	}
	base, model := os.Getenv("AI_BASE_URL"), os.Getenv("AI_MODEL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-5.6-luna"
	}

	g, err := New(base, key, model).Identify(context.Background(), Input{
		Caption: text, Now: time.Now(), Recipes: recipes,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	for i, c := range g.Candidates {
		t.Logf("candidate %d: %s (%s)", i+1, c.Recipe.Name, c.Confidence)
	}
	for _, c := range g.Alongside {
		t.Logf("alongside: %s (%s)", c.Recipe.Name, c.Confidence)
	}
	t.Logf("slot=%q date=%q note=%q", g.Slot, g.Date, g.Note)
}
