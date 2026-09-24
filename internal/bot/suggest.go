package bot

import (
	"context"
	"errors"
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
	id     int64
	name   string
	note   string
	meal   string
	days   string
	status string
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
		fmt.Fprintf(&sb, "\n<b>%s</b> · %s\n", html.EscapeString(d.name), suggestWhen(d.meal, d.days))
		if d.note != "" {
			sb.WriteString(html.EscapeString(d.note) + "\n")
		}
		switch {
		case d.status == model.DishActive:
			sb.WriteString("➕ Додано в меню\n")
		case d.status == model.DishRejected:
			sb.WriteString("✖ Не пропонуватиму\n")
		case d.maybe:
			sb.WriteString("🤔 Подумаємо\n")
		}
	}

	mk := &tele.ReplyMarkup{}
	rows := make([]tele.Row, 0, len(dishes))
	for _, d := range dishes {
		id := strconv.FormatInt(d.id, 10)
		mark := func(on bool, label string) string {
			if on {
				return chosenPrefix + label
			}
			return label
		}
		proposed := d.status == model.DishProposed
		rows = append(rows, tele.Row{
			mk.Data(mark(d.status == model.DishActive, "➕ "+shorten(d.name, menuButtonName)), suggAddUnique, id),
			mk.Data(mark(proposed && d.maybe, "🤔 Подумаю"), suggMaybeUnique, id),
			mk.Data(mark(d.status == model.DishRejected, "✖ Ні"), suggNoUnique, id),
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
// keyboard comes in (see choreCallbackPayload); the app row is skipped.
func suggestRefsFromMarkup(m *tele.ReplyMarkup) suggestRefs {
	refs := suggestRefs{maybe: map[int64]bool{}}
	if m == nil {
		return refs
	}
	for _, row := range m.InlineKeyboard {
		for _, btn := range row {
			unique, payload, ok := suggestCallbackPayload(btn)
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

func suggestCallbackPayload(btn tele.InlineButton) (unique, payload string, ok bool) {
	for _, u := range suggUniques {
		if btn.Unique == u {
			return u, btn.Data, true
		}
		if rest, found := strings.CutPrefix(btn.Data, "\f"+u+"|"); found {
			return u, rest, true
		}
	}
	return "", "", false
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
		out = append(out, suggestDish{id: d.ID, name: d.Name, note: d.Note, meal: d.Meal, days: d.Days, status: d.Status})
	}
	return out, nil
}

// applySuggestTap carries out one answer and returns the card to redraw.
// current is the tapped message's keyboard: it says which dishes the card
// holds and which were answered "maybe", that answer being nowhere else.
//
// The add and the no set the status whatever it was, so a changed mind is
// one more tap. The maybe writes nothing: the dish is proposed already.
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
	all, err := b.store.Dishes()
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]model.Dish, len(all))
	for _, d := range all {
		byID[d.ID] = d
	}
	out := make([]suggestDish, 0, len(refs.ids))
	for _, id := range refs.ids {
		d, ok := byID[id]
		if !ok {
			continue
		}
		out = append(out, suggestDish{
			id: d.ID, name: d.Name, note: d.Note, meal: d.Meal, days: d.Days,
			status: d.Status, maybe: refs.maybe[id],
		})
	}
	return out, nil
}

// sendDishSuggestions asks the model for new dishes and posts the card.
//
// The call runs in its own goroutine for the reason sendSchoolWeekReview's
// collect does: the model can take up to a minute, and time.Ticker drops the
// ticks RunDigests spends waiting — whatever else was due in them would be
// lost. A failed call or nothing new to offer is logged and sends nothing.
func (b *Bot) sendDishSuggestions(ctx context.Context, now time.Time) {
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

		existing, err := b.store.Dishes()
		if err != nil {
			b.logger.Error("bot: dish suggestions catalogue", "err", err)
			return
		}
		sugg, err := b.cfg.Dish.Suggest(ctx, suggestInput(existing))
		if err != nil {
			b.logger.Error("bot: dish suggestions model", "err", err)
			return
		}
		dishes, err := b.proposeDishes(sugg, existing)
		if err != nil {
			// Whatever was written before the failure is proposed already and
			// will turn up in the menu; the card for it is only a nicety.
			b.logger.Error("bot: dish suggestions write", "err", err)
		}
		if len(dishes) == 0 {
			b.logger.Info("bot: no new dishes to suggest", "date", now.Format(time.DateOnly), "from_model", len(sugg))
			return
		}
		text, markup := suggestView(dishes)
		if _, err := b.sendToGroup(text, markup, tele.ModeHTML); err != nil {
			b.logger.Error("bot: send dish suggestions", "err", err)
		}
	}()
}

func (b *Bot) onSuggestAdd(c tele.Context) error   { return b.onSuggestTap(c, suggAddUnique) }
func (b *Bot) onSuggestMaybe(c tele.Context) error { return b.onSuggestTap(c, suggMaybeUnique) }
func (b *Bot) onSuggestNo(c tele.Context) error    { return b.onSuggestTap(c, suggNoUnique) }

func (b *Bot) onSuggestTap(c tele.Context, unique string) error {
	var current *tele.ReplyMarkup
	if msg := c.Message(); msg != nil {
		current = msg.ReplyMarkup
	}
	dishes, toast, redraw, err := b.applySuggestTap(unique, c.Data(), current)
	if err != nil {
		b.logger.Error("bot: suggestion tap", "unique", unique, "data", c.Data(), "err", err)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не вдалося"})
		return nil
	}
	_ = c.Respond(&tele.CallbackResponse{Text: toast})
	if !redraw {
		return nil
	}
	// The same answer tapped twice redraws the same card, which Telegram
	// answers with an error; the tap did what it said, so it is not one.
	text, markup := suggestView(dishes)
	if err := c.Edit(text, b.withAppButton([]any{markup, tele.ModeHTML})...); err != nil &&
		!errors.Is(err, tele.ErrSameMessageContent) {
		return err
	}
	return nil
}
