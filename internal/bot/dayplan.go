package bot

import (
	"context"
	"fmt"
	"html"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/dish"
	"familyhub/internal/kitchen"
	"familyhub/internal/menu"
	"familyhub/internal/model"
	"familyhub/internal/store"
)

// /menu: a few whole-day menus for a day ahead — tomorrow unless told
// otherwise — chosen with the recent history in view, and one tap to make
// one of them the day's plan.
//
// The morning menu offers dishes one meal at a time, for today. This is the
// other question, asked the evening before: what do we cook tomorrow, lunch
// and dinner together, so that the two do not clash and neither repeats what
// was eaten this week. The model composes the variants from the catalogue and
// the last weeks of meals; without one, or when it fails, the morning menu's
// own rotation fills them, a dish per meal.
//
// A picked variant is written as the day's plan with SetPlan, the same write
// the Mini App's menu makes, so tomorrow's morning menu opens with it ticked
// and the evening check asks about it. Nothing about the message is kept in
// memory: the date and each variant's dishes travel in its buttons.

const (
	planPickUnique = "plan_pick"
	planMoreUnique = "plan_more"

	// planTimeout bounds the model call and the writes behind it, as
	// suggestTimeout does for the weekly suggestions.
	planTimeout = 90 * time.Second
	// planHistoryDays is how far back the model sees what was eaten: long
	// enough to tell "we had it last week" from "not in a while".
	planHistoryDays = 21
	// planVariants is how many menus the rotation fills when there is no
	// model; the model is asked for the same number.
	planVariants = 3

	planThinking = "🤔 Думаю, що приготувати…"
	planUsage    = "На коли? /menu — на завтра, /menu сьогодні, /menu післязавтра"
)

// planVariant is one whole-day menu as the message shows it.
type planVariant struct {
	lunch, dinner []menuDish
	note          string
}

func (v planVariant) of(meal menu.Meal) []menuDish {
	if meal == menu.Dinner {
		return v.dinner
	}
	return v.lunch
}

// dayPlan is the message as it stands: the variants to choose from, or, once
// one was picked, the plan written.
type dayPlan struct {
	date  string
	label string // "Завтра, 11 листопада"
	// eaten is the meals of the day already reported eaten: they are shown
	// and not planned.
	eaten    []model.MealEntry
	variants []planVariant
	// picked is the day's plan after a tap; the message then only says it.
	picked []model.MealEntry
	done   bool
	// full is a day with both meals eaten: nothing left to plan.
	full bool
}

// localDay is a stored date as midnight in loc: ParseDate reads it in
// time.Local, which is not the bot's zone everywhere it runs.
func localDay(date string, loc *time.Location) (time.Time, bool) {
	d, err := model.ParseDate(date)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc), true
}

// planDayFrom reads the command's argument: no argument is tomorrow, the
// question this command exists for.
func planDayFrom(arg string, now time.Time) (time.Time, bool) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "", "завтра", "tomorrow":
		return today.AddDate(0, 0, 1), true
	case "сьогодні", "сегодня", "today":
		return today, true
	case "післязавтра", "послезавтра":
		return today.AddDate(0, 0, 2), true
	}
	return time.Time{}, false
}

func planView(p dayPlan) (string, *tele.ReplyMarkup) {
	var sb strings.Builder
	mk := &tele.ReplyMarkup{}
	if p.done {
		fmt.Fprintf(&sb, "🍽 <b>Меню · %s</b>\n\n", html.EscapeString(p.label))
		writeChoiceLines(&sb, p.picked)
		return strings.TrimRight(sb.String(), "\n"), mk
	}

	fmt.Fprintf(&sb, "🍽 <b>Що готувати · %s</b>\n", html.EscapeString(p.label))
	if len(p.eaten) > 0 {
		sb.WriteString("\n")
		writeChoiceLines(&sb, p.eaten)
	}
	if len(p.variants) == 0 {
		if p.full {
			return strings.TrimRight(sb.String(), "\n"), mk
		}
		sb.WriteString("\nНемає з чого скласти меню на цей день.")
		return sb.String(), mk
	}

	var picks tele.Row
	for i, v := range p.variants {
		fmt.Fprintf(&sb, "\n<b>%d.</b> ", i+1)
		var lines []string
		for _, meal := range menuMeals {
			dishes := v.of(meal)
			if len(dishes) == 0 {
				continue
			}
			names := make([]string, 0, len(dishes))
			for _, d := range dishes {
				names = append(names, newMark(d)+html.EscapeString(d.name))
			}
			lines = append(lines, fmt.Sprintf("%s: %s", meal.Title(), strings.Join(names, " · ")))
		}
		sb.WriteString(strings.Join(lines, "\n") + "\n")
		if v.note != "" {
			fmt.Fprintf(&sb, "<i>%s</i>\n", html.EscapeString(v.note))
		}
		picks = append(picks, mk.Data(fmt.Sprintf("✅ %d", i+1), planPickUnique, planPickData(p.date, v)))
	}
	mk.Inline(picks, mk.Row(mk.Data("🔀 Інші варіанти", planMoreUnique, p.date)))
	return strings.TrimRight(sb.String(), "\n"), mk
}

