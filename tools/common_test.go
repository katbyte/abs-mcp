package tools

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The server's search also answers on subtitle, asin and isbn (and older
// servers on authors and narrators), without saying which field matched. A
// lone hit whose title does not contain the words asked for is not the item
// that was named: it is refused with the id on offer, never acted on.
func TestResolveItemRejectsNonTitleMatch(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/search", `{"book":[{"libraryItem":`+item(itemID, "Dune Messiah", `"asin":"B0DUNE"`, "")+`}]}`)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune Messiah", `"asin":"B0DUNE"`, ""))
	f.json("DELETE /api/items/"+itemID, `{}`)
	call := toolCaller(t, f)

	_, err := call("item_delete", map[string]any{"item": "B0DUNE"})
	if err == nil {
		t.Fatal("an item whose title does not contain the query was resolved, and deleted")
	}
	if !strings.Contains(err.Error(), "another field") || !strings.Contains(err.Error(), itemID) {
		t.Errorf("the refusal does not say what matched or offer the id: %v", err)
	}
	if got := f.requests("/api/items/" + itemID); len(got) != 0 {
		t.Errorf("the item was touched: %v", got)
	}

	// a title that contains the words asked for is the partial match a
	// lookup has always accepted when it is the only one
	out, err := call("item_get", map[string]any{"item": "Messiah"})
	if err != nil {
		t.Fatal(err)
	}
	if got := str(t, out["id"]); got != itemID {
		t.Errorf("resolved %q, want %s", got, itemID)
	}
}

