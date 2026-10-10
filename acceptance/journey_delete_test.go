//go:build integration

// Journey 12: a book deleted, and everything that pointed at it. The book is
// in collections and playlists, has progress and a bookmark, a series, an
// author and a narrator, and someone else has listened to it; after
// item_delete every tool that names books is asked about it, and none may
// still count it, list it or fail over it. History is the exception: what was
// listened to stays listened to.
package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
	acc "github.com/katbyte/go-kt/mcp/acctest"
)

func TestJourneyADeletedBookLeavesNothingBehind(t *testing.T) {
	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	admin := adminClient(t)

	const library, author, reader = "Zzyzx Gone", "Zzyzx Gone Author", "Zzyzx Gone Reader"
	root := filepath.Join(data, "scratch", "zzyzx-gone")
	audio, err := os.ReadFile(filepath.Join(data, "fiction", "Isaac Asimov", "Foundation", "01.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	titles := []string{"Zzyzx Gone One", "Zzyzx Gone Two", "Zzyzx Gone Three"}
	for _, title := range titles {
		dir := filepath.Join(root, author, title)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "01.mp3"), audio, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	libID := acc.Str(object(suite.Call(t, "library_create", map[string]any{"name": library, "folders": []any{"/scratch/zzyzx-gone"}})["library"])["id"])
	t.Cleanup(func() {
		for _, c := range []string{"Zzyzx Gone Shelf", "Zzyzx Gone Alone"} {
			_, _ = suite.Invoke("collection_delete", map[string]any{"collection": c})
		}
		for _, p := range []string{"Zzyzx Gone Queue", "Zzyzx Gone Solo"} {
			_, _ = suite.Invoke("playlist_delete", map[string]any{"playlist": p})
		}
		eventually(t, "deleting the library", func() error {
			if err := admin.DeleteLibrary(ctx, libID); err != nil && !isNotFound(err) {
				return err
			}
			return nil
		})
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("removing %s: %v", root, err)
		}
	})
	suite.Call(t, "library_scan", map[string]any{"library": library})
	if err := waitForItems(library, 3); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	ids := map[string]string{}
	for _, it := range acc.Rows(t, suite.Call(t, "library_items", map[string]any{"library": library})["items"], "items") {
		ids[acc.Str(it["title"])] = acc.Str(it["id"])
	}
	one, two, three := ids[titles[0]], ids[titles[1]], ids[titles[2]]

	suite.Call(t, "item_edit", map[string]any{"library": library, "items": []any{one, two, three}, "authors": []any{author}})
	for id, series := range map[string]string{one: "Zzyzx Gone Saga #1", two: "Zzyzx Gone Saga #2", three: "Zzyzx Lone Saga #1"} {
		suite.Call(t, "item_edit", map[string]any{"item": id, "narrators": []any{reader}, "series": []any{series}})
	}
	// described, so the server keeps the record once it has no books
	suite.Call(t, "author_edit", map[string]any{"library": library, "author": author, "description": "Zzyzx: an author the journey deletes the books of."})

	suite.Call(t, "collection_create", map[string]any{"library": library, "name": "Zzyzx Gone Shelf", "items": []any{one, two}})
	suite.Call(t, "collection_create", map[string]any{"library": library, "name": "Zzyzx Gone Alone", "items": []any{one}})
	suite.Call(t, "playlist_create", map[string]any{"library": library, "name": "Zzyzx Gone Queue", "entries": []any{map[string]any{"item": one}, map[string]any{"item": three}}})
	suite.Call(t, "playlist_create", map[string]any{"library": library, "name": "Zzyzx Gone Solo", "entries": []any{map[string]any{"item": one}}})
	suite.Call(t, "user_progress_set", map[string]any{"item": one, "percent": 50})
	suite.Call(t, "user_bookmark_edit", map[string]any{"item": one, "add_bookmarks": []any{map[string]any{"time_s": 0.5, "title": "Zzyzx Gone Mark"}}})

	// someone else listens to it
	listener := newUser(t, "zzyzx-gone-listener", abs.UserCreate{})
	lc, err := abs.New(os.Getenv("ABS_SERVER"), listener.Token)
	if err != nil {
		t.Fatal(err)
	}
	session, err := lc.Play(ctx, one, "", abs.PlayRequest{MediaPlayer: "zzyzx-player", SupportedMimeTypes: []string{"audio/mpeg"}, ForceDirectPlay: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := lc.SyncSession(ctx, session.ID, 0.4, 0.4); err != nil {
		t.Fatal(err)
	}
	if err := lc.CloseSession(ctx, session.ID, map[string]any{"currentTime": 0.4, "timeListened": 0.4}); err != nil {
		t.Fatal(err)
	}
	listener.call(t, "user_bookmark_edit", map[string]any{"item": one, "add_bookmarks": []any{map[string]any{"time_s": 0.25, "title": "Zzyzx Listener Mark"}}})
	named := map[string]any{"user": listener.Name}

	// the server keeps a bookmark on a deleted book and will not remove it
	// afterwards, so the deleting account's own go first
	if n := acc.Num(t, suite.Call(t, "item_delete", map[string]any{"confirm": true, "item": one})["bookmarks_removed"], "bookmarks_removed"); n != 1 {
		t.Errorf("bookmarks_removed = %d, want the admin's one", n)
	}

	t.Run("the book itself", func(t *testing.T) {
		if msg := suite.CallErr(t, "item_get", map[string]any{"item": one}); msg == "" {
			t.Error("the deleted book still resolves by id")
		}
		if items := acc.Rows(t, suite.Call(t, "library_search", map[string]any{"library": library, "query": titles[0]})["items"], "items"); len(items) != 0 {
			t.Errorf("library_search finds the deleted book: %v", items)
		}
		if n := acc.Num(t, suite.Call(t, "library_get", map[string]any{"library": library})["items"], "items"); n != 2 {
			t.Errorf("library_get items = %d, want 2", n)
		}
		if n := acc.Num(t, suite.Call(t, "audit_all", map[string]any{"library": library})["items_scanned"], "items_scanned"); n != 2 {
			t.Errorf("audit_all scanned %d, want 2", n)
		}
	})

	t.Run("collections", func(t *testing.T) {
		listed := map[string]int{}
		for _, c := range acc.Rows(t, suite.Call(t, "collection_list", map[string]any{"library": library})["collections"], "collections") {
			listed[acc.Str(c["name"])] = acc.Num(t, c["books"], "books")
		}
		if got := titlesIn(t, suite.Call(t, "collection_get", map[string]any{"collection": "Zzyzx Gone Shelf"})["items"], "items"); !slices.Equal(got, []string{titles[1]}) || listed["Zzyzx Gone Shelf"] != 1 {
			t.Errorf("the shelf holds %v and is listed with %d books, want Two alone", got, listed["Zzyzx Gone Shelf"])
		}
		// a collection whose only book went: gone, or empty in both views
		if n, ok := listed["Zzyzx Gone Alone"]; ok {
			got := acc.Rows(t, suite.Call(t, "collection_get", map[string]any{"collection": "Zzyzx Gone Alone"})["items"], "items")
			if n != 0 || len(got) != 0 {
				t.Errorf("the emptied collection is listed with %d books and holds %v", n, got)
			}
		}
	})

	t.Run("playlists", func(t *testing.T) {
		listed := map[string]int{}
		for _, p := range acc.Rows(t, suite.Call(t, "playlist_list", map[string]any{"library": library})["playlists"], "playlists") {
			listed[acc.Str(p["name"])] = acc.Num(t, p["entries"], "entries")
		}
		entries := acc.Rows(t, suite.Call(t, "playlist_get", map[string]any{"playlist": "Zzyzx Gone Queue"})["entries"], "entries")
		if len(entries) != 1 || listed["Zzyzx Gone Queue"] != 1 {
			t.Errorf("the queue holds %v and is listed with %d entries, want Three alone", entries, listed["Zzyzx Gone Queue"])
		}
		if n, ok := listed["Zzyzx Gone Solo"]; ok {
			got := acc.Rows(t, suite.Call(t, "playlist_get", map[string]any{"playlist": "Zzyzx Gone Solo"})["entries"], "entries")
			if n != 0 || len(got) != 0 {
				t.Errorf("the emptied playlist is listed with %d entries and holds %v", n, got)
			}
		}
	})

	t.Run("progress", func(t *testing.T) {
		for _, it := range acc.Rows(t, suite.Call(t, "user_in_progress", nil)["items"], "items") {
			if it["id"] == one {
				t.Errorf("the deleted book is still in progress: %v", it)
			}
		}
		for _, p := range acc.Rows(t, suite.Call(t, "user_get", nil)["recent_progress"], "recent_progress") {
			if p["item_id"] == one {
				t.Errorf("user_get still has progress on the deleted book: %v", p)
			}
		}
		for _, p := range acc.Rows(t, suite.Call(t, "user_get", named)["recent_progress"], "recent_progress") {
			if p["item_id"] == one {
				t.Errorf("the listener still has progress on the deleted book: %v", p)
			}
		}
	})

	t.Run("bookmarks on it", func(t *testing.T) {
		for _, b := range acc.Rows(t, suite.Call(t, "user_bookmarks", nil)["bookmarks"], "bookmarks") {
			if b["item_id"] == one {
				t.Errorf("the admin's bookmark outlived the delete: %v", b)
			}
		}
		// another account's is out of reach: it reads as on a deleted book,
		// from both sides, and a removal says why it cannot be done
		for who, marks := range map[string]map[string]any{"self": listener.call(t, "user_bookmarks", nil), "admin": suite.Call(t, "user_bookmarks", named)} {
			got := acc.Rows(t, marks["bookmarks"], "bookmarks")
			if len(got) != 1 || got[0]["item_id"] != one || !acc.BoolOf(got[0]["item_deleted"]) {
				t.Errorf("%s: the listener's bookmarks = %v, want the one, marked item_deleted", who, got)
			}
		}
		if msg := listener.callErr(t, "user_bookmark_edit", map[string]any{"item": one, "remove_bookmarks": []any{0.25}}); !strings.Contains(msg, "will not remove") {
			t.Errorf("removing a bookmark on a deleted book: %s", msg)
		}
	})

	t.Run("catalogue", func(t *testing.T) {
		if books := titlesIn(t, suite.Call(t, "series_get", map[string]any{"library": library, "series": "Zzyzx Gone Saga"})["books"], "books"); !slices.Equal(books, []string{titles[1]}) {
			t.Errorf("the series holds %v, want Two", books)
		}
		for _, s := range acc.Rows(t, suite.Call(t, "series_list", map[string]any{"library": library})["series"], "series") {
			if s["name"] == "Zzyzx Gone Saga" && acc.Num(t, s["books"], "books") != 1 {
				t.Errorf("series_list row = %v, want 1 book", s)
			}
		}
		if books := titlesIn(t, suite.Call(t, "author_get", map[string]any{"library": library, "author": author})["books"], "books"); len(books) != 2 {
			t.Errorf("author_get books = %v, want Two and Three", books)
		}
		for _, n := range acc.Rows(t, suite.Call(t, "narrator_list", map[string]any{"library": library})["narrators"], "narrators") {
			if n["name"] == reader && acc.Num(t, n["books"], "books") != 2 {
				t.Errorf("narrator_list row = %v, want 2 books", n)
			}
		}
	})

	t.Run("history stays", func(t *testing.T) {
		history := acc.Rows(t, suite.Call(t, "user_history", named)["sessions"], "sessions")
		if len(history) != 1 || history[0]["title"] != titles[0] {
			t.Errorf("the listener's history = %v, want the session on the deleted book, by its title", history)
		}
		if top := acc.Rows(t, suite.Call(t, "user_stats", named)["top_items"], "top_items"); len(top) != 1 || top[0]["title"] != titles[0] {
			t.Errorf("the listener's top items = %v", top)
		}
	})

	t.Run("the rest deleted: an empty series, a bookless author", func(t *testing.T) {
		suite.Call(t, "item_delete", map[string]any{"confirm": true, "item": two})
		suite.Call(t, "item_delete", map[string]any{"confirm": true, "item": three})
		if err := waitForItems(library, 0); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"Zzyzx Gone Saga", "Zzyzx Lone Saga"} {
			if msg := suite.CallErr(t, "series_get", map[string]any{"library": library, "series": name}); !strings.Contains(msg, "no series named") {
				t.Errorf("a series with no books left: %s", msg)
			}
		}
		if narrators := acc.Rows(t, suite.Call(t, "narrator_list", map[string]any{"library": library})["narrators"], "narrators"); len(narrators) != 0 {
			t.Errorf("narrator_list = %v, want nobody", narrators)
		}
		bookless := func() bool {
			for _, r := range acc.Rows(t, suite.Call(t, "audit_authors", map[string]any{"library": library})["records"], "records") {
				if r["name"] == author && slices.Contains(acc.Strs(t, r["problems"], "problems"), "no_books") {
					return true
				}
			}
			return false
		}
		if !bookless() {
			t.Fatal("audit_authors does not report the author with no books")
		}
		suite.Call(t, "author_delete", map[string]any{"library": library, "author": author})
		if bookless() {
			t.Error("the deleted author is still reported")
		}
		if msg := suite.CallErr(t, "author_get", map[string]any{"library": library, "author": author}); msg == "" {
			t.Error("the deleted author still resolves")
		}
	})

	t.Run("scanned back from the folders", func(t *testing.T) {
		suite.Call(t, "library_scan", map[string]any{"library": library})
		if err := waitForItems(library, 3); err != nil {
			t.Fatal(err)
		}
		waitIdle(t)
		listed := acc.Rows(t, suite.Call(t, "library_items", map[string]any{"library": library})["items"], "items")
		back := make([]string, 0, len(listed))
		for _, it := range listed {
			back = append(back, acc.Str(it["title"]))
			if slices.Contains([]string{one, two, three}, acc.Str(it["id"])) {
				t.Errorf("%v came back under its deleted id", it["title"])
			}
		}
		slices.Sort(back)
		want := slices.Clone(titles)
		slices.Sort(want)
		if !slices.Equal(back, want) {
			t.Errorf("scanned back %v, want %v", back, want)
		}
	})
}
