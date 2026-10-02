package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	meID     = "f1f1f1f1-0000-4000-8000-000000000001"
	readerID = "f1f1f1f1-0000-4000-8000-000000000002"
)

// accounts is the API key's own account, kt, and one other, reader.
func accounts(f *fakeABS) {
	f.json("GET /api/me", `{"id":"`+meID+`","username":"kt","type":"root"}`)
	f.json("GET /api/users", `{"users":[{"id":"`+meID+`","username":"kt","type":"root"},{"id":"`+readerID+`","username":"reader","type":"user"}]}`)
	f.json("GET /api/users/"+meID, `{"id":"`+meID+`","username":"kt","type":"root"}`)
	f.json("GET /api/users/"+readerID, `{"id":"`+readerID+`","username":"reader","type":"user"}`)
}

// The API key's own account named by its username or id is still its own: a
// year in review, kept only for the caller, was refused as "not kt" to kt.
func TestNamingYourOwnAccountIsYourOwn(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	accounts(f)
	f.json("GET /api/me/stats/year/2025", `{"totalListeningSessions":3,"totalListeningTime":3600}`)
	call := toolCaller(t, f)

	for _, name := range []string{"kt", "KT", meID} {
		out, err := call("user_stats", map[string]any{"user": name, "year": 2025})
		if err != nil {
			t.Errorf("user=%s year=2025: %v", name, err)
			continue
		}
		if num(t, out["sessions"]) != 3 || str(t, out["user"]) != "kt" {
			t.Errorf("user=%s year=2025: %v", name, out)
		}
		got, err := call("user_get", map[string]any{"user": name})
		if err != nil {
			t.Fatal(err)
		}
		if !boolOf(t, got["self"]) {
			t.Errorf("user_get user=%s: self = false", name)
		}
	}
	if _, err := call("user_stats", map[string]any{"user": "reader", "year": 2025}); err == nil || !strings.Contains(err.Error(), "not reader") {
		t.Errorf("someone else's year in review: %v, want refused", err)
	}
	if got, err := call("user_get", map[string]any{"user": "reader"}); err != nil || boolOf(t, got["self"]) {
		t.Errorf("user_get user=reader: %v %v, want someone else", got, err)
	}
}

// sessionsOn serves a user's sessions newest first, a page at a time, the way
// the server does: n of them a minute apart, on bookB2 but for those at the
// positions in on, which are on bookB1; from old on they are a year old.
func sessionsOn(f *fakeABS, userID string, n, old int, on ...int) {
	now := time.Now()
	f.mux.HandleFunc("GET /api/users/"+userID+"/listening-sessions", func(w http.ResponseWriter, r *http.Request) {
		per, _ := strconv.Atoi(r.URL.Query().Get("itemsPerPage"))
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		rows := []string{}
		for i := pg * per; i < min((pg+1)*per, n); i++ {
			book, when := bookB2, now.Add(-time.Duration(i)*time.Minute)
			if slices.Contains(on, i) {
				book = bookB1
			}
			if i >= old {
				when = when.AddDate(-1, 0, 0)
			}
			rows = append(rows, fmt.Sprintf(`{"id":"s%d","userId":%q,"libraryItemId":%q,"displayTitle":"t","updatedAt":%d}`, i, userID, book, when.UnixMilli()))
		}
		_, _ = fmt.Fprintf(w, `{"total":%d,"sessions":[%s]}`, n, strings.Join(rows, ","))
	})
}

