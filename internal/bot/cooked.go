package bot

import (
	"context"
	"fmt"
	"html"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"familyhub/internal/cooking"
	"familyhub/internal/dish"
	"familyhub/internal/mealie"
)

// The cooking log: photograph a plate, the bot works out which recipe it is
// and writes down that it was cooked.
//
// Two entry points, mirroring how appointments are captured. A bare photo
// counts only in a private chat; in the family group it takes /cooked, because
// this bot can read every message there and running the family's snapshots
// through a vision model would be both expensive and none of its business.

// cookedPending holds a card between the recognition and the taps that follow.
// In memory, like the appointment cards: a restart loses it and the photo is
// simply re-sent.
type cookedPending struct {
	mu    sync.Mutex
	seq   int64
	items map[string]*cookedEntry
}

type cookedEntry struct {
	photo []byte
	ext   string
	mime  string
	cook  string
	guess dish.Guess
	// dropped holds the sides the cook has un-ticked. Absence means included:
	// everything the model saw on the plate is recorded unless it is waved
	// off, so the common case is one tap on one button.
	dropped map[string]bool
	main    int // index into guess.Candidates
	slot    cooking.Slot
	date    time.Time // local midnight of the day it was cooked
	created time.Time

	// recipe and result are set once the meal has been written down, so the
	// follow-up "make it the main picture" tap knows what it is acting on and
	// can redraw the card instead of patching its text.
	recipe   mealie.Recipe
	result   cooking.Result
	recorded bool
}

func newCookedPending() *cookedPending {
	return &cookedPending{items: make(map[string]*cookedEntry)}
}

func (p *cookedPending) put(e *cookedEntry, now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, old := range p.items {
		if now.Sub(old.created) > time.Hour {
			delete(p.items, k)
		}
	}
	p.seq++
	key := strconv.FormatInt(p.seq, 36)
	e.created = now
	p.items[key] = e
	return key
}

func (p *cookedPending) get(key string) (*cookedEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[key]
	return e, ok
}

// claim marks the card as written down, returning false if it already was.
// Two taps on the same card must not produce two timeline entries, and the
// card is deliberately kept afterwards so the photo stays available to the
// "make it the main picture" button.
// release undoes a claim after a failed write, so the same card can be tapped
// again rather than forcing the cook to re-send the photo.
func (p *cookedPending) release(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.items[key]; ok {
		e.recorded = false
	}
}

func (p *cookedPending) claim(key string) (*cookedEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[key]
	if !ok || e.recorded {
		return nil, false
	}
	e.recorded = true
	return e, true
}

// onPhoto is the main path. A caption is a hint, never a command: it may be in
// Russian while the recipes are in Ukrainian, so it is handed to the model as
// context rather than matched against anything here.
func (b *Bot) onPhoto(c tele.Context) error {
	caption := strings.TrimSpace(c.Message().Caption)
	cmd, rest := splitCommand(caption)
	if !isPrivate(c) && cmd != "/cooked" {
		return nil
	}
	if cmd == "/cooked" {
		caption = rest
	}

	photo, err := b.downloadPhoto(c.Message().Photo)
	if err != nil {
		b.logger.Error("bot: download photo", "err", err)
		return c.Send("Не вдалося завантажити фото 😕")
	}
	return b.recognise(c, photo, "jpg", "image/jpeg", caption)
}

// cmdCooked is the text-only path: no picture, just "we had deruny for lunch".
// It works in the group as well, because the command is the explicit ask.
func (b *Bot) cmdCooked(c tele.Context) error {
	text := commandPayload(c.Text())
	if text == "" {
		return c.Send("Що приготували? Напиши, наприклад: /cooked драники на обед\nАбо просто надішли фото тарілки.")
	}
	return b.recognise(c, nil, "", "", text)
}

