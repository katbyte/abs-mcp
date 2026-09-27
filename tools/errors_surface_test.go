package tools

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// The server failing a request a tool relies on is an error the caller sees,
// never an answer built as if the request had come back empty: a title left
// blank, a book counted as missing from a store, progress read as none.

// failsAfter serves a route that answers body n times, then 500.
func (f *fakeABS) failsAfter(route string, n int32, body string) {
	var served atomic.Int32
	f.mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) {
		if served.Add(1) > n {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// wantErr fails the test unless err is set and says each of the words.
func wantErr(t *testing.T, what string, err error, words ...string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error, want one saying %q", what, words)
		return
	}
	for _, w := range words {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("%s: %v, want it to say %q", what, err, w)
		}
	}
}

func TestProgressRemovalThatCannotBeReadBackIsAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", `"duration":3600`))
	f.failsAfter("GET /api/me/progress/"+bookB1, 1, `{"id":"mp1","libraryItemId":"`+bookB1+`","currentTime":60,"duration":3600,"progress":0.02}`)
	f.json("DELETE /api/me/progress/mp1", `{}`)
	call := toolCaller(t, f)

	_, err := call("user_progress_remove", map[string]any{"item": bookB1})
	wantErr(t, "a removal whose read-back failed", err, "accepted the removal", "reading it back", "500")
}

func TestBookmarksWhoseItemsCannotBeReadAreAnError(t *testing.T) {
	t.Parallel()

	marks := `{"bookmarks":[{"libraryItemId":"` + bookB1 + `","title":"Mark","time":5}]}`

	f := newFakeABS(t)
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root"}`)
	f.json("GET /api/me/bookmarks", marks)
	f.fails("POST /api/items/batch/get")
	call := toolCaller(t, f)
	_, err := call("user_bookmarks", nil)
	wantErr(t, "the batch of bookmarked items failing", err, "reading the bookmarked items", "500")

	// the batch leaves the item out, and reading it alone fails with
	// something other than not found: not a deleted item
	f = newFakeABS(t)
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root"}`)
	f.json("GET /api/me/bookmarks", marks)
	f.json("POST /api/items/batch/get", `{"libraryItems":[]}`)
	f.fails("GET /api/items/" + bookB1)
	call = toolCaller(t, f)
	_, err = call("user_bookmarks", nil)
	wantErr(t, "a left-out item failing to read", err, bookB1, "500")
}

func TestBookmarkRemovalOnAGoneItemSaysTheAccountReadFailed(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.fails("GET /api/me")
	call := toolCaller(t, f)

	_, err := call("user_bookmark_edit", map[string]any{"item": itemID, "action": "remove", "time_s": 5})
	wantErr(t, "the account read failing beside a gone item", err, "404", "reading the account's bookmarks", "500")
}

func TestListeningStatsTheServerGarbledAreAnError(t *testing.T) {
	t.Parallel()

	for name, stats := range map[string]string{
		"a day that is not a date": `{"totalTime":60,"today":0,"days":{"yesterday":60},"items":{}}`,
		"a title that is not read": `{"totalTime":60,"today":0,"days":{},"items":{"x":{"id":"x","timeListening":60,"mediaMetadata":"not an object"}}}`,
	} {
		f := newFakeABS(t)
		accounts(f)
		f.json("GET /api/me/listening-stats", stats)
		call := toolCaller(t, f)

		_, err := call("user_stats", nil)
		wantErr(t, name, err, "listening stats")
	}
}

func TestEpisodeProgressThatCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith(`{"id":"e1","title":"One","publishedAt":1000}`))
	f.fails("GET /api/me")
	f.fails("GET /api/me/progress/" + podcastID + "/e1")
	call := toolCaller(t, f)

	_, err := call("podcast_episodes", map[string]any{"item": podcastID})
	wantErr(t, "podcast_episodes with the account unreadable", err, "progress", "500")
	_, err = call("podcast_episode_get", map[string]any{"item": podcastID, "episode": "e1"})
	wantErr(t, "podcast_episode_get with its progress unreadable", err, "progress", "500")
}