// planPickData is "date|lunchIDs|dinnerIDs", the ids dot-separated: a
// variant is two short lists, and the whole of it has to fit Telegram's 64
// bytes of callback data with the unique in front.
func planPickData(date string, v planVariant) string {
	ids := func(dishes []menuDish) string {
		parts := make([]string, 0, len(dishes))
		for _, d := range dishes {
			parts = append(parts, strconv.FormatInt(d.id, 10))
		}
		return strings.Join(parts, ".")
	}
	return date + "|" + ids(v.lunch) + "|" + ids(v.dinner)
}

func parsePlanPick(data string) (date string, v dish.Variant, ok bool) {
	parts := strings.Split(data, "|")
	if len(parts) != 3 {
		return "", dish.Variant{}, false
	}
	if _, err := model.ParseDate(parts[0]); err != nil {
		return "", dish.Variant{}, false
	}
	ids := func(s string) ([]int64, bool) {
		if s == "" {
			return nil, true
		}
		var out []int64
		for _, p := range strings.Split(s, ".") {
			id, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				return nil, false
			}
			out = append(out, id)
		}
		return out, true
	}
	var okL, okD bool
	v.Lunch, okL = ids(parts[1])
	v.Dinner, okD = ids(parts[2])
	if !okL || !okD || len(v.Lunch)+len(v.Dinner) == 0 {
		return "", dish.Variant{}, false
	}
	return parts[0], v, true
}