func (b *Bot) recognise(c tele.Context, photo []byte, ext, mime, caption string) error {
	now := b.now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	recipes, err := b.cfg.Cooking.Catalogue(ctx)
	if err != nil {
		b.logger.Error("bot: mealie catalogue", "err", err)
		return c.Send("Не дістав список рецептів з Mealie 😕")
	}

	guess, err := b.cfg.Dish.Identify(ctx, dish.Input{
		Photo:      photo,
		Mime:       mime,
		Caption:    caption,
		Now:        now,
		Recipes:    recipes,
		Categories: b.organizerNames(ctx, b.cfg.Cooking.Categories),
		Tags:       b.organizerNames(ctx, b.cfg.Cooking.Tags),
	})
	if err != nil {
		b.logger.Error("bot: identify dish", "err", err)
		return c.Send("Не вдалося розпізнати страву 😕 Спробуй ще раз або підкажи назву.")
	}
	if len(guess.Candidates) == 0 && guess.NewName == "" {
		return c.Send("Не зрозумів, що це за страва. Спробуй підказати назву: /cooked <назва>")
	}

	e := &cookedEntry{
		photo: photo, ext: ext, mime: mime,
		cook:    b.senderName(c),
		guess:   guess,
		dropped: map[string]bool{},
		slot:    slotFrom(guess.Slot, now),
		date:    dateFrom(guess.Date, now, b.cfg.Loc),
	}
	key := b.cookedCards.put(e, now)
	text, markup := b.cookedCard(key, e)
	return c.Send(text, markup, tele.ModeHTML)
}

// organizerNames is the list of names the model may pick from when it proposes
// a new recipe. A lookup failure is not fatal: an unfiled recipe is still the
// dish, and the alternative — refusing to record a meal because a tag list did
// not load — helps nobody.
func (b *Bot) organizerNames(ctx context.Context, load func(context.Context) ([]mealie.Organizer, error)) []string {
	items, err := load(ctx)
	if err != nil {
		b.logger.Warn("bot: mealie organizers", "err", err)
		return nil
	}
	out := make([]string, 0, len(items))
	for _, o := range items {
		out = append(out, o.Name)
	}
	return out
}

func (b *Bot) cookedCard(key string, e *cookedEntry) (string, *tele.ReplyMarkup) {
	var sb strings.Builder
	if main, ok := e.mainDish(); ok {
		fmt.Fprintf(&sb, "🍽 <b>%s</b>", html.EscapeString(main.Name))
		for _, side := range e.chosenAlongside() {
			fmt.Fprintf(&sb, " + %s", html.EscapeString(side.Name))
		}
	} else {
		fmt.Fprintf(&sb, "🍽 Нової страви немає в базі: <b>%s</b>", html.EscapeString(e.guess.NewName))
	}
	fmt.Fprintf(&sb, "\n%s · %s", dayLabel(e.date, b.now()), strings.ToLower(e.slot.Title()))
	if e.guess.Note != "" {
		fmt.Fprintf(&sb, "\n<i>%s</i>", html.EscapeString(e.guess.Note))
	}

	m := &tele.ReplyMarkup{}
	var rows []tele.Row

	// One button writes the whole plate down. Everything below it is a
	// correction, and corrections only redraw the card.
	if _, ok := e.mainDish(); ok {
		rows = append(rows, m.Row(m.Data("✓ Зафіксувати", "ckd_ok", key)))
	}
	if e.guess.NewName != "" {
		rows = append(rows, m.Row(m.Data("➕ Створити «"+e.guess.NewName+"»", "ckd_new", key)))
	}

	// Alternatives replace the main dish; sides are added to it. Two
	// different questions, so they never share a row.
	var alts []tele.Btn
	for i, cand := range e.guess.Candidates {
		if i == e.main {
			continue
		}
		alts = append(alts, m.Data("↔ "+cand.Recipe.Name, "ckd_main", key, strconv.Itoa(i)))
	}
	if len(alts) > 0 {
		rows = append(rows, m.Row(alts...))
	}

	var sides []tele.Btn
	for _, cand := range e.guess.Alongside {
		if main, ok := e.mainDish(); ok && main.Slug == cand.Recipe.Slug {
			continue
		}
		mark := "✓ "
		if e.dropped[cand.Recipe.Slug] {
			mark = "✗ "
		}
		sides = append(sides, m.Data(mark+cand.Recipe.Name, "ckd_side", key, cand.Recipe.Slug))
	}
	if len(sides) > 0 {
		rows = append(rows, m.Row(sides...))
	}

	rows = append(rows, m.Row(
		b.dayButton(m, key, e, 0, "Сьогодні"),
		b.dayButton(m, key, e, 1, "Вчора"),
		b.dayButton(m, key, e, 2, "Позавчора"),
	))
	rows = append(rows, m.Row(
		b.slotButton(m, key, e, cooking.SlotLunch),
		b.slotButton(m, key, e, cooking.SlotDinner),
		m.Data("✕ Скасувати", "ckd_cancel", key),
	))
	m.Inline(rows...)
	return sb.String(), m
}

