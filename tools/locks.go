package tools

import (
	"github.com/katbyte/go-kt/lock"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// The calls of one turn are kept from undoing each other with go-kt's locks.
// The MCP server runs a turn's calls at once, and most edits read a record
// and send part of it back whole: eight item_edit calls each adding a tag to
// one book each sent the tags as they were before the others, and one tag
// stayed.
//
// An edit of known records locks those records, and reads them again once it
// has them; a sweep that reads a whole library and writes it back locks
// everything. The locks are a set of this server's own (registry.locks),
// shared by every session it serves; the Audiobookshelf web app, or another
// abs-mcp, is not held back.

// bookmarksLock is the lock on the account's bookmarks, which the server
// keeps as one list that every add and removal reads and saves whole. The
// set is one server's as one account, so the name needs no more than this.
const bookmarksLock = "bookmarks"

// itemLocks are the items with these ids, for an edit of several that has
// not fetched them yet: all are locked in one call.
func itemLocks(ids ...string) []lock.Thing {
	things := make([]lock.Thing, 0, len(ids))
	for _, id := range ids {
		things = append(things, lock.ID[abs.Item](id))
	}

	return things
}
