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

	"familyhub/internal/kitchen"
	"familyhub/internal/menu"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The morning menu: a few dishes for lunch and for dinner, yesterday's pot to
// finish, and a link to the Mini App, where the day's menu is actually made —
// any number of dishes per meal, more suggestions on request, the whole
// catalogue searchable. A keyboard can only plan one dish per tap, which is
// why it moved there. The evening check and the photo of the plate say what
// was eaten.
//
// The buttons of the older keyboard — a dish per tap and a 🔀 per meal — are
// still handled, for a menu posted before the link existed, and are still
// drawn when there is no link to draw.
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
	// appURL is the Mini App deep link base; empty draws the old keyboard.
	appURL string
}

func (s menuState) isChosen(meal menu.Meal, dishID int64) bool {
	for _, m := range s.chosen {
		if m.Meal == string(meal) && m.DishID == dishID {
			return true
		}
	}
	return false
}

// choiceLines is what the ✅ lines show: per meal, what was eaten if anything
// was, else the plan. A pick that another dish replaced stays in the journal
// as history, but the message says what the meal turned out to be.
func (s menuState) choiceLines() []model.MealEntry {
	eaten := map[string]bool{}
	for _, m := range s.chosen {
		if m.Status == model.MealEaten {
			eaten[m.Meal] = true
		}
	}
	var out []model.MealEntry
	for _, m := range s.chosen {
		if m.Status == model.MealEaten || !eaten[m.Meal] {
			out = append(out, m)
		}
	}
	return out
}