// mainDish is the candidate currently chosen as the dish of the meal.
func (e *cookedEntry) mainDish() (mealie.Recipe, bool) {
	if e.main < 0 || e.main >= len(e.guess.Candidates) {
		return mealie.Recipe{}, false
	}
	return e.guess.Candidates[e.main].Recipe, true
}

// chosenAlongside are the sides that will be recorded: everything the model saw,
// less what was un-ticked, less whatever is currently the main dish — the
// same recipe must not be written down twice for one meal.
func (e *cookedEntry) chosenAlongside() []mealie.Recipe {
	main, hasMain := e.mainDish()
	var out []mealie.Recipe
	for _, c := range e.guess.Alongside {
		if e.dropped[c.Recipe.Slug] {
			continue
		}
		if hasMain && c.Recipe.Slug == main.Slug {
			continue
		}
		out = append(out, c.Recipe)
	}
	return out
}

func (b *Bot) dayButton(m *tele.ReplyMarkup, key string, e *cookedEntry, back int, label string) tele.Btn {
	want := midnight(b.now().AddDate(0, 0, -back), b.cfg.Loc)
	if e.date.Equal(want) {
		label = "• " + label
	}
	return m.Data(label, "ckd_day", key, strconv.Itoa(back))
}

func (b *Bot) slotButton(m *tele.ReplyMarkup, key string, e *cookedEntry, slot cooking.Slot) tele.Btn {
	label := slot.Title()
	if e.slot == slot {
		label = "• " + label
	}
	return m.Data(label, "ckd_slot", key, string(slot))
}