func TestServerInfoSaysWhatFailed(t *testing.T) {
	t.Parallel()

	base := func() *fakeABS {
		f := newFakeABS(t)
		f.json("GET /status", `{"serverVersion":"2.30.0"}`)
		f.json("GET /api/me", `{"id":"u1","username":"kt","type":"user"}`)
		f.json("GET /api/libraries", `{"libraries":[]}`)
		return f
	}

	// a key the totals are refused to is told so, and is no error
	f := base()
	f.mux.HandleFunc("GET /api/stats/server", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	})
	out, err := toolCaller(t, f)("server_info", nil)
	if err != nil || !strings.Contains(str(t, out["note"]), "need an admin key") {
		t.Errorf("totals refused to the key = %v, %v, want the note and no error", out, err)
	}

	for name, fail := range map[string]string{
		"the providers":     "GET /api/search/providers",
		"the totals":        "GET /api/stats/server",
		"the users":         "GET /api/users",
		"the open sessions": "GET /api/sessions/open",
	} {
		f := base()
		f.json("GET /api/stats/server", `{"books":{"numItems":0},"podcasts":{"numItems":0},"total":{"numItems":0}}`)
		f.json("GET /api/users", `{"users":[]}`)
		f.json("GET /api/sessions/open", `{"sessions":[]}`)
		f.json("GET /api/search/providers", `{"providers":{"books":[],"podcasts":[]}}`)
		f.fails(fail)
		_, err := toolCaller(t, f)("server_info", nil)
		wantErr(t, name+" failing", err, "500")
	}
}

func TestLibraryStatsThatFailAreAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"accessAllTags":true,"accessExplicitContent":true}}`)
	f.json("GET /api/libraries/"+libID, `{"library":{"id":"`+libID+`","name":"Books","mediaType":"book"},"filterdata":{},"issues":0}`)
	f.fails("GET /api/libraries/" + libID + "/stats")
	call := toolCaller(t, f)

	_, err := call("library_get", map[string]any{"library": "Books"})
	wantErr(t, "library_get with its stats failing", err, "library's stats", "500")
}

func TestAnAuthorEditThatCannotBeReadBackSaysItWasMade(t *testing.T) {
	t.Parallel()

	const authorID = "55555555-5555-4555-8555-55555555ffff"
	author := `{"id":"` + authorID + `","name":"Emily Andras","libraryItems":[]}`
	f := newFakeABS(t)
	f.failsAfter("GET /api/authors/"+authorID, 1, author)
	f.json("PATCH /api/authors/"+authorID, `{"author":`+author+`,"merged":false}`)
	call := toolCaller(t, f)

	_, err := call("author_edit", map[string]any{"author": authorID, "description": "new"})
	wantErr(t, "an edit whose read-back failed", err, "was made", "reading it back", "500")
}

func TestAMatchWhoseLibraryCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert"`, ""))
	f.fails("GET /api/libraries/" + libID)
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	_, err := call("item_match", map[string]any{"item": itemID})
	wantErr(t, "item_match with no provider and the library unreadable", err, "library's provider", "500")
	if got := f.requests("/api/search/books"); len(got) != 0 {
		t.Errorf("searched anyway, with no provider: %v", got)
	}

	out, err := call("item_match_apply_batch", map[string]any{"matches": []any{map[string]any{"item": itemID, "asin": "B0X"}}})
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["results"])
	if num(t, out["failed"]) != 1 || len(rows) != 1 || !strings.Contains(str(t, rows[0]["error"]), "library's provider") {
		t.Errorf("batch = %v, want the row failed, saying why", out)
	}
	if got := f.requests("/api/items/" + itemID + "/match"); len(got) != 0 {
		t.Errorf("matched anyway, with no provider: %v", got)
	}
}

