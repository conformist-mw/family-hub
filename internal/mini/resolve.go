package mini

import (
	"context"
	"net/http"
	"strings"
	"time"

	"familyhub/internal/dish"
)

// A name typed into a picker is read by the same model that reads a photo of
// the plate or a caption, not stored as typed. Typed as typed, "морковь
// по-корейски" became a second dish beside "Морква по-корейськи", and a line
// naming three dishes became one dish with a long name. The model matches the
// catalogue, names a new dish in Ukrainian, and says when it is eaten.

var errNoModel = &apiError{Code: "no_model", Message: "розпізнавання страв вимкнене", status: http.StatusServiceUnavailable}

// maxResolveText bounds what is sent to the model; a dish line is a few words.
const maxResolveText = 200

type resolvedDish struct {
	// ID is the catalogue dish it matched, 0 for a new one.
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Meal and Days are what a new dish would be created with.
	Meal string `json:"meal"`
	Days string `json:"days"`
}

func (rt *Router) handleResolveDish(w http.ResponseWriter, r *http.Request) {
	if _, bad := rt.v.authenticate(r); bad != nil {
		rt.fail(w, bad)
		return
	}
	if rt.dish == nil {
		rt.fail(w, errNoModel)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &body); err != nil {
		rt.fail(w, errBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" || len([]rune(text)) > maxResolveText {
		rt.fail(w, errBadRequest)
		return
	}

	catalogue, _, err := rt.catalogue()
	if err != nil {
		rt.log.Error("mini: resolve catalogue", "err", err)
		rt.fail(w, errInternal)
		return
	}
	refs := make([]dish.DishRef, 0, len(catalogue))
	for _, d := range catalogue {
		refs = append(refs, dish.DishRef{ID: d.ID, Name: d.Name})
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	guess, err := rt.dish.Identify(ctx, dish.Input{
		Caption: text,
		Now:     rt.now().In(rt.loc),
		Dishes:  refs,
	})
	if err != nil {
		rt.log.Error("mini: resolve dish", "err", err)
		rt.fail(w, errInternal)
		return
	}
	out := make([]resolvedDish, 0, len(guess.Items))
	for _, it := range guess.Items {
		if it.Known() {
			out = append(out, resolvedDish{ID: it.Dish.ID, Name: it.Dish.Name})
			continue
		}
		out = append(out, resolvedDish{Name: it.Name, Meal: it.Meal, Days: it.Days})
	}
	rt.writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
