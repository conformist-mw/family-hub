package cooking

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"familyhub/internal/mealie"
)

// fakeMealie answers the handful of endpoints the service calls and records
// the order it called them in — the order is the point in a sequence that
// cannot be a transaction.
type fakeMealie struct {
	calls    []string
	hasPhoto bool
	// hasEventAt answers the "is this meal already written down" probe, and
	// dupSides answers it for one side's recipe id only.
	hasEventAt bool
	dupSides   map[string]bool
	failOn     string // "METHOD /path" to answer 400 instead
}

func (f *fakeMealie) start(t *testing.T) *mealie.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		f.calls = append(f.calls, key)
		if key == f.failOn {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":"nope"}`))
			return
		}
		switch {
		case r.URL.Path == "/api/recipes/timeline/events" && r.Method == http.MethodGet:
			// The two probes differ by what they filter on: one asks whether a
			// photo exists, the other whether this exact meal does.
			filter := r.URL.Query().Get("queryFilter")
			total := 0
			switch {
			case strings.Contains(filter, "has image"):
				if f.hasPhoto {
					total = 1
				}
			case f.hasEventAt:
				total = 1
			default:
				for id := range f.dupSides {
					if strings.Contains(filter, id) {
						total = 1
					}
				}
			}
			_, _ = w.Write([]byte(`{"total":` + strconv.Itoa(total) + `}`))
		case r.URL.Path == "/api/recipes/timeline/events":
			_, _ = w.Write([]byte(`{"id":"ev1"}`))
		case r.URL.Path == "/api/organizers/categories":
			_, _ = w.Write([]byte(`{"items":[{"id":"c1","slug":"snidanki","name":"Сніданки"}]}`))
		case r.URL.Path == "/api/organizers/tags":
			_, _ = w.Write([]byte(`{"items":[{"id":"t1","slug":"shvidko","name":"швидко"},{"id":"t2","slug":"obid","name":"обід"}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return mealie.New(srv.URL, "tkn")
}

func record(photo bool) Record {
	r := Record{
		Main: mealie.Recipe{ID: "u1", Slug: "deruni", Name: "Деруни"},
		Slot: SlotLunch,
		At:   time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC),
		Cook: "Олег",
		Note: "деруни зі сметаною",
	}
	if photo {
		r.Photo, r.Ext = []byte("JPEG"), "jpg"
	}
	return r
}

// The first real photograph of a dish replaces whatever stock picture the
// recipe was seeded with.
func TestFirstPhotoBecomesMainImage(t *testing.T) {
	f := &fakeMealie{hasPhoto: false}
	res, err := NewService(f.start(t), "").Do(context.Background(), record(true))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !res.MadeMain || res.HadPhoto {
		t.Fatalf("res = %+v, want MadeMain", res)
	}
	want := []string{
		"GET /api/recipes/timeline/events", // already recorded?
		"POST /api/recipes/timeline/events",
		"GET /api/recipes/timeline/events", // photographed before?
		"PUT /api/recipes/timeline/events/ev1/image",
		"PUT /api/recipes/deruni/image",
		"PATCH /api/recipes/deruni/last-made",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
}

// A recipe that already has a real photograph keeps it; the new one is
// history, and promoting it is a separate, deliberate tap.
func TestLaterPhotoStaysInHistory(t *testing.T) {
	f := &fakeMealie{hasPhoto: true}
	res, err := NewService(f.start(t), "").Do(context.Background(), record(true))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if res.MadeMain || !res.HadPhoto {
		t.Fatalf("res = %+v, want the main image left alone", res)
	}
	for _, c := range f.calls {
		if c == "PUT /api/recipes/deruni/image" {
			t.Fatal("must not overwrite an existing real photo")
		}
	}
}

// The photo probe has to run before this entry's own picture is attached, or
// the entry would be the evidence that stops itself.
func TestProbeRunsBeforeAttachingPhoto(t *testing.T) {
	f := &fakeMealie{}
	if _, err := NewService(f.start(t), "").Do(context.Background(), record(true)); err != nil {
		t.Fatalf("Do: %v", err)
	}
	probe, attach := -1, -1
	for i, c := range f.calls {
		if c == "GET /api/recipes/timeline/events" {
			probe = i
		}
		if c == "PUT /api/recipes/timeline/events/ev1/image" {
			attach = i
		}
	}
	if probe < 0 || attach < 0 || probe > attach {
		t.Fatalf("probe at %d, attach at %d", probe, attach)
	}
}

func TestTextOnlyRecordSkipsImages(t *testing.T) {
	f := &fakeMealie{}
	res, err := NewService(f.start(t), "").Do(context.Background(), record(false))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if res.MadeMain {
		t.Fatal("no photo means no main image")
	}
	want := []string{
		"GET /api/recipes/timeline/events",
		"POST /api/recipes/timeline/events",
		"PATCH /api/recipes/deruni/last-made",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %v", f.calls)
	}
}

// A failed write must name the step, so the bot can say what did not happen
// instead of claiming success or going quiet.
func TestFailureNamesTheStep(t *testing.T) {
	tests := []struct {
		failOn string
		want   string
	}{
		{"POST /api/recipes/timeline/events", "запис в історію"},
		{"PUT /api/recipes/timeline/events/ev1/image", "фото до запису"},
		{"PUT /api/recipes/deruni/image", "головне фото"},
		{"PATCH /api/recipes/deruni/last-made", "дата приготування"},
	}
	for _, tt := range tests {
		t.Run(tt.failOn, func(t *testing.T) {
			f := &fakeMealie{failOn: tt.failOn}
			res, err := NewService(f.start(t), "").Do(context.Background(), record(true))
			if err == nil {
				t.Fatal("want an error")
			}
			if res.FailedAt != tt.want {
				t.Fatalf("FailedAt = %q, want %q", res.FailedAt, tt.want)
			}
		})
	}
}

// A probe that errors must not cost the whole recording: it degrades to
// "leave the existing image alone", the recoverable half of the choice.
func TestProbeFailureKeepsRecording(t *testing.T) {
	f := &fakeMealie{failOn: "GET /api/recipes/timeline/events"}
	res, err := NewService(f.start(t), "").Do(context.Background(), record(true))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if res.MadeMain {
		t.Fatal("an unknown history must not promote the photo")
	}
}

func TestSubjectCarriesSlotAndCook(t *testing.T) {
	var subject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/recipes/timeline/events" && r.Method == http.MethodPost {
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			subject, _ = in["subject"].(string)
			_, _ = w.Write([]byte(`{"id":"ev1"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	rec := record(false)
	rec.Slot = SlotDinner
	if _, err := NewService(mealie.New(srv.URL, "t"), "").Do(context.Background(), rec); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if subject != "Вечеря · Олег" {
		t.Fatalf("subject = %q", subject)
	}
}

func TestCreateKeepsKnownOrganizersAndDropsInvented(t *testing.T) {
	var patched struct {
		Cats     []mealie.Organizer `json:"recipeCategory"`
		Tags     []mealie.Organizer `json:"tags"`
		Servings float64            `json:"recipeServings"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/recipes/sirniki":
			_ = json.NewDecoder(r.Body).Decode(&patched)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipes":
			_, _ = w.Write([]byte(`"sirniki"`))
		case r.URL.Path == "/api/recipes":
			_, _ = w.Write([]byte(`{"items":[{"id":"u9","slug":"sirniki","name":"Сирники"}]}`))
		case r.URL.Path == "/api/organizers/categories":
			_, _ = w.Write([]byte(`{"items":[{"id":"c1","slug":"snidanki","name":"Сніданки"}]}`))
		case r.URL.Path == "/api/organizers/tags":
			_, _ = w.Write([]byte(`{"items":[{"id":"t1","slug":"shvidko","name":"швидко"}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	rec, err := NewService(mealie.New(srv.URL, "tkn"), "").Create(context.Background(), NewRecipe{
		Name:     "Сирники",
		Category: "Сніданки",
		// "вечеря" is not in this household's tag list. An unknown organizer
		// id makes Mealie answer 200 and discard the entire payload, taking
		// the good tag and the serving count with it, so it is dropped here.
		Tags: []string{"швидко", "вечеря"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.Slug != "sirniki" || rec.ID != "u9" {
		t.Fatalf("recipe = %+v, want the uuid read back", rec)
	}
	if len(patched.Cats) != 1 || patched.Cats[0].ID != "c1" {
		t.Fatalf("category = %+v", patched.Cats)
	}
	if len(patched.Tags) != 1 || patched.Tags[0].ID != "t1" {
		t.Fatalf("tags = %+v, want only the known one", patched.Tags)
	}
	if patched.Servings != 3 {
		t.Fatalf("servings = %v, want the household default", patched.Servings)
	}
}

func TestCreateRefusesEmptyName(t *testing.T) {
	f := &fakeMealie{}
	if _, err := NewService(f.start(t), "").Create(context.Background(), NewRecipe{Name: "  "}); err == nil {
		t.Fatal("want an error")
	}
	if len(f.calls) != 0 {
		t.Fatalf("nothing should have been called: %v", f.calls)
	}
}

func TestSlotTitle(t *testing.T) {
	for slot, want := range map[Slot]string{
		SlotLunch:  "Обід",
		SlotDinner: "Вечеря",
		Slot(""):   "Приготовано",
	} {
		if got := slot.Title(); got != want {
			t.Errorf("%q.Title() = %q, want %q", slot, got, want)
		}
	}
}

// A side is a dish too: it gets its own entry and its own last-made date, or
// the planner keeps believing nobody has eaten mash since whenever.
func TestSidesGetEntryAndDateButNoPhoto(t *testing.T) {
	f := &fakeMealie{}
	r := record(true)
	r.Sides = []mealie.Recipe{{ID: "u2", Slug: "piure-kartopliane", Name: "Пюре картопляне"}}

	res, err := NewService(f.start(t), "").Do(context.Background(), r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(res.Sides) != 1 || res.Sides[0] != "Пюре картопляне" {
		t.Fatalf("res.Sides = %v", res.Sides)
	}

	var sawEntry, sawDate bool
	for _, c := range f.calls {
		switch c {
		case "PATCH /api/recipes/piure-kartopliane/last-made":
			sawDate = true
		case "POST /api/recipes/timeline/events":
			sawEntry = true // both dishes post here; the count is checked below
		case "PUT /api/recipes/piure-kartopliane/image":
			t.Fatal("a photo of a plate of goulash must not become the mash's picture")
		}
	}
	if !sawEntry || !sawDate {
		t.Fatalf("side not recorded: %v", f.calls)
	}
	var entries int
	for _, c := range f.calls {
		if c == "POST /api/recipes/timeline/events" {
			entries++
		}
	}
	if entries != 2 {
		t.Fatalf("want one entry per dish, got %d", entries)
	}
}

// Attaching the plate photo to a side would also mark that recipe as
// "already photographed", blocking a future picture that is really of it.
func TestSideStaysUnphotographed(t *testing.T) {
	f := &fakeMealie{}
	r := record(true)
	r.Sides = []mealie.Recipe{{ID: "u2", Slug: "grechka", Name: "Гречка"}}
	if _, err := NewService(f.start(t), "").Do(context.Background(), r); err != nil {
		t.Fatalf("Do: %v", err)
	}
	var images int
	for _, c := range f.calls {
		if strings.HasSuffix(c, "/image") {
			images++
		}
	}
	// The event image and the main dish's picture — and nothing for the side.
	if images != 2 {
		t.Fatalf("image writes = %d, want 2 (%v)", images, f.calls)
	}
}

func TestSideFailureNamesTheDish(t *testing.T) {
	f := &fakeMealie{failOn: "PATCH /api/recipes/grechka/last-made"}
	r := record(false)
	r.Sides = []mealie.Recipe{{ID: "u2", Slug: "grechka", Name: "Гречка"}}

	res, err := NewService(f.start(t), "").Do(context.Background(), r)
	if err == nil {
		t.Fatal("want an error")
	}
	if res.FailedAt != "дата гарніру: Гречка" {
		t.Fatalf("FailedAt = %q", res.FailedAt)
	}
}

// The web address carries the group, which is read from the API rather than
// assumed — a wrong guess makes a link that only 404s for whoever taps it.
func TestRecipeURLUsesGroupFromAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/groups/self" {
			_, _ = w.Write([]byte(`{"slug":"home"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	svc := NewService(mealie.New(srv.URL, "t"), "https://mealie.example.com/")
	got := svc.RecipeURL(context.Background(), "guliash")
	if got != "https://mealie.example.com/g/home/r/guliash" {
		t.Fatalf("url = %q", got)
	}
}

func TestRecipeURLEmptyWithoutPublicAddress(t *testing.T) {
	f := &fakeMealie{}
	if got := NewService(f.start(t), "").RecipeURL(context.Background(), "guliash"); got != "" {
		t.Fatalf("url = %q, want empty", got)
	}
}

// Two people photographing the same dinner must not produce two records of
// it. Different dishes at the same meal are a different matter entirely and
// are written independently — that case is covered by the sides tests.
func TestSameMealRecordedTwiceIsANoOp(t *testing.T) {
	f := &fakeMealie{hasEventAt: true}
	res, err := NewService(f.start(t), "").Do(context.Background(), record(true))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !res.AlreadyDone {
		t.Fatal("want AlreadyDone")
	}
	for _, c := range f.calls {
		if c != "GET /api/recipes/timeline/events" {
			t.Fatalf("nothing should have been written, got %v", f.calls)
		}
	}
}

// A side already recorded by the other cook is still named in the reply — it
// was on the plate — but is not written a second time.
func TestDuplicateSideIsReportedButNotRewritten(t *testing.T) {
	f := &fakeMealie{dupSides: map[string]bool{"u2": true}}
	r := record(false)
	r.Sides = []mealie.Recipe{{ID: "u2", Slug: "grechka", Name: "Гречка"}}

	res, err := NewService(f.start(t), "").Do(context.Background(), r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(res.Sides) != 1 || res.Sides[0] != "Гречка" {
		t.Fatalf("res.Sides = %v", res.Sides)
	}
	for _, c := range f.calls {
		if c == "PATCH /api/recipes/grechka/last-made" {
			t.Fatal("side was already recorded; it must not be written again")
		}
	}
}

// A check that errors must not swallow the meal: writing a duplicate line
// somebody can delete beats losing the record.
func TestFailedDuplicateCheckStillRecords(t *testing.T) {
	f := &fakeMealie{failOn: "GET /api/recipes/timeline/events"}
	res, err := NewService(f.start(t), "").Do(context.Background(), record(false))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if res.AlreadyDone {
		t.Fatal("an unanswerable check must not read as 'already done'")
	}
	if res.EventID == "" {
		t.Fatal("the meal should still have been written")
	}
}
