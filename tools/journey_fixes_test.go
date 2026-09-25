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

// The bugs the live journeys found (acceptance/journey_*_test.go), pinned
// against a canned server so each stays fixed without a container.

const (
	otherLibID = "33333333-3333-4333-8333-333333333333"
	podLibID   = "44444444-4444-4444-8444-444444444444"
	bookB1     = "b1b1b1b1-0000-4000-8000-000000000001"
	bookB2     = "b1b1b1b1-0000-4000-8000-000000000002"
	bookB3     = "b1b1b1b1-0000-4000-8000-000000000003"
	otherBook  = "b1b1b1b1-0000-4000-8000-000000000009"
	podcastID  = "c1c1c1c1-0000-4000-8000-000000000001"
	playlistP  = "d1d1d1d1-0000-4000-8000-000000000001"
	playlistQ  = "d1d1d1d1-0000-4000-8000-000000000002"
	colC       = "e1e1e1e1-0000-4000-8000-000000000001"
)

// shelfRoutes is a book library of three books, a second book library with a
// book of its own, and a podcast library with one show of one episode.
func shelfRoutes(f *fakeABS) {
	f.json("GET /api/libraries", `{"libraries":[`+
		`{"id":"`+libID+`","name":"Books","mediaType":"book"},`+
		`{"id":"`+otherLibID+`","name":"Other","mediaType":"book"},`+
		`{"id":"`+podLibID+`","name":"Shows","mediaType":"podcast"}]}`)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
	f.json("GET /api/items/"+bookB2, item(bookB2, "Second", "", ""))
	f.json("GET /api/items/"+bookB3, item(bookB3, "Third", "", ""))
	f.json("GET /api/items/"+otherBook, `{"id":"`+otherBook+`","libraryId":"`+otherLibID+`","mediaType":"book","media":{"metadata":{"title":"Elsewhere"}}}`)
	f.json("GET /api/items/"+podcastID, `{"id":"`+podcastID+`","libraryId":"`+podLibID+`","mediaType":"podcast","media":{"metadata":{"title":"The Show"},"episodes":[{"id":"ep-1","title":"Pilot"}]}}`)
}

// nowMillis is the time as the server writes it, for a session the 60-day
// history window keeps.
func nowMillis() string { return strconv.FormatInt(time.Now().UnixMilli(), 10) }

// A playlist entry the server cannot take is refused before anything is sent:
// Audiobookshelf's batch add exits the whole server on a book sent with an
// episode or a podcast episode that is not the podcast's, stores a broken
// entry for a podcast with no episode, and takes a book from another library.
func TestPlaylistEntriesRefusedBeforeTheServerSeesThem(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	shelfRoutes(f)
	f.json("GET /api/playlists/"+playlistP, `{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Books List","items":[]}`)
	f.json("GET /api/playlists/"+playlistQ, `{"id":"`+playlistQ+`","libraryId":"`+podLibID+`","name":"Shows List","items":[]}`)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		playlist string
		entry    map[string]any
		want     string
	}{
		{playlistP, map[string]any{"item": bookB1, "episode": "ep-1"}, "is a book"},
		{playlistQ, map[string]any{"item": podcastID}, "is a podcast"},
		{playlistQ, map[string]any{"item": podcastID, "episode": "ep-somebody-elses"}, "has no episode"},
		{playlistP, map[string]any{"item": otherBook}, "another library"},
	} {
		_, err := call("playlist_entries_edit", map[string]any{"playlist": tc.playlist, "action": "add", "entries": []any{tc.entry}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.entry, err, tc.want)
		}
	}
	for _, path := range []string{"/api/playlists/" + playlistP + "/batch/add", "/api/playlists/" + playlistQ + "/batch/add"} {
		if got := f.requests(path); len(got) != 0 {
			t.Errorf("%s was sent %d times, want never", path, len(got))
		}
	}
}

