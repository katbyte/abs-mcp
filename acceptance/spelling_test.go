//go:build integration

package acceptance

import (
	"slices"
	"testing"
	"time"
)

// metadata_rename merges a group audit_spelling found, whichever field it is
// in. The fixtures are deliberately consistent, so each case here introduces
// the divergence, merges it away, and puts the fixture back the way it found
// it. Tags and narrators are renamed in server_test.go and catalog_test.go.

func TestMetadataRenameLanguage(t *testing.T) {
	const item = "Abaddon's Gate"

	// "eng" and "English" are the same language spelled two ways
	call(t, "item_edit", map[string]any{"item": item, "language": "eng"})
	t.Cleanup(func() {
		call(t, "item_edit", map[string]any{"item": item, "language": "English"})
	})

	// which audit_spelling now groups
	groups := rows(t, call(t, "audit_spelling", map[string]any{"field": "languages"})["groups"], "groups")
	if len(groups) != 1 {
		t.Fatalf("audit_spelling found %d language groups, want 1: %v", len(groups), groups)
	}

	out := call(t, "metadata_rename", map[string]any{
		"library": "Fiction", "field": "languages", "from": "eng", "to": "English",
	})
	if n := num(t, out["items_updated"], "items_updated"); n != 1 {
		t.Errorf("items_updated = %d, want 1", n)
	}
	if titles := strs(t, out["items"], "items"); len(titles) != 1 || titles[0] != item {
		t.Errorf("items = %v, want [%s]", titles, item)
	}

	// and the group is gone
	if after := rows(t, call(t, "audit_spelling", map[string]any{"field": "languages"})["groups"], "groups"); len(after) != 0 {
		t.Errorf("audit_spelling still reports %d groups after the merge: %v", len(after), after)
	}
}

func TestMetadataRenamePublisher(t *testing.T) {
	const item = "War Is a Racket"

	call(t, "item_edit", map[string]any{"item": item, "publisher": "round table"})
	t.Cleanup(func() {
		call(t, "item_edit", map[string]any{"item": item, "publisher": "Round Table"})
	})

	out := call(t, "metadata_rename", map[string]any{
		"field": "publisher", "from": "round table", "to": "Round Table",
	})
	if n := num(t, out["items_updated"], "items_updated"); n != 1 {
		t.Errorf("items_updated = %d, want 1", n)
	}

	// the item really carries the kept spelling now
	if got := call(t, "item_get", map[string]any{"item": item})["publisher"]; got != "Round Table" {
		t.Errorf("publisher = %v, want Round Table", got)
	}
}

// A language two podcasts and a book carry, renamed in one sweep: the shows
// change with the book. The server's batch update is written for books and
// fails on a podcast, so the shows are sent on their own.
func TestMetadataRenameLanguageOnPodcasts(t *testing.T) {
	// a show subscribed from a feed beside the two laid out on disk: the
	// server holds more about one it follows
	feed := showServe(t, "zzyzx-tongue.test", "Zzyzx Tongue Show",
		feedItem{guid: "zzyzx-tongue-1", title: "Zzyzx Tongue One", published: time.Now().Add(-24 * time.Hour)})
	showSubscribe(t, feed, "Zzyzx Tongue Show", 1)
	waitIdle(t)

	shows := map[string]string{}
	for _, show := range append(slices.Clone(podcasts), feed.title) {
		shows[show] = text(call(t, "item_get", map[string]any{"library": "Podcasts", "item": show})["language"])
	}
	const book = "War Is a Racket"
	was := text(call(t, "item_get", map[string]any{"item": book})["language"])
	restore := func(item, library, language string) {
		args := map[string]any{"item": item, "library": library, "language": language}
		if language == "" {
			args = map[string]any{"item": item, "library": library, "clear": []any{"language"}}
		}
		if _, err := invoke("item_edit", args); err != nil {
			t.Errorf("putting %s's language back: %v", item, err)
		}
	}
	t.Cleanup(func() {
		for show, language := range shows {
			restore(show, "Podcasts", language)
		}
		restore(book, "Non-Fiction", was)
	})
	for show := range shows {
		call(t, "item_edit", map[string]any{"library": "Podcasts", "item": show, "language": "zzyzx-eng"})
	}
	call(t, "item_edit", map[string]any{"item": book, "language": "zzyzx-eng"})

	out := call(t, "metadata_rename", map[string]any{"field": "languages", "from": "zzyzx-eng", "to": "Zzyzxish"})
	if n := num(t, out["items_updated"], "items_updated"); n != len(shows)+1 {
		t.Errorf("items_updated = %d, want the %d shows and the book: %v", n, len(shows), out)
	}
	for show := range shows {
		if got := call(t, "item_get", map[string]any{"library": "Podcasts", "item": show})["language"]; got != "Zzyzxish" {
			t.Errorf("%s's language = %v, want Zzyzxish", show, got)
		}
	}
	if got := call(t, "item_get", map[string]any{"item": book})["language"]; got != "Zzyzxish" {
		t.Errorf("the book's language = %v, want Zzyzxish", got)
	}
}

