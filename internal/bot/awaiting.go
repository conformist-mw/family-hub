package bot

import (
	"sync"
	"time"

	"familyhub/internal/menu"
)

// awaitingStore tracks users whose next message answers a question the bot
// just asked them, rather than being a message of its own. Two questions ask
// for a free-form reply: which new value a field of an appointment gets, and
// what was eaten at a meal the evening check asked about. Keyed by sender id,
// so in a group only the person who tapped is listened to.
type awaitingStore struct {
	mu    sync.Mutex
	items map[int64]awaitingEntry
}

// What an awaited reply is for. The kind is explicit rather than read off
// which fields are set: an entry for a meal has a zero appointment id, and
// taking it for an edit would reschedule appointment 0.
const (
	awaitApptEdit  = "appt_edit"
	awaitMealOther = "meal_other"
)

type awaitingEntry struct {
	kind string

	// awaitApptEdit: the appointment and which of its fields.
	apptID int64
	field  string // "time" | "title" | "who"

	// awaitMealOther: the meal the evening check asked about, local midnight
	// of its day.
	date time.Time
	meal menu.Meal

	created time.Time
}

// awaitTTL is how long a question stays asked. Past it, the reply is most
// likely about something else.
const awaitTTL = 10 * time.Minute

func newAwaitingStore() *awaitingStore {
	return &awaitingStore{items: make(map[int64]awaitingEntry)}
}

// setEdit arms an appointment field edit for the sender.
func (a *awaitingStore) setEdit(senderID, apptID int64, field string, now time.Time) {
	a.put(senderID, awaitingEntry{kind: awaitApptEdit, apptID: apptID, field: field}, now)
}

// setMealOther arms the "Інше" answer of the evening check for the sender.
func (a *awaitingStore) setMealOther(senderID int64, date time.Time, meal menu.Meal, now time.Time) {
	a.put(senderID, awaitingEntry{kind: awaitMealOther, date: date, meal: meal}, now)
}

func (a *awaitingStore) put(senderID int64, e awaitingEntry, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evictLocked(now)
	e.created = now
	a.items[senderID] = e
}

// take returns and clears the sender's pending question of any kind, if it is
// still fresh. A text message can answer either kind.
func (a *awaitingStore) take(senderID int64, now time.Time) (awaitingEntry, bool) {
	return a.takeKind(senderID, "", now)
}

// takeMealOther is take for a photo, which only answers the evening check: a
// picture is never a new title for an appointment, so a pending edit is left
// armed for the text that will follow.
func (a *awaitingStore) takeMealOther(senderID int64, now time.Time) (awaitingEntry, bool) {
	return a.takeKind(senderID, awaitMealOther, now)
}

func (a *awaitingStore) takeKind(senderID int64, kind string, now time.Time) (awaitingEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.items[senderID]
	if !ok || (kind != "" && e.kind != kind) {
		return awaitingEntry{}, false
	}
	delete(a.items, senderID)
	if now.Sub(e.created) > awaitTTL {
		return awaitingEntry{}, false
	}
	return e, true
}

func (a *awaitingStore) evictLocked(now time.Time) {
	for k, e := range a.items {
		if now.Sub(e.created) > awaitTTL {
			delete(a.items, k)
		}
	}
}
