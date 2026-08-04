package bot

import (
	"strconv"
	"sync"
	"time"

	"familyhub/internal/parse"
)

// pendingStore holds parsed-but-unconfirmed appointments between the parse
// message and the Save/Cancel tap. In-memory only: a restart drops pending
// confirmations, which is harmless — the user just re-sends the text.
type pendingStore struct {
	mu    sync.Mutex
	seq   int64
	items map[string]pendingEntry
}

type pendingEntry struct {
	parsed   []parse.Parsed
	updateID int64 // >0: a same-time visit the user may choose to update instead
	created  time.Time
}

func newPendingStore() *pendingStore {
	return &pendingStore{items: make(map[string]pendingEntry)}
}

// put stores parsed items (and an optional same-time visit id to offer updating)
// and returns a short key to embed in callback data. now is passed in (no
// Date.now in this codebase's spirit of testability) to stamp the entry for
// opportunistic eviction.
func (p *pendingStore) put(parsed []parse.Parsed, updateID int64, now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evictLocked(now)
	p.seq++
	key := strconv.FormatInt(p.seq, 36)
	p.items[key] = pendingEntry{parsed: parsed, updateID: updateID, created: now}
	return key
}

func (p *pendingStore) take(key string) (pendingEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[key]
	if ok {
		delete(p.items, key)
	}
	return e, ok
}

// evictLocked drops entries older than an hour; a stale confirmation card is
// no longer actionable anyway.
func (p *pendingStore) evictLocked(now time.Time) {
	for k, e := range p.items {
		if now.Sub(e.created) > time.Hour {
			delete(p.items, k)
		}
	}
}