// onCookedDay and onCookedSlot only redraw the card; nothing is written until
// the confirmation tap.
func (b *Bot) onCookedDay(c tele.Context) error {
	key, arg := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok {
		return b.cardExpired(c)
	}
	back, _ := strconv.Atoi(arg)
	e.date = midnight(b.now().AddDate(0, 0, -back), b.cfg.Loc)
	_ = c.Respond()
	text, markup := b.cookedCard(key, e)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (b *Bot) onCookedSlot(c tele.Context) error {
	key, arg := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok {
		return b.cardExpired(c)
	}
	e.slot = cooking.Slot(arg)
	_ = c.Respond()
	text, markup := b.cookedCard(key, e)
	return c.Edit(text, markup, tele.ModeHTML)
}

// onCookedPickMain swaps which candidate is the dish of the meal.
func (b *Bot) onCookedPickMain(c tele.Context) error {
	key, arg := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok {
		return b.cardExpired(c)
	}
	idx, err := strconv.Atoi(arg)
	if err != nil || idx < 0 || idx >= len(e.guess.Candidates) {
		return b.cardExpired(c)
	}
	e.main = idx
	_ = c.Respond()
	text, markup := b.cookedCard(key, e)
	return c.Edit(text, markup, tele.ModeHTML)
}

// onCookedSide includes or drops one of the dishes that came with the main.
func (b *Bot) onCookedSide(c tele.Context) error {
	key, slug := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok {
		return b.cardExpired(c)
	}
	e.dropped[slug] = !e.dropped[slug]
	_ = c.Respond()
	text, markup := b.cookedCard(key, e)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (b *Bot) onCookedCancel(c tele.Context) error {
	key, _ := splitCookedData(c.Data())
	b.cookedCards.claim(key) // consume it so a late tap cannot record
	_ = c.Respond()
	return c.Edit("✕ Скасовано", b.appMarkup())
}

// onCookedConfirm writes the meal down against one of the offered recipes.
func (b *Bot) onCookedConfirm(c tele.Context) error {
	key, _ := splitCookedData(c.Data())
	e, ok := b.cookedCards.claim(key)
	if !ok {
		return b.cardExpired(c)
	}
	main, ok := e.mainDish()
	if !ok {
		return b.cardExpired(c)
	}
	e.recipe = main
	return b.writeCooked(c, key, e)
}

// onCookedNew creates the recipe first, then records the meal against it.
func (b *Bot) onCookedNew(c tele.Context) error {
	key, _ := splitCookedData(c.Data())
	e, ok := b.cookedCards.claim(key)
	if !ok {
		return b.cardExpired(c)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_ = c.Respond(&tele.CallbackResponse{Text: "Створюю рецепт…"})
	rec, err := b.cfg.Cooking.Create(ctx, cooking.NewRecipe{
		Name:     e.guess.NewName,
		Category: e.guess.Category,
		Tags:     e.guess.Tags,
	})
	if err != nil {
		b.logger.Error("bot: create recipe", "err", err, "name", e.guess.NewName)
		if rec.Slug == "" {
			return c.Edit("Не вдалося створити рецепт 😕 " + html.EscapeString(e.guess.NewName))
		}
		// Created but not filed: keep going, the meal is still worth recording.
	}
	e.recipe = rec
	return b.writeCooked(c, key, e)
}

func (b *Bot) writeCooked(c tele.Context, key string, e *cookedEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := b.cfg.Cooking.Do(ctx, cooking.Record{
		Main:      e.recipe,
		Alongside: e.chosenAlongside(),
		Slot:      e.slot,
		At:        cookedAt(e.date, e.slot),
		Cook:      e.cook,
		Note:      e.guess.Note,
		Photo:     e.photo,
		Ext:       e.ext,
	})
	if err != nil {
		b.logger.Error("bot: record cooked", "err", err, "recipe", e.recipe.Slug, "step", res.FailedAt)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не записалося"})
		// Name the step rather than saying "error": these writes are not a
		// transaction, so which one failed says what did land. The claim is
		// released so the same card can be tapped again — the photo is still
		// in it, and re-sending it would be the only alternative.
		b.cookedCards.release(key)
		m := &tele.ReplyMarkup{}
		m.Inline(m.Row(m.Data("↻ Повторити", "ckd_ok", key), m.Data("✕ Скасувати", "ckd_cancel", key)))
		return c.Edit(fmt.Sprintf("⚠️ <b>%s</b> — не вдалося: %s", html.EscapeString(e.recipe.Name), res.FailedAt), m, tele.ModeHTML)
	}

	if res.AlreadyDone {
		_ = c.Respond(&tele.CallbackResponse{Text: "Вже записано"})
		return c.Edit(fmt.Sprintf("✓ <b>%s</b> · %s · %s — вже було записано",
			html.EscapeString(e.recipe.Name), e.date.Format("02.01"),
			strings.ToLower(e.slot.Title())), b.appMarkup(), tele.ModeHTML)
	}

	e.result = res
	_ = c.Respond(&tele.CallbackResponse{Text: "Записано"})
	return c.Edit(b.cookedDone(e, res), b.cookedDoneMarkup(key, e, res), tele.ModeHTML)
}

func (b *Bot) cookedDone(e *cookedEntry, res cooking.Result) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "✓ <b>%s</b>", html.EscapeString(e.recipe.Name))
	for _, name := range res.Alongside {
		fmt.Fprintf(&sb, " + %s", html.EscapeString(name))
	}
	fmt.Fprintf(&sb, " · %s · %s", e.date.Format("02.01"), strings.ToLower(e.slot.Title()))
	switch {
	case res.MadeMain:
		sb.WriteString("\nфото додано і стало головним")
	case len(e.photo) > 0:
		sb.WriteString("\nфото додано в історію")
	}
	if res.RecipeURL != "" {
		fmt.Fprintf(&sb, "\n<a href=%q>відкрити в Mealie</a>", res.RecipeURL)
	}
	return sb.String()
}

// cookedDoneMarkup offers the promotion only when it is a real choice: the
// recipe already had a photograph of its own and this one stayed in history.
func (b *Bot) cookedDoneMarkup(key string, e *cookedEntry, res cooking.Result) *tele.ReplyMarkup {
	if len(e.photo) == 0 || res.MadeMain {
		return b.appMarkup()
	}
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data("🖼 Зробити головним", "ckd_promote", key)))
	return m
}

