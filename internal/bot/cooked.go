package bot

import (
	"context"
	"errors"
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

// The cooking log: photograph a plate, the bot works out which recipes are on
// it and writes down that they were cooked.
//
// Two entry points, mirroring how appointments are captured. A bare photo
// counts only in a private chat; in the family group it takes /cooked, because
// this bot can read every message there and running the family's snapshots
// through a vision model would be both expensive and none of its business.
//
// The card reads the plate as a list of dishes, not as one dish with
// runners-up. A meal of meat, porridge and salad is three lines and three
// buttons, each answering "is this the right recipe?" for its own dish —
// the earlier card offered the whole plate as alternatives to each other,
// which made the porridge look like a rival reading of the meat.

// cookedPending holds a card between the recognition and the taps that follow.
// In memory, like the appointment cards: a restart loses it and the photo is
// simply re-sent.
type cookedPending struct {
	mu    sync.Mutex
	seq   int64
	items map[string]*cookedEntry
}

// plateItem is one dish of the meal as the card currently has it: the model's
// reading, plus whatever the cook has since corrected.
type plateItem struct {
	dish.Item
	dropped bool
}

// title is what this dish is called on the card: the recipe's name once it is
// one, and otherwise what the model read off the plate — which is also the
// name the "create it" button would use, so a mismatch would be a lie.
func (it plateItem) title() string {
	if it.Known() {
		return it.Recipe.Name
	}
	return it.Name
}

// usable dishes are the ones that will be written down: still on the plate and
// already a recipe.
func (it plateItem) usable() bool { return !it.dropped && it.Known() }

// doubt marks a match the model was not sure of. A recipe it settled on
// because nothing closer was on the list looks exactly like one it recognised,
// and the difference only shows up later, in the history of a dish nobody
// cooked — so the card says which of the two this is.
func (it plateItem) doubt() string {
	if it.Known() && it.Confidence == "low" {
		return " <i>(не точно)</i>"
	}
	return ""
}

type cookedEntry struct {
	photo []byte
	ext   string
	mime  string
	cook  string
	note  string
	items []plateItem
	// main is the dish the meal is recorded against, -1 until the cook picks
	// one by hand: the model already puts the main dish first.
	main int
	// focus is the dish whose own card is on screen, -1 for the whole plate.
	// Sorting a dish out is a question about that dish, so it gets the screen
	// to itself rather than another row on an already tall card.
	focus   int
	slot    cooking.Slot
	date    time.Time // local midnight of the day it was cooked
	created time.Time

	// recipe and result are set once the meal has been written down, so the
	// follow-up "make it the main picture" tap knows what it is acting on and
	// can redraw the card instead of patching its text.
	recipe   mealie.Recipe
	result   cooking.Result
	recorded bool
	// busy guards the one tap that writes to Mealie before the meal is
	// recorded — creating a recipe — against a double tap creating it twice.
	busy bool
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

// release undoes a claim after a failed write, so the same card can be tapped
// again rather than forcing the cook to re-send the photo.
func (p *cookedPending) release(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.items[key]; ok {
		e.recorded = false
	}
}

// begin and end bracket a slow write, so the second of two impatient taps
// finds the card busy instead of repeating it.
func (p *cookedPending) begin(key string) (*cookedEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[key]
	if !ok || e.busy || e.recorded {
		return nil, false
	}
	e.busy = true
	return e, true
}

func (p *cookedPending) end(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.items[key]; ok {
		e.busy = false
	}
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
	if len(guess.Items) == 0 {
		return c.Send("Не зрозумів, що це за страва. Спробуй підказати назву: /cooked <назва>")
	}

	e := &cookedEntry{
		photo: photo, ext: ext, mime: mime,
		cook:  b.senderName(c),
		note:  guess.Note,
		main:  -1,
		focus: -1,
		slot:  slotFrom(guess.Slot, now),
		date:  dateFrom(guess.Date, now, b.cfg.Loc),
	}
	for _, it := range guess.Items {
		e.items = append(e.items, plateItem{Item: it})
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

// mainIndex is the dish the meal is recorded against: the cook's pick when
// they made one, otherwise the first dish on the plate the database knows.
func (e *cookedEntry) mainIndex() int {
	if e.main >= 0 && e.main < len(e.items) && e.items[e.main].usable() {
		return e.main
	}
	for i, it := range e.items {
		if it.usable() {
			return i
		}
	}
	return -1
}

func (e *cookedEntry) mainDish() (mealie.Recipe, bool) {
	i := e.mainIndex()
	if i < 0 {
		return mealie.Recipe{}, false
	}
	return e.items[i].Recipe, true
}

// chosenAlongside is everything else that will be written down: on the plate,
// in the database, and not already the main dish.
func (e *cookedEntry) chosenAlongside() []mealie.Recipe {
	main := e.mainIndex()
	var out []mealie.Recipe
	for i, it := range e.items {
		if i == main || !it.usable() {
			continue
		}
		out = append(out, it.Recipe)
	}
	return out
}

// unknown are the dishes still waiting for a decision: eaten, but not a recipe
// yet. They are why the card cannot simply be confirmed.
func (e *cookedEntry) unknown() []int {
	var out []int
	for i, it := range e.items {
		if !it.dropped && !it.Known() {
			out = append(out, i)
		}
	}
	return out
}

func (b *Bot) cookedCard(key string, e *cookedEntry) (string, *tele.ReplyMarkup) {
	if e.focus >= 0 && e.focus < len(e.items) {
		return b.cookedItemCard(key, e)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🍽 %s · %s", dayLabel(e.date, b.now()), strings.ToLower(e.slot.Title()))
	main := e.mainIndex()
	for i, it := range e.items {
		name := html.EscapeString(it.title())
		switch {
		case it.dropped:
			fmt.Fprintf(&sb, "\n%d. <s>%s</s>", i+1, name)
		case i == main:
			fmt.Fprintf(&sb, "\n%d. <b>%s</b>%s", i+1, name, it.doubt())
		case !it.Known():
			fmt.Fprintf(&sb, "\n%d. %s — <i>немає в базі</i>", i+1, name)
		default:
			fmt.Fprintf(&sb, "\n%d. %s%s", i+1, name, it.doubt())
		}
	}
	if e.note != "" {
		fmt.Fprintf(&sb, "\n<i>%s</i>", html.EscapeString(e.note))
	}

	m := &tele.ReplyMarkup{}
	var rows []tele.Row

	// One button writes the plate down — but only once there is something to
	// write it against, and it says so when a dish is being left out.
	if _, ok := e.mainDish(); ok {
		label := "✓ Зафіксувати"
		if len(e.unknown()) > 0 {
			label = "✓ Зафіксувати без нових"
		}
		rows = append(rows, m.Row(m.Data(label, "ckd_ok", key)))
	}
	// A dish the database does not have is one tap from being added: this is
	// the question the card is really asking when it cannot be confirmed.
	for _, i := range e.unknown() {
		rows = append(rows, m.Row(m.Data("➕ Створити «"+e.items[i].Name+"»", "ckd_new", key, strconv.Itoa(i))))
	}
	// And one button per dish for "this is not what you think it is".
	var picks []tele.Btn
	for i, it := range e.items {
		picks = append(picks, m.Data(fmt.Sprintf("%d. %s", i+1, shorten(it.title(), 18)), "ckd_item", key, strconv.Itoa(i)))
	}
	if len(picks) > 0 {
		rows = append(rows, m.Row(picks...))
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

// cookedItemCard is one dish on its own: what the bot made of it, and every
// way the cook can say otherwise.
func (b *Bot) cookedItemCard(key string, e *cookedEntry) (string, *tele.ReplyMarkup) {
	it := e.items[e.focus]

	var sb strings.Builder
	fmt.Fprintf(&sb, "Страва %d з %d: <b>%s</b>\n", e.focus+1, len(e.items), html.EscapeString(it.title()))
	switch {
	case it.dropped:
		sb.WriteString("прибрана з тарілки")
	case !it.Known():
		sb.WriteString("немає в базі")
	case e.mainIndex() == e.focus:
		sb.WriteString("головна страва цього прийому їжі")
	default:
		sb.WriteString("є в базі, запишеться разом з головною")
	}

	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	if !it.Known() && it.Name != "" {
		rows = append(rows, m.Row(m.Data("➕ Створити «"+it.Name+"»", "ckd_new", key, strconv.Itoa(e.focus))))
	}
	for j, alt := range it.Alts {
		rows = append(rows, m.Row(m.Data("↔ Це "+alt.Name, "ckd_pick", key, strconv.Itoa(j))))
	}
	if it.usable() && e.mainIndex() != e.focus {
		rows = append(rows, m.Row(m.Data("⭐ Зробити головною", "ckd_main", key)))
	}
	drop := "🗑 Не було цього"
	if it.dropped {
		drop = "↩ Повернути на тарілку"
	}
	rows = append(rows, m.Row(m.Data(drop, "ckd_drop", key), m.Data("← Назад", "ckd_back", key)))
	m.Inline(rows...)
	return sb.String(), m
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

// redraw is what every correcting tap does: nothing is written until the
// confirmation.
//
// Tapping the day the card already shows leaves it byte-identical, which
// Telegram answers with an error rather than a shrug. The tap did what it
// said it would — the card already says today — so it is not one.
func (b *Bot) redraw(c tele.Context, key string, e *cookedEntry) error {
	_ = c.Respond()
	text, markup := b.cookedCard(key, e)
	if err := c.Edit(text, markup, tele.ModeHTML); err != nil && !errors.Is(err, tele.ErrSameMessageContent) {
		return err
	}
	return nil
}

func (b *Bot) onCookedDay(c tele.Context) error {
	key, arg := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok {
		return b.cardExpired(c)
	}
	back, _ := strconv.Atoi(arg)
	e.date = midnight(b.now().AddDate(0, 0, -back), b.cfg.Loc)
	return b.redraw(c, key, e)
}

func (b *Bot) onCookedSlot(c tele.Context) error {
	key, arg := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok {
		return b.cardExpired(c)
	}
	e.slot = cooking.Slot(arg)
	return b.redraw(c, key, e)
}

// onCookedItem opens one dish; onCookedBack closes it again.
func (b *Bot) onCookedItem(c tele.Context) error {
	key, arg := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok {
		return b.cardExpired(c)
	}
	idx, err := strconv.Atoi(arg)
	if err != nil || idx < 0 || idx >= len(e.items) {
		return b.cardExpired(c)
	}
	e.focus = idx
	return b.redraw(c, key, e)
}

func (b *Bot) onCookedBack(c tele.Context) error {
	key, _ := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok {
		return b.cardExpired(c)
	}
	e.focus = -1
	return b.redraw(c, key, e)
}

// onCookedPick takes one of the other readings of the dish being sorted out.
// The reading it replaces stays on offer: the cook has to be able to change
// their mind back without re-sending the photo.
func (b *Bot) onCookedPick(c tele.Context) error {
	key, arg := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok || e.focus < 0 || e.focus >= len(e.items) {
		return b.cardExpired(c)
	}
	it := &e.items[e.focus]
	j, err := strconv.Atoi(arg)
	if err != nil || j < 0 || j >= len(it.Alts) {
		return b.cardExpired(c)
	}
	alt := it.Alts[j]
	if it.Known() {
		it.Alts[j] = it.Recipe
	} else {
		it.Alts = append(it.Alts[:j], it.Alts[j+1:]...)
	}
	it.Recipe = alt
	e.focus = -1
	return b.redraw(c, key, e)
}

// onCookedMakeMain moves the meal's entry — and the photograph with it — onto
// this dish.
func (b *Bot) onCookedMakeMain(c tele.Context) error {
	key, _ := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok || e.focus < 0 || e.focus >= len(e.items) {
		return b.cardExpired(c)
	}
	e.main = e.focus
	e.focus = -1
	return b.redraw(c, key, e)
}

// onCookedDrop takes a dish off the plate, or puts it back: the model reads
// one dish too many often enough that this is the common correction.
func (b *Bot) onCookedDrop(c tele.Context) error {
	key, _ := splitCookedData(c.Data())
	e, ok := b.cookedCards.get(key)
	if !ok || e.focus < 0 || e.focus >= len(e.items) {
		return b.cardExpired(c)
	}
	e.items[e.focus].dropped = !e.items[e.focus].dropped
	e.focus = -1
	return b.redraw(c, key, e)
}

func (b *Bot) onCookedCancel(c tele.Context) error {
	key, _ := splitCookedData(c.Data())
	b.cookedCards.claim(key) // consume it so a late tap cannot record
	_ = c.Respond()
	return c.Edit("✕ Скасовано", b.appMarkup())
}

// onCookedNew adds one dish to the database and hands the card back. It
// deliberately does not record the meal as well: the recipe is one decision
// and the plate is another, and a card that writes the meal down the moment a
// side dish is created is the card that made the buttons unpredictable.
//
// The recipe outlives a cancelled card, which is the right way round: the
// dish was cooked, and an empty recipe nobody records against is a line in
// Mealie, not a wrong entry in the history.
func (b *Bot) onCookedNew(c tele.Context) error {
	key, arg := splitCookedData(c.Data())
	e, ok := b.cookedCards.begin(key)
	if !ok {
		return b.cardExpired(c)
	}
	defer b.cookedCards.end(key)

	idx, err := strconv.Atoi(arg)
	if err != nil || idx < 0 || idx >= len(e.items) {
		return b.cardExpired(c)
	}
	it := &e.items[idx]
	if it.Known() { // a second tap on a button that already did its work
		return b.redraw(c, key, e)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_ = c.Respond(&tele.CallbackResponse{Text: "Створюю рецепт…"})
	rec, err := b.cfg.Cooking.Create(ctx, cooking.NewRecipe{
		Name:     it.Name,
		Category: it.Category,
		Tags:     it.Tags,
	})
	if err != nil {
		b.logger.Error("bot: create recipe", "err", err, "name", it.Name)
		if rec.Slug == "" {
			return c.Edit("Не вдалося створити рецепт 😕 " + html.EscapeString(it.Name))
		}
		// Created but not filed: keep going, the dish is still the dish.
	}
	it.Recipe = rec
	e.focus = -1
	text, markup := b.cookedCard(key, e)
	return c.Edit(text, markup, tele.ModeHTML)
}

// onCookedConfirm writes the meal down: one entry for the main dish, one for
// everything else still on the plate.
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

func (b *Bot) writeCooked(c tele.Context, key string, e *cookedEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := b.cfg.Cooking.Do(ctx, cooking.Record{
		Main:      e.recipe,
		Alongside: e.chosenAlongside(),
		Slot:      e.slot,
		At:        cookedAt(e.date, e.slot),
		Cook:      e.cook,
		Note:      e.note,
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
	// A dish nobody added is a dish nobody recorded. Said plainly here, where
	// it is still fresh enough to fix, rather than discovered in Mealie later.
	if left := e.unknown(); len(left) > 0 {
		var names []string
		for _, i := range left {
			names = append(names, html.EscapeString(e.items[i].Name))
		}
		fmt.Fprintf(&sb, "\nне записано (немає в базі): %s", strings.Join(names, ", "))
	}
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

// shorten keeps a dish's name inside a button that shares its row with two
// others. Telegram will not wrap it: what does not fit is simply not read.
func shorten(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max-1])) + "…"
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