// The route for someone else's sessions has no item filter, so the newest
// limit of them - all on another book - held none of the item's: they are
// paged through until limit are found or the window is passed.
func TestUserHistoryForAnotherOnOneItem(t *testing.T) {
	t.Parallel()

	t.Run("found past the first page", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		accounts(f)
		f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
		sessionsOn(f, readerID, 1000, 200, 120, 180, 250)
		call := toolCaller(t, f)

		out, err := call("user_history", map[string]any{"user": "reader", "item": bookB1, "limit": 5})
		if err != nil {
			t.Fatal(err)
		}
		sessions := list(t, out["sessions"])
		ids := make([]string, 0, len(sessions))
		for _, s := range sessions {
			ids = append(ids, str(t, s["id"]))
		}
		if !slices.Equal(ids, []string{"s120", "s180"}) {
			t.Errorf("sessions = %v, want s120 and s180: s250 is past the 60 days", ids)
		}
		// read to the end of their history, all ten pages, to count s250 too
		if got := f.requests("/api/users/" + readerID + "/listening-sessions"); len(got) != 10 {
			t.Errorf("read %d pages, want 10", len(got))
		}
		if out["note"] != nil || num(t, out["total_sessions"]) != 3 {
			t.Errorf("note = %v, total_sessions = %v: want no note, the search reached the end, and the book's 3", out["note"], out["total_sessions"])
		}

		out, err = call("user_history", map[string]any{"user": "reader", "item": bookB1, "limit": 1})
		if err != nil {
			t.Fatal(err)
		}
		if got := list(t, out["sessions"]); len(got) != 1 || str(t, got[0]["id"]) != "s120" {
			t.Errorf("limit 1: %v, want s120", got)
		}
		// the count is the book's sessions whatever the limit: it answered
		// with every session they have, on anything
		if n := num(t, out["total_sessions"]); n != 3 {
			t.Errorf("limit 1: total_sessions = %d, want the book's 3, not all 1000", n)
		}
	})

	t.Run("bounded, and says so", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		accounts(f)
		f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
		sessionsOn(f, readerID, 50000, 50000)
		call := toolCaller(t, f)

		out, err := call("user_history", map[string]any{"user": "reader", "item": bookB1})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.requests("/api/users/" + readerID + "/listening-sessions"); len(got) != historyPages {
			t.Errorf("read %d pages, want the %d bound", len(got), historyPages)
		}
		if len(list(t, out["sessions"])) != 0 || !strings.Contains(str(t, out["note"]), "newest") {
			t.Errorf("out = %v, want nothing found and a note saying how far it looked", out)
		}
	})
}