// A store whose search fails says nothing about whether it has the book: the
// book is not counted as missing from the stores, not tagged with the next
// store, and the row says what failed.
func TestAStoreSearchThatFailsIsNotANotFound(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f)
	serveListing(f, item("m1", "One", `"asin":"B001"`, ""))
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("provider") == "audible.ca" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`[{"title":"One","asin":"B001"}]`))
	})
	call := toolCaller(t, f)

	out, err := call("item_match_tag", map[string]any{"library": "Books", "providers": []any{"audible.ca", "audible"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["rows"])
	if num(t, out["failed"]) != 1 || num(t, out["not_found"]) != 0 || num(t, out["tagged"]) != 0 || len(rows) != 1 || !strings.Contains(str(t, rows[0]["error"]), "audible.ca") {
		t.Errorf("tag = %v, want one failed row naming the store, nothing tagged or not found", out)
	}
	if got := f.requests("/api/search/books"); len(got) != 1 {
		t.Errorf("%d searches, want the failed store's only", len(got))
	}

	// and the provider list the names are checked against failing is an
	// error, not a check skipped
	f = newFakeABS(t)
	audibleLibrary(f)
	f.fails("GET /api/search/providers")
	_, err = toolCaller(t, f)("item_match_tag", map[string]any{"library": "Books", "providers": []any{"audible.ca"}})
	wantErr(t, "the provider list failing", err, "server's providers", "500")
}

// coverFails is a book whose cover the fake server fails to send.
const coverFails = "li_fail"

// A cover the server fails to send is no clean cover, and a cover file that
// is gone is a missing one: the library sweep says both, book by book, and
// audit_all says how many covers it could not judge.
func TestCoversTheServerFailsToSendAreSaid(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("li_ok", "Fine", "", `"coverPath":"/ok.jpg"`),
		item("li_gone", "File Gone", "", `"coverPath":"/gone.jpg"`),
		item(coverFails, "Fails", "", `"coverPath":"/fail.jpg"`),
	))
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "li_ok":
			_, _ = io.WriteString(w, encodeJPEG(t, artwork(600, 0), 85))
		case coverFails:
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	})
	call := toolCaller(t, f)

	out, err := call("audit_covers", map[string]any{"library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]map[string]any{}
	for _, row := range list(t, out["findings"]) {
		rows[str(t, row["id"])] = row
	}
	if r := rows["li_gone"]; r == nil || str(t, r["problem"]) != "missing" || !strings.Contains(str(t, r["why"]), "gone") {
		t.Errorf("the gone file = %v, want missing", r)
	}
	if r := rows[coverFails]; r == nil || str(t, r["problem"]) != "skipped" || !strings.Contains(str(t, r["why"]), "500") {
		t.Errorf("the failed cover = %v, want skipped with the server's 500", r)
	}
	if num(t, out["skipped"]) != 1 || num(t, out["covers_checked"]) != 1 {
		t.Errorf("skipped %v checked %v, want one each", out["skipped"], out["covers_checked"])
	}

	// audit_all counts findings only, so it says the cover it could not
	// judge, or an outage would read as clean covers. Its deep audits read
	// every book's files and ask the store about each
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+item("li_ok", "Fine", "", "")+`,`+item("li_gone", "File Gone", "", "")+`,`+item(coverFails, "Fails", "", "")+`]}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/search/books", `[]`)
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"}`)
	all, err := call("audit_all", map[string]any{"library": "Books", "deep": true})
	if err != nil {
		t.Fatal(err)
	}
	partial := list(t, all["partial"])
	if !slices.ContainsFunc(partial, func(row map[string]any) bool {
		return row["audit"] == "audit_covers" && strings.Contains(str(t, row["reason"]), "1 covers could not be read")
	}) {
		t.Errorf("audit_all partial = %v, want audit_covers saying one cover was not judged", partial)
	}
}