// an author rename goes through the author record, so the books follow it
func TestMetadataRenameAuthor(t *testing.T) {
	out := call(t, "metadata_rename", map[string]any{
		"library": "Non-Fiction", "field": "authors", "from": "Smedley D. Butler", "to": "Smedley Butler",
	})
	if n := num(t, out["items_updated"], "items_updated"); n != 1 {
		t.Errorf("items_updated = %d, want 1", n)
	}
	if merged := truth(out["merged"]); merged {
		t.Error("a rename onto a new name reported a merge")
	}
	t.Cleanup(func() {
		call(t, "metadata_rename", map[string]any{
			"library": "Non-Fiction", "field": "authors", "from": "Smedley Butler", "to": "Smedley D. Butler",
		})
	})

	if got := call(t, "item_get", map[string]any{"item": "War Is a Racket"})["author"]; got != "Smedley Butler" {
		t.Errorf("author = %v, want Smedley Butler", got)
	}
}

// remove drops a value everywhere; a genre, restored through item_edit
func TestMetadataRemoveGenre(t *testing.T) {
	call(t, "item_edit", map[string]any{"item": "War Is a Racket", "genres": []any{"History", "Temporary Genre"}})
	t.Cleanup(func() {
		call(t, "item_edit", map[string]any{"item": "War Is a Racket", "genres": []any{"History"}})
	})

	// without confirm it names what it would drop the genre from and drops nothing
	out := call(t, "metadata_rename", map[string]any{"field": "genres", "from": "Temporary Genre", "remove": true})
	preview := object(out["preview"])
	if found := num(t, preview["found"], "preview.found"); found != 1 || num(t, out["items_updated"], "items_updated") != 0 {
		t.Errorf("preview = %v, want the one book and nothing changed", out)
	}
	if items := strs(t, preview["items"], "preview.items"); len(items) != 1 || items[0] != "War Is a Racket" {
		t.Errorf("preview items = %v, want War Is a Racket", items)
	}
	if genres := strs(t, call(t, "item_get", map[string]any{"item": "War Is a Racket"})["genres"], "genres"); len(genres) != 2 {
		t.Errorf("the preview dropped the genre: %v", genres)
	}

	out = call(t, "metadata_rename", map[string]any{"field": "genres", "from": "Temporary Genre", "remove": true, "confirm": true})
	if n := num(t, out["items_updated"], "items_updated"); n != 1 {
		t.Errorf("items_updated = %d, want 1", n)
	}
	if genres := strs(t, call(t, "item_get", map[string]any{"item": "War Is a Racket"})["genres"], "genres"); len(genres) != 1 {
		t.Errorf("genres = %v, want only History", genres)
	}
}

// refusals: an unknown field, a missing from or to, both to and remove, a
// scoped rename of a server-wide field, and a value nothing carries.
func TestMetadataRenameRefusals(t *testing.T) {
	for _, args := range []map[string]any{
		{"field": "nope", "from": "a", "to": "b"},
		{"field": "languages", "from": "", "to": "English"},
		{"field": "languages", "from": "English"},
		{"field": "languages", "from": "English", "to": "en", "remove": true},
		{"field": "authors", "from": "Isaac Asimov", "remove": true},
		{"field": "tags", "from": "sf", "to": "scifi", "library": "Fiction"},
		{"field": "languages", "from": "Old High Martian", "to": "English"},
	} {
		if msg := callErr(t, "metadata_rename", args); msg == "" {
			t.Errorf("%v should be refused", args)
		}
	}
}
