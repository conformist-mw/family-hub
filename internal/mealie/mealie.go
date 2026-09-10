// Package mealie is a thin client for the recipe database's HTTP API, holding
// only the calls the cooking log needs: read the catalogue, create a recipe,
// attach a photo, and record that something was made.
package mealie

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client talks to one Mealie instance as one API token's user.
type Client struct {
	base  string
	token string
	hc    *http.Client

	// The group slug is part of every recipe's web address and never changes
	// for a running instance, so it is fetched once and remembered.
	groupOnce sync.Once
	groupSlug string
}

func New(baseURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		hc:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Recipe is the identity of a dish. Both keys travel because the API is
// inconsistent about which one addresses a resource: the timeline takes the
// uuid, everything else takes the slug.
type Recipe struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Organizer is a tag or a category. Its ID must always come from the live
// list — see SetOrganizers for what an invented one does.
type Organizer struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Recipes returns the whole catalogue. There is no paging here on purpose: a
// household's recipe list is in the hundreds at most, and the caller needs all
// of it — it becomes the list of names the model chooses from.
func (c *Client) Recipes(ctx context.Context) ([]Recipe, error) {
	var out struct {
		Items []Recipe `json:"items"`
	}
	if err := c.getJSON(ctx, "/api/recipes?perPage=1000", &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// GroupSlug is the segment a recipe's web address is built around
// (/g/<group>/r/<slug>). It is read from the API rather than configured,
// because getting it wrong produces a link that 404s only for whoever taps
// it. An empty answer means "could not tell", and the caller drops the link.
func (c *Client) GroupSlug(ctx context.Context) string {
	c.groupOnce.Do(func() {
		var out struct {
			Slug string `json:"slug"`
		}
		if err := c.getJSON(ctx, "/api/groups/self", &out); err != nil {
			return
		}
		c.groupSlug = out.Slug
	})
	return c.groupSlug
}

func (c *Client) Tags(ctx context.Context) ([]Organizer, error) {
	return c.organizers(ctx, "/api/organizers/tags?perPage=500")
}

func (c *Client) Categories(ctx context.Context) ([]Organizer, error) {
	return c.organizers(ctx, "/api/organizers/categories?perPage=500")
}

func (c *Client) organizers(ctx context.Context, path string) ([]Organizer, error) {
	var out struct {
		Items []Organizer `json:"items"`
	}
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// CreateRecipe makes an empty recipe and returns its slug, which the API hands
// back as a bare JSON string rather than an object.
func (c *Client) CreateRecipe(ctx context.Context, name string) (string, error) {
	body, err := c.do(ctx, http.MethodPost, "/api/recipes", jsonBody(map[string]string{"name": name}))
	if err != nil {
		return "", err
	}
	var slug string
	if err := json.Unmarshal(body, &slug); err != nil {
		return "", fmt.Errorf("create recipe %q: unexpected response %.80s", name, body)
	}
	return slug, nil
}

// SetOrganizers files a recipe under its category and tags and sets the
// serving count.
//
// Every organizer must be one read back from Tags/Categories. Mealie validates
// the ids and, on finding one it does not know, answers 200 and silently drops
// the *whole* payload — the bogus category takes the good tags and the serving
// count down with it. So a caller that invents an id gets no error and no
// change, which is why the resolution against the live list happens before the
// call and never inside the request body.
func (c *Client) SetOrganizers(ctx context.Context, slug string, cats, tags []Organizer, servings float64) error {
	payload := map[string]any{
		"recipeCategory": nonNil(cats),
		"tags":           nonNil(tags),
	}
	if servings > 0 {
		payload["recipeServings"] = servings
	}
	_, err := c.do(ctx, http.MethodPatch, "/api/recipes/"+url.PathEscape(slug), jsonBody(payload))
	return err
}

// SetRecipeImage replaces the recipe's main picture. Mealie re-encodes whatever
// arrives to WebP, so the extension only has to be honest, not special.
func (c *Client) SetRecipeImage(ctx context.Context, slug string, img []byte, ext string) error {
	body, ctype, err := imageForm(img, ext)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPut, "/api/recipes/"+url.PathEscape(slug)+"/image", reqBody{r: body, ctype: ctype})
	return err
}

// AddTimelineEvent records one entry in a recipe's history and returns its id,
// which is needed to attach the photo in a second call.
//
// ts is sent as UTC: Mealie stores a naive timestamp verbatim and reports it
// back as Z, so handing it local wall-clock time would shift every entry by
// the offset.
func (c *Client) AddTimelineEvent(ctx context.Context, recipeID, subject, message string, ts time.Time) (string, error) {
	payload := map[string]any{
		"recipeId":  recipeID,
		"subject":   subject,
		"eventType": "info",
		"timestamp": ts.UTC().Format("2006-01-02T15:04:05"),
	}
	if message != "" {
		payload["eventMessage"] = message
	}
	body, err := c.do(ctx, http.MethodPost, "/api/recipes/timeline/events", jsonBody(payload))
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("timeline event: unexpected response %.80s", body)
	}
	return out.ID, nil
}

func (c *Client) SetEventImage(ctx context.Context, eventID string, img []byte, ext string) error {
	body, ctype, err := imageForm(img, ext)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPut, "/api/recipes/timeline/events/"+url.PathEscape(eventID)+"/image", reqBody{r: body, ctype: ctype})
	return err
}

// SetLastMade moves the "last cooked" date. This is the field the meal-plan
// rules filter on, so it — not the timeline entry beside it — is what stops a
// dish being suggested again next week.
func (c *Client) SetLastMade(ctx context.Context, slug string, ts time.Time) error {
	payload := map[string]string{"timestamp": ts.UTC().Format("2006-01-02T15:04:05")}
	_, err := c.do(ctx, http.MethodPatch, "/api/recipes/"+url.PathEscape(slug)+"/last-made", jsonBody(payload))
	return err
}

// HasPhotoEvent reports whether this recipe's history already holds an entry
// with a picture. That is the test for "someone has photographed this dish for
// real", which decides whether a new photo may take over the main image from
// whatever was pulled off the internet when the recipe was seeded. Mealie
// records no provenance for images, so its own timeline is the closest thing
// to that fact, and it keeps the answer out of this app's database.
func (c *Client) HasPhotoEvent(ctx context.Context, recipeID string) (bool, error) {
	filter := fmt.Sprintf(`recipeId=%q AND image="has image"`, recipeID)
	var out struct {
		Total int `json:"total"`
	}
	path := "/api/recipes/timeline/events?perPage=1&queryFilter=" + url.QueryEscape(filter)
	if err := c.getJSON(ctx, path, &out); err != nil {
		return false, err
	}
	return out.Total > 0, nil
}

// HasEventAt reports whether this recipe already has an entry at exactly this
// instant. Meals are pinned to a canonical hour, so "same recipe, same
// instant" is precisely "this meal is already written down" — which is what
// makes it safe for two people to photograph the same dinner.
func (c *Client) HasEventAt(ctx context.Context, recipeID string, ts time.Time) (bool, error) {
	filter := fmt.Sprintf(`recipeId=%q AND timestamp=%q`, recipeID, ts.UTC().Format("2006-01-02T15:04:05"))
	var out struct {
		Total int `json:"total"`
	}
	path := "/api/recipes/timeline/events?perPage=1&queryFilter=" + url.QueryEscape(filter)
	if err := c.getJSON(ctx, path, &out); err != nil {
		return false, err
	}
	return out.Total > 0, nil
}

func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	body, err := c.do(ctx, http.MethodGet, path, reqBody{})
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

type reqBody struct {
	r     io.Reader
	ctype string
}

func jsonBody(v any) reqBody {
	b, _ := json.Marshal(v)
	return reqBody{r: bytes.NewReader(b), ctype: "application/json"}
}

func (c *Client) do(ctx context.Context, method, path string, body reqBody) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body.r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body.ctype != "" {
		req.Header.Set("Content-Type", body.ctype)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: %s: %.200s", method, path, resp.Status, raw)
	}
	return raw, nil
}

func imageForm(img []byte, ext string) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("image", "photo."+ext)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(img); err != nil {
		return nil, "", err
	}
	// Mealie takes the extension as its own form field and does not look at
	// the filename, so an omitted one lands as a file it refuses to convert.
	if err := w.WriteField("extension", ext); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// nonNil keeps an empty list from marshalling as null, which Mealie reads as
// "no change" rather than "clear the field".
func nonNil(o []Organizer) []Organizer {
	if o == nil {
		return []Organizer{}
	}
	return o
}