// planVariantsFromMarkup is the variants a message offers, read back from its
// buttons — what 🔀 asks the next round not to repeat.
func planVariantsFromMarkup(m *tele.ReplyMarkup) []dish.Variant {
	if m == nil {
		return nil
	}
	var out []dish.Variant
	for _, row := range m.InlineKeyboard {
		for _, btn := range row {
			if _, data, ok := callbackPayload(btn, planPickUnique); ok {
				if _, v, ok := parsePlanPick(data); ok {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// buildPlan composes the variants for day. A meal already eaten is shown, not
// planned; a day with both eaten has nothing to offer. avoid is the variants
// already on screen, for 🔀.
func (b *Bot) buildPlan(ctx context.Context, now, day time.Time, avoid []dish.Variant, rnd *rand.Rand) (dayPlan, error) {
	date := day.Format(time.DateOnly)
	p := dayPlan{date: date, label: model.DayLabel(day, now)}

	rows, err := b.store.MealsOn(date)
	if err != nil {
		return dayPlan{}, err
	}
	closed := map[menu.Meal]bool{}
	for _, m := range rows {
		if m.Status == model.MealEaten {
			closed[menu.Meal(m.Meal)] = true
			p.eaten = append(p.eaten, m)
		}
	}
	if closed[menu.Lunch] && closed[menu.Dinner] {
		p.full = true
		return p, nil
	}

	cands, err := b.kitchen().Candidates()
	if err != nil {
		return dayPlan{}, err
	}
	pools := map[menu.Meal][]dish.PlanDish{}
	for _, meal := range menuMeals {
		if closed[meal] {
			continue
		}
		for _, c := range cands {
			if menu.Fits(c.Dish, day, meal) {
				pools[meal] = append(pools[meal], dish.PlanDish{
					ID: c.Dish.ID, Name: c.Dish.Name, Proposed: c.Dish.Status == model.DishProposed,
				})
			}
		}
	}
	if len(pools[menu.Lunch]) == 0 && len(pools[menu.Dinner]) == 0 {
		return p, nil
	}

	var variants []dish.Variant
	if b.cfg.Dish != nil {
		history, leftovers, err := b.planHistory(day)
		if err != nil {
			return dayPlan{}, err
		}
		variants, err = b.cfg.Dish.PlanDay(ctx, dish.PlanInput{
			Date:      day,
			Lunch:     pools[menu.Lunch],
			Dinner:    pools[menu.Dinner],
			History:   history,
			Leftovers: leftovers,
			Avoid:     avoid,
		})
		if err != nil {
			b.logger.Warn("bot: day menu model, falling back to the rotation", "date", date, "err", err)
		}
	}
	if len(variants) == 0 {
		variants = rotationVariants(cands, day, closed, avoid, rnd)
	}

	byID, err := b.dishesByID()
	if err != nil {
		return dayPlan{}, err
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
	for _, v := range variants {
		pv := planVariant{lunch: resolve(v.Lunch), dinner: resolve(v.Dinner), note: v.Note}
		if len(pv.lunch)+len(pv.dinner) > 0 {
			p.variants = append(p.variants, pv)
		}
	}
	return p, nil
}

// planHistory is the days before day as the model reads them, oldest first —
// per meal what was eaten, or the plan where nothing was reported, since an
// unanswered plan was most likely cooked (the rotation reads it the same way)
// — and the day before's dishes, the pot that may be left.
func (b *Bot) planHistory(day time.Time) ([]dish.PlanDay, []string, error) {
	from := day.AddDate(0, 0, -planHistoryDays).Format(time.DateOnly)
	prev := day.AddDate(0, 0, -1).Format(time.DateOnly)
	rows, err := b.store.MealsBetween(from, prev)
	if err != nil {
		return nil, nil, err
	}
	byDate := map[string][]model.MealEntry{}
	var dates []string
	for _, m := range rows {
		if _, ok := byDate[m.Date]; !ok {
			dates = append(dates, m.Date)
		}
		byDate[m.Date] = append(byDate[m.Date], m)
	}
	var history []dish.PlanDay
	var leftovers []string
	for _, date := range dates {
		pd := dish.PlanDay{Date: date}
		for _, m := range (menuState{chosen: byDate[date]}).choiceLines() {
			if m.Meal == model.MealDinner {
				pd.Dinner = append(pd.Dinner, m.Dish)
			} else {
				pd.Lunch = append(pd.Lunch, m.Dish)
			}
			if date == prev {
				leftovers = append(leftovers, m.Dish)
			}
		}
		history = append(history, pd)
	}
	return history, leftovers, nil
}

// rotationVariants fills the variants from the morning menu's rotation when
// there is no model to compose them: a dish per meal, the ones not had in the
// longest, none of them on screen already.
func rotationVariants(cands []menu.Candidate, day time.Time, closed map[menu.Meal]bool, avoid []dish.Variant, rnd *rand.Rand) []dish.Variant {
	exclude := map[int64]bool{}
	for _, v := range avoid {
		for _, id := range append(append([]int64{}, v.Lunch...), v.Dinner...) {
			exclude[id] = true
		}
	}
	picks := map[menu.Meal][]model.Dish{}
	for _, meal := range menuMeals {
		if closed[meal] {
			continue
		}
		picks[meal] = menu.Pick(cands, day, meal, menu.Exclude{Today: exclude}, planVariants, false, rnd)
		for _, d := range picks[meal] {
			exclude[d.ID] = true
		}
	}
	n := max(len(picks[menu.Lunch]), len(picks[menu.Dinner]))
	out := make([]dish.Variant, 0, n)
	for i := range n {
		var v dish.Variant
		if i < len(picks[menu.Lunch]) {
			v.Lunch = []int64{picks[menu.Lunch][i].ID}
		}
		if i < len(picks[menu.Dinner]) {
			v.Dinner = []int64{picks[menu.Dinner][i].ID}
		}
		out = append(out, v)
	}
	return out
}

// applyPlanPick writes the tapped variant as the day's plan and returns the
// message to redraw. A day already past writes nothing. A meal eaten
// meanwhile is left as it is (SetPlan never plans beside an eaten row), and
// the message then says what the day really holds. A dish from the day
// before is planned as a leftover.
func (b *Bot) applyPlanPick(now time.Time, data, who string) (dayPlan, string, bool, error) {
	date, v, ok := parsePlanPick(data)
	if !ok {
		return dayPlan{}, "Невірні дані", false, nil
	}
	if date < now.Format(time.DateOnly) {
		return dayPlan{}, "Цей день уже минув", false, nil
	}
	day, ok := localDay(date, now.Location())
	if !ok {
		return dayPlan{}, "Невірні дані", false, nil
	}
	before, err := b.store.MealsOn(day.AddDate(0, 0, -1).Format(time.DateOnly))
	if err != nil {
		return dayPlan{}, "", false, err
	}
	leftover := map[int64]bool{}
	for _, m := range before {
		leftover[m.DishID] = true
	}
	for _, meal := range menuMeals {
		ids := v.Lunch
		if meal == menu.Dinner {
			ids = v.Dinner
		}
		if len(ids) == 0 {
			continue
		}
		if _, err := b.store.SetPlan(date, string(meal), who, ids, leftover); store.IsNotFound(err) {
			return dayPlan{}, "Цієї страви вже немає", false, nil
		} else if err != nil {
			return dayPlan{}, "", false, err
		}
	}
	rows, err := b.store.MealsOn(date)
	if err != nil {
		return dayPlan{}, "", false, err
	}
	p := dayPlan{
		date:   date,
		label:  model.DayLabel(day, now),
		picked: (menuState{chosen: rows}).choiceLines(),
		done:   true,
	}
	return p, "Меню записано", true, nil
}

func (b *Bot) cmdMenu(c tele.Context) error {
	now := b.now()
	day, ok := planDayFrom(commandPayload(c.Text()), now)
	if !ok {
		return c.Send(planUsage)
	}
	msg, err := b.b.Send(c.Chat(), planThinking)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), planTimeout)
	defer cancel()
	p, err := b.buildPlan(ctx, now, day, nil, kitchen.Rand())
	if err != nil {
		b.logger.Error("bot: day menu", "date", day.Format(time.DateOnly), "err", err)
		_, err = b.b.Edit(msg, "Не вийшло скласти меню 😕")
		return err
	}
	text, markup := planView(p)
	_, err = b.b.Edit(msg, text, b.withAppButton([]any{markup, tele.ModeHTML})...)
	return err
}

func (b *Bot) onPlanPick(c tele.Context) error {
	now := b.now()
	p, toast, redraw, err := b.applyPlanPick(now, c.Data(), b.senderName(c))
	if err == nil && redraw && p.date == now.Format(time.DateOnly) {
		// Today's morning menu says what the day holds; a plan made here
		// would otherwise leave it offering what was just decided.
		if err := b.RedrawMenu(p.date); err != nil {
			b.logger.Warn("bot: redraw menu after /menu pick", "date", p.date, "err", err)
		}
	}
	return b.respondAndRedraw(c, "day menu", planPickUnique, toast, redraw, err, func() (string, *tele.ReplyMarkup) {
		return planView(p)
	})
}

// onPlanMore asks for a fresh set. The model can take a while, longer than a
// tap's answer may wait, so the toast goes first and the redraw after.
func (b *Bot) onPlanMore(c tele.Context) error {
	now := b.now()
	date := c.Data()
	day, ok := localDay(date, now.Location())
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: "Невірні дані"})
	}
	if date < now.Format(time.DateOnly) {
		return c.Respond(&tele.CallbackResponse{Text: "Цей день уже минув"})
	}
	_ = c.Respond(&tele.CallbackResponse{Text: "Думаю…"})

	ctx, cancel := context.WithTimeout(context.Background(), planTimeout)
	defer cancel()
	p, err := b.buildPlan(ctx, now, day, planVariantsFromMarkup(currentMarkup(c)), kitchen.Rand())
	if err != nil {
		b.logger.Error("bot: day menu more", "date", date, "err", err)
		return nil
	}
	text, markup := planView(p)
	return editIgnoringSame(c, text, b.withAppButton([]any{markup, tele.ModeHTML})...)
}