func (b *Bot) onCookedPromote(c tele.Context) error {
	key, _ := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok || len(e.photo) == 0 {
		return b.cardExpired(c)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := b.cfg.Cooking.PromotePhoto(ctx, e.recipe.Slug, e.photo, e.ext); err != nil {
		b.logger.Error("bot: promote photo", "err", err, "recipe", e.recipe.Slug)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не вдалося"})
		return nil
	}
	e.result.MadeMain = true
	_ = c.Respond(&tele.CallbackResponse{Text: "Готово"})
	// Rendered again rather than patched in place: the card carries bold text
	// and a link, and editing the plain text back in would drop both.
	return c.Edit(b.cookedDone(e, e.result), b.appMarkup(), tele.ModeHTML)
}

func (b *Bot) cardExpired(c tele.Context) error {
	_ = c.Respond(&tele.CallbackResponse{Text: "Картка застаріла — надішли фото ще раз"})
	return nil
}

func (b *Bot) downloadPhoto(p *tele.Photo) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("no photo in message")
	}
	rc, err := b.b.File(&p.File)
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read-only
	return io.ReadAll(rc)
}

// slotFrom trusts the model when it read a meal out of the hint, and otherwise
// reads the clock: this household eats twice a day, so anything before late
// afternoon is lunch.
func slotFrom(modelSlot string, now time.Time) cooking.Slot {
	switch cooking.Slot(modelSlot) {
	case cooking.SlotLunch:
		return cooking.SlotLunch
	case cooking.SlotDinner:
		return cooking.SlotDinner
	}
	if now.Hour() < 16 {
		return cooking.SlotLunch
	}
	return cooking.SlotDinner
}

// dateFrom accepts only the last week from the model: a date further out than
// that is a misread hint ("5.09" in a year the model guessed), and the wrong
// day silently entering the record is worse than the cook tapping "Вчора".
func dateFrom(modelDate string, now time.Time, loc *time.Location) time.Time {
	today := midnight(now, loc)
	d, err := time.ParseInLocation("2006-01-02", modelDate, loc)
	if err != nil {
		return today
	}
	if d.After(today) || today.Sub(d) > 7*24*time.Hour {
		return today
	}
	return d
}

// cookedAt pins a meal to a canonical hour rather than the moment the photo
// was sent. The record answers "which day, which meal"; a plate photographed
// at 15:40 was still lunch, and a fixed hour keeps the timeline ordered by
// meal instead of by when someone got round to posting.
func cookedAt(date time.Time, slot cooking.Slot) time.Time {
	hour := 13
	if slot == cooking.SlotDinner {
		hour = 19
	}
	return time.Date(date.Year(), date.Month(), date.Day(), hour, 0, 0, 0, date.Location())
}

func midnight(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

func dayLabel(date, now time.Time) string {
	switch days := int(midnight(now, date.Location()).Sub(date).Hours() / 24); days {
	case 0:
		return "сьогодні"
	case 1:
		return "вчора"
	case 2:
		return "позавчора"
	default:
		return date.Format("02.01")
	}
}

// splitCookedData splits "key|arg" callback payloads. telebot joins the strings
// passed to Data with "|", and everything here uses at most one argument.
func splitCookedData(data string) (key, arg string) {
	parts := strings.SplitN(strings.TrimSpace(data), "|", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

// splitCommand pulls a leading /command off a caption, tolerating the
// "/cooked@bot" form groups produce.
func splitCommand(text string) (cmd, rest string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	cmd = text
	if i := strings.IndexAny(text, " \n\t"); i >= 0 {
		cmd, rest = text[:i], strings.TrimSpace(text[i+1:])
	}
	if i := strings.Index(cmd, "@"); i >= 0 {
		cmd = cmd[:i]
	}
	return cmd, rest
}
