//go:build integration

// Accounts and their own lists: a book one account may not see, a listener's
// own queue, lists deleted, and a listener's own listening read back. Each
// journey acts as the account in question, over its own MCP session, and
// reads the server back as the admin as well, because what a tool says it
// did for one account is not proof of what another account now sees.
package acceptance

import (
	"crypto/rand"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/katbyte/abs-mcp/tools"
)

// accessTitles lists the titles of an item listing's rows.
func accessTitles(t *testing.T, out map[string]any, field string) []string {
	t.Helper()

	var titles []string
	for _, row := range rows(t, out[field], field) {
		titles = append(titles, text(row["title"]))
	}
	slices.Sort(titles)

	return titles
}

// accessNarratorBooks is how many books narrator_list gives a narrator, or -1
// when it does not list them.
func accessNarratorBooks(t *testing.T, out map[string]any, name string) int {
	t.Helper()

	for _, n := range rows(t, out["narrators"], "narrators") {
		if n["name"] == name {
			return num(t, n["books"], "books")
		}
	}

	return -1
}

// A book marked explicit, and a listener whose account is not allowed
// explicit content. Every tool that reads a library on their behalf leaves
// the book out - the listing, the search, its series, its author, the
// library's counts, the narrator list, what is new and the audits - and every
// write that names it by id is refused. Once unmarked it is back everywhere.
// A tool that answered from the server's whole-library caches, or that
// resolved an id with more reach than the account has, would show the
// listener a book the server keeps from them.
func TestJourneyAnExplicitBookKeptFromAnAccount(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}

	// the only Expanse book but one, and the only other book its author and
	// narrator have, so every count it is in drops to one
	const book, series, author, narrator = "Abaddon's Gate", "The Expanse", "James S. A. Corey", "Jefferson Mays"
	id := itemID(t, "Fiction", book)
	t.Cleanup(func() { call(t, "item_edit", map[string]any{"item": id, "explicit": false}) })

	call(t, "item_edit", map[string]any{"item": id, "explicit": true})
	if explicit, _ := call(t, "item_get", map[string]any{"item": id})["explicit"].(bool); !explicit {
		t.Fatal("item_get does not read the book back as explicit")
	}

	no := false
	listener := newUser(t, "zzyzx-no-explicit", abs.UserCreate{
		Permissions: map[string]bool{"accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": no},
	})
	for who, u := range map[string]map[string]any{"self": listener.call(t, "user_get", nil), "admin": call(t, "user_get", map[string]any{"user": listener.Name})} {
		if explicit, ok := u["explicit"].(bool); !ok || explicit {
			t.Errorf("%s: user_get explicit = %v, want false", who, u["explicit"])
		}
		if all, _ := u["all_tags"].(bool); !all {
			t.Errorf("%s: all_tags = %v: only the explicit flag keeps anything from this account", who, u["all_tags"])
		}
	}

	t.Run("left out of every read", func(t *testing.T) {
		items := listener.call(t, "library_items", map[string]any{"library": "Fiction", "limit": 50})
		if titles := accessTitles(t, items, "items"); slices.Contains(titles, book) || len(titles) != 6 || num(t, items["total"], "total") != 6 {
			t.Errorf("library_items Fiction = %v (total %v), want the other 6", titles, items["total"])
		}
		search := listener.call(t, "library_search", map[string]any{"query": "Abaddon"})
		if found := rows(t, search["items"], "items"); len(found) != 0 {
			t.Errorf("library_search Abaddon = %v, want nothing", found)
		}
		// the groups a search names count the one book left
		for query, group := range map[string]string{"Expanse": "series", "Jefferson Mays": "narrators", "Corey": "authors"} {
			out := listener.call(t, "library_search", map[string]any{"library": "Fiction", "query": query})
			for _, g := range rows(t, out[group], group) {
				if n, ok := g["count"].(float64); ok && n != 1 {
					t.Errorf("library_search %s: %s %v counts %v books, want 1", query, group, g["name"], n)
				}
			}
			for _, it := range rows(t, out["items"], "items") {
				if it["id"] == id {
					t.Errorf("library_search %s lists the explicit book", query)
				}
			}
		}
		// the series and the author are still there, with the one book left
		if got := titlesIn(t, listener.call(t, "series_get", map[string]any{"library": "Fiction", "series": series})["books"], "books"); !slices.Equal(got, []string{"Leviathan Wakes"}) {
			t.Errorf("series_get %s = %v, want Leviathan Wakes alone", series, got)
		}
		if got := titlesIn(t, listener.call(t, "author_get", map[string]any{"library": "Fiction", "author": author})["books"], "books"); !slices.Equal(got, []string{"Leviathan Wakes"}) {
			t.Errorf("author_get %s = %v, want Leviathan Wakes alone", author, got)
		}
		if n := num(t, listener.call(t, "library_get", map[string]any{"library": "Fiction"})["items"], "items"); n != 6 {
			t.Errorf("library_get Fiction items = %d, want 6", n)
		}
		if n := accessNarratorBooks(t, listener.call(t, "narrator_list", map[string]any{"library": "Fiction"}), narrator); n != 1 {
			t.Errorf("narrator_list gives %s %d books, want 1", narrator, n)
		}
		for _, it := range rows(t, listener.call(t, "library_recent", map[string]any{"limit": 100})["items"], "items") {
			if it["id"] == id {
				t.Errorf("library_recent lists the explicit book: %v", it)
			}
		}
		if n := num(t, listener.call(t, "audit_all", map[string]any{"library": "Fiction"})["items_scanned"], "items_scanned"); n != 6 {
			t.Errorf("audit_all scanned %d, want 6", n)
		}
		listener.callErr(t, "item_get", map[string]any{"item": id})
		listener.callErr(t, "item_get", map[string]any{"library": "Fiction", "item": book})
		// the admin, allowed everything, still sees seven
		if n := num(t, call(t, "library_items", map[string]any{"library": "Fiction", "limit": 1})["total"], "total"); n != 7 {
			t.Errorf("the admin's Fiction holds %d, want 7", n)
		}
	})

	t.Run("refused every write naming it", func(t *testing.T) {
		t.Cleanup(func() { _, _ = listener.invoke("playlist_delete", map[string]any{"playlist": "Zzyzx Explicit Queue"}) })
		listener.callErr(t, "user_progress_set", map[string]any{"item": id, "percent": 50})
		listener.callErr(t, "user_bookmark_edit", map[string]any{"item": id, "action": "add", "time_s": 0.5, "title": "Zzyzx Explicit Mark"})
		listener.callErr(t, "playlist_create", map[string]any{"library": "Fiction", "name": "Zzyzx Explicit Queue", "entries": []any{map[string]any{"item": id}}})

		// read back as the admin: nothing was written anywhere
		u := call(t, "user_get", map[string]any{"user": listener.Name})
		if recent := rows(t, u["recent_progress"], "recent_progress"); len(recent) != 0 || num(t, u["bookmarks"], "bookmarks") != 0 {
			t.Errorf("the listener has progress %v and %v bookmarks, want none", recent, u["bookmarks"])
		}
		if lists := rows(t, listener.call(t, "playlist_list", nil)["playlists"], "playlists"); len(lists) != 0 {
			t.Errorf("the listener has playlists %v, want none", lists)
		}
	})

	t.Run("unmarked, and back", func(t *testing.T) {
		call(t, "item_edit", map[string]any{"item": id, "explicit": false})
		if got := listener.call(t, "item_get", map[string]any{"item": id}); got["id"] != id || got["explicit"] == true {
			t.Errorf("item_get once unmarked = %v", got)
		}
		if n := num(t, listener.call(t, "library_items", map[string]any{"library": "Fiction", "limit": 1})["total"], "total"); n != 7 {
			t.Errorf("library_items Fiction total = %d, want 7", n)
		}
		if got := titlesIn(t, listener.call(t, "series_get", map[string]any{"library": "Fiction", "series": series})["books"], "books"); !slices.Equal(got, []string{"Leviathan Wakes", book}) {
			t.Errorf("series_get %s = %v, want both books in order", series, got)
		}
		if n := accessNarratorBooks(t, listener.call(t, "narrator_list", map[string]any{"library": "Fiction"}), narrator); n != 2 {
			t.Errorf("narrator_list gives %s %d books, want 2", narrator, n)
		}
	})
}

