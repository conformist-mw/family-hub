package bot

import (
	"errors"
	"fmt"
	"html"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/menu"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The morning menu: a few dishes for lunch and for dinner, yesterday's pot to
// finish, and a 🔀 per meal for when nothing on offer appeals. A tap plans the
// meal; the evening check and the photo of the plate say what was eaten.
//
// The flow is split three ways so it can be tested without a Telegram
// context: menuView renders a state, buildMenu and applyMenuTap compute the
// state against the store, and the handlers only glue the two to Telegram.
//
// Nothing about the message is kept in memory. Which dishes it currently
// offers is read back from its own keyboard, what was chosen from the meals
// table, so the buttons keep working across a restart or a deploy.

const (
	menuPickUnique = "menu_pick"
	menuShufUnique = "menu_shuf"
	menuLeftUnique = "menu_left"

	// menuOptions is how many dishes a meal offers: enough to choose from,
	// few enough to fit one row of buttons on a phone.
	menuOptions = 3
	// menuButtonName keeps three names side by side readable; the full names
	// are in the text above.
	menuButtonName = 14
)

var menuMeals = []menu.Meal{menu.Lunch, menu.Dinner}

// menuDish is a dish as the menu shows it.
type menuDish struct {
	id       int64
	name     string
	proposed bool
}

// menuState is one day's menu as it stands.
type menuState struct {
	date      string
	leftovers []menuDish
	offered   map[menu.Meal][]menuDish
	// chosen is every row of the day in the meals table — the morning's plan
	// and anything already reported eaten.
	chosen []model.MealEntry
}

func (s menuState) isChosen(meal menu.Meal, dishID int64) bool {
	for _, m := range s.chosen {
		if m.Meal == string(meal) && m.DishID == dishID {
			return true
		}
	}
	return false
}

// menuRefs is what a menu message's keyboard remembers: the day, and the dish
// ids on each row.
type menuRefs struct {
	date      string
	leftovers []int64
	offered   map[menu.Meal][]int64
}

// menuView renders the message. It does not add the app button: that is
// added on the way out by sendToGroup and the redraw, so the keyboard read
// back here stays only the menu's own.
func menuView(s menuState) (string, *tele.ReplyMarkup) {
	var sb strings.Builder
	sb.WriteString("🍽 <b>Що приготувати сьогодні</b>\n")
	if len(s.leftovers) > 0 || len(s.offered[menu.Lunch]) > 0 || len(s.offered[menu.Dinner]) > 0 {
		sb.WriteString("\n")
	}
	if len(s.leftovers) > 0 {
		names := make([]string, 0, len(s.leftovers))
		for _, d := range s.leftovers {
			names = append(names, html.EscapeString(d.name))
		}
		fmt.Fprintf(&sb, "Доїдаємо: %s\n", strings.Join(names, " · "))
	}
	for _, meal := range menuMeals {
		dishes := s.offered[meal]
		if len(dishes) == 0 {
			continue
		}
		names := make([]string, 0, len(dishes))
		for _, d := range dishes {
			names = append(names, newMark(d)+html.EscapeString(d.name))
		}
		fmt.Fprintf(&sb, "%s: %s\n", meal.Title(), strings.Join(names, " · "))
	}
	if len(s.chosen) > 0 {
		sb.WriteString("\n")
		for _, m := range s.chosen {
			fmt.Fprintf(&sb, "✅ %s: %s", menu.Meal(m.Meal).Title(), html.EscapeString(m.Dish))
			if m.Leftover {
				sb.WriteString(" (доїдаємо)")
			}
			if m.Who != "" {
				fmt.Fprintf(&sb, " — %s", html.EscapeString(m.Who))
			}
			sb.WriteString("\n")
		}
	}

	mk := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, d := range s.leftovers {
		var row tele.Row
		for _, meal := range menuMeals {
			label := fmt.Sprintf("↩ %s · %s", shorten(d.name, menuButtonName), strings.ToLower(meal.Title()))
			row = append(row, mk.Data(chosenMark(s, meal, d.id)+label,
				menuLeftUnique, s.date, string(meal), strconv.FormatInt(d.id, 10)))
		}
		rows = append(rows, row)
	}
	for _, meal := range menuMeals {
		dishes := s.offered[meal]
		if len(dishes) == 0 {
			continue
		}
		var row tele.Row
		for _, d := range dishes {
			label := chosenMark(s, meal, d.id) + newMark(d) + shorten(d.name, menuButtonName)
			row = append(row, mk.Data(label, menuPickUnique, s.date, string(meal), strconv.FormatInt(d.id, 10)))
		}
		rows = append(rows, row,
			mk.Row(mk.Data("🔀 "+strings.ToLower(meal.Title()), menuShufUnique, s.date, string(meal))))
	}
	mk.Inline(rows...)
	return strings.TrimRight(sb.String(), "\n"), mk
}

func chosenMark(s menuState, meal menu.Meal, dishID int64) string {
	if s.isChosen(meal, dishID) {
		return "✅ "
	}
	return ""
}

func newMark(d menuDish) string {
	if d.proposed {
		return "🆕 "
	}
	return ""
}

// menuRefsFromMarkup reads back what menuView wrote, from either shape a
// keyboard comes in (see choreCallbackPayload). Buttons that are not the
// menu's — the app row — are skipped.
func menuRefsFromMarkup(m *tele.ReplyMarkup) menuRefs {
	refs := menuRefs{offered: map[menu.Meal][]int64{}}
	if m == nil {
		return refs
	}
	for _, row := range m.InlineKeyboard {
		for _, btn := range row {
			unique, payload, ok := menuCallbackPayload(btn)
			if !ok {
				continue
			}
			parts := strings.Split(payload, "|")
			if refs.date == "" && len(parts) >= 2 {
				refs.date = parts[0]
			}
			if unique == menuShufUnique || len(parts) != 3 || !model.ValidMeal(parts[1]) {
				continue
			}
			id, err := strconv.ParseInt(parts[2], 10, 64)
			if err != nil {
				continue
			}
			switch unique {
			case menuLeftUnique:
				// One leftover owns a row of two buttons, one per meal.
				if !slices.Contains(refs.leftovers, id) {
					refs.leftovers = append(refs.leftovers, id)
				}
			case menuPickUnique:
				meal := menu.Meal(parts[1])
				refs.offered[meal] = append(refs.offered[meal], id)
			}
		}
	}
	return refs
}

func menuCallbackPayload(btn tele.InlineButton) (unique, payload string, ok bool) {
	for _, u := range []string{menuPickUnique, menuShufUnique, menuLeftUnique} {
		if btn.Unique == u {
			return u, btn.Data, true
		}
		if rest, found := strings.CutPrefix(btn.Data, "\f"+u+"|"); found {
			return u, rest, true
		}
	}
	return "", "", false
}

// buildMenu decides today's menu. ok is false when there is nothing to send:
// today's menu already went out — a restart in the minute of sending must not
// post it twice — or no dish fits either meal.
//
// Dinner is picked with lunch's dishes kept out, so a dish good for either
// meal is not offered twice in one message unless there is nothing else.
func (b *Bot) buildMenu(now time.Time, rnd *rand.Rand) (menuState, bool, error) {
	today := now.Format(time.DateOnly)
	if _, err := b.store.MenuMessage(today); err == nil {
		return menuState{}, false, nil
	} else if !store.IsNotFound(err) {
		return menuState{}, false, err
	}
	yesterday := now.AddDate(0, 0, -1).Format(time.DateOnly)
	shownYesterday, err := b.shownOn(yesterday)
	if err != nil {
		return menuState{}, false, err
	}
	cands, err := b.menuCandidates()
	if err != nil {
		return menuState{}, false, err
	}

	refs := menuRefs{date: today, offered: map[menu.Meal][]int64{}}
	lunch := menu.Pick(cands, now, menu.Lunch, nil, shownYesterday[menu.Lunch], menuOptions, rnd)
	refs.offered[menu.Lunch] = dishIDs(lunch)
	dinner := menu.Pick(cands, now, menu.Dinner, idSet(refs.offered[menu.Lunch]),
		shownYesterday[menu.Dinner], menuOptions, rnd)
	refs.offered[menu.Dinner] = dishIDs(dinner)
	if len(lunch) == 0 && len(dinner) == 0 {
		return menuState{}, false, nil
	}

	eaten, err := b.store.EatenOn(yesterday)
	if err != nil {
		return menuState{}, false, err
	}
	refs.leftovers = menu.Leftovers(eaten)

	st, err := b.menuStateFrom(refs)
	return st, err == nil, err
}

// applyMenuTap carries out one tap on the menu and returns the state to
// redraw. current is the tapped message's keyboard: it says which dishes the
// message shows, so a tap on one meal leaves the other's row as it was.
//
// The date travels in the button, and a button from an earlier day writes
// nothing: yesterday's menu tapped by mistake must not plan today's lunch.
func (b *Bot) applyMenuTap(now time.Time, unique, data, who string, current *tele.ReplyMarkup, rnd *rand.Rand) (menuState, string, bool, error) {
	parts := strings.Split(data, "|")
	want := 3
	if unique == menuShufUnique {
		want = 2
	}
	if len(parts) != want || !model.ValidMeal(parts[1]) {
		return menuState{}, "Невірні дані", false, nil
	}
	date, meal := parts[0], menu.Meal(parts[1])
	if _, err := model.ParseDate(date); err != nil {
		return menuState{}, "Невірні дані", false, nil
	}
	if date != now.Format(time.DateOnly) {
		return menuState{}, "Це меню вже минуло", false, nil
	}

	refs := menuRefsFromMarkup(current)
	refs.date = date

	var toast string
	switch unique {
	case menuPickUnique, menuLeftUnique:
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return menuState{}, "Невірні дані", false, nil
		}
		row, err := b.store.PlanMeal(id, date, string(meal), who, unique == menuLeftUnique)
		if store.IsNotFound(err) {
			return menuState{}, "Цієї страви вже немає", false, nil
		}
		if err != nil {
			return menuState{}, "", false, err
		}
		toast = fmt.Sprintf("%s: %s", meal.Title(), row.Dish)

	case menuShufUnique:
		ids, err := b.shuffleMeal(now, date, meal, refs, rnd)
		if err != nil {
			return menuState{}, "", false, err
		}
		if len(ids) == 0 {
			return menuState{}, "Більше нічого немає", false, nil
		}
		refs.offered[meal] = ids

	default:
		return menuState{}, "Невірні дані", false, nil
	}

	st, err := b.menuStateFrom(refs)
	if err != nil {
		return menuState{}, "", false, err
	}
	return st, toast, true, nil
}