// A percent outside 0-100, a position before the start, a percent of
// something with no length, and progress on a podcast rather than one of its
// episodes are refused before anything is sent.
func TestUserProgressSetRefusesWhatCannotBe(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", `"duration":60`))
	f.json("GET /api/items/"+bookB2, item(bookB2, "Second", "", ""))
	f.json("GET /api/items/"+podcastID, podcastWith(`{"id":"e1","title":"Pilot","duration":100}`))
	f.json("PATCH /api/me/progress/"+bookB1, `{}`)
	f.json("GET /api/me/progress/"+bookB1, `{"id":"mp1","libraryItemId":"`+bookB1+`","currentTime":30,"duration":60,"progress":0.5}`)
	f.json("PATCH /api/me/progress/"+podcastID+"/e1", `{}`)
	f.json("GET /api/me/progress/"+podcastID+"/e1", `{"id":"mp2","libraryItemId":"`+podcastID+`","episodeId":"e1","currentTime":50,"duration":100,"progress":0.5}`)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"item": bookB1, "percent": 150}, "between 0 and 100"},
		{map[string]any{"item": bookB1, "percent": -5}, "between 0 and 100"},
		{map[string]any{"item": bookB1, "position_s": -1}, "before the start"},
		{map[string]any{"item": bookB2, "percent": 50}, "no length"},
		{map[string]any{"item": podcastID, "percent": 50}, "name the episode"},
		{map[string]any{"item": podcastID, "finished": true}, "name the episode"},
	} {
		if _, err := call("user_progress_set", tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
	for _, r := range f.changes() {
		t.Fatalf("a refused change was sent: %s %s", r.Method, r.Path)
	}

	for _, args := range []map[string]any{{"item": bookB1, "percent": 50}, {"item": podcastID, "episode": "e1", "percent": 50}} {
		out, err := call("user_progress_set", args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if p, ok := out["progress"].(map[string]any); !ok || num(t, p["percent"]) != 50 {
			t.Errorf("%v: %v", args, out)
		}
	}
}

// An add, a rename and a removal each say which they were and name the
// bookmark; removing one that is not there is refused, not sent.
func TestUserBookmarkEditSaysWhatChanged(t *testing.T) {
	t.Parallel()

	type mark struct {
		LibraryItemID string  `json:"libraryItemId"`
		Title         string  `json:"title"`
		Time          float64 `json:"time"`
	}
	var mu sync.Mutex
	var marks []mark
	f := newFakeABS(t)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
	f.mux.HandleFunc("GET /api/me/bookmarks", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"bookmarks": marks})
	})
	f.mux.HandleFunc("POST /api/me/item/"+bookB1+"/bookmark", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		m := mark{LibraryItemID: bookB1}
		_ = json.NewDecoder(r.Body).Decode(&m)
		marks = append(marks, m)
		_ = json.NewEncoder(w).Encode(m)
	})
	f.mux.HandleFunc("PATCH /api/me/item/"+bookB1+"/bookmark", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var m mark
		_ = json.NewDecoder(r.Body).Decode(&m)
		for i := range marks {
			if marks[i].Time == m.Time {
				marks[i].Title = m.Title
				_ = json.NewEncoder(w).Encode(marks[i])
				return
			}
		}
		http.NotFound(w, r)
	})
	f.mux.HandleFunc("DELETE /api/me/item/"+bookB1+"/bookmark/{time}", func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		at, _ := strconv.ParseFloat(r.PathValue("time"), 64)
		marks = slices.DeleteFunc(marks, func(m mark) bool { return m.Time == at })
	})
	call := toolCaller(t, f)

	for _, tc := range []struct {
		args             map[string]any
		list, title, was string
	}{
		{map[string]any{"add_bookmarks": []any{map[string]any{"time_s": 5, "title": "Mark"}}}, "added", "Mark", ""},
		{map[string]any{"add_bookmarks": []any{map[string]any{"time_s": 5, "title": "Renamed"}}}, "renamed", "Renamed", "Mark"},
		{map[string]any{"remove_bookmarks": []any{5}}, "removed", "Renamed", ""},
	} {
		tc.args["item"] = bookB1
		out, err := call("user_bookmark_edit", tc.args)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		rows := list(t, out[tc.list])
		if len(rows) != 1 || str(t, rows[0]["title"]) != tc.title || str(t, rows[0]["was_titled"]) != tc.was || len(out) != 1 {
			t.Errorf("%v: %v, want one %s, %q", tc.args, out, tc.list, tc.title)
		}
	}
	if adds := f.requests("/api/me/item/" + bookB1 + "/bookmark"); len(adds) != 2 || adds[0].Method != http.MethodPost || adds[1].Method != http.MethodPatch {
		t.Errorf("sent %v, want a POST to add and a PATCH to rename", adds)
	}

	if _, err := call("user_bookmark_edit", map[string]any{"item": bookB1, "remove_bookmarks": []any{5}}); err == nil || !strings.Contains(err.Error(), "no bookmark") {
		t.Errorf("removing one not there: %v", err)
	}
	if got := f.requests("/api/me/item/" + bookB1 + "/bookmark/5"); len(got) != 1 {
		t.Errorf("sent %d removals, want the one", len(got))
	}
}

