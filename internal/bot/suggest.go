package bot

import (
	"context"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/dish"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The weekly suggestions: the model pitches a few dishes the family does not
// cook yet, and each gets a row of answers — add, think about it, no.
//
// The dishes are written as proposed before the message goes out, so an
// ignored card needs no code: an unanswered suggestion is a "maybe", and it
// shows up in the morning menu marked 🆕 like one. Split like the menu:
// suggestView renders, proposeDishes and applySuggestTap work against the
// store, the handlers glue. The store holds the add and the no; the maybe,
// which writes nothing, is kept on the card itself.

const (
	suggAddUnique   = "sugg_add"
	suggMaybeUnique = "sugg_maybe"
	suggNoUnique    = "sugg_no"

	// suggestTimeout bounds the whole run, not just the HTTP call: the
	// recognizer's client already gives up after a minute, and this leaves the
	// writes and the send room behind it.
	suggestTimeout = 90 * time.Second

	// chosenPrefix marks the answer given on a card's button. For the maybe it
	// is also the only record of the answer, read back by suggestRefsFromMarkup.
	chosenPrefix = "✓ "
)

var suggUniques = []string{suggAddUnique, suggMaybeUnique, suggNoUnique}

// suggestDish is one dish of the card as it stands.
type suggestDish struct {
	model.Dish
	// maybe is "think about it" tapped. It means something only while the
	// dish is still proposed — the store's add or no outranks it.
	maybe bool
}

// suggestView renders the card. Every row starts with the dish's name, so the
// three rows of identical answers can be told apart; the full name and the
// model's pitch are in the text above.
func suggestView(dishes []suggestDish) (string, *tele.ReplyMarkup) {
	var sb strings.Builder
	sb.WriteString("💡 <b>Може, спробуємо нове?</b>\n")
	for _, d := range dishes {
		fmt.Fprintf(&sb, "\n<b>%s</b> · %s\n", html.EscapeString(d.Name), suggestWhen(d.Meal, d.Days))
		if d.Note != "" {
			sb.WriteString(html.EscapeString(d.Note) + "\n")
		}
		switch {
		case d.Status == model.DishActive:
			sb.WriteString("➕ Додано в меню\n")
		case d.Status == model.DishRejected:
			sb.WriteString("✖ Не пропонуватиму\n")
		case d.maybe:
			sb.WriteString("🤔 Подумаємо\n")
		}
	}

	mk := &tele.ReplyMarkup{}
	rows := make([]tele.Row, 0, len(dishes))
	for _, d := range dishes {
		id := strconv.FormatInt(d.ID, 10)
		mark := func(on bool, label string) string {
			if on {
				return chosenPrefix + label
			}
			return label
		}
		proposed := d.Status == model.DishProposed
		rows = append(rows, tele.Row{
			mk.Data(mark(d.Status == model.DishActive, "➕ "+shorten(d.Name, menuButtonName)), suggAddUnique, id),
			mk.Data(mark(proposed && d.maybe, "🤔 Подумаю"), suggMaybeUnique, id),
			mk.Data(mark(d.Status == model.DishRejected, "✖ Ні"), suggNoUnique, id),
		})
	}
	mk.Inline(rows...)
	return strings.TrimRight(sb.String(), "\n"), mk
}

// suggestWhen is the meal and days of a dish in words, for the card.
func suggestWhen(meal, days string) string {
	var when string
	switch meal {
	case model.MealLunch:
		when = "на обід"
	case model.MealDinner:
		when = "на вечерю"
	default:
		when = "на обід чи вечерю"
	}
	if days == model.DishDaysWeekend {
		when += ", на вихідні"
	}
	return when
}

// suggestRefs is what a card's keyboard remembers: the dishes in order, and
// which of them were answered "maybe".
type suggestRefs struct {
	ids   []int64
	maybe map[int64]bool
}

// suggestRefsFromMarkup reads back what suggestView wrote, from either shape a
// keyboard comes in (see callbackPayload); the app row is skipped.
func suggestRefsFromMarkup(m *tele.ReplyMarkup) suggestRefs {
	refs := suggestRefs{maybe: map[int64]bool{}}
	if m == nil {
		return refs
	}
	for _, row := range m.InlineKeyboard {
		for _, btn := range row {
			unique, payload, ok := callbackPayload(btn, suggUniques...)
			if !ok {
				continue
			}
			id, err := strconv.ParseInt(payload, 10, 64)
			if err != nil {
				continue
			}
			if !slices.Contains(refs.ids, id) {
				refs.ids = append(refs.ids, id)
			}
			if unique == suggMaybeUnique && strings.HasPrefix(btn.Text, chosenPrefix) {
				refs.maybe[id] = true
			}
		}
	}
	return refs
}

// filterSuggestions drops every suggestion whose name the catalogue already
// has, in any status, by the key the store dedupes on. The model is told what
// not to repeat, and it still does: a rejected dish coming back as "new" is
// exactly what the rejected list exists to prevent, so it is checked here
// rather than trusted there.
func filterSuggestions(sugg []dish.Suggestion, existing []model.Dish) []dish.Suggestion {
	known := make(map[string]bool, len(existing)+len(sugg))
	for _, d := range existing {
		known[store.NameKey(d.Name)] = true
	}
	var out []dish.Suggestion
	for _, s := range sugg {
		key := store.NameKey(s.Name)
		if key == "" || known[key] {
			continue
		}
		known[key] = true
		out = append(out, s)
	}
	return out
}

// suggestInput sorts the catalogue into the three lists the model is given.
func suggestInput(dishes []model.Dish) dish.SuggestInput {
	var in dish.SuggestInput
	for _, d := range dishes {
		switch d.Status {
		case model.DishActive:
			in.Active = append(in.Active, d.Name)
		case model.DishProposed:
			in.Proposed = append(in.Proposed, d.Name)
		case model.DishRejected:
			in.Rejected = append(in.Rejected, d.Name)
		}
	}
	return in
}

// proposeDishes writes the suggestions that survive the filter as proposed
// dishes and returns them as the card shows them. A name that turns out to
// exist after all — created between the read and the write — is dropped
// rather than offered as new.
func (b *Bot) proposeDishes(sugg []dish.Suggestion, existing []model.Dish) ([]suggestDish, error) {
	var out []suggestDish
	for _, s := range filterSuggestions(sugg, existing) {
		d, existed, err := b.store.CreateDish(model.Dish{
			Name: s.Name, Meal: s.Meal, Days: s.Days, Status: model.DishProposed, Note: s.Note,
		})
		if err != nil {
			return out, err
		}
		if existed {
			continue
		}
		out = append(out, suggestDish{Dish: d})
	}
	return out, nil
}

// applySuggestTap carries out one answer and returns the card to redraw.
// current is the tapped message's keyboard: it says which dishes the card
// holds and which were answered "maybe", that answer being nowhere else.
//
// The add and the no set the status whatever it was, so a changed mind is
// one more tap — with one exception: a dish the family has eaten since is its
// own, and a stale card's no does not reject it. The maybe writes nothing:
// the dish is proposed already.
func (b *Bot) applySuggestTap(unique, data string, current *tele.ReplyMarkup) ([]suggestDish, string, bool, error) {
	id, err := strconv.ParseInt(data, 10, 64)
	if err != nil || !slices.Contains(suggUniques, unique) {
		return nil, "Невірні дані", false, nil
	}
	d, err := b.store.Dish(id)
	if store.IsNotFound(err) {
		return nil, "Цієї страви вже немає", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}

	refs := suggestRefsFromMarkup(current)
	if !slices.Contains(refs.ids, id) {
		refs.ids = append(refs.ids, id)
	}

	var toast string
	switch unique {
	case suggAddUnique:
		if err := b.store.SetDishStatus(id, model.DishActive); err != nil {
			return nil, "", false, err
		}
		delete(refs.maybe, id)
		toast = fmt.Sprintf("%s — у меню", d.Name)
	case suggMaybeUnique:
		refs.maybe[id] = true
		toast = "Добре, подумаємо"
		if d.Status != model.DishProposed {
			toast = "Вже вирішено"
		}
	case suggNoUnique:
		eaten := false
		if d.Status == model.DishActive {
			if eaten, err = b.store.DishEaten(id); err != nil {
				return nil, "", false, err
			}
		}
		if eaten {
			toast = "Вже вирішено"
			break
		}
		if err := b.store.SetDishStatus(id, model.DishRejected); err != nil {
			return nil, "", false, err
		}
		delete(refs.maybe, id)
		toast = "Більше не пропонуватиму"
	}

	dishes, err := b.suggestDishesFrom(refs)
	if err != nil {
		return nil, "", false, err
	}
	return dishes, toast, true, nil
}

// suggestDishesFrom fills the card's ids from the store. A dish deleted since
// the card was sent drops out of the redraw.
func (b *Bot) suggestDishesFrom(refs suggestRefs) ([]suggestDish, error) {
	byID, err := b.dishesByID()
	if err != nil {
		return nil, err
	}
	out := make([]suggestDish, 0, len(refs.ids))
	for _, id := range refs.ids {
		if d, ok := byID[id]; ok {
			out = append(out, suggestDish{Dish: d, maybe: refs.maybe[id]})
		}
	}
	return out, nil
}

// sendDishSuggestions asks the model for new dishes and posts the card.
//
// The call runs in its own goroutine for the reason sendSchoolWeekReview's
// collect does: the model can take up to a minute, and time.Ticker drops the
// ticks RunDigests spends waiting — whatever else was due in them would be
// lost.
func (b *Bot) sendDishSuggestions(ctx context.Context, now time.Time) {
	// RunDigests only calls this when dishSuggestEnabled, which requires a
	// recognizer; the check stays because a nil one reached from that
	// goroutine would take the whole server down.
	if b.cfg.Dish == nil {
		return
	}
	// A run still waiting on the model would otherwise be joined by a second
	// one, and the group would get two cards of different dishes.
	if !b.suggestRunning.CompareAndSwap(false, true) {
		b.logger.Warn("bot: dish suggestions still running, skipping this one")
		return
	}
	go func() {
		defer b.suggestRunning.Store(false)

		ctx, cancel := context.WithTimeout(ctx, suggestTimeout)
		defer cancel()

		dishes := b.collectSuggestions(ctx, now)
		if len(dishes) == 0 {
			return
		}
		text, markup := suggestView(dishes)
		if _, err := b.sendToGroup(text, markup, tele.ModeHTML); err != nil {
			b.logger.Error("bot: send dish suggestions", "err", err)
		}
	}()
}

// collectSuggestions asks the model and writes what survives the filter as
// proposed, returning the card's dishes. A failed call, or nothing new to
// offer, is logged and comes back empty: there is no card to send.
func (b *Bot) collectSuggestions(ctx context.Context, now time.Time) []suggestDish {
	existing, err := b.store.Dishes()
	if err != nil {
		b.logger.Error("bot: dish suggestions catalogue", "err", err)
		return nil
	}
	sugg, err := b.cfg.Dish.Suggest(ctx, suggestInput(existing))
	if err != nil {
		b.logger.Error("bot: dish suggestions model", "err", err)
		return nil
	}
	dishes, err := b.proposeDishes(sugg, existing)
	if err != nil {
		// Whatever was written before the failure is proposed already and
		// will turn up in the menu; the card for it is only a nicety, so it
		// still goes out with what there is.
		b.logger.Error("bot: dish suggestions write", "err", err)
	}
	if len(dishes) == 0 {
		b.logger.Info("bot: no new dishes to suggest", "date", now.Format(time.DateOnly), "from_model", len(sugg))
	}
	return dishes
}

func (b *Bot) onSuggestAdd(c tele.Context) error   { return b.onSuggestTap(c, suggAddUnique) }
func (b *Bot) onSuggestMaybe(c tele.Context) error { return b.onSuggestTap(c, suggMaybeUnique) }
func (b *Bot) onSuggestNo(c tele.Context) error    { return b.onSuggestTap(c, suggNoUnique) }

func (b *Bot) onSuggestTap(c tele.Context, unique string) error {
	dishes, toast, redraw, err := b.applySuggestTap(unique, c.Data(), currentMarkup(c))
	if err != nil {
		b.logger.Error("bot: suggestion tap", "unique", unique, "data", c.Data(), "err", err)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не вдалося"})
		return nil
	}
	_ = c.Respond(&tele.CallbackResponse{Text: toast})
	if !redraw {
		return nil
	}
	text, markup := suggestView(dishes)
	return editIgnoringSame(c, text, b.withAppButton([]any{markup, tele.ModeHTML})...)
}
