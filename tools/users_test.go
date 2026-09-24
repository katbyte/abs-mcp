package tools

import (
	"encoding/json"
	"fmt"
	"io"
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
		args               map[string]any
		result, title, was string
	}{
		{map[string]any{"action": "add", "title": "Mark"}, "added", "Mark", ""},
		{map[string]any{"action": "add", "title": "Renamed"}, "renamed", "Renamed", "Mark"},
		{map[string]any{"action": "remove"}, "removed", "Renamed", ""},
	} {
		tc.args["item"], tc.args["time_s"] = bookB1, 5
		out, err := call("user_bookmark_edit", tc.args)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		b, ok := out["bookmark"].(map[string]any)
		if !ok || str(t, out["result"]) != tc.result || str(t, b["title"]) != tc.title || str(t, out["was_titled"]) != tc.was {
			t.Errorf("%v: %v, want %s %q", tc.args, out, tc.result, tc.title)
		}
	}
	if adds := f.requests("/api/me/item/" + bookB1 + "/bookmark"); len(adds) != 2 || adds[0].Method != http.MethodPost || adds[1].Method != http.MethodPatch {
		t.Errorf("sent %v, want a POST to add and a PATCH to rename", adds)
	}

	if _, err := call("user_bookmark_edit", map[string]any{"item": bookB1, "action": "remove", "time_s": 5}); err == nil || !strings.Contains(err.Error(), "no bookmark") {
		t.Errorf("removing one not there: %v", err)
	}
	if got := f.requests("/api/me/item/" + bookB1 + "/bookmark/5"); len(got) != 1 {
		t.Errorf("sent %d removals, want the one", len(got))
	}
}

// user_progress_remove says whether there was progress to remove, and reads
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

	out, err := call("user_progress_remove", map[string]any{"item": bookB1})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["removed"]) || str(t, out["item"]) != "First" {
		t.Errorf("first removal = %v, want removed", out)
	}
	if out, err = call("user_progress_remove", map[string]any{"item": bookB1}); err != nil || boolOf(t, out["removed"]) {
		t.Errorf("second removal = %v, %v; want nothing removed", out, err)
	}
	if got := f.requests("/api/me/progress/mp1"); len(got) != 1 {
		t.Errorf("deletes sent %v, want one", got)
	}
}
