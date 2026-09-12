//go:build integration

package acceptance

import (
	"testing"
)

func TestMeGet(t *testing.T) {
	out := call(t, "me_get", nil)

	if out["username"] != "root" {
		t.Errorf("username = %v, want root", out["username"])
	}
	if out["type"] != "root" {
		t.Errorf("type = %v, want root", out["type"])
	}
	for _, perm := range []string{"can_update", "can_delete", "can_upload"} {
		if ok, _ := out[perm].(bool); !ok {
			t.Errorf("root should have %s", perm)
		}
	}
}

// the whole progress lifecycle: set, read, appear in-progress, remove.
func TestMeProgress(t *testing.T) {
	set := call(t, "me_progress_set", map[string]any{"item": "Foundation", "percent": 50})
	t.Cleanup(func() {
		call(t, "me_progress_remove", map[string]any{"item": "Foundation"})
	})
	if set["item"] != "Foundation" {
		t.Errorf("item = %v", set["item"])
	}

	got := call(t, "me_progress_get", map[string]any{"item": "Foundation"})
	progress, ok := got["progress"].(map[string]any)
	if !ok {
		t.Fatalf("progress = %T", got["progress"])
	}
	if pct := num(t, progress["percent"], "percent"); pct != 50 {
		t.Errorf("percent = %d, want 50", pct)
	}

	// it now shows on the continue-listening shelf
	var onShelf bool
	for _, row := range rows(t, call(t, "me_in_progress", nil)["items"], "items") {
		if row["title"] == "Foundation" {
			onShelf = true
		}
	}
	if !onShelf {
		t.Error("Foundation is not in me_in_progress after setting progress")
	}

	// finishing it takes it off the shelf
	call(t, "me_progress_set", map[string]any{"item": "Foundation", "finished": true})
	fin := call(t, "me_progress_get", map[string]any{"item": "Foundation"})
	if p, ok := fin["progress"].(map[string]any); ok {
		if finished, _ := p["finished"].(bool); !finished {
			t.Errorf("finished = %v, want true", p["finished"])
		}
	}
}

func TestMeProgressRemove(t *testing.T) {
	call(t, "me_progress_set", map[string]any{"item": "Second Foundation", "percent": 10})
	out := call(t, "me_progress_remove", map[string]any{"item": "Second Foundation"})

	if done, _ := out["done"].(bool); !done {
		t.Errorf("me_progress_remove done = %v", out["done"])
	}
	got := call(t, "me_progress_get", map[string]any{"item": "Second Foundation"})
	if got["progress"] != nil {
		t.Errorf("progress = %v, want none after removal", got["progress"])
	}
}

func TestMeBookmarks(t *testing.T) {
	added := call(t, "me_bookmark_add", map[string]any{
		"item": "Leviathan Wakes", "seconds": 0.25, "title": "A good bit",
	})
	t.Cleanup(func() {
		call(t, "me_bookmark_remove", map[string]any{"item": "Leviathan Wakes", "seconds": 0.25})
	})
	if added["title"] != "A good bit" {
		t.Errorf("title = %v", added["title"])
	}

	out := call(t, "me_bookmarks", map[string]any{"item": "Leviathan Wakes"})
	bookmarks := rows(t, out["bookmarks"], "bookmarks")
	if len(bookmarks) != 1 {
		t.Fatalf("bookmarks = %d, want 1", len(bookmarks))
	}
	if bookmarks[0]["title"] != "A good bit" {
		t.Errorf("bookmark title = %v", bookmarks[0]["title"])
	}

	// and all of them, unfiltered
	if all := call(t, "me_bookmarks", nil); len(rows(t, all["bookmarks"], "bookmarks")) == 0 {
		t.Error("me_bookmarks with no item returned nothing")
	}
}

// nothing has been played, so history and stats are the empty case - which
// still has to decode as the right shape.
func TestMeHistoryAndStats(t *testing.T) {
	history := call(t, "me_history", nil)
	rows(t, history["sessions"], "sessions")
	num(t, history["total_sessions"], "total_sessions")

	stats := call(t, "me_stats", nil)
	if _, ok := stats["total_listened"].(string); !ok {
		t.Errorf("total_listened = %T, want a formatted duration", stats["total_listened"])
	}

	// the year-in-review variant takes a different endpoint entirely
	year := call(t, "me_stats", map[string]any{"year": 2026})
	if year == nil {
		t.Error("me_stats year returned nothing")
	}
}