// user_progress_set remove says whether there was progress to remove, and reads
// the progress back rather than trusting the delete's answer.
func TestUserProgressRemoveSaysWhatItRemoved(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", `"duration":60`))
	var gone atomic.Bool
	f.mux.HandleFunc("GET /api/me/progress/"+bookB1, func(w http.ResponseWriter, r *http.Request) {
		if gone.Load() {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"id":"mp1","libraryItemId":"`+bookB1+`","currentTime":30,"duration":60,"progress":0.5}`)
	})
	f.mux.HandleFunc("DELETE /api/me/progress/mp1", func(w http.ResponseWriter, _ *http.Request) {
		gone.Store(true)
		_, _ = io.WriteString(w, "OK")
	})
	call := toolCaller(t, f)

	out, err := call("user_progress_set", map[string]any{"item": bookB1, "remove": true})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["removed"]) || str(t, out["item"]) != "First" {
		t.Errorf("first removal = %v, want removed", out)
	}
	if out, err = call("user_progress_set", map[string]any{"item": bookB1, "remove": true}); err != nil || boolOf(t, out["removed"]) {
		t.Errorf("second removal = %v, %v; want nothing removed", out, err)
	}
	if got := f.requests("/api/me/progress/mp1"); len(got) != 1 {
		t.Errorf("deletes sent %v, want one", got)
	}
}

// A book the caller hid from continue listening is off their shelf, as it is
// when an admin reads the shelf for them: the server's own shelf route keeps
// it, and it took the one place a limit of one had.
func TestUserInProgressLeavesOutWhatWasHidden(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/me", `{"id":"u1","username":"reader","type":"user","mediaProgress":[`+
		`{"id":"mp1","libraryItemId":"`+bookB1+`","currentTime":30,"duration":60,"progress":0.5,"hideFromContinueListening":true,"lastUpdate":2000},`+
		`{"id":"mp2","libraryItemId":"`+bookB2+`","currentTime":15,"duration":60,"progress":0.25,"lastUpdate":1000}]}`)
	// newest first, hidden or not, as many as the limit asks
	f.mux.HandleFunc("GET /api/me/items-in-progress", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		shelf := []string{item(bookB1, "First", "", `"duration":60`), item(bookB2, "Second", "", `"duration":60`)}
		_, _ = fmt.Fprintf(w, `{"libraryItems":[%s]}`, strings.Join(shelf[:min(n, len(shelf))], ","))
	})
	call := toolCaller(t, f)

	for _, limit := range []int{0, 1} {
		args := map[string]any{}
		if limit > 0 {
			args["limit"] = limit
		}
		out, err := call("user_in_progress", args)
		if err != nil {
			t.Fatal(err)
		}
		rows := list(t, out["items"])
		if len(rows) != 1 || str(t, rows[0]["id"]) != bookB2 {
			t.Errorf("limit %d: items = %v, want Second alone", limit, out["items"])
			continue
		}
		if p, ok := rows[0]["progress"].(map[string]any); !ok || num(t, p["percent"]) != 25 {
			t.Errorf("limit %d: progress = %v, want 25 percent", limit, rows[0]["progress"])
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

	_, err := call("user_progress_set", map[string]any{"item": bookB1, "remove": true})
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

	_, err := call("user_bookmark_edit", map[string]any{"item": itemID, "remove_bookmarks": []any{5}})
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

	_, err := call("user_bookmark_edit", map[string]any{"item": bookB1, "add_bookmarks": []any{map[string]any{"time_s": 5, "title": "Mark"}}})
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

// The continue-listening route for the API key's own account carries no
// position or percent, so they come from the account's own progress.
func TestUserInProgressHasTheCallersPosition(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/me", `{"id":"u1","username":"reader","type":"user","mediaProgress":[{"id":"mp1","libraryItemId":"`+bookB1+`","currentTime":30,"duration":60,"progress":0.5}]}`)
	f.json("GET /api/me/items-in-progress", `{"libraryItems":[`+item(bookB1, "First", "", `"duration":60`)+`]}`)
	call := toolCaller(t, f)

	out, err := call("user_in_progress", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["items"])
	if len(rows) != 1 {
		t.Fatalf("items = %v", out["items"])
	}
	progress, ok := rows[0]["progress"].(map[string]any)
	if !ok || num(t, progress["percent"]) != 50 {
		t.Errorf("progress = %v, want 50 percent", rows[0]["progress"])
	}
}

// user_get says what an account is kept from: libraries, tags and explicit
// books. An account limited to no libraries is not reported as seeing all.
func TestUserGetReportsRestrictions(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/me", `{"id":"u1","username":"kid","type":"user","librariesAccessible":["`+libID+`"],"itemTagsSelected":["grown-up"],`+
		`"permissions":{"accessAllLibraries":false,"accessAllTags":false,"selectedTagsNotAccessible":true,"accessExplicitContent":false}}`)
	call := toolCaller(t, f)

	out, err := call("user_get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if boolOf(t, out["all_libraries"]) || boolOf(t, out["all_tags"]) || boolOf(t, out["explicit"]) {
		t.Errorf("all_libraries/all_tags/explicit = %v/%v/%v, want all false", out["all_libraries"], out["all_tags"], out["explicit"])
	}
	if !slices.Equal(strs(t, out["libraries"]), []string{libID}) || !slices.Equal(strs(t, out["denied_tags"]), []string{"grown-up"}) || out["tags"] != nil {
		t.Errorf("libraries = %v, denied_tags = %v, tags = %v", out["libraries"], out["denied_tags"], out["tags"])
	}
}

// The server keeps a bookmark on a deleted item and will not remove it: it
// reads as such, a removal says why it fails, and item_delete removes the
// caller's own first, once it knows the delete is allowed.
func TestBookmarksOnADeletedItem(t *testing.T) {
	t.Parallel()

	t.Run("seen, and not removable", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		marks := `[{"libraryItemId":"` + itemID + `","title":"Mark","time":5},{"libraryItemId":"` + bookB1 + `","title":"Kept","time":1}]`
		f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","bookmarks":`+marks+`}`)
		f.json("GET /api/me/bookmarks", `{"bookmarks":`+marks+`}`)
		f.json("POST /api/items/batch/get", `{"libraryItems":[`+item(bookB1, "First", "", "")+`]}`)
		f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
		call := toolCaller(t, f)

		out, err := call("user_bookmarks", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range list(t, out["bookmarks"]) {
			if deleted := b["item_deleted"] != nil && boolOf(t, b["item_deleted"]); deleted != (b["item_id"] == itemID) {
				t.Errorf("bookmark %v: item_deleted = %v", b, deleted)
			}
		}
		if _, err := call("user_bookmark_edit", map[string]any{"item": itemID, "remove_bookmarks": []any{5}}); err == nil || !strings.Contains(err.Error(), "will not remove") {
			t.Errorf("removing a bookmark on a deleted item: %v", err)
		}
	})

	t.Run("item_delete removes the caller's first", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
		f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"delete":true},"bookmarks":[{"libraryItemId":"`+bookB1+`","title":"Mark","time":5},{"libraryItemId":"`+bookB2+`","title":"Other","time":7}]}`)
		f.json("DELETE /api/me/item/"+bookB1+"/bookmark/5", `OK`)
		f.json("DELETE /api/items/"+bookB1, `OK`)
		call := toolCaller(t, f)

		out, err := call("item_delete", map[string]any{"item": bookB1, "confirm": true})
		if err != nil {
			t.Fatal(err)
		}
		if num(t, out["bookmarks_removed"]) != 1 {
			t.Errorf("bookmarks_removed = %v, want 1", out["bookmarks_removed"])
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var deletes []string
		for _, r := range f.seen {
			if r.Method == http.MethodDelete {
				deletes = append(deletes, r.Path)
			}
		}
		if !slices.Equal(deletes, []string{"/api/me/item/" + bookB1 + "/bookmark/5", "/api/items/" + bookB1}) {
			t.Errorf("deletes = %v, want the bookmark, then the item", deletes)
		}
	})

	t.Run("a refused delete costs no bookmark", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
		f.json("GET /api/me", `{"id":"u2","username":"guest","type":"user","permissions":{"delete":false},"bookmarks":[{"libraryItemId":"`+bookB1+`","title":"Mark","time":5}]}`)
		call := toolCaller(t, f)

		if _, err := call("item_delete", map[string]any{"item": bookB1}); err == nil || !strings.Contains(err.Error(), "delete permission") {
			t.Errorf("item_delete without the permission: %v", err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, r := range f.seen {
			if r.Method == http.MethodDelete {
				t.Errorf("sent %s %s", r.Method, r.Path)
			}
		}
	})
}

func TestListeningTimesInSeconds(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	accounts(f)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", `"duration":3600.6`))
	f.json("GET /api/me/progress/"+bookB1, `{"id":"mp1","libraryItemId":"`+bookB1+`","currentTime":1799.5,"duration":3600.6,"progress":0.5}`)
	f.json("GET /api/me/bookmarks", `{"bookmarks":[{"libraryItemId":"`+bookB1+`","title":"Mark","time":12.345}]}`)
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+item(bookB1, "First", "", `"duration":3600.6`)+`]}`)
	today := time.Now().UTC().Format("2006-01-02")
	f.json("GET /api/me/listening-stats", `{"totalTime":7200.4,"today":600.4,"days":{"`+today+`":600.4},`+
		`"items":{"`+bookB1+`":{"id":"`+bookB1+`","timeListening":7200.4,"mediaMetadata":{"title":"First"}}}}`)
	f.json("GET /api/me/stats/year/2025", `{"totalListeningTime":7200.4,"topAuthors":[{"name":"A","time":3600.4}],"topNarrators":[{"name":"N","time":1800.4}],`+
		`"topGenres":[{"genre":"G","time":900.4}],"booksFinished":[{"id":"`+bookB1+`","title":"First","duration":3600.6}]}`)
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	f.mux.HandleFunc("GET /api/me/listening-sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total":2,"sessions":[{"id":"s1","userId":"` + meID + `","libraryItemId":"` + bookB1 + `","displayTitle":"First",` +
			`"duration":3600.6,"timeListening":300.4,"currentTime":1799.5,"updatedAt":` + now + `},` +
			// a session barely begun has a time and a position all the same
			`{"id":"s0","userId":"` + meID + `","libraryItemId":"` + bookB1 + `","displayTitle":"First","duration":3600.6,"timeListening":0.1,"updatedAt":` + now + `}]}`))
	})
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool   string
		args   map[string]any
		want   map[string]float64
		absent []string
	}{
		{"user_progress_get", map[string]any{"item": bookB1}, map[string]float64{"duration_s": 3601, "progress.current_time_s": 1800}, []string{"duration", "progress.current_time"}},
		// a bookmark is removed by its exact time, so the fraction stays
		{"user_bookmarks", nil, map[string]float64{"bookmarks.0.time_s": 12.345}, []string{"bookmarks.0.time", "bookmarks.0.seconds"}},
		{"user_stats", nil, map[string]float64{
			"total_listened_s": 7200, "today_s": 600, "last_7_days_s": 600, "last_30_days_s": 600, "top_items.0.time_s": 7200,
		}, []string{"total_listened", "today", "top_items.0.time"}},
		{"user_stats", map[string]any{"year": 2025}, map[string]float64{
			"total_listened_s": 7200, "top_authors.0.time_s": 3600, "top_narrators.0.time_s": 1800, "top_genres.0.time_s": 900, "finished.0.time_s": 3601,
		}, []string{"total_listened", "top_authors.0.time", "finished.0.time"}},
		{"user_history", nil, map[string]float64{
			"sessions.0.listened_s": 300, "sessions.0.position_s": 1800, "sessions.1.listened_s": 0, "sessions.1.position_s": 0,
		}, []string{"sessions.0.listened", "sessions.0.position"}},
	} {
		out, err := call(tc.tool, tc.args)
		if err != nil {
			t.Fatalf("%s %v: %v", tc.tool, tc.args, err)
		}
		wantNumbers(t, tc.tool, out, tc.want)
		wantAbsent(t, tc.tool, out, tc.absent...)
	}
}