// decided says whether the meal already has a dish for today, planned or
// eaten. A decided meal loses its buttons: the choice is made, and a keyboard
// still offering the other dishes reads as a question nobody has answered. A
// change of mind after that is recorded the way any meal is — a photo or
// /cooked — and replaces the plan.
func (s menuState) decided(meal menu.Meal) bool {
	for _, m := range s.chosen {
		if m.Meal == string(meal) {
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
	var open []menu.Meal
	for _, meal := range menuMeals {
		if !s.decided(meal) {
			open = append(open, meal)
		}
	}
	leftovers := s.leftovers
	if len(open) == 0 {
		leftovers = nil
	}

	var sb strings.Builder
	sb.WriteString("🍽 <b>Що приготувати сьогодні</b>\n")
	offering := len(leftovers) > 0
	for _, meal := range open {
		offering = offering || len(s.offered[meal]) > 0
	}
	if offering {
		sb.WriteString("\n")
	}
	if len(leftovers) > 0 {
		names := make([]string, 0, len(leftovers))
		for _, d := range leftovers {
			names = append(names, html.EscapeString(d.name))
		}
		fmt.Fprintf(&sb, "Доїдаємо: %s\n", strings.Join(names, " · "))
	}
	for _, meal := range open {
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
	if shown := s.choiceLines(); len(shown) > 0 {
		sb.WriteString("\n")
		// One line per meal, however many dishes it has: "✅ Обід: Борщ ·
		// Салат — Олег".
		for _, meal := range menuMeals {
			var names, whos []string
			for _, m := range shown {
				if m.Meal != string(meal) {
					continue
				}
				name := html.EscapeString(m.Dish)
				if m.Leftover {
					name += " (доїдаємо)"
				}
				names = append(names, name)
				if m.Who != "" && !slices.Contains(whos, m.Who) {
					whos = append(whos, m.Who)
				}
			}
			if len(names) == 0 {
				continue
			}
			fmt.Fprintf(&sb, "✅ %s: %s", meal.Title(), strings.Join(names, " · "))
			if len(whos) > 0 {
				fmt.Fprintf(&sb, " — %s", html.EscapeString(strings.Join(whos, ", ")))
			}
			sb.WriteString("\n")
		}
	}

	mk := &tele.ReplyMarkup{}
	if s.appURL != "" {
		label := menuMakeLabel
		if len(open) == 0 {
			label = menuChangeLabel
		}
		mk.Inline(tele.Row{mk.URL(label, menuPlannerURL(s.appURL, s.date))})
		return strings.TrimRight(sb.String(), "\n"), mk
	}
	var rows []tele.Row
	for _, d := range leftovers {
		var row tele.Row
		for _, meal := range open {
			label := fmt.Sprintf("↩ %s · %s", shorten(d.name, menuButtonName), strings.ToLower(meal.Title()))
			row = append(row, mk.Data(label, menuLeftUnique, s.date, string(meal), strconv.FormatInt(d.id, 10)))
		}
		rows = append(rows, row)
	}
	for _, meal := range open {
		dishes := s.offered[meal]
		if len(dishes) == 0 {
			continue
		}
		var row tele.Row
		for _, d := range dishes {
			label := newMark(d) + shorten(d.name, menuButtonName)
			row = append(row, mk.Data(label, menuPickUnique, s.date, string(meal), strconv.FormatInt(d.id, 10)))
		}
		rows = append(rows, row,
			mk.Row(mk.Data("🔀 "+strings.ToLower(meal.Title()), menuShufUnique, s.date, string(meal))))
	}
	mk.Inline(rows...)
	return strings.TrimRight(sb.String(), "\n"), mk
}

func newMark(d menuDish) string {
	if d.proposed {
		return "🆕 "
	}
	return ""
}

// menuRefsFromMarkup reads back what menuView wrote, from either shape a
// keyboard comes in (see callbackPayload). Buttons that are not the menu's —
// the app row — are skipped.
func menuRefsFromMarkup(m *tele.ReplyMarkup) menuRefs {
	refs := menuRefs{offered: map[menu.Meal][]int64{}}
	if m == nil {
		return refs
	}
	for _, row := range m.InlineKeyboard {
		for _, btn := range row {
			unique, payload, ok := callbackPayload(btn, menuPickUnique, menuShufUnique, menuLeftUnique)
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

// buildMenu decides today's menu. ok is false when there is nothing to send:
// today's menu already went out — a restart in the minute of sending must not
// post it twice — or no dish fits either meal. What is picked is
// kitchen.FirstPicks, the same the Mini App falls back to on a day without a
// message.
func (b *Bot) buildMenu(now time.Time, rnd *rand.Rand) (menuState, bool, error) {
	today := now.Format(time.DateOnly)
	if _, err := b.store.MenuMessage(today); err == nil {
		return menuState{}, false, nil
	} else if !store.IsNotFound(err) {
		return menuState{}, false, err
	}
	leftovers, offered, err := b.kitchen().FirstPicks(now, rnd)
	if err != nil {
		return menuState{}, false, err
	}
	if len(offered[menu.Lunch]) == 0 && len(offered[menu.Dinner]) == 0 {
		return menuState{}, false, nil
	}
	st, err := b.menuStateFrom(menuRefs{date: today, leftovers: leftovers, offered: offered})
	return st, err == nil, err
}

// applyMenuTap carries out one tap on the menu and returns the state to
// redraw. current is the tapped message's keyboard: it says which dishes the
// message shows, so a tap on one meal leaves the other's row as it was.
//
// The date travels in the button, and a button from an earlier day writes
// nothing: yesterday's menu tapped by mistake must not plan today's lunch.
func (b *Bot) applyMenuTap(now time.Time, unique, data, who string, current *tele.ReplyMarkup, rnd *rand.Rand) (menuState, string, bool, error) {
	date, meal, id, ok := parseMealData(data, unique != menuShufUnique)
	if !ok {
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
		row, err := b.store.PlanMeal(id, date, string(meal), who, unique == menuLeftUnique)
		if store.IsNotFound(err) {
			return menuState{}, "Цієї страви вже немає", false, nil
		}
		if err != nil {
			return menuState{}, "", false, err
		}
		toast = fmt.Sprintf("%s: %s", meal.Title(), row.Dish)
		if row.Status == model.MealEaten {
			// Reported eaten before the tap, so nothing was planned.
			toast = fmt.Sprintf("%s вже записано: %s", meal.Title(), row.Dish)
		}

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

// parseMealData checks one button's data: "date|meal", or "date|meal|dishID"
// withID. The date only has to be one; which dates a tap may write is the
// caller's rule.
func parseMealData(data string, withID bool) (date string, meal menu.Meal, dishID int64, ok bool) {
	parts := strings.Split(data, "|")
	want := 2
	if withID {
		want = 3
	}
	if len(parts) != want || !model.ValidMeal(parts[1]) {
		return "", "", 0, false
	}
	if _, err := model.ParseDate(parts[0]); err != nil {
		return "", "", 0, false
	}
	if withID {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return "", "", 0, false
		}
		dishID = id
	}
	return parts[0], menu.Meal(parts[1]), dishID, true
}

// shuffleMeal offers a fresh row for one meal of an older keyboard menu; see
// kitchen.More. Everything on screen is kept out, the other meal's row and
// yesterday's pot included.
func (b *Bot) shuffleMeal(now time.Time, date string, meal menu.Meal, refs menuRefs, rnd *rand.Rand) ([]int64, error) {
	var onScreen []int64
	for _, m := range menuMeals {
		if m != meal {
			onScreen = append(onScreen, refs.offered[m]...)
		}
	}
	onScreen = append(onScreen, refs.leftovers...)
	return b.kitchen().More(now, date, meal, refs.offered[meal], onScreen, rnd)
}

func (b *Bot) kitchen() *kitchen.Service { return kitchen.New(b.store) }

// dishesByID is the whole catalogue, any status, by id. The screens that
// redraw from ids read it whole rather than per id: it is a few dozen rows.
func (b *Bot) dishesByID() (map[int64]model.Dish, error) {
	all, err := b.store.Dishes()
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]model.Dish, len(all))
	for _, d := range all {
		byID[d.ID] = d
	}
	return byID, nil
}

// menuStateFrom fills the ids with names and statuses and reads what was
// chosen. A leftover may be a dish of any status, hence the whole catalogue.
// A dish deleted since the message was sent simply drops out of the redraw.
func (b *Bot) menuStateFrom(refs menuRefs) (menuState, error) {
	byID, err := b.dishesByID()
	if err != nil {
		return menuState{}, err
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
		appURL:    b.miniAppURL(),
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

// menuRand is a fresh source per message; only the tests need a fixed one.
func menuRand() *rand.Rand { return kitchen.Rand() }

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
	if err := b.store.SaveMenuMessage(model.MenuMessage{
		Date: st.date, ChatID: msg.Chat.ID, MessageID: int64(msg.ID), Shown: menuShown(st),
	}); err != nil {
		b.logger.Error("bot: save menu message", "date", st.date, "err", err)
	}
}

// menuShown is what a menu just sent offered, per meal, for its shown set.
func menuShown(st menuState) map[string][]int64 {
	shown := map[string][]int64{}
	for _, meal := range menuMeals {
		for _, d := range st.offered[meal] {
			shown[string(meal)] = append(shown[string(meal)], d.id)
		}
	}
	return shown
}

// RedrawMenu redraws a day's posted menu from the store, for a plan made in
// the Mini App: the message then says what was chosen, the way a tap on it
// would have. The text keeps the morning's first picks; anything asked for
// later in the app was asked for there. A day without a menu message has
// nothing to redraw.
func (b *Bot) RedrawMenu(date string) error {
	msg, err := b.store.MenuMessage(date)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	day, err := model.ParseDate(date)
	if err != nil {
		return err
	}
	leftovers, err := b.kitchen().Leftovers(day)
	if err != nil {
		return err
	}
	refs := menuRefs{date: date, leftovers: leftovers, offered: map[menu.Meal][]int64{}}
	for _, meal := range menuMeals {
		ids := msg.Shown[string(meal)]
		refs.offered[meal] = ids[:min(len(ids), menuOptions)]
	}
	st, err := b.menuStateFrom(refs)
	if err != nil {
		return err
	}
	text, markup := menuView(st)
	where := tele.StoredMessage{MessageID: strconv.FormatInt(msg.MessageID, 10), ChatID: msg.ChatID}
	if _, err := b.b.Edit(where, text, b.withAppButton([]any{markup, tele.ModeHTML})...); err != nil &&
		!errors.Is(err, tele.ErrSameMessageContent) {
		return err
	}
	return nil
}

func (b *Bot) onMenuPick(c tele.Context) error { return b.onMenuTap(c, menuPickUnique) }
func (b *Bot) onMenuShuf(c tele.Context) error { return b.onMenuTap(c, menuShufUnique) }
func (b *Bot) onMenuLeft(c tele.Context) error { return b.onMenuTap(c, menuLeftUnique) }

func (b *Bot) onMenuTap(c tele.Context, unique string) error {
	st, toast, redraw, err := b.applyMenuTap(b.now(), unique, c.Data(), b.senderName(c), currentMarkup(c), menuRand())
	return b.respondAndRedraw(c, "menu", unique, toast, redraw, err, func() (string, *tele.ReplyMarkup) {
		return menuView(st)
	})
}

// respondAndRedraw finishes a tap on the menu, the evening check or the
// suggestion card: the toast, then the redraw of the tapped message when the
// tap changed something. what names the message in the log.
func (b *Bot) respondAndRedraw(c tele.Context, what, unique, toast string, redraw bool, err error, render func() (string, *tele.ReplyMarkup)) error {
	if err != nil {
		b.logger.Error("bot: "+what+" tap", "unique", unique, "data", c.Data(), "err", err)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не вдалося"})
		return nil
	}
	_ = c.Respond(&tele.CallbackResponse{Text: toast})
	if !redraw {
		return nil
	}
	text, markup := render()
	return editIgnoringSame(c, text, b.withAppButton([]any{markup, tele.ModeHTML})...)
}

// currentMarkup is the keyboard of the message a tap came from — the only
// memory the menu, the evening check and the suggestion card have.
func currentMarkup(c tele.Context) *tele.ReplyMarkup {
	if msg := c.Message(); msg != nil {
		return msg.ReplyMarkup
	}
	return nil
}

// editIgnoringSame redraws the tapped message. Tapping what is already
// chosen redraws the same message, which Telegram answers with an error; the
// tap did what it said, so it is not one.
func editIgnoringSame(c tele.Context, what any, opts ...any) error {
	if err := c.Edit(what, opts...); err != nil && !errors.Is(err, tele.ErrSameMessageContent) {
		return err
	}
	return nil
}

// The evening check: for each meal nothing has been reported eaten for, ask
// what it was. A planned dish is asked about by name; with no plan, what was
// eaten yesterday is offered, since the pot most often lasts a second day.
//
// Split the same way as the morning menu: eveningView renders, buildEvening
// and applyEveningTap work against the store, the handlers glue. The answers
// that are rows in the store are read back from it; the one that is not —
// "not at home" writes nothing — is kept in the keyboard, by the meal's
// buttons being gone.

const (
	eveYesUnique   = "eve_yes"
	evePickUnique  = "eve_pick"
	eveHomeUnique  = "eve_home"
	eveRejUnique   = "eve_rej"
	eveOtherUnique = "eve_other"
)

var eveUniques = []string{eveYesUnique, evePickUnique, eveHomeUnique, eveRejUnique, eveOtherUnique}

// eveningState is one day's evening check as it stands.
type eveningState struct {
	date string
	// open are the meals still being asked about. A meal eaten according to
	// the store is closed whatever this says.
	open map[menu.Meal]bool
	// meals is every row of the day, planned and eaten.
	meals []model.MealEntry
	// yesterday is what was eaten the day before, each dish once — the
	// answers offered for a meal nobody planned.
	yesterday []menuDish
	// other offers "Інше", which hands the answer to the recognizer and so
	// exists only with one.
	other bool
	// appURL is the Mini App deep link, empty when the bot's username did not
	// resolve. With it, each meal gets a button to the picker, which takes
	// several dishes at once — a keyboard tap answers exactly one, so
	// yesterday's mash with a new salad could not be said here. Without it the
	// check falls back to yesterday's dishes as buttons.
	appURL string
}

func (s eveningState) eaten(meal menu.Meal) []model.MealEntry {
	var out []model.MealEntry
	for _, m := range s.meals {
		if m.Meal == string(meal) && m.Status == model.MealEaten {
			out = append(out, m)
		}
	}
	return out
}

// answerable is whether a meal has an answer besides "Не вдома".
func (s eveningState) answerable(meal menu.Meal) bool {
	return len(s.plans(meal)) > 0 || len(s.yesterday) > 0 || s.other || s.appURL != ""
}

// plans is the meal's plan — several dishes, since the menu is made in the
// Mini App.
func (s eveningState) plans(meal menu.Meal) []model.MealEntry {
	var out []model.MealEntry
	for _, m := range s.meals {
		if m.Meal == string(meal) && m.Status == model.MealPlanned {
			out = append(out, m)
		}
	}
	return out
}

// eveningView renders the check. Once the last meal is answered the redraw
// has no buttons left; whether there is anything to send in the first place
// is buildEvening's call.
//
// Every button names its meal: the two meals' rows sit one under the other,
// and a bare "Не вдома" would not say which.
func eveningView(s eveningState) (string, *tele.ReplyMarkup) {
	var sb strings.Builder
	sb.WriteString("🍽 <b>Що їли сьогодні?</b>\n")

	mk := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, meal := range menuMeals {
		lower := strings.ToLower(meal.Title())
		if eaten := s.eaten(meal); len(eaten) > 0 {
			names := make([]string, 0, len(eaten))
			for _, m := range eaten {
				names = append(names, html.EscapeString(m.Dish))
			}
			fmt.Fprintf(&sb, "\n✅ %s: %s", meal.Title(), strings.Join(names, " · "))
			continue
		}
		if !s.open[meal] {
			fmt.Fprintf(&sb, "\n%s: не їли вдома", meal.Title())
			continue
		}
		id := func(dishID int64) string { return strconv.FormatInt(dishID, 10) }

		if plans := s.plans(meal); len(plans) > 0 {
			names := make([]string, 0, len(plans))
			var proposals []model.MealEntry
			for _, p := range plans {
				mark := ""
				if p.DishStatus == model.DishProposed {
					mark = "🆕 "
					proposals = append(proposals, p)
				}
				names = append(names, mark+"<b>"+html.EscapeString(p.Dish)+"</b>")
			}
			fmt.Fprintf(&sb, "\n%s: %s — так?", meal.Title(), strings.Join(names, " · "))
			// "Так" confirms the whole plan. The dish in the data is only
			// the first; it is what an older single-dish check carried.
			first := tele.Row{mk.Data("✓ Так · "+lower, eveYesUnique, s.date, string(meal), id(plans[0].DishID))}
			if s.appURL != "" {
				first = append(first, mk.URL(pickLabel+" · "+lower, mealPickerURL(s.appURL, s.date, meal)))
			}
			var second tele.Row
			if s.other {
				second = append(second, mk.Data("Інше · "+lower, eveOtherUnique, s.date, string(meal)))
			}
			second = append(second, mk.Data("Не вдома · "+lower, eveHomeUnique, s.date, string(meal)))
			rows = append(rows, first, second)
			for _, p := range proposals {
				label := "✖ Ні, не наше"
				if len(plans) > 1 {
					label = "✖ " + shorten(p.Dish, menuButtonName) + " — не наше"
				}
				rows = append(rows, tele.Row{mk.Data(label, eveRejUnique, s.date, string(meal), id(p.DishID))})
			}
			continue
		}

		fmt.Fprintf(&sb, "\n%s: що їли?", meal.Title())
		if s.appURL != "" {
			rows = append(rows, tele.Row{mk.URL(pickLabel+" · "+lower, mealPickerURL(s.appURL, s.date, meal))})
		} else {
			var picks tele.Row
			for _, d := range s.yesterday {
				picks = append(picks, mk.Data("↩ "+shorten(d.name, menuButtonName), evePickUnique, s.date, string(meal), id(d.id)))
				if len(picks) == menuOptions {
					rows = append(rows, picks)
					picks = nil
				}
			}
			if len(picks) > 0 {
				rows = append(rows, picks)
			}
		}
		var last tele.Row
		if s.other {
			last = append(last, mk.Data("Інше · "+lower, eveOtherUnique, s.date, string(meal)))
		}
		rows = append(rows, append(last, mk.Data("Не вдома · "+lower, eveHomeUnique, s.date, string(meal))))
	}
	mk.Inline(rows...)
	return sb.String(), mk
}

// eveningOpenFromMarkup reads back which meals a check still asks about: the
// meals that have buttons.
func eveningOpenFromMarkup(m *tele.ReplyMarkup) map[menu.Meal]bool {
	open := map[menu.Meal]bool{}
	if m == nil {
		return open
	}
	for _, row := range m.InlineKeyboard {
		for _, btn := range row {
			_, payload, ok := callbackPayload(btn, eveUniques...)
			if !ok {
				continue
			}
			if parts := strings.Split(payload, "|"); len(parts) >= 2 && model.ValidMeal(parts[1]) {
				open[menu.Meal(parts[1])] = true
			}
		}
	}
	return open
}

// parseEveningData checks one button's data: "date|meal" for eve_home and
// eve_other, "date|meal|dishID" for the rest. A date after today is refused;
// an earlier one is not — an answer tapped after midnight is still about the
// evening it was asked.
func parseEveningData(now time.Time, unique, data string) (date string, meal menu.Meal, dishID int64, ok bool) {
	date, meal, dishID, ok = parseMealData(data, unique != eveHomeUnique && unique != eveOtherUnique)
	if !ok || date > now.Format(time.DateOnly) {
		return "", "", 0, false
	}
	return date, meal, dishID, true
}

// buildEvening decides tonight's check: every meal without an eaten row is
// asked about. ok is false when there is nothing to ask — both meals already
// answered, the photo of the plate having got there first, or no meal with an
// answer besides "Не вдома": no plan, nothing eaten yesterday and no
// recognizer behind "Інше". A check that could only be answered "not at home"
// would arrive every evening the menu went unused and ask nothing.
func (b *Bot) buildEvening(now time.Time) (eveningState, bool, error) {
	st, err := b.eveningStateFrom(now.Format(time.DateOnly), nil)
	if err != nil {
		return eveningState{}, false, err
	}
	ok := false
	for _, meal := range menuMeals {
		st.open[meal] = len(st.eaten(meal)) == 0
		ok = ok || (st.open[meal] && st.answerable(meal))
	}
	return st, ok, nil
}

// applyEveningTap carries out one answer and returns the state to redraw.
// current is the tapped message's keyboard: it holds which meals are still
// asked about, the "not at home" answers being nowhere else.
//
// eve_other is not handled here: it only arms a reply, see onEveningOther.
func (b *Bot) applyEveningTap(now time.Time, unique, data, who string, current *tele.ReplyMarkup) (eveningState, string, bool, error) {
	date, meal, dishID, ok := parseEveningData(now, unique, data)
	if !ok || unique == eveOtherUnique {
		return eveningState{}, "Невірні дані", false, nil
	}
	open := eveningOpenFromMarkup(current)

	var toast string
	switch unique {
	case eveYesUnique:
		// The whole plan was eaten. A meal answered since the check was drawn
		// only redraws: confirming the plan beside it would put a second
		// lunch on the day.
		rows, err := b.store.MealsOn(date)
		if err != nil {
			return eveningState{}, "", false, err
		}
		var plan []int64
		var names []string
		for _, m := range rows {
			if m.Meal != string(meal) {
				continue
			}
			if m.Status == model.MealEaten {
				toast = "Вже записано"
				break
			}
			plan = append(plan, m.DishID)
			names = append(names, m.Dish)
		}
		if toast != "" {
			break
		}
		if len(plan) == 0 {
			// The plan was dropped since; the button still names its dish.
			plan = []int64{dishID}
		}
		if err := b.store.SetEaten(date, string(meal), who, plan, nil); store.IsNotFound(err) {
			return eveningState{}, "Цієї страви вже немає", false, nil
		} else if err != nil {
			return eveningState{}, "", false, err
		}
		toast = meal.Title() + " ✓"
		if len(names) > 0 {
			toast = fmt.Sprintf("%s: %s ✓", meal.Title(), strings.Join(names, " · "))
		}

	case evePickUnique:
		stale, err := b.eatenOtherThan(date, meal, dishID)
		if err != nil {
			return eveningState{}, "", false, err
		}
		if stale {
			toast = "Вже записано"
			break
		}
		// A dish from yesterday eaten again today is yesterday's pot, which is
		// what the morning's "Доїдаємо" means too.
		row, already, err := b.store.RecordEaten(dishID, date, string(meal), who, unique == evePickUnique)
		if store.IsNotFound(err) {
			return eveningState{}, "Цієї страви вже немає", false, nil
		}
		if err != nil {
			return eveningState{}, "", false, err
		}
		toast = fmt.Sprintf("%s: %s ✓", meal.Title(), row.Dish)
		if already {
			toast = "Вже записано"
		}

	case eveHomeUnique:
		if err := b.store.DropPlans(date, string(meal), 0); err != nil {
			return eveningState{}, "", false, err
		}
		// Kept in the store as well as the keyboard, for a redraw from the
		// Mini App. A check posted before that was recorded has no row.
		if err := b.store.MarkAway(date, string(meal)); err != nil && !store.IsNotFound(err) {
			return eveningState{}, "", false, err
		}
		open[meal] = false
		toast = fmt.Sprintf("%s: не вдома", meal.Title())

	case eveRejUnique:
		// The plan goes, the dish goes out of the menu for good — but the
		// meal is still unanswered, so it stays asked, now without a plan.
		if _, err := b.store.TurnDown(dishID, date, string(meal)); store.IsNotFound(err) {
			return eveningState{}, "Цієї страви вже немає", false, nil
		} else if err != nil {
			return eveningState{}, "", false, err
		}
		open[meal] = true
		toast = "Більше не пропонуватиму"

	default:
		return eveningState{}, "Невірні дані", false, nil
	}

	st, err := b.eveningStateFrom(date, open)
	if err != nil {
		return eveningState{}, "", false, err
	}
	return st, toast, true, nil
}

// eatenOtherThan reports a stale answer button: the meal was reported eaten
// since the check was drawn — through "Інше" or a photo of the plate — and
// not with this dish. Recording it as well would put two lunches on one day,
// so such a tap only redraws the check. The same dish is not stale: that is
// RecordEaten's "already".
func (b *Bot) eatenOtherThan(date string, meal menu.Meal, dishID int64) (bool, error) {
	rows, err := b.store.MealsOn(date)
	if err != nil {
		return false, err
	}
	other := false
	for _, m := range rows {
		if m.Meal != string(meal) || m.Status != model.MealEaten {
			continue
		}
		if m.DishID == dishID {
			return false, nil
		}
		other = true
	}
	return other, nil
}

func (b *Bot) eveningStateFrom(date string, open map[menu.Meal]bool) (eveningState, error) {
	if open == nil {
		open = map[menu.Meal]bool{}
	}
	st := eveningState{date: date, open: open, other: b.cfg.Dish != nil, appURL: b.miniAppURL()}
	var err error
	if st.meals, err = b.store.MealsOn(date); err != nil {
		return eveningState{}, err
	}
	day, err := model.ParseDate(date)
	if err != nil {
		return eveningState{}, err
	}
	eaten, err := b.store.EatenOn(day.AddDate(0, 0, -1).Format(time.DateOnly))
	if err != nil {
		return eveningState{}, err
	}
	names := make(map[int64]string, len(eaten))
	for _, m := range eaten {
		names[m.DishID] = m.Dish
	}
	for _, id := range menu.Leftovers(eaten) {
		st.yesterday = append(st.yesterday, menuDish{id: id, name: names[id]})
	}
	return st, nil
}

// sendEveningCheck posts tonight's check, or nothing when there is nothing to
// ask (see buildEvening).
func (b *Bot) sendEveningCheck(now time.Time) {
	st, ok, err := b.buildEvening(now)
	if err != nil {
		b.logger.Error("bot: build evening check", "err", err)
		return
	}
	if !ok {
		b.logger.Info("bot: no evening check to send (every meal answered, or nothing to answer with)", "date", st.date)
		return
	}
	text, markup := eveningView(st)
	msg, err := b.sendToGroup(text, markup, tele.ModeHTML)
	if err != nil {
		b.logger.Error("bot: send evening check", "err", err)
		return
	}
	if msg == nil {
		return
	}
	// Remembered so an answer from the Mini App can redraw it.
	if err := b.store.SaveEveningMessage(model.EveningMessage{
		Date: st.date, ChatID: msg.Chat.ID, MessageID: int64(msg.ID),
	}); err != nil {
		b.logger.Error("bot: save evening message", "date", st.date, "err", err)
	}
}

// RedrawEvening redraws a day's evening check from the store, after the Mini
// App's picker recorded a meal: the check then says what was eaten, the way a
// tap on it would have. A meal answered «Не вдома» stays answered — that is
// kept in evening_messages, the keyboard being out of the server's reach. A
// day without a recorded check has nothing to redraw.
func (b *Bot) RedrawEvening(date string) error {
	msg, err := b.store.EveningMessage(date)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	open := map[menu.Meal]bool{}
	for _, meal := range menuMeals {
		open[meal] = !slices.Contains(msg.Away, string(meal))
	}
	st, err := b.eveningStateFrom(date, open)
	if err != nil {
		return err
	}
	text, markup := eveningView(st)
	where := tele.StoredMessage{MessageID: strconv.FormatInt(msg.MessageID, 10), ChatID: msg.ChatID}
	if _, err := b.b.Edit(where, text, b.withAppButton([]any{markup, tele.ModeHTML})...); err != nil &&
		!errors.Is(err, tele.ErrSameMessageContent) {
		return err
	}
	return nil
}

func (b *Bot) onEveningYes(c tele.Context) error  { return b.onEveningTap(c, eveYesUnique) }
func (b *Bot) onEveningPick(c tele.Context) error { return b.onEveningTap(c, evePickUnique) }
func (b *Bot) onEveningHome(c tele.Context) error { return b.onEveningTap(c, eveHomeUnique) }
func (b *Bot) onEveningRej(c tele.Context) error  { return b.onEveningTap(c, eveRejUnique) }

func (b *Bot) onEveningTap(c tele.Context, unique string) error {
	st, toast, redraw, err := b.applyEveningTap(b.now(), unique, c.Data(), b.senderName(c), currentMarkup(c))
	return b.respondAndRedraw(c, "evening", unique, toast, redraw, err, func() (string, *tele.ReplyMarkup) {
		return eveningView(st)
	})
}

// onEveningOther arms the tapper's next message — text or a photo — as the
// answer for that meal; the recognizer then shows the usual plate card, with
// the day and meal already set.
func (b *Bot) onEveningOther(c tele.Context) error {
	now := b.now()
	date, meal, _, ok := parseEveningData(now, eveOtherUnique, c.Data())
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: "Невірні дані"})
	}
	day, err := time.ParseInLocation(time.DateOnly, date, b.cfg.Loc)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: "Невірні дані"})
	}
	b.awaiting.setMealOther(senderID(c), plateFor{date: day, meal: meal}, now)
	_ = c.Respond()
	// In the group the question is addressed: only the tapper's reply counts.
	ask := fmt.Sprintf("Що їли на %s? Напиши або надішли фото.", meal.Accusative())
	if name := b.senderName(c); name != "" {
		ask = fmt.Sprintf("%s, що їли на %s? Напиши або надішли фото.", html.EscapeString(name), meal.Accusative())
	}
	return c.Send(ask, tele.ModeHTML)
}
