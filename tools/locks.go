package tools

import (
	"slices"
	"sync"
)

// writeLocks keep the calls of one turn from undoing each other. The MCP
// server runs a turn's calls at once, and most edits read a record and send
// part of it back whole: eight item_edit calls each adding a tag to one book
// each sent the tags as they were before the others, and one tag stayed.
//
// An edit of known records holds those records, and reads them again once it
// has them; a sweep that reads a whole library and writes it back holds
// everything. The locks belong to this process, shared by every session it
// serves; the Audiobookshelf web app, or another abs-mcp, is not held back.
type writeLocks struct {
	sweep sync.RWMutex

	mu      sync.Mutex
	records map[string]*recordLock
}

type recordLock struct {
	sync.Mutex
	holders int
}

// hold takes the named records until release is called. Keys are taken in
// sorted order, so two calls holding overlapping sets cannot deadlock. A call
// holds once: taking a second set while holding one can wait on a sweep that
// is itself waiting for the first to be released.
func (l *writeLocks) hold(keys ...string) (release func()) {
	keys = slices.Clone(keys)
	slices.Sort(keys)
	keys = slices.Compact(keys)

	l.sweep.RLock()
	held := make([]*recordLock, 0, len(keys))
	for _, k := range keys {
		l.mu.Lock()
		if l.records == nil {
			l.records = map[string]*recordLock{}
		}
		rl := l.records[k]
		if rl == nil {
			rl = &recordLock{}
			l.records[k] = rl
		}
		rl.holders++
		l.mu.Unlock()

		rl.Lock()
		held = append(held, rl)
	}

	return func() {
		for i, rl := range slices.Backward(held) {
			rl.Unlock()
			l.mu.Lock()
			rl.holders--
			if rl.holders == 0 {
				delete(l.records, keys[i])
			}
			l.mu.Unlock()
		}
		l.sweep.RUnlock()
	}
}

// holdAll takes every record, for a sweep across libraries.
func (l *writeLocks) holdAll() (release func()) {
	l.sweep.Lock()
	return l.sweep.Unlock
}

// itemKeys are the lock keys of library items.
func itemKeys(ids ...string) []string {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, "item:"+id)
	}
	return keys
}