// series takes a whole series off the Continue Series shelf or puts it back,
// and the account is read back to say which it is.
func TestUserProgressSetHidesASeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/series/"+seriesA, `{"id":"`+seriesA+`","name":"Dune","libraryId":"`+libID+`"}`)
	f.json("GET /api/me/series/"+seriesA+"/remove-from-continue-listening", `{}`)
	f.json("GET /api/me/series/"+seriesA+"/readd-to-continue-listening", `{}`)
	f.inTurn("GET /api/me", `{"id":"`+meID+`","username":"kt","type":"root","seriesHideFromContinueListening":["`+seriesA+`"]}`,
		`{"id":"`+meID+`","username":"kt","type":"root","seriesHideFromContinueListening":[]}`,
		`{"id":"`+meID+`","username":"kt","type":"root","seriesHideFromContinueListening":[]}`)
	call := toolCaller(t, f)

	out, err := call("user_progress_set", map[string]any{"series": seriesA, "hide_from_continue": true})
	if err != nil {
		t.Fatal(err)
	}
	if out["series"] != "Dune" || !isTrue(out["series_hidden"]) || len(f.requests("/api/me/series/"+seriesA+"/remove-from-continue-listening")) != 1 {
		t.Errorf("hide = %v", out)
	}
	out, err = call("user_progress_set", map[string]any{"series": seriesA, "hide_from_continue": false})
	if err != nil {
		t.Fatal(err)
	}
	if boolOf(t, out["series_hidden"]) || len(f.requests("/api/me/series/"+seriesA+"/readd-to-continue-listening")) != 1 {
		t.Errorf("show = %v", out)
	}
	// the server answered, but the account says otherwise
	_, err = call("user_progress_set", map[string]any{"series": seriesA, "hide_from_continue": true})
	wantErr(t, "a hide not kept", err, "still has it shown")

	for _, c := range []struct {
		args map[string]any
		says string
	}{
		{map[string]any{"series": seriesA}, "pass hide_from_continue"},
		{map[string]any{"series": seriesA, "item": itemID, "hide_from_continue": true}, "one or the other"},
		{map[string]any{"series": seriesA, "finished": true, "hide_from_continue": true}, "no progress of its own"},
	} {
		_, err := call("user_progress_set", c.args)
		wantErr(t, fmt.Sprint(c.args), err, c.says)
	}
}

