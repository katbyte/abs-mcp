//go:build integration

// Journey 13: calls made at once. A client sends one turn's tool calls
// together and the MCP server runs them together, so two edits of one record
// overlap: every change each call asked for has to be there afterwards, not
// only the last one to land.
package acceptance

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

// atOnce makes n calls concurrently and fails on any error.
func atOnce(t *testing.T, n int, next func(i int) (string, map[string]any)) {
	t.Helper()

	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		tool, args := next(i)
		wg.Go(func() {
			_, errs[i] = invoke(tool, args)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestJourneyCallsAtOnce(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}
	const n = 8

	t.Run("tags added to one book", func(t *testing.T) {
		t.Cleanup(func() { restoreBook(t, "Leviathan Wakes") })
		atOnce(t, n, func(i int) (string, map[string]any) {
			return "item_edit", map[string]any{"library": "Fiction", "item": "Leviathan Wakes", "add_tags": []any{fmt.Sprintf("zzyzx-at-once-%d", i)}}
		})
		tags := strs(t, call(t, "item_get", map[string]any{"library": "Fiction", "item": "Leviathan Wakes"})["tags"], "tags")
		for i := range n {
			if !slices.Contains(tags, fmt.Sprintf("zzyzx-at-once-%d", i)) {
				t.Errorf("tags = %v, missing zzyzx-at-once-%d", tags, i)
			}
		}
		if !slices.Contains(tags, "space-opera") {
			t.Errorf("tags = %v, lost the book's own", tags)
		}
	})

	t.Run("series added to one book", func(t *testing.T) {
		t.Cleanup(func() { restoreBook(t, "Leviathan Wakes") })
		atOnce(t, n, func(i int) (string, map[string]any) {
			return "item_edit", map[string]any{"library": "Fiction", "item": "Leviathan Wakes", "add_series": []any{fmt.Sprintf("Zzyzx At Once %d #1", i)}}
		})
		series := strs(t, call(t, "item_get", map[string]any{"library": "Fiction", "item": "Leviathan Wakes"})["series"], "series")
		if len(series) != n+1 || !slices.Contains(series, "The Expanse #1") {
			t.Errorf("series = %v, want The Expanse and the %d added", series, n)
		}
	})

	t.Run("one book's tags from item_edit and item_batch_edit together", func(t *testing.T) {
		t.Cleanup(func() { restoreBook(t, "Abaddon's Gate") })
		atOnce(t, 2*n, func(i int) (string, map[string]any) {
			tag := fmt.Sprintf("zzyzx-mixed-%d", i)
			if i%2 == 0 {
				return "item_edit", map[string]any{"library": "Fiction", "item": "Abaddon's Gate", "add_tags": []any{tag}}
			}
			return "item_batch_edit", map[string]any{"library": "Fiction", "items": []any{"Abaddon's Gate"}, "add_tags": []any{tag}}
		})
		tags := strs(t, call(t, "item_get", map[string]any{"library": "Fiction", "item": "Abaddon's Gate"})["tags"], "tags")
		for i := range 2 * n {
			if !slices.Contains(tags, fmt.Sprintf("zzyzx-mixed-%d", i)) {
				t.Errorf("tags = %v, missing zzyzx-mixed-%d", tags, i)
			}
		}
	})

	t.Run("books added to one collection", func(t *testing.T) {
		call(t, "collection_create", map[string]any{"library": "Fiction", "name": "Zzyzx At Once Shelf", "items": []any{"Foundation"}})
		t.Cleanup(func() { call(t, "collection_delete", map[string]any{"collection": "Zzyzx At Once Shelf"}) })
		others := []string{"Foundation and Empire", "Second Foundation", "City of Golden Shadow", "Sea of Silver Light", "Leviathan Wakes", "Abaddon's Gate"}
		atOnce(t, len(others), func(i int) (string, map[string]any) {
			return "collection_books_edit", map[string]any{"collection": "Zzyzx At Once Shelf", "action": "add", "items": []any{others[i]}}
		})
		if got := titlesIn(t, call(t, "collection_get", map[string]any{"collection": "Zzyzx At Once Shelf"})["items"], "items"); len(got) != 1+len(others) {
			t.Errorf("the collection holds %v, want all %d", got, 1+len(others))
		}
	})

	t.Run("entries added to one playlist", func(t *testing.T) {
		call(t, "playlist_create", map[string]any{"library": "Fiction", "name": "Zzyzx At Once Queue", "entries": []any{map[string]any{"item": "Foundation"}}})
		t.Cleanup(func() { call(t, "playlist_delete", map[string]any{"playlist": "Zzyzx At Once Queue"}) })
		others := []string{"Foundation and Empire", "Second Foundation", "City of Golden Shadow", "Sea of Silver Light", "Leviathan Wakes", "Abaddon's Gate"}
		atOnce(t, len(others), func(i int) (string, map[string]any) {
			return "playlist_entries_edit", map[string]any{"playlist": "Zzyzx At Once Queue", "action": "add", "entries": []any{map[string]any{"item": others[i]}}}
		})
		if got := rows(t, call(t, "playlist_get", map[string]any{"playlist": "Zzyzx At Once Queue"})["entries"], "entries"); len(got) != 1+len(others) {
			t.Errorf("the playlist holds %d entries, want all %d", len(got), 1+len(others))
		}
	})

	t.Run("bookmarks added to one book", func(t *testing.T) {
		seconds := func(i int) float64 { return 0.1 + float64(i)/10 }
		t.Cleanup(func() {
			for i := range n {
				_, _ = invoke("user_bookmark_edit", map[string]any{"library": "Fiction", "item": "Second Foundation", "action": "remove", "seconds": seconds(i)})
			}
		})
		atOnce(t, n, func(i int) (string, map[string]any) {
			return "user_bookmark_edit", map[string]any{"library": "Fiction", "item": "Second Foundation", "action": "add", "seconds": seconds(i), "title": fmt.Sprintf("Zzyzx At Once %d", i)}
		})
		if got := rows(t, call(t, "user_bookmarks", map[string]any{"library": "Fiction", "item": "Second Foundation"})["bookmarks"], "bookmarks"); len(got) != n {
			t.Errorf("bookmarks = %v, want all %d", titlesIn(t, got, "bookmarks"), n)
		}
	})

	t.Run("progress on several books", func(t *testing.T) {
		books := []string{"Foundation", "Foundation and Empire", "Second Foundation", "City of Golden Shadow"}
		t.Cleanup(func() {
			for _, b := range books {
				_, _ = invoke("user_progress_remove", map[string]any{"library": "Fiction", "item": b})
			}
		})
		atOnce(t, len(books), func(i int) (string, map[string]any) {
			return "user_progress_set", map[string]any{"library": "Fiction", "item": books[i], "percent": 20 + 10*i}
		})
		for i, b := range books {
			p, _ := call(t, "user_progress_get", map[string]any{"library": "Fiction", "item": b})["progress"].(map[string]any)
			if p == nil || num(t, p["percent"], "percent") != 20+10*i {
				t.Errorf("%s progress = %v, want %d percent", b, p, 20+10*i)
			}
		}
	})
}
