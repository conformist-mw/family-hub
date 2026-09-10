// Package cooking records what the family actually ate: it turns "this photo
// is deruny, we had it for lunch today" into the handful of Mealie writes that
// make the recipe database reflect reality.
//
// The rules that are policy rather than transport live here — how an entry is
// worded, when a real photograph is allowed to replace the stock image a
// recipe was seeded with, and which organizers a new recipe may claim.
package cooking

import (
	"context"
	"fmt"
	"strings"
	"time"

	"familyhub/internal/mealie"
)

// Slot is which meal a dish was eaten at. The family eats two meals a day, so
// there is no breakfast here; an empty slot is normal and means nobody said.
type Slot string

const (
	SlotLunch  Slot = "obid"
	SlotDinner Slot = "vecheria"
)

var slotTitles = map[Slot]string{
	SlotLunch:  "Обід",
	SlotDinner: "Вечеря",
}

// Title is the human label of a slot, used in the timeline entry and in the
// bot's confirmation card so the two cannot disagree.
func (s Slot) Title() string {
	if t, ok := slotTitles[s]; ok {
		return t
	}
	return "Приготовано"
}

type Service struct {
	c *mealie.Client
	// publicURL is the database as a phone can reach it, used to link a
	// recorded meal back to its recipe. The client speaks to the API on an
	// internal address, so the two are not the same string.
	publicURL string
	// servings is what a new recipe is created with. Three is this
	// household's default portion count; the bot cannot know better from a
	// photograph, and a wrong number is easier to notice than a missing one.
	servings float64
}

func NewService(c *mealie.Client, publicURL string) *Service {
	return &Service{c: c, publicURL: strings.TrimRight(publicURL, "/"), servings: 3}
}

// RecipeURL is where a person can open this recipe. Empty when the public
// address is unset or the group could not be read — a missing link is better
// than one that 404s for whoever taps it.
func (s *Service) RecipeURL(ctx context.Context, slug string) string {
	if s.publicURL == "" || slug == "" {
		return ""
	}
	group := s.c.GroupSlug(ctx)
	if group == "" {
		return ""
	}
	return s.publicURL + "/g/" + group + "/r/" + slug
}

// Catalogue is every recipe in the database. The recognizer needs all of it —
// it is the closed list of answers the model is allowed to give.
func (s *Service) Catalogue(ctx context.Context) ([]mealie.Recipe, error) {
	return s.c.Recipes(ctx)
}

// Categories and Tags are the organizer names a new recipe may claim. They
// are read fresh each time rather than cached: the taxonomy changes by hand,
// rarely, and a stale list would quietly file dishes under the wrong thing.
func (s *Service) Categories(ctx context.Context) ([]mealie.Organizer, error) {
	return s.c.Categories(ctx)
}

func (s *Service) Tags(ctx context.Context) ([]mealie.Organizer, error) {
	return s.c.Tags(ctx)
}

// Record is one meal to write down. A meal is a plate, not a dish: the goulash
// and the mash beside it are two recipes in this database and both were eaten,
// so both are recorded. Combining them into a "goulash with mash" recipe was
// rejected — it multiplies out to every pairing the kitchen ever makes, and
// each combination then accumulates its own history while the dishes
// themselves stop accumulating any.
type Record struct {
	Main  mealie.Recipe
	Sides []mealie.Recipe
	Slot  Slot
	At    time.Time // when it was cooked, local wall clock
	Cook  string    // who sent the photo, for the entry's byline
	Note  string    // what the model saw on the plate; may be empty
	Photo []byte    // may be nil: the text-only path records no picture
	Ext   string    // photo's file extension, without the dot
}

// Result says what actually happened, so the bot's final message can report it
// and a partial failure can name the step that did not run.
type Result struct {
	// AlreadyDone means this exact meal was already written down — the other
	// cook got there first, or the same card was confirmed twice. Nothing was
	// written and it is not an error.
	AlreadyDone bool

	EventID   string   // the main dish's entry
	MadeMain  bool     // the photo became the main dish's main image
	HadPhoto  bool     // that recipe already had a real photograph
	Sides     []string // names of the sides recorded alongside
	FailedAt  string
	RecipeURL string
}

