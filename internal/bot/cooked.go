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

	"familyhub/internal/dish"
	"familyhub/internal/menu"
	"familyhub/internal/model"
)

// The cooking log: photograph a plate, the bot works out which of the family's
// dishes are on it and writes down that they were eaten.
//
// Two entry points, mirroring how appointments are captured. A bare photo
// counts only in a private chat; in the family group it takes /cooked, because
// this bot can read every message there and running the family's snapshots
// through a vision model would be both expensive and none of its business.
//
// The card reads the plate as a list of dishes, not as one dish with
// runners-up. A meal of meat, porridge and salad is three lines and three
// buttons, each answering "is this the right dish?" for its own dish —
// the earlier card offered the whole plate as alternatives to each other,
// which made the porridge look like a rival reading of the meat.
//
// Every dish on the plate is written down the same way, as its own eaten row
// of the meals table, so the card has no main dish to choose: which dish came
// first changes nothing that is stored. The photo is used for the recognition
// and then dropped.

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

// title is what this dish is called on the card: the catalogue's name once it
// is a dish there, and otherwise what the model read off the plate — which is
// also the name the "create it" button would use, so a mismatch would be a lie.
func (it plateItem) title() string {
	if it.Known() {
		return it.Dish.Name
	}
	return it.Name
}

// usable dishes are the ones that will be written down: still on the plate and
// already in the catalogue.
func (it plateItem) usable() bool { return !it.dropped && it.Known() }

// doubt marks a match the model was not sure of. A dish it settled on
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
	cook  string
	note  string
	items []plateItem
	// focus is the dish whose own card is on screen, -1 for the whole plate.
	// Sorting a dish out is a question about that dish, so it gets the screen
	// to itself rather than another row on an already tall card.
	focus   int
	meal    menu.Meal
	date    time.Time // local midnight of the day it was cooked
	created time.Time

	recorded bool
	// busy guards the one tap that writes before the meal is recorded —
	// creating a dish — against a double tap racing it.
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
// Two taps on the same card must not write the meal twice; the card itself is
// kept until it ages out, so a late tap finds it recorded rather than expired.
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
// Russian while the dishes are in Ukrainian, so it is handed to the model as
// context rather than matched against anything here.
//
// A photo from someone who just tapped "Інше" on the evening check is the
// answer to it, and is taken before the /cooked gate: they were asked for a
// picture, so in the group it needs no command either.
func (b *Bot) onPhoto(c tele.Context) error {
	caption := strings.TrimSpace(c.Message().Caption)
	cmd, rest := splitCommand(caption)
	var pinned *plateFor
	if e, ok := b.awaiting.takeMealOther(senderID(c), b.now()); ok {
		pinned = &e.plateFor
	} else if !isPrivate(c) && cmd != "/cooked" {
		return nil
	}
	if cmd == "/cooked" {
		caption = rest
	}

	photo, err := b.downloadPhoto(c.Message().Photo)
	if err != nil {
		b.logger.Error("bot: download photo", "err", err)
		if again := pinned.again(); again != nil {
			b.awaiting.setMealOther(senderID(c), *again, b.now())
		}
		return c.Send("Не вдалося завантажити фото 😕")
	}
	return b.recognise(c, photo, "image/jpeg", caption, pinned)
}

// cmdCooked is the text-only path: no picture, just "we had deruny for lunch".
// It works in the group as well, because the command is the explicit ask.
func (b *Bot) cmdCooked(c tele.Context) error {
	text := commandPayload(c.Text())
	if text == "" {
		return c.Send("Що приготували? Напиши, наприклад: /cooked драники на обед\nАбо просто надішли фото тарілки.")
	}
	return b.recognise(c, nil, "", text, nil)
}

// plateFor pins a recognition to the meal the evening check asked about: the
// family already said which day and meal it was by tapping "Інше" under it,
// so neither the model's reading of the hint nor the clock gets a say.
type plateFor struct {
	date time.Time // local midnight
	meal menu.Meal
	// retried is the question asked a second time, after an answer the model
	// could not read.
	retried bool
}

