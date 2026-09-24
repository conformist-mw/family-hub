package dish

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSuggestRequest(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		answer := `{"dishes":[` +
			`{"name":"Солянка","meal":"lunch","days":"any","note":"Густий суп з копченостями."},` +
			`{"name":"Курячі стегна з печеною картоплею","meal":"dinner","days":"any","note":"Одна деко — і вечеря готова."},` +
			`{"name":"Голубці","meal":"any","days":"weekend","note":"Довго, але на два дні."}]}`
		out, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": answer}}},
		})
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	got, err := New(srv.URL, "k", "test-model").Suggest(context.Background(), SuggestInput{
		Active:   []string{"Борщ", "Деруни"},
		Proposed: []string{"Шакшука"},
		Rejected: []string{"Плов з бараниною"},
	})
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	want := []Suggestion{
		{Name: "Солянка", Meal: "lunch", Days: "any", Note: "Густий суп з копченостями."},
		{Name: "Курячі стегна з печеною картоплею", Meal: "dinner", Days: "any", Note: "Одна деко — і вечеря готова."},
		{Name: "Голубці", Meal: "any", Days: "weekend", Note: "Довго, але на два дні."},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d suggestions, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("suggestion %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	msgs, _ := json.Marshal(body["messages"])
	text := string(msgs)
	// The rejected list is what keeps last week's "no" from coming back, and
	// the proposed one what keeps the model from repeating itself; both have
	// to reach it, and so does the standing context of the household.
	for _, want := range []string{
		"- Борщ", "- Деруни", "- Шакшука", "- Плов з бараниною",
		"Відхилені", "Баранину не їдять", "не їсть рибу", "обід і вечеря",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("request missing %q", want)
		}
	}
	schema, _ := json.Marshal(body["response_format"])
	for _, want := range []string{`"strict":true`, `"enum":["lunch","dinner","any"]`, `"enum":["any","weekend"]`} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("schema missing %s", want)
		}
	}
}

func TestSuggestUserPromptMarksAnEmptyList(t *testing.T) {
	p := suggestUserPrompt(SuggestInput{Active: []string{"Борщ"}})
	if strings.Count(p, "(немає)") != 2 {
		t.Fatalf("an empty list must say so rather than run into the next heading:\n%s", p)
	}
}

func TestParseSuggestions(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		names []string
	}{
		{
			name:  "at most three",
			raw:   `{"dishes":[{"name":"А","meal":"any","days":"any","note":""},{"name":"Б","meal":"any","days":"any","note":""},{"name":"В","meal":"any","days":"any","note":""},{"name":"Г","meal":"any","days":"any","note":""}]}`,
			names: []string{"А", "Б", "В"},
		},
		{
			name:  "a name twice is one dish, an empty name none",
			raw:   `{"dishes":[{"name":" Солянка ","meal":"any","days":"any","note":""},{"name":"солянка","meal":"any","days":"any","note":""},{"name":"  ","meal":"any","days":"any","note":""}]}`,
			names: []string{"Солянка"},
		},
		{
			name:  "nothing new is not an error",
			raw:   `{"dishes":[]}`,
			names: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSuggestions([]byte(tt.raw))
			if err != nil {
				t.Fatalf("parseSuggestions: %v", err)
			}
			var names []string
			for _, s := range got {
				names = append(names, s.Name)
			}
			if strings.Join(names, ",") != strings.Join(tt.names, ",") {
				t.Fatalf("names = %v, want %v", names, tt.names)
			}
		})
	}
}

// Providers that ignore strict mode can answer outside the enum; the dish
// then falls back to "any" rather than failing the CHECK in the store.
func TestParseSuggestionsDefaultsUnknownMealAndDays(t *testing.T) {
	got, err := parseSuggestions([]byte(`{"dishes":[{"name":"Солянка","meal":"supper","days":"holiday","note":" суп "}]}`))
	if err != nil {
		t.Fatalf("parseSuggestions: %v", err)
	}
	if len(got) != 1 || got[0].Meal != "any" || got[0].Days != "any" || got[0].Note != "суп" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseSuggestionsRejectsBrokenJSON(t *testing.T) {
	for _, raw := range []string{"not json", `{"dishes":"Солянка"}`, `{"dishes":[{"name":`} {
		if _, err := parseSuggestions([]byte(raw)); err == nil {
			t.Errorf("parseSuggestions(%q) = nil error", raw)
		}
	}
}

func TestSuggestSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "k", "m").Suggest(context.Background(), SuggestInput{})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want the provider's message", err)
	}
}
