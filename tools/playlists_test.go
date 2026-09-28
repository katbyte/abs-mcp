package tools

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

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
	if got := strs(t, out["added"]); !slices.Equal(got, []string{"Second"}) {
		t.Errorf("added = %v, want [Second]", got)
	}
	if got := strs(t, out["already_held"]); !slices.Equal(got, []string{"First"}) {
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
	if got := strs(t, out["not_held"]); !slices.Equal(got, []string{"Third"}) || len(f.requests("/api/playlists/"+playlistP+"/batch/remove")) != 0 {
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

// A playlist copied from a collection takes the description asked for, not
// only when a new name is asked for too; a blank name keeps the collection's.
func TestPlaylistFromCollectionKeepsTheDescription(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/collections/"+colC, `{"id":"`+colC+`","libraryId":"`+libID+`","name":"Shelf","description":"the shelf's"}`)
	f.json("GET /api/libraries/"+libID+"/playlists", `{"results":[]}`)
	f.json("POST /api/playlists/collection/"+colC, `{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Shelf","description":"the shelf's","items":[]}`)
	f.json("PATCH /api/playlists/"+playlistP, `{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Shelf","description":"mine","items":[]}`)
	call := toolCaller(t, f)

	for _, name := range []string{"", "shelf"} {
		out, err := call("playlist_create", map[string]any{"from_collection": colC, "name": name, "description": "mine"})
		if err != nil {
			t.Fatal(err)
		}
		if str(t, out["description"]) != "mine" {
			t.Errorf("name %q: description = %v, want mine", name, out["description"])
		}
	}
	sent := f.requests("/api/playlists/" + playlistP)
	if len(sent) != 2 {
		t.Fatalf("sent %v, want the description set on each copy", sent)
	}
	for _, r := range sent {
		if r.Body != `{"description":"mine"}` {
			t.Errorf("sent %s, want the description alone", r.Body)
		}
	}

	if _, err := call("playlist_create", map[string]any{"from_collection": colC, "name": "   "}); err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/playlists/" + playlistP); len(got) != 2 {
		t.Errorf("a blank name was sent as a rename: %v", got[len(got)-1])
	}
}

// Audiobookshelf deletes a playlist its last entry leaves, so emptying one is
// a delete and needs the delete tools switched on, as playlist_delete does;
// otherwise it was a way round --enable-delete.
func TestEmptyingAPlaylistNeedsDeletesOn(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	shelfRoutes(f)
	f.json("GET /api/playlists/"+playlistP, `{"id":"`+playlistP+`","libraryId":"`+libID+`","name":"Books List","items":[{"libraryItemId":"`+bookB1+`"}]}`)
	f.json("POST /api/playlists/"+playlistP+"/batch/remove", `{}`)
	call := callerWith(t, f, Options{})

	_, err := call("playlist_entries_edit", map[string]any{"playlist": playlistP, "action": "remove", "entries": []any{map[string]any{"item": bookB1}}})
	if err == nil || !strings.Contains(err.Error(), "--enable-delete") {
		t.Errorf("emptying a playlist with deletes off: %v", err)
	}
	if got := f.requests("/api/playlists/" + playlistP + "/batch/remove"); len(got) != 0 {
		t.Errorf("the removal was sent: %v", got)
	}
}