// shuffleMeal offers a fresh row for one meal: nothing shown today, nothing on
// the other meal's row, nothing shown yesterday — Pick lets those back in, in
// reverse order, once the pool runs out. The new row is added to the day's
// shown set in the store, so the next shuffle moves on again.
func (b *Bot) shuffleMeal(now time.Time, date string, meal menu.Meal, refs menuRefs, rnd *rand.Rand) ([]int64, error) {
	shownToday, err := b.shownOn(date)
	if err != nil {
		return nil, err
	}
	exclude := shownToday[meal]
	if exclude == nil {
		exclude = map[int64]bool{}
	}
	// The rows on screen count as shown even if the day's row in the store is
	// missing — a menu posted by hand, or a save that failed after sending.
	for _, m := range menuMeals {
		for _, id := range refs.offered[m] {
			exclude[id] = true
		}
	}
	yesterday, err := b.shownOn(now.AddDate(0, 0, -1).Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	cands, err := b.menuCandidates()
	if err != nil {
		return nil, err
	}
	ids := dishIDs(menu.Pick(cands, now, meal, exclude, yesterday[meal], menuOptions, rnd))
	if len(ids) == 0 {
		return nil, nil
	}
	if err := b.store.AppendShown(date, string(meal), ids); err != nil && !store.IsNotFound(err) {
		return nil, err
	}
	return ids, nil
}

// shownOn is the day's shown set per meal; a day without a menu showed
// nothing.
func (b *Bot) shownOn(date string) (map[menu.Meal]map[int64]bool, error) {
	out := map[menu.Meal]map[int64]bool{}
	msg, err := b.store.MenuMessage(date)
	if store.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for meal, ids := range msg.Shown {
		out[menu.Meal(meal)] = idSet(ids)
	}
	return out, nil
}

func (b *Bot) menuCandidates() ([]menu.Candidate, error) {
	dishes, err := b.store.Dishes(model.DishActive, model.DishProposed)
	if err != nil {
		return nil, err
	}
	seen, err := b.store.LastSeen()
	if err != nil {
		return nil, err
	}
	cands := make([]menu.Candidate, 0, len(dishes))
	for _, d := range dishes {
		cands = append(cands, menu.Candidate{Dish: d, LastSeen: seen[d.ID]})
	}
	return cands, nil
}

// menuStateFrom fills the ids with names and statuses and reads what was
// chosen. The whole catalogue is read rather than looked up per id: it is a
// few dozen rows, and a leftover may be a dish of any status. A dish deleted
// since the message was sent simply drops out of the redraw.
func (b *Bot) menuStateFrom(refs menuRefs) (menuState, error) {
	all, err := b.store.Dishes()
	if err != nil {
		return menuState{}, err
	}
	byID := make(map[int64]model.Dish, len(all))
	for _, d := range all {
		byID[d.ID] = d
	}
	resolve := func(ids []int64) []menuDish {
		out := make([]menuDish, 0, len(ids))
		for _, id := range ids {
			if d, ok := byID[id]; ok {
				out = append(out, menuDish{id: d.ID, name: d.Name, proposed: d.Status == model.DishProposed})
			}
		}
		return out
	}
	st := menuState{
		date:      refs.date,
		leftovers: resolve(refs.leftovers),
		offered:   map[menu.Meal][]menuDish{},
	}
	for _, meal := range menuMeals {
		st.offered[meal] = resolve(refs.offered[meal])
	}
	if st.chosen, err = b.store.MealsOn(refs.date); err != nil {
		return menuState{}, err
	}
	return st, nil
}

func dishIDs(dishes []model.Dish) []int64 {
	out := make([]int64, 0, len(dishes))
	for _, d := range dishes {
		out = append(out, d.ID)
	}
	return out
}

func idSet(ids []int64) map[int64]bool {
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// menuRand is a fresh source per message; only the tests need a fixed one.
func menuRand() *rand.Rand {
	return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
}

// sendMenu posts today's menu and records it, which is also what keeps a
// restart from posting it again.
func (b *Bot) sendMenu(now time.Time) {
	st, ok, err := b.buildMenu(now, menuRand())
	if err != nil {
		b.logger.Error("bot: build menu", "err", err)
		return
	}
	if !ok {
		b.logger.Info("bot: no menu to send (already sent today, or no dish fits)", "date", now.Format(time.DateOnly))
		return
	}
	text, markup := menuView(st)
	msg, err := b.sendToGroup(text, markup, tele.ModeHTML)
	if err != nil {
		b.logger.Error("bot: send menu", "err", err)
		return
	}
	if msg == nil {
		return
	}
	shown := map[string][]int64{}
	for _, meal := range menuMeals {
		for _, d := range st.offered[meal] {
			shown[string(meal)] = append(shown[string(meal)], d.id)
		}
	}
	if err := b.store.SaveMenuMessage(model.MenuMessage{
		Date: st.date, ChatID: msg.Chat.ID, MessageID: int64(msg.ID), Shown: shown,
	}); err != nil {
		b.logger.Error("bot: save menu message", "date", st.date, "err", err)
	}
}

func (b *Bot) onMenuPick(c tele.Context) error { return b.onMenuTap(c, menuPickUnique) }
func (b *Bot) onMenuShuf(c tele.Context) error { return b.onMenuTap(c, menuShufUnique) }
func (b *Bot) onMenuLeft(c tele.Context) error { return b.onMenuTap(c, menuLeftUnique) }

func (b *Bot) onMenuTap(c tele.Context, unique string) error {
	var current *tele.ReplyMarkup
	if msg := c.Message(); msg != nil {
		current = msg.ReplyMarkup
	}
	st, toast, redraw, err := b.applyMenuTap(b.now(), unique, c.Data(), b.senderName(c), current, menuRand())
	if err != nil {
		b.logger.Error("bot: menu tap", "unique", unique, "data", c.Data(), "err", err)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не вдалося"})
		return nil
	}
	_ = c.Respond(&tele.CallbackResponse{Text: toast})
	if !redraw {
		return nil
	}
	// Tapping the dish that is already chosen redraws the same message, which
	// Telegram answers with an error; the tap did what it said, so it is not one.
	text, markup := menuView(st)
	if err := c.Edit(text, b.withAppButton([]any{markup, tele.ModeHTML})...); err != nil &&
		!errors.Is(err, tele.ErrSameMessageContent) {
		return err
	}
	return nil
}
