package mealie

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testServer answers each "METHOD /path" from replies and records the query
// of every request.
func testServer(t *testing.T, replies map[string]string) (*Client, *[]string) {
	t.Helper()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.Header.Get("Authorization") != "Bearer tkn" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reply, ok := replies[r.Method+" "+r.URL.Path]
		if !ok {
			reply = "{}"
		}
		if strings.HasPrefix(reply, "!") { // "!<body>" means fail
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(reply[1:]))
			return
		}
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "tkn"), &queries
}

func TestRecipes(t *testing.T) {
	c, queries := testServer(t, map[string]string{
		"GET /api/recipes": `{"items":[{"id":"u1","slug":"deruni","name":"Деруни"}]}`,
	})
	got, err := c.Recipes(context.Background())
	if err != nil {
		t.Fatalf("Recipes: %v", err)
	}
	// One page holding everything: the import needs the whole catalogue.
	if len(*queries) != 1 || (*queries)[0] != "perPage=1000" {
		t.Errorf("queries = %v, want one request for a single large page", *queries)
	}
	if len(got) != 1 || got[0].Slug != "deruni" || got[0].Name != "Деруни" || got[0].ID != "u1" {
		t.Fatalf("unexpected recipes: %+v", got)
	}
}

// The import reads the meal and the category off the listing itself, so
// both organizer lists must decode from it without a per-recipe call.
func TestRecipesCarryTagsAndCategories(t *testing.T) {
	c, _ := testServer(t, map[string]string{
		"GET /api/recipes": `{"items":[{"id":"u1","slug":"borshch","name":"Борщ",
			"tags":[{"id":"t1","slug":"obid","name":"обід"}],
			"recipeCategory":[{"id":"c1","slug":"supi","name":"Супи"}]}]}`,
	})
	got, err := c.Recipes(context.Background())
	if err != nil {
		t.Fatalf("Recipes: %v", err)
	}
	if len(got) != 1 || len(got[0].Tags) != 1 || got[0].Tags[0].Slug != "obid" ||
		len(got[0].RecipeCategory) != 1 || got[0].RecipeCategory[0].Name != "Супи" {
		t.Fatalf("organizers not decoded: %+v", got)
	}
}

func TestErrorCarriesStatusAndBody(t *testing.T) {
	c, _ := testServer(t, map[string]string{
		"GET /api/recipes": `!{"detail":"nope"}`,
	})
	_, err := c.Recipes(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unhelpful error: %v", err)
	}
}