// A listener limited to books tagged cyberpunk builds their own queue from
// the books they can see, and names one they cannot by its id - the one way
// to reach a book no listing showed them. Adding it to the queue, alone or
// beside a book they can see, setting progress on it and bookmarking it are
// each refused, and the queue, their progress and their bookmarks read back
// with none of it. A tool that looked the id up with more reach than the
// account, or left the check to the server's batch add, would put a hidden
// book in the queue.
func TestJourneyATagLimitedListenersOwnQueue(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}

	no := false
	listener := newUser(t, "zzyzx-tag-queue", abs.UserCreate{
		Permissions:      map[string]bool{"accessAllLibraries": true, "accessAllTags": no, "accessExplicitContent": true},
		ItemTagsSelected: []string{"cyberpunk"},
	})
	hidden := itemID(t, "Fiction", "Foundation")
	const queue = "Zzyzx Tag Queue"
	t.Cleanup(func() { _, _ = listener.invoke("playlist_delete", map[string]any{"playlist": queue}) })

	listener.call(t, "playlist_create", map[string]any{"library": "Fiction", "name": queue, "entries": []any{map[string]any{"item": "City of Golden Shadow"}}})
	entries := func() []string {
		t.Helper()
		var out []string
		for _, e := range rows(t, listener.call(t, "playlist_get", map[string]any{"playlist": queue})["entries"], "entries") {
			it, _ := e["item"].(map[string]any)
			out = append(out, text(it["title"]))
		}
		return out
	}

	// a book they can see and one they cannot, in one call: refused whole,
	// not the one added and the other dropped
	listener.callErr(t, "playlist_entries_edit", map[string]any{"playlist": queue, "action": "add", "entries": []any{map[string]any{"item": "Sea of Silver Light"}, map[string]any{"item": hidden}}})
	if got := entries(); !slices.Equal(got, []string{"City of Golden Shadow"}) {
		t.Errorf("the queue after a refused add = %v, want City of Golden Shadow alone", got)
	}
	// and the hidden book alone, three ways
	listener.callErr(t, "playlist_entries_edit", map[string]any{"playlist": queue, "action": "add", "entries": []any{map[string]any{"item": hidden}}})
	listener.callErr(t, "user_progress_set", map[string]any{"item": hidden, "percent": 50})
	listener.callErr(t, "user_bookmark_edit", map[string]any{"item": hidden, "action": "add", "time_s": 0.5, "title": "Zzyzx Hidden Mark"})

	// the book they can see goes in on its own
	added := listener.call(t, "playlist_entries_edit", map[string]any{"playlist": queue, "action": "add", "entries": []any{map[string]any{"item": "Sea of Silver Light"}}})
	if !slices.Equal(strs(t, added["added"], "added"), []string{"Sea of Silver Light"}) || num(t, added["entries"], "entries") != 2 {
		t.Errorf("adding a visible book = %v", added)
	}
	if got, want := entries(), []string{"City of Golden Shadow", "Sea of Silver Light"}; !slices.Equal(got, want) {
		t.Errorf("the queue = %v, want %v", got, want)
	}
	for _, p := range rows(t, listener.call(t, "playlist_list", nil)["playlists"], "playlists") {
		if p["name"] == queue && num(t, p["entries"], "entries") != 2 {
			t.Errorf("playlist_list = %v, want 2 entries", p)
		}
	}
	u := call(t, "user_get", map[string]any{"user": listener.Name})
	if recent := rows(t, u["recent_progress"], "recent_progress"); len(recent) != 0 || num(t, u["bookmarks"], "bookmarks") != 0 {
		t.Errorf("the listener has progress %v and %v bookmarks, want none", recent, u["bookmarks"])
	}
	if marks := rows(t, listener.call(t, "user_bookmarks", nil)["bookmarks"], "bookmarks"); len(marks) != 0 {
		t.Errorf("user_bookmarks = %v, want none", marks)
	}
}