// A cover whose size the server fails to give is not replaced as if it had
// none: the store's copy may be smaller.
func TestACoverUpgradeWithTheSizeUnreadWritesNothing(t *testing.T) {
	t.Parallel()

	f := storeFixture(t)
	f.json("GET /api/items/"+coverFails, item(coverFails, "Fails", `"asin":"B001"`, `"coverPath":"/fail.jpg"`))
	call := toolCaller(t, f)

	_, err := call("item_cover_upgrade", map[string]any{"items": []any{coverFails}, "providers": []any{"audible"}})
	wantErr(t, "an upgrade over a cover whose size failed", err, "size", "500")
	if got := f.requests("/api/items/" + coverFails + "/cover"); slices.ContainsFunc(got, func(r request) bool { return r.Method == http.MethodPost }) {
		t.Errorf("the cover was replaced: %v", got)
	}

	// and the store comparison lists the book as skipped, saying why,
	// rather than dropping its size and comparing on
	f.json("GET /api/libraries/"+libID+"/items", page(item(coverFails, "Fails", `"asin":"B001"`, `"coverPath":"/fail.jpg"`)))
	out, err := call("audit_covers", map[string]any{"store": true, "providers": []any{"audible"}, "library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	var skipped bool
	for _, row := range list(t, out["findings"]) {
		why := str(t, row["why"])
		if str(t, row["id"]) == coverFails && str(t, row["problem"]) == "skipped" && strings.Contains(why, "the cover's size") && strings.Contains(why, "500") {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("findings = %v, want the store's row for the book skipped over its size, with the server's 500", out["findings"])
	}
}

func TestAMatchThatCannotBeReadBackIsAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var matched atomic.Bool
	f.mux.HandleFunc("GET /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
		if matched.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, item(itemID, "Dune", `"authorName":"Frank Herbert"`, ""))
	})
	f.mux.HandleFunc("POST /api/items/"+itemID+"/match", func(w http.ResponseWriter, _ *http.Request) {
		matched.Store(true)
		_, _ = io.WriteString(w, `{"updated":true,"libraryItem":`+item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B0"`, "")+`}`)
	})
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true,"libraryItem":`+item(itemID, "Dune", `"asin":"B0"`, "")+`}`)
	call := toolCaller(t, f)

	_, err := call("item_match_apply", map[string]any{"item": itemID, "asin": "B0", "provider": "audible"})
	wantErr(t, "a match whose read-back failed", err, "matched", "reading the item back", "500")
}

func TestSessionsWhoseUsersCannotBeReadAreAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/sessions/open", `{"sessions":[{"id":"s1","userId":"u1","libraryItemId":"`+bookB1+`","displayTitle":"First","duration":60,"currentTime":30}]}`)
	f.fails("GET /api/users")
	call := toolCaller(t, f)

	_, err := call("server_sessions", nil)
	wantErr(t, "sessions with the users unreadable", err, "reading the users", "500")
}

// An add that fails, and the rename tried in case another client made it
// meanwhile failing too, says both.
func TestABookmarkAddThatFailsSaysWhy(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", `"duration":3600`))
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","bookmarks":[]}`)
	f.json("GET /api/me/bookmarks", `{"bookmarks":[]}`)
	f.mux.HandleFunc("POST /api/me/item/"+bookB1+"/bookmark", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the add failed", http.StatusForbidden)
	})
	f.mux.HandleFunc("PATCH /api/me/item/"+bookB1+"/bookmark", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such bookmark", http.StatusNotFound)
	})
	call := toolCaller(t, f)

	_, err := call("user_bookmark_edit", map[string]any{"item": bookB1, "action": "add", "time_s": 5, "title": "Mark"})
	wantErr(t, "an add and a rename both failing", err, "adding the bookmark failed", "the add failed", "no such bookmark")
}