// With server, a year in review of the whole server: every account's
// listening and what the library gained, in its own shape.
func TestUserStatsForTheWholeServer(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/stats/year/2025", `{"numListeningSessions":40,"totalListeningTime":7200.4,"numBooksAdded":3,"totalBooksAddedSize":1048576,"totalBooksAddedDuration":36000.2,`+
		`"numAuthorsAdded":2,"numBooks":120,"totalBooksSize":987654321,"totalBooksDuration":3600000,"topAuthors":[{"name":"Frank Herbert","time":3600}],`+
		`"topNarrators":[{"name":"Scott Brick","time":1800}],"topGenres":[{"genre":"Science Fiction","time":7200}],"booksAddedWithCovers":["x"]}`)
	call := toolCaller(t, f)

	out, err := call("user_stats", map[string]any{"year": 2025, "server": true})
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "user_stats", out, map[string]float64{
		"total_listened_s": 7200, "sessions": 40, "books_added": 3, "added_size": 1048576, "added_s": 36000,
		"authors_added": 2, "books": 120, "size": 987654321, "length_s": 3600000,
		"top_authors.0.time_s": 3600, "top_narrators.0.time_s": 1800, "top_genres.0.time_s": 7200,
	})
	if out["user"] != "every account" || dig(out, "top_genres.0.name") != "Science Fiction" {
		t.Errorf("answer = %v", out)
	}

	_, err = call("user_stats", map[string]any{"server": true})
	wantErr(t, "server without a year", err, "pass year")
	_, err = call("user_stats", map[string]any{"server": true, "year": 2025, "user": "reader"})
	wantErr(t, "server with a user", err, "omit user")
}