// again is the question to arm once more after an answer that could not be
// read, or nil. Only once: whatever the person writes next in the group goes
// to the model while the question is armed, so re-arming on every failure
// would turn their ordinary chat into a paid call and a "could not read"
// each, for as long as they kept talking. The buttons under the check are
// still there after the second miss.
func (p *plateFor) again() *plateFor {
	if p == nil || p.retried {
		return nil
	}
	return &plateFor{date: p.date, meal: p.meal, retried: true}
}

// recognise asks the model about the plate and shows the card. pinned is nil
// for the cooking log's own entry points.
//
// An answer to the evening check that the model could not read is asked
// again once rather than dropped (see plateFor.again): take() already cleared
// the question, and the buttons under the check are still there for whoever
// would rather tap.
func (b *Bot) recognise(c tele.Context, photo []byte, mime, caption string, pinned *plateFor) error {
	now := b.now()
	retry := func() error {
		again := pinned.again()
		if again == nil {
			return c.Send("Не розпізнав 😕 Обери кнопкою під питанням.")
		}
		b.awaiting.setMealOther(senderID(c), *again, now)
		return c.Send("Не розпізнав 😕 Обери кнопкою або напиши чи надішли фото ще раз.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	catalogue, err := b.plateCatalogue()
	if err != nil {
		b.logger.Error("bot: dish catalogue", "err", err)
		if pinned != nil {
			return retry()
		}
		return c.Send("Не дістав список страв 😕")
	}

	guess, err := b.cfg.Dish.Identify(ctx, dish.Input{
		Photo:   photo,
		Mime:    mime,
		Caption: caption,
		Now:     now,
		Dishes:  catalogue,
	})
	if err != nil {
		b.logger.Error("bot: identify dish", "err", err)
		if pinned != nil {
			return retry()
		}
		return c.Send("Не вдалося розпізнати страву 😕 Спробуй ще раз або підкажи назву.")
	}
	if len(guess.Items) == 0 {
		if pinned != nil {
			return retry()
		}
		return c.Send("Не зрозумів, що це за страва. Спробуй підказати назву: /cooked <назва>")
	}

	e := plateEntry(guess, b.senderName(c), now, b.cfg.Loc, pinned)
	key := b.cookedCards.put(e, now)
	text, markup := b.cookedCard(key, e)
	return c.Send(text, markup, tele.ModeHTML)
}

// plateEntry is the card for what the model read. The day and meal come
// from the question when the plate answers one, and otherwise from the
// model's reading of the hint, then the clock.
func plateEntry(guess dish.Guess, cook string, now time.Time, loc *time.Location, pinned *plateFor) *cookedEntry {
	e := &cookedEntry{
		cook:  cook,
		note:  guess.Note,
		focus: -1,
		meal:  mealFrom(guess.Slot, now),
		date:  dateFrom(guess.Date, now, loc),
	}
	if pinned != nil {
		e.meal, e.date = pinned.meal, pinned.date
	}
	for _, it := range guess.Items {
		e.items = append(e.items, plateItem{Item: it})
	}
	return e
}

// plateCatalogue is what the model matches a plate against: the family's own
// dishes and the proposals nobody has decided on — eating one is the decision.
// Rejected dishes are left out; one that turns up on a plate anyway comes back
// as a new dish, and creating it brings the old row back to life.
func (b *Bot) plateCatalogue() ([]dish.DishRef, error) {
	dishes, err := b.store.Dishes(model.DishActive, model.DishProposed)
	if err != nil {
		return nil, err
	}
	out := make([]dish.DishRef, 0, len(dishes))
	for _, d := range dishes {
		out = append(out, dish.DishRef{ID: d.ID, Name: d.Name})
	}
	return out, nil
}

// chosen are the dishes that will be written down: on the plate and in the
// catalogue.
func (e *cookedEntry) chosen() []plateItem {
	var out []plateItem
	for _, it := range e.items {
		if it.usable() {
			out = append(out, it)
		}
	}
	return out
}

// unknown are the dishes still waiting for a decision: eaten, but not in the
// catalogue yet. They are why the card cannot simply be confirmed.
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
	fmt.Fprintf(&sb, "🍽 %s · %s", dayLabel(e.date, b.now()), strings.ToLower(e.meal.Title()))
	for i, it := range e.items {
		name := html.EscapeString(it.title())
		switch {
		case it.dropped:
			fmt.Fprintf(&sb, "\n%d. <s>%s</s>", i+1, name)
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
	// write, and it says so when a dish is being left out.
	if len(e.chosen()) > 0 {
		label := "✓ Зафіксувати"
		if len(e.unknown()) > 0 {
			label = "✓ Зафіксувати без нових"
		}
		rows = append(rows, m.Row(m.Data(label, "ckd_ok", key)))
	}
	// A dish the catalogue does not have is one tap from being added: this is
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
		b.mealButton(m, key, e, menu.Lunch),
		b.mealButton(m, key, e, menu.Dinner),
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
	default:
		sb.WriteString("є в базі, запишеться разом з рештою тарілки")
	}

	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	if !it.Known() && it.Name != "" {
		rows = append(rows, m.Row(m.Data("➕ Створити «"+it.Name+"»", "ckd_new", key, strconv.Itoa(e.focus))))
	}
	for j, alt := range it.Alts {
		rows = append(rows, m.Row(m.Data("↔ Це "+alt.Name, "ckd_pick", key, strconv.Itoa(j))))
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

func (b *Bot) mealButton(m *tele.ReplyMarkup, key string, e *cookedEntry, meal menu.Meal) tele.Btn {
	label := meal.Title()
	if e.meal == meal {
		label = "• " + label
	}
	return m.Data(label, "ckd_slot", key, string(meal))
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
	return editIgnoringSame(c, text, markup, tele.ModeHTML)
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
	// Callback data is not trusted to name a real meal; a bad one would only
	// fail later, at the write.
	if !model.ValidMeal(arg) {
		return b.cardExpired(c)
	}
	e.meal = menu.Meal(arg)
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
		it.Alts[j] = it.Dish
	} else {
		it.Alts = append(it.Alts[:j], it.Alts[j+1:]...)
	}
	it.Dish = alt
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

// onCookedNew adds one dish to the catalogue and hands the card back. It
// deliberately does not record the meal as well: the dish is one decision and
// the plate is another, and a card that writes the meal down the moment a side
// dish is created is the card that made the buttons unpredictable.
//
// The dish outlives a cancelled card, which is the right way round: it was
// cooked, and a catalogue entry nobody records against is a line in the list,
// not a wrong entry in the history.
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
	if err := b.createPlateDish(e, idx); err != nil {
		b.logger.Error("bot: create dish", "err", err, "name", e.items[idx].Name)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не вдалося створити страву"})
		return nil
	}
	e.focus = -1
	return b.redraw(c, key, e)
}

// createPlateDish puts one item of the plate into the catalogue, with the meal
// and days the model read for it. EnsureDish rather than CreateDish: the dish
// was eaten, so a proposal is accepted by it and an earlier "no" is overruled.
// A second tap on a button that already did its work changes nothing.
func (b *Bot) createPlateDish(e *cookedEntry, idx int) error {
	it := &e.items[idx]
	if it.Known() {
		return nil
	}
	d, _, err := b.store.EnsureDish(model.Dish{Name: it.Name, Meal: it.Meal, Days: it.Days})
	if err != nil {
		return err
	}
	it.Dish = dish.DishRef{ID: d.ID, Name: d.Name}
	return nil
}

// onCookedConfirm writes the meal down: one eaten row per dish on the plate.
func (b *Bot) onCookedConfirm(c tele.Context) error {
	key, _ := splitCookedData(c.Data())
	e, ok := b.cookedCards.claim(key)
	if !ok {
		return b.cardExpired(c)
	}
	if len(e.chosen()) == 0 {
		b.cookedCards.release(key)
		return b.cardExpired(c)
	}

	w, err := b.recordPlate(e)
	if err != nil {
		b.logger.Error("bot: record cooked", "err", err, "failed", w.failed)
		_ = c.Respond(&tele.CallbackResponse{Text: "Не записалося"})
		// The rows are not one transaction, so the dishes before the failed
		// one did land. Retrying is safe all the same: a dish already eaten
		// at this meal comes back as "already recorded" rather than twice.
		b.cookedCards.release(key)
		m := &tele.ReplyMarkup{}
		m.Inline(m.Row(m.Data("↻ Повторити", "ckd_ok", key), m.Data("✕ Скасувати", "ckd_cancel", key)))
		return c.Edit(fmt.Sprintf("⚠️ <b>%s</b> — не вдалося записати", html.EscapeString(w.failed)), m, tele.ModeHTML)
	}

	toast := "Записано"
	if len(w.written) == 0 {
		toast = "Вже записано"
	}
	_ = c.Respond(&tele.CallbackResponse{Text: toast})
	return c.Edit(b.cookedDone(e, w), b.appMarkup(), tele.ModeHTML)
}

// plateWrite is what recordPlate did, dish by dish, for the card to report.
type plateWrite struct {
	written []string // names newly recorded as eaten
	already []string // names that were already eaten at this meal
	failed  string   // the dish the write stopped at, on error
}

// recordPlate writes every dish on the plate as eaten at the card's day and
// meal. RecordEaten does the reconciling with the morning's plan: the planned
// dish, if it is on the plate, turns eaten, and a plan for a dish that is not
// on it is dropped.
func (b *Bot) recordPlate(e *cookedEntry) (plateWrite, error) {
	var w plateWrite
	date := e.date.Format(time.DateOnly)
	for _, it := range e.chosen() {
		_, already, err := b.store.RecordEaten(it.Dish.ID, date, string(e.meal), e.cook, false)
		if err != nil {
			w.failed = it.Dish.Name
			return w, err
		}
		if already {
			w.already = append(w.already, it.Dish.Name)
		} else {
			w.written = append(w.written, it.Dish.Name)
		}
	}
	return w, nil
}

func (b *Bot) cookedDone(e *cookedEntry, w plateWrite) string {
	when := fmt.Sprintf("%s · %s", e.date.Format("02.01"), strings.ToLower(e.meal.Title()))
	esc := func(names []string) string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = html.EscapeString(n)
		}
		return strings.Join(out, " + ")
	}

	var sb strings.Builder
	if len(w.written) > 0 {
		fmt.Fprintf(&sb, "✓ <b>%s</b> · %s", esc(w.written), when)
		if len(w.already) > 0 {
			fmt.Fprintf(&sb, "\nвже було записано: %s", esc(w.already))
		}
	} else {
		fmt.Fprintf(&sb, "✓ <b>%s</b> · %s — вже було записано", esc(w.already), when)
	}
	// A dish nobody added is a dish nobody recorded. Said plainly here, where
	// it is still fresh enough to fix, rather than discovered later.
	if left := e.unknown(); len(left) > 0 {
		var names []string
		for _, i := range left {
			names = append(names, html.EscapeString(e.items[i].Name))
		}
		fmt.Fprintf(&sb, "\nне записано (немає в базі): %s", strings.Join(names, ", "))
	}
	return sb.String()
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

// mealFrom trusts the model when it read a meal out of the hint, and otherwise
// reads the clock: this household eats twice a day, so anything before late
// afternoon is lunch.
func mealFrom(modelSlot string, now time.Time) menu.Meal {
	if model.ValidMeal(modelSlot) {
		return menu.Meal(modelSlot)
	}
	if now.Hour() < 16 {
		return menu.Lunch
	}
	return menu.Dinner
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
