package bot

import (
	"sync"
	"time"
)

// awaitingStore tracks users who tapped a field-edit button and whose next text
// message should be interpreted as the new value for a specific field
// (time/title/who) of a specific appointment, rather than as a new appointment.
// Keyed by sender id.
type awaitingStore struct {
	mu    sync.Mutex
	items map[int64]awaitingEntry
}

type awaitingEntry struct {
	apptID  int64
	field   string // "time" | "title" | "who"
	created time.Time
}

func newAwaitingStore() *awaitingStore {
	return &awaitingStore{items: make(map[int64]awaitingEntry)}
}

func (a *awaitingStore) set(senderID, apptID int64, field string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evictLocked(now)
	a.items[senderID] = awaitingEntry{apptID: apptID, field: field, created: now}
}

// take returns and clears the pending edit for a sender, if the entry exists and
// is still fresh (10 min). The returned field says which field to write.
func (a *awaitingStore) take(senderID int64, now time.Time) (int64, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.items[senderID]
	if !ok {
		return 0, "", false
	}
	delete(a.items, senderID)
	if now.Sub(e.created) > 10*time.Minute {
		return 0, "", false
	}
	return e.apptID, e.field, true
}

func (a *awaitingStore) evictLocked(now time.Time) {
	for k, e := range a.items {
		if now.Sub(e.created) > 10*time.Minute {
			delete(a.items, k)
		}
	}
}
