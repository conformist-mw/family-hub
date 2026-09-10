package mealie

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capture struct {
	method string
	path   string
	query  string
	body   []byte
	ctype  string
}

// testServer records every request and answers each path from replies.
func testServer(t *testing.T, replies map[string]string) (*Client, *[]capture) {
	t.Helper()
	var got []capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, capture{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			body: body, ctype: r.Header.Get("Content-Type"),
		})
		if r.Header.Get("Authorization") != "Bearer tkn" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reply, ok := replies[r.Method+" "+r.URL.Path]
		if !ok {
			reply = "{}"
		}
		if strings.HasPrefix(reply, "!") { // "!<status>" means fail
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(reply[1:]))
			return
		}
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "tkn"), &got
}

func TestRecipes(t *testing.T) {
	c, _ := testServer(t, map[string]string{
		"GET /api/recipes": `{"items":[{"id":"u1","slug":"deruni","name":"Деруни"}]}`,
	})
	got, err := c.Recipes(context.Background())
	if err != nil {
		t.Fatalf("Recipes: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "deruni" || got[0].Name != "Деруни" || got[0].ID != "u1" {
		t.Fatalf("unexpected recipes: %+v", got)
	}
}

// The API answers a creation with a bare JSON string, not an object.
func TestCreateRecipeReturnsBareSlug(t *testing.T) {
	c, reqs := testServer(t, map[string]string{"POST /api/recipes": `"sirniki"`})
	slug, err := c.CreateRecipe(context.Background(), "Сирники")
	if err != nil {
		t.Fatalf("CreateRecipe: %v", err)
	}
	if slug != "sirniki" {
		t.Fatalf("slug = %q, want sirniki", slug)
	}
	var sent map[string]string
	if err := json.Unmarshal((*reqs)[0].body, &sent); err != nil {
		t.Fatalf("body: %v", err)
	}
	if sent["name"] != "Сирники" {
		t.Fatalf("sent name = %q", sent["name"])
	}
}

func TestSetOrganizersSendsEmptyListsNotNull(t *testing.T) {
	c, reqs := testServer(t, nil)
	if err := c.SetOrganizers(context.Background(), "deruni", nil, nil, 0); err != nil {
		t.Fatalf("SetOrganizers: %v", err)
	}
	body := string((*reqs)[0].body)
	if !strings.Contains(body, `"tags":[]`) || !strings.Contains(body, `"recipeCategory":[]`) {
		t.Fatalf("empty lists must marshal as [], got %s", body)
	}
	// A zero serving count is "unset", not "zero portions".
	if strings.Contains(body, "recipeServings") {
		t.Fatalf("servings must be omitted when zero: %s", body)
	}
}

func TestSetOrganizersPayload(t *testing.T) {
	c, reqs := testServer(t, nil)
	cats := []Organizer{{ID: "c1", Slug: "osnovni-stravi", Name: "Основні страви"}}
	tags := []Organizer{{ID: "t1", Slug: "obid", Name: "обід"}}
	if err := c.SetOrganizers(context.Background(), "deruni", cats, tags, 3); err != nil {
		t.Fatalf("SetOrganizers: %v", err)
	}
	r := (*reqs)[0]
	if r.method != http.MethodPatch || r.path != "/api/recipes/deruni" {
		t.Fatalf("wrong request: %s %s", r.method, r.path)
	}
	var sent struct {
		Cats     []Organizer `json:"recipeCategory"`
		Tags     []Organizer `json:"tags"`
		Servings float64     `json:"recipeServings"`
	}
	if err := json.Unmarshal(r.body, &sent); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(sent.Cats) != 1 || sent.Cats[0].ID != "c1" || len(sent.Tags) != 1 || sent.Tags[0].ID != "t1" {
		t.Fatalf("organizers not sent: %+v", sent)
	}
	if sent.Servings != 3 {
		t.Fatalf("servings = %v", sent.Servings)
	}
}

// Local wall-clock time must reach the API as UTC, or every entry lands
// shifted by the container's offset.
func TestTimelineEventSendsUTC(t *testing.T) {
	c, reqs := testServer(t, map[string]string{
		"POST /api/recipes/timeline/events": `{"id":"ev1"}`,
	})
	kyiv := time.FixedZone("EEST", 3*3600)
	at := time.Date(2026, 9, 9, 13, 0, 0, 0, kyiv)

	id, err := c.AddTimelineEvent(context.Background(), "u1", "Обід · Олег", "деруни", at)
	if err != nil {
		t.Fatalf("AddTimelineEvent: %v", err)
	}
	if id != "ev1" {
		t.Fatalf("id = %q", id)
	}
	var sent map[string]any
	if err := json.Unmarshal((*reqs)[0].body, &sent); err != nil {
		t.Fatalf("body: %v", err)
	}
	if sent["timestamp"] != "2026-09-09T10:00:00" {
		t.Fatalf("timestamp = %v, want 10:00:00 UTC", sent["timestamp"])
	}
	if sent["eventType"] != "info" || sent["eventMessage"] != "деруни" {
		t.Fatalf("unexpected payload: %+v", sent)
	}
}

func TestTimelineEventOmitsEmptyMessage(t *testing.T) {
	c, reqs := testServer(t, map[string]string{
		"POST /api/recipes/timeline/events": `{"id":"ev1"}`,
	})
	if _, err := c.AddTimelineEvent(context.Background(), "u1", "Обід", "", time.Now()); err != nil {
		t.Fatalf("AddTimelineEvent: %v", err)
	}
	if strings.Contains(string((*reqs)[0].body), "eventMessage") {
		t.Fatalf("empty message must be omitted: %s", (*reqs)[0].body)
	}
}

func TestSetRecipeImageMultipart(t *testing.T) {
	c, reqs := testServer(t, nil)
	if err := c.SetRecipeImage(context.Background(), "deruni", []byte("JPEGDATA"), "jpg"); err != nil {
		t.Fatalf("SetRecipeImage: %v", err)
	}
	r := (*reqs)[0]
	if r.method != http.MethodPut || r.path != "/api/recipes/deruni/image" {
		t.Fatalf("wrong request: %s %s", r.method, r.path)
	}
	fields, files := parseMultipart(t, r)
	// Mealie reads the extension from its own form field, not the filename.
	if fields["extension"] != "jpg" {
		t.Fatalf("extension field = %q", fields["extension"])
	}
	if files["image"] != "JPEGDATA" {
		t.Fatalf("image part = %q", files["image"])
	}
}

func TestHasPhotoEvent(t *testing.T) {
	c, reqs := testServer(t, map[string]string{
		"GET /api/recipes/timeline/events": `{"total":2}`,
	})
	has, err := c.HasPhotoEvent(context.Background(), "u1")
	if err != nil {
		t.Fatalf("HasPhotoEvent: %v", err)
	}
	if !has {
		t.Fatal("total 2 must read as 'has a photo'")
	}
	q := (*reqs)[0].query
	for _, want := range []string{`recipeId%3D%22u1%22`, `AND+image%3D%22has+image%22`} {
		if !strings.Contains(q, want) {
			t.Fatalf("query %q missing %q", q, want)
		}
	}
}

func TestErrorCarriesStatusAndBody(t *testing.T) {
	c, _ := testServer(t, map[string]string{
		"PATCH /api/recipes/deruni/last-made": `!{"detail":"nope"}`,
	})
	err := c.SetLastMade(context.Background(), "deruni", time.Now())
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func parseMultipart(t *testing.T, r capture) (fields, files map[string]string) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.ctype)
	if err != nil {
		t.Fatalf("content type %q: %v", r.ctype, err)
	}
	mr := multipart.NewReader(strings.NewReader(string(r.body)), params["boundary"])
	fields, files = map[string]string{}, map[string]string{}
	for {
		part, err := mr.NextPart()
		if err != nil {
			return fields, files
		}
		data, _ := io.ReadAll(part)
		if part.FileName() != "" {
			files[part.FormName()] = string(data)
		} else {
			fields[part.FormName()] = string(data)
		}
	}
}
