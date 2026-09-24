// Package mealie is a thin client for the recipe database's HTTP API, holding
// only the one call the one-off import (cmd/import-mealie) needs: read the
// catalogue. It goes, with the import, once Mealie itself is retired.
package mealie

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to one Mealie instance as one API token's user.
type Client struct {
	base  string
	token string
	hc    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		hc:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Recipe is one dish of the catalogue. Tags and RecipeCategory come with
// every catalogue listing; the import reads the meal and the weekend flag off
// them.
type Recipe struct {
	ID             string      `json:"id"`
	Slug           string      `json:"slug"`
	Name           string      `json:"name"`
	Tags           []Organizer `json:"tags,omitempty"`
	RecipeCategory []Organizer `json:"recipeCategory,omitempty"`
}

// Organizer is a tag or a category.
type Organizer struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Recipes returns the whole catalogue. There is no paging here on purpose: a
// household's recipe list is in the hundreds at most, and the import needs all
// of it.
func (c *Client) Recipes(ctx context.Context) ([]Recipe, error) {
	var out struct {
		Items []Recipe `json:"items"`
	}
	if err := c.getJSON(ctx, "/api/recipes?perPage=1000", &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %s: %.200s", path, resp.Status, raw)
	}
	return json.Unmarshal(raw, dst)
}