// user_history_remove finds each session in the account's own history before
// anything goes, previews without confirm, and reads the history back after.
func TestUserHistoryRemove(t *testing.T) {
	t.Parallel()

	const s1, s2 = "a1a1a1a1-0000-4000-8000-000000000001", "a1a1a1a1-0000-4000-8000-000000000002"
	session := func(id string) string {
		return `{"id":"` + id + `","userId":"` + meID + `","libraryItemId":"` + itemID + `","displayTitle":"Dune","timeListening":600,"currentTime":1200,"updatedAt":1759000000000}`
	}
	f := newFakeABS(t)
	accounts(f)
	both := `{"total":2,"sessions":[` + session(s1) + `,` + session(s2) + `]}`
	f.inTurn("GET /api/me/listening-sessions", both, both, both, `{"total":1,"sessions":[`+session(s2)+`]}`)
	f.json("DELETE /api/sessions/"+s1, `OK`)
	call := toolCaller(t, f)

	out, err := call("user_history_remove", map[string]any{"sessions": []any{s1}})
	if err != nil {
		t.Fatal(err)
	}
	if got := column(t, "id", out["would_remove"]); !slices.Equal(got, []string{s1}) || len(f.changes()) != 0 {
		t.Errorf("preview = %v, sent %v", out, f.changes())
	}

	_, err = call("user_history_remove", map[string]any{"sessions": []any{s1, "a1a1a1a1-0000-4000-8000-000000000009"}, "confirm": true})
	wantErr(t, "a session not theirs", err, "kt has no session", "000000000009")
	if len(f.changes()) != 0 {
		t.Errorf("sent %v", f.changes())
	}

	out, err = call("user_history_remove", map[string]any{"sessions": []any{s1}, "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := column(t, "id", out["removed"]); !slices.Equal(got, []string{s1}) {
		t.Errorf("removed = %v", out)
	}
	if got := f.changes(); len(got) != 1 || got[0].Path != "/api/sessions/"+s1 {
		t.Errorf("sent %v", got)
	}
}

// remove takes the progress away and nothing else: beside something to set
// it is refused, before anything is sent.
func TestUserProgressSetRemoveStandsAlone(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", `"duration":60`))
	call := toolCaller(t, f)

	for _, extra := range []map[string]any{{"percent": 50}, {"finished": true}, {"hide_from_continue": true}, {"series": "Dune"}} {
		args := map[string]any{"item": bookB1, "remove": true}
		maps.Copy(args, extra)
		_, err := call("user_progress_set", args)
		wantErr(t, fmt.Sprint(extra), err, "remove deletes the progress", "alone")
	}
	if got := f.changes(); len(got) != 0 {
		t.Errorf("sent %v", got)
	}
}

// Several bookmarks in one call: the removals are checked before anything is
// sent, so one that is not there changes nothing, and a position both added
// and removed is refused.
func TestUserBookmarkEditTakesSeveral(t *testing.T) {
	t.Parallel()

	marks := `[{"libraryItemId":"` + bookB1 + `","title":"One","time":1},{"libraryItemId":"` + bookB1 + `","title":"Two","time":2}]`
	f := newFakeABS(t)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", `"duration":3600`))
	// read once by the refused removal, once before the changes, and once after
	f.inTurn("GET /api/me/bookmarks", `{"bookmarks":`+marks+`}`, `{"bookmarks":`+marks+`}`, `{"bookmarks":[]}`)
	f.json("DELETE /api/me/item/"+bookB1+"/bookmark/{time}", `{}`)
	f.json("POST /api/me/item/"+bookB1+"/bookmark", `{"libraryItemId":"`+bookB1+`","title":"Three","time":3}`)
	call := toolCaller(t, f)

	_, err := call("user_bookmark_edit", map[string]any{"item": bookB1, "remove_bookmarks": []any{1, 9}})
	wantErr(t, "one of two removals not there", err, "no bookmark at 9", "nothing was changed")
	_, err = call("user_bookmark_edit", map[string]any{"item": bookB1, "add_bookmarks": []any{map[string]any{"time_s": 1, "title": "Again"}}, "remove_bookmarks": []any{1}})
	wantErr(t, "a position added and removed", err, "one or the other")
	_, err = call("user_bookmark_edit", map[string]any{"item": bookB1, "add_bookmarks": []any{map[string]any{"time_s": 4, "title": " "}}})
	wantErr(t, "an add with no title", err, "no title")
	if got := f.changes(); len(got) != 0 {
		t.Fatalf("refused calls sent %v", got)
	}

	out, err := call("user_bookmark_edit", map[string]any{"item": bookB1, "remove_bookmarks": []any{2, 1}, "add_bookmarks": []any{map[string]any{"time_s": 3, "title": "Three"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(column(t, "title", out["removed"]), []string{"One", "Two"}) || !slices.Equal(column(t, "title", out["added"]), []string{"Three"}) || out["renamed"] != nil {
		t.Errorf("answer = %v, want One and Two removed and Three added", out)
	}
	if sent := f.changes(); len(sent) != 3 || sent[0].Method != http.MethodDelete || sent[1].Method != http.MethodDelete || sent[2].Method != http.MethodPost {
		t.Errorf("sent %v, want the two removals and then the add", sent)
	}
}