// Two records with one name cannot be told apart by name: lookups refuse the
// name with both ids, and creates and renames refuse a name already taken.
func TestNamesThatMeanTwoThingsAreRefused(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","folders":[{"fullPath":"/books"}]},{"id":"`+otherLibID+`","name":"books","mediaType":"book"}]}`)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
	f.json("GET /api/collections", `{"collections":[{"id":"`+colC+`","libraryId":"`+libID+`","name":"Shelf"},{"id":"e1e1e1e1-0000-4000-8000-000000000002","libraryId":"`+libID+`","name":"shelf"}]}`)
	f.json("GET /api/libraries/"+libID+"/collections", `{"results":[{"id":"`+colC+`","libraryId":"`+libID+`","name":"Shelf"}]}`)
	f.json("GET /api/playlists", `{"playlists":[{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Queue"},{"id":"`+playlistQ+`","libraryId":"`+libID+`","name":"Queue"}]}`)
	f.json("GET /api/libraries/"+libID+"/playlists", `{"results":[{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Queue"}]}`)
	f.mux.HandleFunc("GET /api/filesystem", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") != "/books" {
			http.Error(w, `Invalid "path" query string`, http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"posix":true,"directories":[]}`)
	})
	call := toolCaller(t, f)

	for tool, args := range map[string]map[string]any{
		"collection_get": {"collection": "Shelf"},
		"playlist_get":   {"playlist": "queue"},
		"library_get":    {"library": "Books"},
	} {
		_, err := call(tool, args)
		if err == nil || !strings.Contains(err.Error(), "pass an id") {
			t.Errorf("%s %v: %v, want the ids listed", tool, args, err)
		}
	}

	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"collection_create", map[string]any{"library": libID, "name": "SHELF", "items": []any{bookB1}}, colC},
		{"collection_create", map[string]any{"library": libID, "name": "New Shelf"}, "items"},
		{"playlist_create", map[string]any{"library": libID, "name": "queue"}, playlistP},
		{"library_create", map[string]any{"name": "Fresh", "folders": []any{"/nowhere"}}, "no folder"},
		{"library_create", map[string]any{"name": "Fresh", "folders": []any{"books"}}, "absolute"},
		{"library_create", map[string]any{"name": "BOOKS", "folders": []any{"/books"}}, "already exists"},
		{"library_edit", map[string]any{"library": otherLibID, "name": "Books"}, "already exists"},
	} {
		_, err := call(tc.tool, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %v: %v, want %q", tc.tool, tc.args, err, tc.want)
		}
	}
	for _, path := range []string{"/api/collections", "/api/playlists", "/api/libraries"} {
		for _, r := range f.requests(path) {
			if r.Method != http.MethodGet {
				t.Errorf("%s %s was sent", r.Method, path)
			}
		}
	}
}

// page is gone from every tool that paged by number: a caller still sending
// it is refused by the schema, which names it, rather than read from the start.
func TestPageIsRefused(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page())
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"item_match_batch", map[string]any{"page": 1}},
		{"item_match_tag", map[string]any{"library": "Books", "page": 1}},
		{"audit_matched", map[string]any{"page": 1}},
		{"audit_covers", map[string]any{"library": "Books", "store": true, "page": 1}},
	} {
		if _, err := call(tc.tool, tc.args); err == nil || !strings.Contains(err.Error(), `unexpected additional properties ["page"]`) {
			t.Errorf("%s with page: %v", tc.tool, err)
		}
	}
	if got := f.requests("/api/libraries/" + libID + "/items"); len(got) != 0 {
		t.Errorf("a call with page listed the library: %v", got)
	}
}

// A tool that changes an item needs its whole title: a title that is only part
// of one book's would change that book, which is not the one named - asking to
// delete "Foundation" once Foundation is gone must not delete Foundation and
// Empire. A lookup still takes the part, and the refusal names the book so its
// id can be passed.
func TestAChangeNeedsTheWholeTitle(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/search", `{"book":[{"libraryItem":`+item(itemID, "Foundation and Empire", "", "")+`}]}`)
	f.json("GET /api/items/"+itemID, item(itemID, "Foundation and Empire", "", ""))
	f.json("DELETE /api/items/"+itemID, `{}`)
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"item_delete", map[string]any{"item": "Foundation", "delete_files": true}},
		{"item_edit", map[string]any{"item": "Foundation", "publisher": "Gnome Press"}},
		{"user_progress_set", map[string]any{"item": "Foundation", "finished": true}},
	} {
		_, err := call(tc.tool, tc.args)
		if err == nil {
			t.Fatalf("%s changed the book a part of its title named", tc.tool)
		}
		if !strings.Contains(err.Error(), "Foundation and Empire") || !strings.Contains(err.Error(), itemID) {
			t.Errorf("%s: the refusal does not name the nearest book and its id: %v", tc.tool, err)
		}
	}
	for _, r := range f.seen {
		if r.Method != http.MethodGet {
			t.Errorf("a refused change reached the server: %s %s", r.Method, r.Path)
		}
	}

	// reading is still forgiving
	out, err := call("item_get", map[string]any{"item": "Foundation"})
	if err != nil {
		t.Fatal(err)
	}
	if got := str(t, out["id"]); got != itemID {
		t.Errorf("item_get resolved %q, want %s", got, itemID)
	}

	// and the whole title changes it
	if _, err := call("item_edit", map[string]any{"item": "foundation AND empire", "publisher": "Gnome Press"}); err != nil {
		t.Errorf("the whole title, in another case, was refused: %v", err)
	}
}

func TestFormatting(t *testing.T) {
	t.Parallel()

	for secs, want := range map[float64]string{0: "", 45: "45s", 125: "2m 5s", 3600: "1h 0m", 45296: "12h 34m"} {
		if got := fmtDuration(secs); got != want {
			t.Errorf("fmtDuration(%v) = %q want %q", secs, got, want)
		}
	}
	for secs, want := range map[float64]int{0: 0, 0.4: 0, 0.5: 1, 39942.36: 39942, 45296.5: 45297} {
		if got := wholeSec(secs); got != want {
			t.Errorf("wholeSec(%v) = %d want %d", secs, got, want)
		}
	}
	if got := fmtDate(1_700_000_000_000); got != "2023-11-14" {
		t.Errorf("fmtDate = %q", got)
	}
	if got := fmtTime(0); got != "" {
		t.Errorf("fmtTime(0) = %q", got)
	}
	if got := plain("<p>Hi <b>there</b></p>"); got != "Hi there" {
		t.Errorf("plain = %q", got)
	}
	if got := clip("one two three four five", 12); got != "one two..." {
		t.Errorf("clip = %q", got)
	}
	if !looksLikeID("f0e9d8c7-b6a5-4321-8765-0123456789ab") || looksLikeID("Dune") {
		t.Error("looksLikeID")
	}
}

func TestLimitOr(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ limit, def, want int }{
		{0, 50, 50}, {-1, 50, 50}, {10, 50, 10}, {1, 50, 1},
	} {
		if got := limitOr(tc.limit, tc.def); got != tc.want {
			t.Errorf("limitOr(%d, %d) = %d, want %d", tc.limit, tc.def, got, tc.want)
		}
	}
}