// accessDeleteTools are the tools --enable-delete registers.
var accessDeleteTools = []string{"author_delete", "collection_delete", "item_delete", "library_issues_remove", "playlist_delete", "podcast_episode_delete"}

// A collection and a playlist deleted, and each list read back to see them
// gone and their books still there; a playlist emptied by removing its last
// entry, which Audiobookshelf deletes. Then a session run without the delete
// tools: it does not list them, and it is refused emptying its own playlist,
// which would be a delete by another name. A delete that answered without
// deleting, or a delete that slipped past --enable-delete, would pass a test
// that only looked at the answer.
func TestJourneyListsDeletedAndReadBack(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}
	for _, tool := range accessDeleteTools {
		if !slices.Contains(toolNames(t), tool) {
			t.Fatalf("%s is not registered in the harness's session, which has deletes on", tool)
		}
	}

	t.Run("a collection", func(t *testing.T) {
		made := text(call(t, "collection_create", map[string]any{"library": "Fiction", "name": "Zzyzx Doomed Shelf", "items": []any{"Foundation", "Second Foundation"}})["id"])
		t.Cleanup(func() { _, _ = invoke("collection_delete", map[string]any{"collection": made}) })

		out := call(t, "collection_delete", map[string]any{"collection": "Zzyzx Doomed Shelf"})
		if out["deleted"] != "Zzyzx Doomed Shelf" || out["id"] != made {
			t.Errorf("collection_delete = %v, want the shelf and its id", out)
		}
		for _, c := range rows(t, call(t, "collection_list", nil)["collections"], "collections") {
			if c["id"] == made {
				t.Errorf("the deleted collection is still listed: %v", c)
			}
		}
		if msg := callErr(t, "collection_get", map[string]any{"collection": made}); msg == "" {
			t.Error("the deleted collection still resolves by id")
		}
		if msg := callErr(t, "collection_get", map[string]any{"collection": "Zzyzx Doomed Shelf"}); !strings.Contains(msg, "no collection named") {
			t.Errorf("the deleted collection by name: %s", msg)
		}
		// its books were only shelved there
		if n := num(t, call(t, "library_items", map[string]any{"library": "Fiction", "limit": 1})["total"], "total"); n != 7 {
			t.Errorf("Fiction holds %d books after the delete, want 7", n)
		}
	})

	t.Run("a playlist", func(t *testing.T) {
		made := text(call(t, "playlist_create", map[string]any{"library": "Fiction", "name": "Zzyzx Doomed Queue", "entries": []any{map[string]any{"item": "Foundation"}, map[string]any{"item": "Leviathan Wakes"}}})["id"])
		t.Cleanup(func() { _, _ = invoke("playlist_delete", map[string]any{"playlist": made}) })

		out := call(t, "playlist_delete", map[string]any{"playlist": "Zzyzx Doomed Queue"})
		if out["deleted"] != "Zzyzx Doomed Queue" || out["id"] != made {
			t.Errorf("playlist_delete = %v, want the queue and its id", out)
		}
		for _, p := range rows(t, call(t, "playlist_list", nil)["playlists"], "playlists") {
			if p["id"] == made {
				t.Errorf("the deleted playlist is still listed: %v", p)
			}
		}
		if msg := callErr(t, "playlist_get", map[string]any{"playlist": made}); msg == "" {
			t.Error("the deleted playlist still resolves by id")
		}
		if got := call(t, "item_get", map[string]any{"library": "Fiction", "item": "Leviathan Wakes"}); got["title"] != "Leviathan Wakes" {
			t.Errorf("a book of the deleted playlist = %v", got)
		}
	})

	t.Run("a playlist emptied, with deletes on", func(t *testing.T) {
		made := text(call(t, "playlist_create", map[string]any{"library": "Fiction", "name": "Zzyzx Emptied Queue", "entries": []any{map[string]any{"item": "Foundation"}}})["id"])
		t.Cleanup(func() { _, _ = invoke("playlist_delete", map[string]any{"playlist": made}) })

		out := call(t, "playlist_entries_edit", map[string]any{"playlist": made, "action": "remove", "entries": []any{map[string]any{"item": "Foundation"}}})
		if deleted, _ := out["deleted"].(bool); !deleted || num(t, out["entries"], "entries") != 0 {
			t.Errorf("removing the last entry = %v, want the playlist reported deleted", out)
		}
		for _, p := range rows(t, call(t, "playlist_list", nil)["playlists"], "playlists") {
			if p["id"] == made {
				t.Errorf("the emptied playlist is still listed: %v", p)
			}
		}
	})

	t.Run("a session without the delete tools", func(t *testing.T) {
		keeper := newUserWith(t, "zzyzx-no-deletes", abs.UserCreate{}, tools.Options{})
		listed, err := keeper.session.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range listed.Tools {
			if slices.Contains(accessDeleteTools, tool.Name) {
				t.Errorf("%s is listed in a session without --enable-delete", tool.Name)
			}
		}
		if msg := keeper.callErr(t, "playlist_delete", map[string]any{"playlist": "anything"}); !strings.Contains(msg, "playlist_delete") {
			t.Errorf("calling an unregistered delete tool: %s", msg)
		}

		const queue = "Zzyzx Kept Queue"
		made := text(keeper.call(t, "playlist_create", map[string]any{"library": "Fiction", "name": queue, "entries": []any{map[string]any{"item": "Foundation"}, map[string]any{"item": "Second Foundation"}}})["id"])
		own, err := abs.New(os.Getenv("ABS_SERVER"), keeper.Token)
		if err != nil {
			t.Fatal(err)
		}
		// the account's own key, while it still has one: an admin may not
		// delete someone else's playlist
		t.Cleanup(func() { _ = own.DeletePlaylist(ctx, made) })

		// taking one of two out is an edit, and is allowed
		if out := keeper.call(t, "playlist_entries_edit", map[string]any{"playlist": queue, "action": "remove", "entries": []any{map[string]any{"item": "Foundation"}}}); num(t, out["entries"], "entries") != 1 {
			t.Errorf("removing one of two = %v", out)
		}
		// taking the last out would delete it
		msg := keeper.callErr(t, "playlist_entries_edit", map[string]any{"playlist": queue, "action": "remove", "entries": []any{map[string]any{"item": "Second Foundation"}}})
		if !strings.Contains(msg, "--enable-delete") {
			t.Errorf("emptying a playlist without deletes: %s", msg)
		}
		got := keeper.call(t, "playlist_get", map[string]any{"playlist": made})
		entries := rows(t, got["entries"], "entries")
		if len(entries) != 1 {
			t.Fatalf("the playlist after the refusal = %v, want Second Foundation still in it", got)
		}
		if it, _ := entries[0]["item"].(map[string]any); text(it["title"]) != "Second Foundation" {
			t.Errorf("the entry left = %v, want Second Foundation", entries[0])
		}
	})
}