// A name looked up while the account list fails is an error, not a quiet
// turn to the caller's own account; "me" is the caller whatever the list.
func TestAUserLookupThatFailsIsAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/me", `{"id":"`+meID+`","username":"kt","type":"root"}`)
	f.fails("GET /api/users")
	f.json("GET /api/me/listening-stats", `{"totalTime":60,"today":0,"days":{},"items":{"x":{"id":"x","timeListening":60}}}`)
	call := toolCaller(t, f)

	_, err := call("user_stats", map[string]any{"user": "kt"})
	wantErr(t, "a name looked up with the account list failing", err, "looking up the user", "500")
	// a key refused the account list may still name itself
	f.answer("GET /api/users", reply{http.StatusForbidden, "Forbidden"})
	if _, err := call("user_stats", map[string]any{"user": "kt"}); err != nil {
		t.Errorf("its own name, with the account list refused: %v", err)
	}
	// and an item the stats name with no metadata has no title, not an error
	if _, err := call("user_stats", map[string]any{"user": "me"}); err != nil {
		t.Errorf("me, with the account list failing: %v", err)
	}
}

// A merge the server carried out for fewer books than it was sent says so,
// rather than listing every book as moved.
func TestASeriesMergeThatMovedTooFewSaysSo(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	f.json("POST /api/items/batch/update", `{"updates":1}`) // of the two sent
	call := toolCaller(t, f)

	_, err := call("series_merge", map[string]any{"from": "The Stormlight Archive", "into": "Stormlight Archive"})
	wantErr(t, "a merge the server moved one of two books for", err, "moved 1 of the 2 books")
}

// A series whose book the full read leaves out is not listed from the
// one-series copy the listing gave, which a merge would write back.
func TestASeriesBookLeftOutOfTheFullReadIsAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/series/"+seriesA, `{"id":"`+seriesA+`","name":"Stormlight Archive","libraryId":"`+libID+`"}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"`+seriesA+`","name":"Stormlight Archive","libraryId":"`+libID+`"}],"total":1}`)
	f.json("GET /api/libraries/"+libID+"/items", page(`{"id":"i3","libraryId":"`+libID+`","mediaType":"book","media":{"metadata":{"title":"Oathbringer","series":{"id":"`+seriesA+`","name":"Stormlight Archive","sequence":"3"}}}}`))
	f.json("POST /api/items/batch/get", `{"libraryItems":[]}`)
	call := toolCaller(t, f)

	_, err := call("series_get", map[string]any{"series": seriesA})
	wantErr(t, "a series book left out of the full read", err, "Oathbringer", "not in the server's reply")
}

// A book the server leaves out of a batch read, deleted meanwhile, is not
// edited from what was known of it before.
func TestABookLeftOutOfABatchReadIsNotEdited(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
	f.json("POST /api/items/batch/get", `{"libraryItems":[]}`)
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	call := toolCaller(t, f)

	_, err := call("item_batch_edit", map[string]any{"items": []any{bookB1}, "add_tags": []any{"x"}})
	wantErr(t, "a book left out of the read before the edit", err, "not in the server's reply", "nothing was changed")
	if got := f.requests("/api/items/batch/update"); len(got) != 0 {
		t.Errorf("edited anyway: %v", got)
	}
}

// A cover in a format Go cannot read is the file's problem: the sweep skips
// it, saying so, and goes on.
func TestAnUndecodableCoverIsSkippedNotAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(item("li_webp", "Webp", "", `"coverPath":"/c.webp"`)))
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "RIFF....WEBPVP8 not an image Go reads")
	})
	call := toolCaller(t, f)

	out, err := call("audit_covers", map[string]any{"library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["findings"])
	if num(t, out["skipped"]) != 1 || len(rows) != 1 || !strings.Contains(str(t, rows[0]["why"]), "cannot be decoded") {
		t.Errorf("a webp cover = %v, want one skipped row saying it cannot be decoded", out)
	}
}
