package mini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"familyhub/internal/dish"
	"familyhub/internal/model"
)

// A line typed in Russian, naming two dishes: one the catalogue has under its
// Ukrainian name, one it does not. The model's answer is canned; what is
// checked is that the screen gets the catalogue dish by id and the new one
// named and timed the way the model said.
func TestResolveReadsATypedLineThroughTheModel(t *testing.T) {
	st := testStore(t)
	carrot := seedDish(t, st, "Морква по-корейськи", model.DishActive)

	var prompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		prompt = string(body.Messages[1].Content)
		answer := `{"items":[` +
			`{"name":"Морква по-корейськи","id":` + strconv.FormatInt(carrot.ID, 10) + `,"confidence":"high","alternatives":[],"meal":"any","days":"any"},` +
			`{"name":"Шаурма","id":null,"confidence":"high","alternatives":[],"meal":"dinner","days":"weekend"}` +
			`],"note":"","slot":"","date":""}`
		raw, _ := json.Marshal(answer)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + string(raw) + `}}]}`))
	}))
	defer srv.Close()

	h, err := NewRouter(st, discardLogger(), Config{
		BotToken: testToken, AllowedUsers: []int64{42}, DevUser: 42,
		Loc: time.UTC, Now: func() time.Time { return testNow },
		Dish: dish.New(srv.URL, "k", "m"),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/dishes/resolve", `{"text":"морковь по-корейски и шаурма"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if !strings.Contains(prompt, "морковь по-корейски") || !strings.Contains(prompt, "Морква по-корейськи") {
		t.Errorf("prompt lacks the line or the catalogue: %s", prompt)
	}
	var got struct {
		Items []resolvedDish `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []resolvedDish{
		{ID: carrot.ID, Name: "Морква по-корейськи"},
		{Name: "Шаурма", Meal: "dinner", Days: "weekend"},
	}
	if len(got.Items) != 2 || got.Items[0] != want[0] || got.Items[1] != want[1] {
		t.Errorf("items = %+v, want %+v", got.Items, want)
	}
}

// Without a model the screen is told so, and adds the name as typed.
func TestResolveWithoutAModel(t *testing.T) {
	h := testRouter(t, testStore(t), []int64{42}, 42)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/mini/api/dishes/resolve", `{"text":"борщ"}`))
	assertAPIError(t, rec, http.StatusServiceUnavailable, "no_model")
}
