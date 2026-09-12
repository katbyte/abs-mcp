//go:build integration

package acceptance

import (
	"strings"
	"testing"
)

// Every tool that takes a thing documents "id, or an exact title" - and only
// the title half was ever tested, across all ten looksLikeID call sites. An id
// is what a model actually has after a search or a list, so this walks each
// resource: list it, take the id, and ask for it again by id.
func TestResolveByID(t *testing.T) {
	// library
	libs := rows(t, call(t, "library_list", nil)["libraries"], "libraries")
	var libID string
	for _, l := range libs {
		if l["name"] == "Fiction" {
			libID, _ = l["id"].(string)
		}
	}
	if libID == "" {
		t.Fatal("no id for the Fiction library")
	}
	if got := call(t, "library_get", map[string]any{"library": libID}); got["name"] != "Fiction" {
		t.Errorf("library by id = %v, want Fiction", got["name"])
	}

	// item, and the library narrowed by id at the same time
	items := rows(t, call(t, "library_items", map[string]any{"library": libID, "limit": 50})["items"], "items")
	var itemID string
	for _, it := range items {
		if it["title"] == "Foundation" {
			itemID, _ = it["id"].(string)
		}
	}
	if itemID == "" {
		t.Fatal("no id for Foundation")
	}
	byID := call(t, "item_get", map[string]any{"item": itemID})
	if byID["title"] != "Foundation" {
		t.Errorf("item by id = %v, want Foundation", byID["title"])
	}
	// the id path has to reach the same record the title path does
	byTitle := call(t, "item_get", map[string]any{"item": "Foundation"})
	if byID["id"] != byTitle["id"] {
		t.Errorf("id and title resolved to different items: %v vs %v", byID["id"], byTitle["id"])
	}

	// author
	authors := rows(t, call(t, "author_list", map[string]any{"library": libID})["authors"], "authors")
	if len(authors) == 0 {
		t.Fatal("no authors")
	}
	authorID, _ := authors[0]["id"].(string)
	name := authors[0]["name"]
	if authorID == "" {
		t.Fatalf("no id on author %v", authors[0])
	}
	if got := call(t, "author_get", map[string]any{"author": authorID}); got["name"] != name {
		t.Errorf("author by id = %v, want %v", got["name"], name)
	}

	// series
	series := rows(t, call(t, "series_list", map[string]any{"library": libID})["series"], "series")
	if len(series) == 0 {
		t.Fatal("no series")
	}
	seriesID, _ := series[0]["id"].(string)
	seriesName := series[0]["name"]
	if seriesID != "" {
		if got := call(t, "series_get", map[string]any{"series": seriesID}); got["name"] != seriesName {
			t.Errorf("series by id = %v, want %v", got["name"], seriesName)
		}
	}

	// user
	users := rows(t, call(t, "user_list", nil)["users"], "users")
	if len(users) == 0 {
		t.Fatal("no users")
	}
	userID, _ := users[0]["id"].(string)
	if got := call(t, "user_get", map[string]any{"user": userID}); got["username"] != "root" {
		t.Errorf("user by id = %v, want root", got["username"])
	}
}

// A collection and a playlist are created here rather than listed, so their id
// path is exercised on a record this test owns.
func TestResolveCollectionAndPlaylistByID(t *testing.T) {
	created := call(t, "collection_create", map[string]any{
		"library": "Fiction", "name": "By ID Collection", "items": []any{"Foundation"},
	})
	colID, _ := created["id"].(string)
	t.Cleanup(func() { call(t, "collection_delete", map[string]any{"collection": colID}) })
	if colID == "" {
		t.Fatalf("no collection id: %v", created)
	}
	if got := call(t, "collection_get", map[string]any{"collection": colID}); got["name"] != "By ID Collection" {
		t.Errorf("collection by id = %v", got["name"])
	}

	pl := call(t, "playlist_create", map[string]any{
		"library": "Fiction", "name": "By ID Playlist",
		"entries": []any{map[string]any{"item": "Foundation"}},
	})
	plID, _ := pl["id"].(string)
	t.Cleanup(func() { call(t, "playlist_delete", map[string]any{"playlist": plID}) })
	if plID == "" {
		t.Fatalf("no playlist id: %v", pl)
	}
	if got := call(t, "playlist_get", map[string]any{"playlist": plID}); got["name"] != "By ID Playlist" {
		t.Errorf("playlist by id = %v", got["name"])
	}
}

// The README promises an ambiguous name comes back as an error listing the
// candidates. Nothing tested it, because no fixture name spans two libraries -
// so this makes one span, checks the error names both, and puts it back.
func TestResolveAmbiguousAcrossLibraries(t *testing.T) {
	const shared = "Isaac Asimov"
	const book = "War Is a Racket" // Non-Fiction

	if _, err := invoke("author_get", map[string]any{"author": shared}); err != nil {
		t.Fatalf("%s should resolve in one library to begin with: %v", shared, err)
	}

	call(t, "item_edit", map[string]any{"item": book, "authors": []any{shared}})
	t.Cleanup(func() {
		call(t, "item_edit", map[string]any{"item": book, "authors": []any{"Smedley D. Butler"}})
	})

	msg := callErr(t, "author_get", map[string]any{"author": shared})
	if msg == "" {
		t.Fatal("an author in two libraries should be ambiguous")
	}
	for _, want := range []string{"several libraries", "pass an id"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not say %q: %s", want, msg)
		}
	}

	// and naming the library resolves it again
	one := call(t, "author_list", map[string]any{"library": "Fiction"})
	var id string
	for _, a := range rows(t, one["authors"], "authors") {
		if a["name"] == shared {
			id, _ = a["id"].(string)
		}
	}
	if id == "" {
		t.Fatalf("%s is not in Fiction's authors", shared)
	}
	if got := call(t, "author_get", map[string]any{"author": id}); got["name"] != shared {
		t.Errorf("by id = %v, want %s", got["name"], shared)
	}
}