// accessUUID is a random id in the shape the server gives its own records.
func accessUUID(t *testing.T) string {
	t.Helper()

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// accessListen plays a book as an app does: open a session, sync, close.
func accessListen(t *testing.T, client *abs.Client, itemID string) string {
	t.Helper()

	s, err := client.Play(ctx, itemID, "", abs.PlayRequest{MediaPlayer: "zzyzx-player", SupportedMimeTypes: []string{"audio/mpeg"}, ForceDirectPlay: true})
	if err != nil {
		t.Fatalf("opening a session: %v", err)
	}
	if err := client.SyncSession(ctx, s.ID, 0.2, 0.2); err != nil {
		t.Fatalf("syncing: %v", err)
	}
	if err := client.CloseSession(ctx, s.ID, map[string]any{"currentTime": 0.3, "timeListened": 0.1}); err != nil {
		t.Fatalf("closing: %v", err)
	}

	return s.ID
}

// accessYear is a year in review with its top lists in name order: the
// server ranks them by whole seconds, and lists a tie in whatever order its
// database hands the sessions back, which is not the same from one call to
// the next.
func accessYear(out map[string]any) map[string]any {
	for _, key := range []string{"top_authors", "top_narrators", "top_genres", "finished"} {
		if list, ok := out[key].([]any); ok {
			slices.SortFunc(list, func(a, b any) int {
				ra, _ := a.(map[string]any)
				rb, _ := b.(map[string]any)
				return strings.Compare(text(ra["name"])+text(ra["title"]), text(rb["name"])+text(rb["title"]))
			})
		}
	}

	return out
}

// accessSessionIDs lists the ids of a history's sessions, in its order.
func accessSessionIDs(t *testing.T, out map[string]any) []string {
	t.Helper()

	var ids []string
	for _, s := range rows(t, out["sessions"], "sessions") {
		ids = append(ids, text(s["id"]))
	}

	return ids
}

// A listener's own listening, read back through every argument the reads
// take. They play one book, then another three times, and an app uploads a
// session on the first book recorded offline a hundred days ago. The history
// of the first book, asked by an admin naming them, has to be found behind
// the newer sessions of the other and cut at the days asked for; the year in
// review asked by their own name is their own; and their progress set by
// position and hidden from the shelf reads back so from both sides. A history
// that filtered only the newest page, or a name taken for someone else,
// would come back empty or refused.
func TestJourneyAListenersOwnListeningReadBack(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}

	listener := newUser(t, "zzyzx-own-listening", abs.UserCreate{})
	client, err := abs.New(os.Getenv("ABS_SERVER"), listener.Token)
	if err != nil {
		t.Fatal(err)
	}
	const first, other = "Leviathan Wakes", "Foundation and Empire"
	firstID, otherID := itemID(t, "Fiction", first), itemID(t, "Fiction", other)
	named := func(extra map[string]any) map[string]any {
		out := map[string]any{"user": listener.Name}
		maps.Copy(out, extra)
		return out
	}

	recent := accessListen(t, client, firstID)
	for range 3 {
		accessListen(t, client, otherID)
	}
	// recorded offline long ago, uploaded now
	old := time.Now().Add(-100 * 24 * time.Hour)
	offline := accessUUID(t)
	if err := client.SyncLocalSession(ctx, map[string]any{
		"id": offline, "libraryId": libraryID(t, "Fiction"), "libraryItemId": firstID, "mediaType": "book",
		"displayTitle": first, "displayAuthor": "James S. A. Corey", "duration": 1, "playMethod": 3, "mediaPlayer": "zzyzx-offline",
		"timeListening": 0.4, "startTime": 0, "currentTime": 0.4,
		"date": old.Format("2006-01-02"), "dayOfWeek": old.Weekday().String(),
		"startedAt": old.UnixMilli(), "updatedAt": old.Add(time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatalf("uploading an offline session: %v", err)
	}

	t.Run("history of one book, behind newer sessions", func(t *testing.T) {
		all := call(t, "user_history", named(map[string]any{"days": 365, "limit": 10}))
		if n := num(t, all["total_sessions"], "total_sessions"); n != 5 {
			t.Errorf("total_sessions = %d, want all 5", n)
		}
		// the newest session is another book's, so a limit of one filtered
		// after the fetch finds nothing
		if got := accessSessionIDs(t, call(t, "user_history", named(map[string]any{"library": "Fiction", "item": first, "limit": 1}))); !slices.Equal(got, []string{recent}) {
			t.Errorf("admin, item %s, limit 1 = %v, want the recent session %s", first, got, recent)
		}
		// sixty days, by default, leaves the offline one out; a year takes it in
		if got := accessSessionIDs(t, call(t, "user_history", named(map[string]any{"library": "Fiction", "item": first}))); !slices.Equal(got, []string{recent}) {
			t.Errorf("admin, item %s, default days = %v, want only the recent one", first, got)
		}
		if got := accessSessionIDs(t, call(t, "user_history", named(map[string]any{"library": "Fiction", "item": first, "days": 120}))); !slices.Equal(got, []string{recent, offline}) {
			t.Errorf("admin, item %s, 120 days = %v, want the recent one then the offline one", first, got)
		}
		if got := accessSessionIDs(t, call(t, "user_history", named(map[string]any{"library": "Fiction", "item": first, "days": 30}))); !slices.Equal(got, []string{recent}) {
			t.Errorf("admin, item %s, 30 days = %v, want only the recent one", first, got)
		}
		// the listener asking the same, through their own routes
		self := listener.call(t, "user_history", map[string]any{"library": "Fiction", "item": first, "days": 120})
		if got := accessSessionIDs(t, self); !slices.Equal(got, []string{recent, offline}) {
			t.Errorf("self, item %s, 120 days = %v, want the recent one then the offline one", first, got)
		}
		for _, s := range rows(t, self["sessions"], "sessions") {
			if s["item_id"] != firstID || s["user"] != listener.Name {
				t.Errorf("a row of the listener's history of %s: %v", first, s)
			}
		}
		// how many sessions the book has is one answer, whoever asks and
		// however far back the list goes
		for _, days := range []int{0, 120} {
			args := map[string]any{"library": "Fiction", "item": first}
			if days > 0 {
				args["days"] = days
			}
			mine, theirs := listener.call(t, "user_history", args), call(t, "user_history", named(args))
			if num(t, mine["total_sessions"], "total_sessions") != 2 || num(t, theirs["total_sessions"], "total_sessions") != 2 {
				t.Errorf("days %d: total_sessions of %s: the listener sees %v, an admin sees %v, want its 2", days, first, mine["total_sessions"], theirs["total_sessions"])
			}
		}
	})

	t.Run("a year in review, by their own name", func(t *testing.T) {
		year := time.Now().Year()
		bare := accessYear(listener.call(t, "user_stats", map[string]any{"year": year}))
		byName := accessYear(listener.call(t, "user_stats", map[string]any{"user": listener.Name, "year": year}))
		if !sameJSON(bare, byName) {
			t.Errorf("the listener's year by their own name = %v, without = %v", byName, bare)
		}
		// four sessions played now, and the offline one if it falls this year
		sessions := 4
		if old.Year() == year {
			sessions++
		}
		if bare["user"] != listener.Name || num(t, bare["sessions"], "sessions") != sessions || num(t, bare["books_listened"], "books_listened") != 2 {
			t.Errorf("the listener's year = %v, want %d sessions over 2 books", bare, sessions)
		}
		// and the admin's own, by name
		if mine, byRoot := accessYear(call(t, "user_stats", map[string]any{"year": year})), accessYear(call(t, "user_stats", map[string]any{"user": "root", "year": year})); !sameJSON(mine, byRoot) {
			t.Errorf("root's year by name = %v, without = %v", byRoot, mine)
		}
	})

	t.Run("progress by position, hidden and restored", func(t *testing.T) {
		library, book, bookID := accessLongBook(t)
		onShelf := func(out map[string]any) bool {
			t.Helper()
			for _, it := range rows(t, out["items"], "items") {
				if it["id"] == bookID {
					return true
				}
			}
			return false
		}
		shelves := func() (bool, bool) {
			t.Helper()
			return onShelf(listener.call(t, "user_in_progress", nil)), onShelf(call(t, "user_in_progress", named(nil)))
		}
		// the listener's view and an admin's naming them are one record
		progress := func() map[string]any {
			t.Helper()
			self, _ := listener.call(t, "user_progress_get", map[string]any{"library": library, "item": book})["progress"].(map[string]any)
			admin, _ := call(t, "user_progress_get", named(map[string]any{"library": library, "item": book}))["progress"].(map[string]any)
			if !sameJSON(self, admin) {
				t.Errorf("progress: the listener sees %v, an admin sees %v", self, admin)
			}
			return self
		}
		at := func(p map[string]any, seconds, pct int, hidden bool) bool {
			return p != nil && num(t, p["current_time_s"], "current_time_s") == seconds && num(t, p["percent"], "percent") == pct &&
				p["finished"] != true && (p["hidden_from_continue"] == true) == hidden
		}

		set := listener.call(t, "user_progress_set", map[string]any{"library": library, "item": book, "position_s": 10})
		if p, _ := set["progress"].(map[string]any); !at(p, 10, 33, false) {
			t.Errorf("user_progress_set position 10 of 30 seconds = %v, want 10 seconds, 33 percent", set)
		}
		if p := progress(); !at(p, 10, 33, false) {
			t.Errorf("progress read back = %v, want 10 seconds, 33 percent, on the shelf", p)
		}
		if self, admin := shelves(); !self || !admin {
			t.Errorf("on the shelf: self %v, admin %v, want both", self, admin)
		}

		listener.call(t, "user_progress_set", map[string]any{"library": library, "item": book, "hide_from_continue": true})
		if p := progress(); !at(p, 10, 33, true) {
			t.Errorf("progress once hidden = %v, want hidden where it was", p)
		}
		if self, admin := shelves(); self || admin {
			t.Errorf("hidden, yet on the shelf: self %v, admin %v", self, admin)
		}

		listener.call(t, "user_progress_set", map[string]any{"library": library, "item": book, "hide_from_continue": false})
		if p := progress(); !at(p, 10, 33, false) {
			t.Errorf("progress once restored = %v, want it where it was, on the shelf", p)
		}
		if self, admin := shelves(); !self || !admin {
			t.Errorf("restored: self %v, admin %v, want both", self, admin)
		}

		// a percent is a position in the book
		listener.call(t, "user_progress_set", map[string]any{"library": library, "item": book, "percent": 50})
		if p := progress(); !at(p, 15, 50, false) {
			t.Errorf("progress set to 50 percent = %v, want 15 seconds", p)
		}

		// what cannot be set is refused, and changes nothing
		for _, args := range []map[string]any{
			{"library": library, "item": book, "percent": 150},
			{"library": library, "item": book, "percent": -5},
			{"library": library, "item": book, "position_s": -1},
		} {
			if msg := listener.callErr(t, "user_progress_set", args); !strings.Contains(msg, "between 0 and 100") && !strings.Contains(msg, "before the start") {
				t.Errorf("user_progress_set %v: %s", args, msg)
			}
		}
		if msg := listener.callErr(t, "user_progress_set", map[string]any{"item": "Behind the Bastards", "percent": 50}); !strings.Contains(msg, "is a podcast") {
			t.Errorf("progress on a podcast with no episode named: %s", msg)
		}
		if p := progress(); !at(p, 15, 50, false) {
			t.Errorf("progress after the refusals = %v, want 15 seconds, 50 percent", p)
		}
	})
}

// accessLongBook makes a library of one thirty-second book, for progress that
// has to stay short of the end: on every update the server marks a book
// finished once fewer than ten seconds of it are left, which is all of a
// one-second fixture. The library and its folder go afterwards.
func accessLongBook(t *testing.T) (library, title, id string) {
	t.Helper()

	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	library, title = "Zzyzx Long Shelf", "Zzyzx Long Book"
	root := filepath.Join(data, "scratch", "zzyzx-long-shelf")
	file := filepath.Join(root, "Zzyzx Long Author", title, "01.mp3")
	if err := os.MkdirAll(filepath.Dir(file), 0o777); err != nil {
		t.Fatal(err)
	}
	gen := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono", "-t", "30", "-q:a", "9", file)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
	admin := adminClient(t)
	made, _ := call(t, "library_create", map[string]any{"name": library, "folders": []any{"/scratch/zzyzx-long-shelf"}})["library"].(map[string]any)
	libID := text(made["id"])
	t.Cleanup(func() {
		eventually(t, "deleting the long shelf", func() error {
			if err := admin.DeleteLibrary(ctx, libID); err != nil && !isNotFound(err) {
				return err
			}
			return nil
		})
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("removing %s: %v", root, err)
		}
	})
	call(t, "library_scan", map[string]any{"library": library})
	for i := 0; num(t, call(t, "library_items", map[string]any{"library": library, "limit": 1})["total"], "total") != 1; i++ {
		if i == 100 {
			t.Fatalf("the scan of %s never found the book", library)
		}
		time.Sleep(100 * time.Millisecond)
	}
	waitIdle(t)

	return library, title, itemID(t, library, title)
}

// A backup made, and found by the list read afterwards: the one it made, by
// id and file, where the list says backups are kept. The create's own answer
// is the list the server returned, which is not proof the backup is there.
func TestJourneyABackupReadBack(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}
	admin := adminClient(t)

	// the backup made is the one the tool names: the server names a backup by
	// the minute it was made, so one made in the minute of another replaces
	// it, and a count of new ids would read that as nothing made
	created, _ := call(t, "server_backup_create", nil)["created"].(map[string]any)
	id := text(created["id"])
	if id == "" {
		t.Fatal("server_backup_create named no backup")
	}
	t.Cleanup(func() {
		eventually(t, "deleting the backup", func() error { _, err := admin.DeleteBackup(ctx, id); return err })
	})

	listed := call(t, "server_backups", nil)
	if text(listed["location"]) == "" {
		t.Error("server_backups names no location")
	}
	var found map[string]any
	for _, b := range rows(t, listed["backups"], "backups") {
		if b["id"] == id {
			found = b
		}
	}
	switch {
	case found == nil:
		t.Fatalf("the new backup %s is not in server_backups", id)
	case found["filename"] != created["filename"] || !strings.HasSuffix(text(found["filename"]), ".audiobookshelf"):
		t.Errorf("listed as %v, made as %v", found, created)
	case text(found["created"]) == "" || text(found["server_version"]) == "":
		t.Errorf("the listed backup has no creation time or version: %v", found)
	}
	if at, err := time.Parse(time.RFC3339, text(found["created"])); err != nil || time.Since(at) > 10*time.Minute {
		t.Errorf("created %v (%v), want just now", found["created"], err)
	}
}
