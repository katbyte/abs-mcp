package tools

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

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

// The history of one book, asked for someone else, counts that book's
// sessions, as the same question asked by the listener does: it answered
// with every session they have, on anything.
func TestUserHistoryForAnotherCountsTheItemsSessions(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	accounts(f)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
	// three of a thousand sessions are on the book, the last a year old
	sessionsOn(f, readerID, 1000, 200, 120, 180, 250)
	call := toolCaller(t, f)

	out, err := call("user_history", map[string]any{"user": "reader", "item": bookB1, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := list(t, out["sessions"]); len(got) != 1 || str(t, got[0]["id"]) != "s120" {
		t.Errorf("sessions = %v, want s120", got)
	}
	if n := num(t, out["total_sessions"]); n != 3 {
		t.Errorf("total_sessions = %d, want the book's 3, not all 1000", n)
	}
}

// An account kept from some books sees a search's authors, narrators, tags
// and genres counted over the books it can see: the server counts the whole
// library, so a count of two told an account of one book that another was
// hidden from it.
func TestRestrictedSearchCountsOnlyWhatTheKeySees(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/me", `{"id":"u1","username":"kid","type":"user","permissions":{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":false}}`)
	visible := item(bookB1, "Shared One", `"authorName":"Shared Author","narratorName":"Shared Narrator","genres":["Shared Genre"]`, `"tags":["shared-tag"],"duration":600`)
	f.json("GET /api/libraries/"+libID+"/items", page(visible))
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1","name":"Shared Author"}],"total":1}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/search", `{"book":[{"libraryItem":`+visible+`}],"authors":[{"id":"a1","name":"Shared Author","numBooks":2}],`+
		`"narrators":[{"name":"Shared Narrator","numBooks":2}],"tags":[{"name":"shared-tag","numItems":2}],"genres":[{"name":"Shared Genre","numItems":2}]}`)
	call := toolCaller(t, f)

	out, err := call("library_search", map[string]any{"query": "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"authors", "narrators", "tags", "genres"} {
		rows := list(t, out[group])
		if len(rows) != 1 || num(t, rows[0]["count"]) != 1 {
			t.Errorf("%s = %v, want the one it can see, counted once", group, out[group])
		}
	}
}