// What an add or a remove actually changed is reported: the server answers 200
// to adding what a playlist holds and removing what it does not, and deletes
// a playlist whose last entry goes.
func TestPlaylistEntriesReportWhatChanged(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	shelfRoutes(f)
	withB1 := `{"id":"` + playlistP + `","libraryId":"` + libID + `","name":"Books List","items":[{"libraryItemId":"` + bookB1 + `"}]}`
	both := `{"id":"` + playlistP + `","libraryId":"` + libID + `","name":"Books List","items":[{"libraryItemId":"` + bookB1 + `"},{"libraryItemId":"` + bookB2 + `"}]}`
	var removed atomic.Bool
	f.mux.HandleFunc("GET /api/playlists/"+playlistP, func(w http.ResponseWriter, r *http.Request) {
		if removed.Load() {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, withB1)
	})
	f.json("POST /api/playlists/"+playlistP+"/batch/add", both)
	f.mux.HandleFunc("POST /api/playlists/"+playlistP+"/batch/remove", func(w http.ResponseWriter, _ *http.Request) {
		removed.Store(true)
		_, _ = io.WriteString(w, withB1)
	})
	call := toolCaller(t, f)

	out, err := call("playlist_entries_edit", map[string]any{"playlist": playlistP, "action": "add", "entries": []any{map[string]any{"item": bookB1}, map[string]any{"item": bookB2}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := anyStrings(out["added"]); !slices.Equal(got, []string{"Second"}) {
		t.Errorf("added = %v, want [Second]", got)
	}
	if got := anyStrings(out["already_held"]); !slices.Equal(got, []string{"First"}) {
		t.Errorf("already_held = %v, want [First]", got)
	}
	adds := f.requests("/api/playlists/" + playlistP + "/batch/add")
	if len(adds) != 1 || strings.Contains(adds[0].Body, bookB1) || !strings.Contains(adds[0].Body, bookB2) {
		t.Errorf("batch add sent %v, want only the new book", adds)
	}

	// nothing new: nothing sent
	out, err = call("playlist_entries_edit", map[string]any{"playlist": playlistP, "action": "add", "entries": []any{map[string]any{"item": bookB1}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests("/api/playlists/"+playlistP+"/batch/add")) != 1 || out["added"] != nil {
		t.Errorf("re-adding a held entry sent a request or reported an add: %v", out)
	}
	out, err = call("playlist_entries_edit", map[string]any{"playlist": playlistP, "action": "remove", "entries": []any{map[string]any{"item": bookB3}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := anyStrings(out["not_held"]); !slices.Equal(got, []string{"Third"}) || len(f.requests("/api/playlists/"+playlistP+"/batch/remove")) != 0 {
		t.Errorf("removing what is not held: %v", out)
	}

	// the last entry goes, and the playlist with it
	out, err = call("playlist_entries_edit", map[string]any{"playlist": playlistP, "action": "remove", "entries": []any{map[string]any{"item": bookB1}}})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["deleted"]) || num(t, out["entries"]) != 0 {
		t.Errorf("emptying the playlist: %v, want deleted and no entries", out)
	}
}

// The same for collections, which the server does not delete when emptied,
// and which refuse a book from another library or a podcast by name.
func TestCollectionBooksEditReportsWhatChanged(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	shelfRoutes(f)
	f.json("GET /api/collections/"+colC, `{"id":"`+colC+`","libraryId":"`+libID+`","name":"Shelf","books":[{"id":"`+bookB1+`"}]}`)
	f.json("POST /api/collections/"+colC+"/batch/add", `{"id":"`+colC+`","libraryId":"`+libID+`","name":"Shelf","books":[{"id":"`+bookB1+`"},{"id":"`+bookB2+`"}]}`)
	call := toolCaller(t, f)

	out, err := call("collection_books_edit", map[string]any{"collection": colC, "action": "add", "items": []any{bookB1, bookB2, bookB2}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(anyStrings(out["added"]), []string{"Second"}) || !slices.Equal(anyStrings(out["already_held"]), []string{"First"}) || num(t, out["books"]) != 2 {
		t.Errorf("out = %v", out)
	}
	if adds := f.requests("/api/collections/" + colC + "/batch/add"); len(adds) != 1 || strings.Contains(adds[0].Body, bookB1) {
		t.Errorf("batch add sent %v, want only the new book, once", adds)
	}
	out, err = call("collection_books_edit", map[string]any{"collection": colC, "action": "remove", "items": []any{bookB3}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(anyStrings(out["not_held"]), []string{"Third"}) || len(f.requests("/api/collections/"+colC+"/batch/remove")) != 0 {
		t.Errorf("removing what is not held: %v", out)
	}
	// a podcast cannot really sit in a book library; the check is there anyway
	strayPodcast := "c1c1c1c1-0000-4000-8000-000000000002"
	f.json("GET /api/items/"+strayPodcast, `{"id":"`+strayPodcast+`","libraryId":"`+libID+`","mediaType":"podcast","media":{"metadata":{"title":"Stray"}}}`)
	for ref, want := range map[string]string{otherBook: "another library", strayPodcast: "podcast"} {
		if _, err := call("collection_books_edit", map[string]any{"collection": colC, "action": "add", "items": []any{ref}}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("adding %s: %v, want %q", ref, err, want)
		}
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

// An open session names its user by id only; the tools say who it is.
func TestSessionsNameTheirUser(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	session := `{"id":"s1","userId":"u1","libraryItemId":"` + bookB1 + `","displayTitle":"First","duration":60,"currentTime":30}`
	f.json("GET /api/sessions/open", `{"sessions":[`+session+`]}`)
	f.json("GET /api/users", `{"users":[{"id":"u1","username":"reader","type":"user"}]}`)
	f.json("GET /api/users/u1", `{"id":"u1","username":"reader","type":"user"}`)
	f.json("GET /api/users/u1/listening-sessions", `{"total":1,"sessions":[`+strings.Replace(session, `"id":"s1"`, `"id":"s1","updatedAt":`+nowMillis(), 1)+`]}`)
	call := toolCaller(t, f)

	out, err := call("server_sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows := list(t, out["sessions"]); len(rows) != 1 || str(t, rows[0]["user"]) != "reader" {
		t.Errorf("sessions = %v, want reader named", out["sessions"])
	}
	out, err = call("user_history", map[string]any{"user": "reader"})
	if err != nil {
		t.Fatal(err)
	}
	if rows := list(t, out["sessions"]); len(rows) != 1 || str(t, rows[0]["user"]) != "reader" {
		t.Errorf("history = %v, want reader named", out["sessions"])
	}
}

// A non-admin key is told why it cannot see open sessions: the server answers
// 404, not 403.
func TestServerSessionsSaysAdminOnly(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	call := toolCaller(t, f)
	if _, err := call("server_sessions", nil); err == nil || !strings.Contains(err.Error(), "admin only") {
		t.Errorf("err = %v, want it to say admin only", err)
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
	if !slices.Equal(anyStrings(out["libraries"]), []string{libID}) || !slices.Equal(anyStrings(out["denied_tags"]), []string{"grown-up"}) || out["tags"] != nil {
		t.Errorf("libraries = %v, denied_tags = %v, tags = %v", out["libraries"], out["denied_tags"], out["tags"])
	}
}

// An account kept from some books by tag is shown only what it can see: the
// server's filter data, stats, narrator list and search name groups cover the
// whole library, so they are not read for it.
func TestRestrictedKeySeesOnlyItsBooks(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/me", `{"id":"u1","username":"kid","type":"user","permissions":{"accessAllLibraries":true,"accessAllTags":false,"accessExplicitContent":true},"itemTagsSelected":["kids"]}`)
	visible := item(bookB1, "Visible Book", `"authorName":"Visible Author","narratorName":"Visible Narrator","genres":["Picture Books"],"publishedYear":"2011"`, `"tags":["kids"],"duration":600,"numAudioFiles":1`)
	f.json("GET /api/libraries/"+libID+"/items", page(visible))
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1","name":"Visible Author"}],"total":1}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	hidden := `"Hidden Author"`
	f.json("GET /api/libraries/"+libID+"/filterdata", `{"authors":[{"id":"a2","name":`+hidden+`}],"narrators":["Hidden Narrator"]}`)
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Hidden Narrator","numBooks":4}]}`)
	f.json("GET /api/libraries/"+libID+"/stats", `{"totalItems":9,"longestItems":[{"id":"x","title":"Hidden Book"}]}`)
	f.json("GET /api/libraries/"+libID+"/search", `{"book":[{"libraryItem":`+visible+`}],"authors":[{"id":"a1","name":"Visible Author"},{"id":"a2","name":`+hidden+`}],"narrators":[{"name":"Visible Narrator"},{"name":"Hidden Narrator"}]}`)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"library_filters", nil},
		{"narrator_list", nil},
		{"library_get", nil},
		{"library_search", map[string]any{"query": "e"}},
	} {
		out, err := call(tc.tool, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), "Hidden") {
			t.Errorf("%s names a book the key cannot see: %s", tc.tool, raw)
		}
		if !strings.Contains(string(raw), "Visible") {
			t.Errorf("%s lost what the key can see: %s", tc.tool, raw)
		}
	}
	for _, path := range []string{"/api/libraries/" + libID + "/filterdata", "/api/libraries/" + libID + "/narrators", "/api/libraries/" + libID + "/stats"} {
		if got := f.requests(path); len(got) != 0 {
			t.Errorf("%s was read for a restricted key", path)
		}
	}
}

// The author routes answer a write with the record alone, so the row is read
// back: a count built from the reply says the author has no books.
func TestAuthorEditCountsBooksAfterTheWrite(t *testing.T) {
	t.Parallel()

	const authorID = "55555555-5555-4555-8555-555555555557"
	f := newFakeABS(t)
	f.json("GET /api/authors/"+authorID, `{"id":"`+authorID+`","name":"Tad Williams","libraryItems":[{"id":"`+bookB1+`"},{"id":"`+bookB2+`"}]}`)
	f.json("PATCH /api/authors/"+authorID, `{"author":{"id":"`+authorID+`","name":"Tad Williams","description":"new"},"merged":false}`)
	call := toolCaller(t, f)

	out, err := call("author_edit", map[string]any{"author": authorID, "description": "new"})
	if err != nil {
		t.Fatal(err)
	}
	author, ok := out["author"].(map[string]any)
	if !ok || num(t, author["books"]) != 2 {
		t.Errorf("books = %v, want 2", author["books"])
	}
}

// item_edit edits the tag list the way item_batch_edit does.
func TestItemEditAddsAndRemovesTags(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", "", `"tags":["sf","Classic"]`))
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	if _, err := call("item_edit", map[string]any{"item": itemID, "add_tags": []any{"desert", "classic"}, "remove_tags": []any{"SF"}}); err != nil {
		t.Fatal(err)
	}
	sent := f.requests("/api/items/" + itemID + "/media")
	if len(sent) != 1 {
		t.Fatalf("media requests = %v", sent)
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(sent[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(body.Tags, []string{"Classic", "desert"}) {
		t.Errorf("tags sent = %v, want [Classic desert]", body.Tags)
	}
	if _, err := call("item_edit", map[string]any{"item": itemID, "tags": []any{"a"}, "add_tags": []any{"b"}}); err == nil {
		t.Error("tags with add_tags was not refused")
	}
}

// A compound genre is split in one call with split, and only the books that
// carry it change: a part that is a genre on other books stays theirs. Doing
// it in two calls (into, then to_field) moved the part off every book.
func TestMetadataRenameSplitChangesOnlyTheCompound(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item(bookB1, "Equal Rites", `"genres":["Science Fiction & Fantasy, Fantasy"]`, `"tags":["witches"]`),
		item(bookB2, "Mort", `"genres":["Fantasy"]`, ""),
	))
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	call := toolCaller(t, f)

	out, err := call("metadata_rename", map[string]any{
		"field": "genres", "from": "Science Fiction & Fantasy, Fantasy",
		"split": map[string]any{"genres": []any{"Science Fiction & Fantasy"}, "tags": []any{"Fantasy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_updated"]) != 1 {
		t.Errorf("items_updated = %v", out["items_updated"])
	}
	sent := f.requests("/api/items/batch/update")
	if len(sent) != 1 {
		t.Fatalf("batch updates = %v", sent)
	}
	var body []struct {
		ID           string `json:"id"`
		MediaPayload struct {
			Tags     []string `json:"tags"`
			Metadata struct {
				Genres []string `json:"genres"`
			} `json:"metadata"`
		} `json:"mediaPayload"`
	}
	if err := json.Unmarshal([]byte(sent[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body[0].ID != bookB1 {
		t.Fatalf("updated %v, want only the book carrying the compound", body)
	}
	if got := body[0].MediaPayload; !slices.Equal(got.Metadata.Genres, []string{"Science Fiction & Fantasy"}) || !slices.Equal(got.Tags, []string{"witches", "Fantasy"}) {
		t.Errorf("sent genres %v tags %v", got.Metadata.Genres, got.Tags)
	}
	if _, err := call("metadata_rename", map[string]any{"field": "genres", "from": "x", "to": "y", "split": map[string]any{"tags": []any{"x"}}}); err == nil {
		t.Error("split with to was not refused")
	}
}

// audit_matched with no library runs its windows on through every book
// library, as audit_all counts it, rather than refusing.
func TestAuditMatchedPagesAcrossBookLibraries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"},{"id":"`+podLibID+`","name":"Shows","mediaType":"podcast"},{"id":"`+otherLibID+`","name":"Other","mediaType":"book","provider":"audible"}]}`)
	f.json("GET /api/libraries/"+libID+"/items", page(item(bookB1, "First", `"asin":"B001"`, "")))
	f.json("GET /api/libraries/"+otherLibID+"/items", page(`{"id":"`+otherBook+`","libraryId":"`+otherLibID+`","mediaType":"book","media":{"metadata":{"title":"Elsewhere","asin":"B009"}}}`))
	f.json("GET /api/libraries/"+podLibID+"/items", page())
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	first, err := call("audit_matched", map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, first["items_scanned"]) != 1 || num(t, first["next_offset"]) != 1 {
		t.Errorf("offset 0 = %v, want one book and a next offset", first)
	}
	second, err := call("audit_matched", map[string]any{"limit": 1, "offset": 1})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, second["items_scanned"]) != 1 || second["next_offset"] != nil {
		t.Errorf("offset 1 = %v, want the other library's book and no next offset", second)
	}
	findings := list(t, second["findings"])
	if len(findings) != 1 || str(t, findings[0]["title"]) != "Elsewhere" {
		t.Errorf("offset 1 findings = %v, want Elsewhere", second["findings"])
	}
}

// showLibrary is a podcast library with one folder.
func showLibrary(f *fakeABS) {
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+podLibID+`","name":"Shows","mediaType":"podcast","folders":[{"id":"f1","fullPath":"/podcasts"}]}]}`)
}

// feedEpisode is one entry of a canned feed.
func feedEpisode(guid, title string, published int) string {
	return fmt.Sprintf(`{"title":%q,"guid":%q,"publishedAt":%d,"enclosure":{"url":"http://feed.test/%s.mp3"}}`, title, guid, published, guid)
}

// The server takes no episodes with a new podcast, and dropped the ones sent
// there, so download_latest queues the newest by publication once the
// podcast exists; and a feed the library already has is refused before
// anything is made.
func TestPodcastAddQueuesTheNewestAndRefusesAHeldFeed(t *testing.T) {
	t.Parallel()

	feed := `{"podcast":{"metadata":{"title":"Show","feedUrl":"http://feed.test/show.xml"},"episodes":[` +
		feedEpisode("g1", "Oldest", 1000) + "," + feedEpisode("g3", "Newest", 3000) + "," + feedEpisode("g2", "Middle", 2000) + `]}}`

	t.Run("queued once it exists", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		showLibrary(f)
		f.json("POST /api/podcasts/feed", feed)
		f.json("GET /api/libraries/"+podLibID+"/items", page())
		f.json("POST /api/podcasts", `{"id":"`+podcastID+`","libraryId":"`+podLibID+`","mediaType":"podcast","media":{"metadata":{"title":"Show"}}}`)
		f.json("POST /api/podcasts/"+podcastID+"/download-episodes", `OK`)
		call := toolCaller(t, f)

		out, err := call("podcast_add", map[string]any{"feed_url": "http://feed.test/show.xml", "download_latest": 2})
		if err != nil {
			t.Fatal(err)
		}
		if got := out["queued"]; fmt.Sprint(got) != "[Newest Middle]" {
			t.Errorf("queued = %v, want [Newest Middle]", got)
		}
		if created := f.requests("/api/podcasts"); len(created) != 1 || strings.Contains(created[0].Body, "episodesToDownload") {
			t.Errorf("create = %v, want no episodes sent with it", created)
		}
		queued := f.requests("/api/podcasts/" + podcastID + "/download-episodes")
		if len(queued) != 1 || !strings.Contains(queued[0].Body, `"Newest"`) || !strings.Contains(queued[0].Body, `"Middle"`) || strings.Contains(queued[0].Body, `"Oldest"`) {
			t.Errorf("download-episodes = %v, want Newest and Middle", queued)
		}
	})

	t.Run("a feed already held", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		showLibrary(f)
		f.json("POST /api/podcasts/feed", feed)
		f.json("GET /api/libraries/"+podLibID+"/items", page(`{"id":"`+podcastID+`","libraryId":"`+podLibID+`","mediaType":"podcast","media":{"metadata":{"title":"Show","feedUrl":"HTTP://feed.test/show.xml"}}}`))
		call := toolCaller(t, f)

		if _, err := call("podcast_add", map[string]any{"feed_url": "http://feed.test/show.xml", "folder": "Show Again"}); err == nil || !strings.Contains(err.Error(), podcastID) {
			t.Errorf("a second subscription: %v, want refused naming %s", err, podcastID)
		}
		if created := f.requests("/api/podcasts"); len(created) != 0 {
			t.Errorf("the podcast was created: %v", created)
		}
	})
}

// podcastWith is a podcast item holding episodes.
func podcastWith(episodes ...string) string {
	return `{"id":"` + podcastID + `","libraryId":"` + podLibID + `","mediaType":"podcast","media":{"metadata":{"title":"Show","feedUrl":"http://feed.test/show.xml"},"episodes":[` + strings.Join(episodes, ",") + `]}}`
}

// The server downloads an episode it holds a second time, and drops a
// request for one it is already fetching without a word: held is matched by
// guid or audio url, not by a title an edit changed, and neither is sent.
func TestPodcastEpisodeDownloadSkipsHeldAndQueued(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith(`{"id":"e1","title":"Retitled","guid":"g1","enclosure":{"url":"http://feed.test/g1.mp3"}}`))
	f.json("POST /api/podcasts/feed", `{"podcast":{"metadata":{"title":"Show"},"episodes":[`+
		feedEpisode("g3", "Three", 3000)+","+feedEpisode("g2", "Two", 2000)+","+feedEpisode("g1", "One", 1000)+`]}}`)
	f.json("GET /api/libraries/"+podLibID+"/episode-downloads", `{"currentDownload":{"url":"http://feed.test/g2.mp3","libraryItemId":"`+podcastID+`"},"queue":[]}`)
	f.json("POST /api/podcasts/"+podcastID+"/download-episodes", `OK`)
	call := toolCaller(t, f)

	out, err := call("podcast_episode_download", map[string]any{"item": podcastID, "indexes": []any{0, 1, 2, 2}})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"queued": "[Three]", "already_queued": "[Two]", "already_held": "[One]"} {
		if got := fmt.Sprint(out[key]); got != want {
			t.Errorf("%s = %s, want %s", key, got, want)
		}
	}
	sent := f.requests("/api/podcasts/" + podcastID + "/download-episodes")
	if len(sent) != 1 || strings.Contains(sent[0].Body, `"One"`) || strings.Contains(sent[0].Body, `"Two"`) {
		t.Errorf("sent %v, want only Three", sent)
	}

	// nothing left to send is not a request
	f2 := newFakeABS(t)
	f2.json("GET /api/items/"+podcastID, podcastWith(`{"id":"e1","title":"One","guid":"g1"}`))
	f2.json("POST /api/podcasts/feed", `{"podcast":{"metadata":{"title":"Show"},"episodes":[`+feedEpisode("g1", "One", 1000)+`]}}`)
	f2.json("GET /api/libraries/"+podLibID+"/episode-downloads", `{"queue":[]}`)
	if _, err := toolCaller(t, f2)("podcast_episode_download", map[string]any{"item": podcastID, "indexes": []any{0}}); err != nil {
		t.Fatal(err)
	}
	if sent := f2.requests("/api/podcasts/" + podcastID + "/download-episodes"); len(sent) != 0 {
		t.Errorf("sent %v for an episode already held", sent)
	}
}

// The server lists a podcast's episodes in the order they were downloaded;
// newest first is by publication.
func TestPodcastEpisodesAreNewestByPublication(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith(
		`{"id":"e3","title":"Newest","publishedAt":3000}`,
		`{"id":"e1","title":"Oldest","publishedAt":1000}`,
		`{"id":"e2","title":"Middle","publishedAt":2000}`,
	))
	call := toolCaller(t, f)

	want := []string{"Newest", "Middle", "Oldest"}
	for tool, key := range map[string]string{"podcast_episodes": "episodes", "item_get": "episodes"} {
		out, err := call(tool, map[string]any{"item": podcastID})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range list(t, out[key]) {
			got = append(got, str(t, e["title"]))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", tool, got, want)
		}
	}
}

// The server tags the files in the background and never reads them back, so
// item_embed_metadata waits for its task, rescans, and judges the tags the
// rescan found.
func TestItemEmbedMetadataWaitsAndReadsTheTagsBack(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var polls, scanned atomic.Int32
	f.mux.HandleFunc("GET /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
		tags := `{}`
		if scanned.Load() > 0 {
			tags = `{"tagTitle":"Dune"}`
		}
		_, _ = io.WriteString(w, item(itemID, "Dune", "", `"audioFiles":[{"index":1,"metadata":{"filename":"01.mp3"},"metaTags":`+tags+`}]`))
	})
	f.json("POST /api/tools/item/"+itemID+"/embed-metadata", `OK`)
	f.mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, _ *http.Request) {
		if polls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"tasks":[],"queuedTaskData":{"embedMetadata":[{"libraryItemId":"`+itemID+`"}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"tasks":[]}`)
	})
	f.mux.HandleFunc("POST /api/items/"+itemID+"/scan", func(w http.ResponseWriter, _ *http.Request) {
		if polls.Load() < 2 {
			t.Error("rescanned while the embed was still queued")
		}
		scanned.Add(1)
		_, _ = io.WriteString(w, `{"result":"UPDATED"}`)
	})
	call := toolCaller(t, f)

	out, err := call("item_embed_metadata", map[string]any{"item": itemID})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["embedded"]) || out["rescan"] != "UPDATED" || out["running"] != nil || out["differs"] != nil {
		t.Errorf("item_embed_metadata = %v, want embedded after the rescan", out)
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
		if _, err := call("user_bookmark_edit", map[string]any{"item": itemID, "action": "remove", "time_s": 5}); err == nil || !strings.Contains(err.Error(), "will not remove") {
			t.Errorf("removing a bookmark on a deleted item: %v", err)
		}
	})

	t.Run("item_delete removes the caller's first", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
		f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","bookmarks":[{"libraryItemId":"`+bookB1+`","title":"Mark","time":5},{"libraryItemId":"`+bookB2+`","title":"Other","time":7}]}`)
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

// A series whose books are all gone can stay listed, with none, and the
// server answers 404 to opening it: by name it is not a series to open.
func TestSeriesWithNoBooksLeftIsNotOpened(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"Gone","books":[]}],"total":1}`)
	call := toolCaller(t, f)

	if _, err := call("series_get", map[string]any{"series": "Gone"}); err == nil || !strings.Contains(err.Error(), "with a book in it") {
		t.Errorf("series_get of an emptied series: %v", err)
	}
	if got := f.requests("/api/series/s1"); len(got) != 0 {
		t.Errorf("opened the emptied series: %v", got)
	}
}

// Calls of one turn run at once. Each edit of a book's tags reads the book
// and sends its tags back whole, so without holding the book eight adds kept
// one tag; held, and read again once held, every tag lands.
func TestItemEditsAtOnceKeepEveryTag(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var mu sync.Mutex
	tags := []string{"sf"}
	f.mux.HandleFunc("GET /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		have, _ := json.Marshal(tags)
		mu.Unlock()
		// a read that takes a while, so the calls overlap
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(w, item(itemID, "Dune", "", `"tags":`+string(have)))
	})
	f.mux.HandleFunc("PATCH /api/items/"+itemID+"/media", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tags []string `json:"tags"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		tags = body.Tags
		mu.Unlock()
		_, _ = io.WriteString(w, `{"updated":true}`)
	})
	call := toolCaller(t, f)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if _, err := call("item_edit", map[string]any{"item": itemID, "add_tags": []any{strconv.Itoa(i)}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(tags) != 9 {
		t.Errorf("tags = %v, want sf and all eight added", tags)
	}
}