// Do writes the meal down: a timeline entry, its photo, and the last-made
// date. The three are not a transaction — Mealie has no way to make them one —
// so the order is chosen for what a half-finished write leaves behind: the
// entry first, because an entry without its photo still reads correctly, and
// last-made last, because that is the one the meal planner reads and a missing
// one only means the dish stays eligible a while longer.
func (s *Service) Do(ctx context.Context, r Record) (Result, error) {
	res := Result{}

	// Two people eating different things at the same meal is normal here and
	// writes two independent records; two people recording the *same* dish is
	// the case worth catching, and "same recipe, same instant" catches it. A
	// failed check falls through to writing: a duplicate line somebody can
	// delete beats a meal that went unrecorded.
	if dup, err := s.c.HasEventAt(ctx, r.Main.ID, r.At); err == nil && dup {
		res.AlreadyDone = true
		res.RecipeURL = s.RecipeURL(ctx, r.Main.Slug)
		return res, nil
	}

	subject := r.Slot.Title()
	if r.Cook != "" {
		subject += " · " + r.Cook
	}
	eventID, err := s.c.AddTimelineEvent(ctx, r.Main.ID, subject, r.Note, r.At)
	if err != nil {
		res.FailedAt = "запис в історію"
		return res, err
	}
	res.EventID = eventID

	if len(r.Photo) > 0 {
		// Asked before the photo is attached: this very entry would otherwise
		// be the "already photographed" evidence that stops itself from
		// becoming the main image.
		had, err := s.c.HasPhotoEvent(ctx, r.Main.ID)
		if err != nil {
			// Not fatal. Erring towards "there was one" leaves the existing
			// image alone, which is the recoverable mistake of the two — the
			// bot still offers the button to promote it by hand.
			had = true
		}
		res.HadPhoto = had

		if err := s.c.SetEventImage(ctx, eventID, r.Photo, r.Ext); err != nil {
			res.FailedAt = "фото до запису"
			return res, err
		}
		if !had {
			if err := s.c.SetRecipeImage(ctx, r.Main.Slug, r.Photo, r.Ext); err != nil {
				res.FailedAt = "головне фото"
				return res, err
			}
			res.MadeMain = true
		}
	}

	if err := s.c.SetLastMade(ctx, r.Main.Slug, r.At); err != nil {
		res.FailedAt = "дата приготування"
		return res, err
	}

	// Sides get an entry and a date of their own, so the planner stops
	// believing the mash has not been eaten since whenever, but deliberately
	// no photograph: the picture is of a plate of goulash, and attaching it
	// here would both misrepresent the dish and mark it as "already
	// photographed", blocking a future photo that is actually of the mash.
	for _, side := range r.Sides {
		// A side already recorded at this instant is still part of the meal
		// and still named in the reply — it just is not written twice.
		if dup, err := s.c.HasEventAt(ctx, side.ID, r.At); err != nil || !dup {
			note := "Гарнір до: " + r.Main.Name
			if _, err := s.c.AddTimelineEvent(ctx, side.ID, subject, note, r.At); err != nil {
				res.FailedAt = "запис гарніру: " + side.Name
				return res, err
			}
			if err := s.c.SetLastMade(ctx, side.Slug, r.At); err != nil {
				res.FailedAt = "дата гарніру: " + side.Name
				return res, err
			}
		}
		res.Sides = append(res.Sides, side.Name)
	}

	res.RecipeURL = s.RecipeURL(ctx, r.Main.Slug)
	return res, nil
}

// PromotePhoto makes an already-recorded photo the recipe's main image, for
// the case where the recipe had a real photograph already and the cook wants
// this one instead.
func (s *Service) PromotePhoto(ctx context.Context, slug string, photo []byte, ext string) error {
	return s.c.SetRecipeImage(ctx, slug, photo, ext)
}

// NewRecipe is a dish the database has never heard of.
type NewRecipe struct {
	Name     string // in Ukrainian: the database's content language
	Category string // a category *name*, matched against the existing ones
	Tags     []string
}

// Create adds the recipe and files it, returning it ready to record against.
//
// It deliberately creates nothing but a name, a category, tags and (later) a
// photograph: no ingredients and no steps. An invented ingredient list would
// flow straight into the shopping list as fact, and how to cook a thing is
// something anyone can look up — what this database is for is which dishes
// exist and when they were last eaten.
//
// Organizers are resolved against the live lists and anything unrecognised is
// dropped rather than created, both because the taxonomy here is deliberate
// and because Mealie answers an unknown organizer id with a silent 200 that
// discards the entire request.
func (s *Service) Create(ctx context.Context, n NewRecipe) (mealie.Recipe, error) {
	name := strings.TrimSpace(n.Name)
	if name == "" {
		return mealie.Recipe{}, fmt.Errorf("recipe needs a name")
	}
	slug, err := s.c.CreateRecipe(ctx, name)
	if err != nil {
		return mealie.Recipe{}, err
	}

	cats, tags := s.resolve(ctx, n)
	if err := s.c.SetOrganizers(ctx, slug, cats, tags, s.servings); err != nil {
		// The recipe exists; only its filing failed. Report it rather than
		// unwinding — a recipe with no category is still the dish, and the
		// caller has the link to fix it by hand.
		return mealie.Recipe{Slug: slug, Name: name}, err
	}

	// Re-read to learn the uuid, which the timeline needs and creation does
	// not hand back.
	all, err := s.c.Recipes(ctx)
	if err != nil {
		return mealie.Recipe{Slug: slug, Name: name}, err
	}
	for _, r := range all {
		if r.Slug == slug {
			return r, nil
		}
	}
	return mealie.Recipe{Slug: slug, Name: name}, fmt.Errorf("created recipe %q not in catalogue", slug)
}

func (s *Service) resolve(ctx context.Context, n NewRecipe) (cats, tags []mealie.Organizer) {
	if known, err := s.c.Categories(ctx); err == nil {
		if o, ok := matchOrganizer(known, n.Category); ok {
			cats = append(cats, o)
		}
	}
	known, err := s.c.Tags(ctx)
	if err != nil {
		return cats, nil
	}
	for _, want := range n.Tags {
		if o, ok := matchOrganizer(known, want); ok {
			tags = append(tags, o)
		}
	}
	return cats, tags
}

// matchOrganizer finds a tag or category by name or slug, case-insensitively.
// The model is asked for names from a list it was given, but it answers in
// prose and sometimes hands back the slug or a different case.
func matchOrganizer(known []mealie.Organizer, want string) (mealie.Organizer, bool) {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return mealie.Organizer{}, false
	}
	for _, o := range known {
		if strings.ToLower(o.Name) == want || strings.ToLower(o.Slug) == want {
			return o, true
		}
	}
	return mealie.Organizer{}, false
}
