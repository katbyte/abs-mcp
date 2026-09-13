//go:build integration

package acceptance

import (
	"slices"
	"testing"
)

func TestItemGet(t *testing.T) {
	// name resolution: the title alone must find it, apostrophe and all
	for _, tc := range []struct{ title, year, author, narrator string }{
		{"Foundation", "1951", "Isaac Asimov", "Scott Brick"},
		{"Abaddon's Gate", "2013", "James S. A. Corey", "Jefferson Mays"},
	} {
		item := call(t, "item_get", map[string]any{"item": tc.title})
		if item["title"] != tc.title {
			t.Errorf("title = %v, want %v", item["title"], tc.title)
		}
		if item["year"] != tc.year {
			t.Errorf("%s year = %v, want %s", tc.title, item["year"], tc.year)
		}
		if item["author"] != tc.author {
			t.Errorf("%s author = %v, want %s", tc.title, item["author"], tc.author)
		}
		if item["narrator"] != tc.narrator {
			t.Errorf("%s narrator = %v, want %s", tc.title, item["narrator"], tc.narrator)
		}
		if item["type"] != "book" {
			t.Errorf("%s type = %v, want book", tc.title, item["type"])
		}
		if noCover, _ := item["no_cover"].(bool); !noCover {
			t.Errorf("%s should report no_cover", tc.title)
		}
	}

	// the series projection is "Name #sequence"
	item := call(t, "item_get", map[string]any{"item": "Sea of Silver Light"})
	if series := strs(t, item["series"], "series"); !slices.Equal(series, []string{"Otherland #4"}) {
		t.Errorf("series = %v, want [Otherland #4]", series)
	}

	// non-fiction has no series at all
	if item := call(t, "item_get", map[string]any{"item": "War Is a Racket"}); item["series"] != nil {
		t.Errorf("War Is a Racket has series %v, want none", item["series"])
	}
}

func TestItemGetUnknown(t *testing.T) {
	if msg := callErr(t, "item_get", map[string]any{"item": "No Such Book"}); msg == "" {
		t.Error("an unknown title should be an error")
	}
}

// files are off by default and pulled in on request, because a real item's
// track list is unbounded.
func TestItemGetFiles(t *testing.T) {
	if bare := call(t, "item_get", map[string]any{"item": "Foundation"}); bare["track_list"] != nil {
		t.Errorf("track_list = %v, want none unless asked for", bare["track_list"])
	}

	out := call(t, "item_get", map[string]any{"item": "Foundation", "files": true})

	if out["title"] != "Foundation" {
		t.Errorf("title = %v", out["title"])
	}
	tracks := rows(t, out["track_list"], "track_list")
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want the single fixture file", len(tracks))
	}
	if name, _ := tracks[0]["filename"].(string); name != "01.mp3" {
		t.Errorf("filename = %v, want 01.mp3", tracks[0]["filename"])
	}
	// ffprobe ran on it, so the codec must have come back
	if codec, _ := tracks[0]["codec"].(string); codec == "" {
		t.Errorf("no codec probed: %v", tracks[0])
	}
}

// the fixtures are one-second files with no embedded chapters.
func TestItemGetChapters(t *testing.T) {
	if bare := call(t, "item_get", map[string]any{"item": "Foundation"}); bare["chapter_list"] != nil {
		t.Errorf("chapter_list = %v, want none unless asked for", bare["chapter_list"])
	}

	out := call(t, "item_get", map[string]any{"item": "Foundation", "chapters": true})

	if out["title"] != "Foundation" {
		t.Errorf("title = %v", out["title"])
	}
	if chapters := rows(t, out["chapter_list"], "chapter_list"); len(chapters) != 0 {
		t.Errorf("chapter_list = %v, want none on the fixtures", chapters)
	}
}

// item_chapters_set has two modes; the explicit list needs no provider.
func TestItemChaptersSetExplicit(t *testing.T) {
	out := call(t, "item_chapters_set", map[string]any{
		"item": "War Is a Racket",
		"chapters": []any{
			map[string]any{"title": "Chapter One", "start": 0},
			map[string]any{"title": "Chapter Two", "start": 0.5},
		},
	})
	if n := num(t, out["chapters"], "chapters"); n != 2 {
		t.Errorf("chapters = %d, want 2", n)
	}
	t.Cleanup(func() {
		call(t, "item_chapters_set", map[string]any{
			"item":     "War Is a Racket",
			"chapters": []any{map[string]any{"title": "Chapter One", "start": 0}},
		})
	})

	got := call(t, "item_get", map[string]any{"item": "War Is a Racket", "chapters": true})
	chapters := rows(t, got["chapter_list"], "chapter_list")
	if len(chapters) != 2 {
		t.Fatalf("read back %d chapters, want 2", len(chapters))
	}
	if chapters[0]["title"] != "Chapter One" {
		t.Errorf("chapter 1 title = %v", chapters[0]["title"])
	}
}

func TestItemEditRoundTrip(t *testing.T) {
	out := call(t, "item_edit", map[string]any{
		"item": "War Is a Racket", "subtitle": "The Antiwar Classic",
		"tags": []any{"war", "politics", "integration-test"},
	})
	if updated, _ := out["updated"].(bool); !updated {
		t.Errorf("item_edit reported no update: %v", out)
	}
	if fields := strs(t, out["fields_sent"], "fields_sent"); !slices.Contains(fields, "subtitle") {
		t.Errorf("fields_sent = %v, want subtitle among them", fields)
	}
	t.Cleanup(func() {
		call(t, "item_edit", map[string]any{
			"item": "War Is a Racket", "tags": []any{"war", "politics"}, "clear": []any{"subtitle"},
		})
	})

	item := call(t, "item_get", map[string]any{"item": "War Is a Racket"})
	if tags := strs(t, item["tags"], "tags"); !slices.Contains(tags, "integration-test") {
		t.Errorf("tags = %v, want the edit to be visible", tags)
	}
	if item["subtitle"] != "The Antiwar Classic" {
		t.Errorf("subtitle = %v", item["subtitle"])
	}
}

// clear blanks a field rather than setting it.
func TestItemEditClear(t *testing.T) {
	call(t, "item_edit", map[string]any{"item": "The Arms of Krupp", "publisher": "Temporary"})
	call(t, "item_edit", map[string]any{"item": "The Arms of Krupp", "clear": []any{"publisher"}})
	t.Cleanup(func() {
		call(t, "item_edit", map[string]any{"item": "The Arms of Krupp", "publisher": "Little, Brown"})
	})

	if item := call(t, "item_get", map[string]any{"item": "The Arms of Krupp"}); item["publisher"] != nil {
		t.Errorf("publisher = %v, want cleared", item["publisher"])
	}
}

func TestItemEditNothingToDo(t *testing.T) {
	if msg := callErr(t, "item_edit", map[string]any{"item": "Foundation"}); msg == "" {
		t.Error("an edit with no fields should be refused")
	}
}

func TestItemRescan(t *testing.T) {
	out := call(t, "item_rescan", map[string]any{"item": "Foundation"})

	// NOTHING, ADDED, UPDATED, REMOVED or UPTODATE
	result, _ := out["result"].(string)
	if !slices.Contains([]string{"NOTHING", "ADDED", "UPDATED", "REMOVED", "UPTODATE"}, result) {
		t.Errorf("item_rescan result = %q, want one of the documented values", result)
	}
}

// no cover was ever set, so removing one is a no-op that must still succeed.
// Removal has to be asked for: a call with nothing to set is refused rather
// than read as "take the cover away".
func TestItemCoverRemove(t *testing.T) {
	if msg := callErr(t, "item_cover_edit", map[string]any{"item": "A Brief History of Vice"}); msg == "" {
		t.Error("item_cover_edit with neither url, file nor remove should be refused")
	}

	out := call(t, "item_cover_edit", map[string]any{"item": "A Brief History of Vice", "remove": true})

	if done, _ := out["done"].(bool); !done {
		t.Errorf("item_cover_edit done = %v", out["done"])
	}
}

// embedding rewrites the audio tags in the background; assert it starts.
func TestItemEmbedMetadata(t *testing.T) {
	out := call(t, "item_embed_metadata", map[string]any{"item": "A Brief History of Vice", "backup": true})

	if started, _ := out["started"].(string); started == "" {
		t.Errorf("item_embed_metadata started = %v", out["started"])
	}
}

func TestItemPodcastGuards(t *testing.T) {
	// a podcast is not a book, and the book-only tools must say so
	if msg := callErr(t, "item_chapters_set", map[string]any{
		"item":     "Behind the Bastards",
		"chapters": []any{map[string]any{"title": "x", "start": 0}},
	}); msg == "" {
		t.Error("item_chapters_set should refuse a podcast")
	}
}

// item_batch_edit sends one payload per item under mediaPayload; a flattened
// entry crashes the server outright rather than erroring, so this is worth
// exercising against the real thing.
func TestItemBatchEdit(t *testing.T) {
	titles := []any{"The Arms of Krupp", "A Brief History of Vice", "War Is a Racket"}

	out := call(t, "item_batch_edit", map[string]any{
		"library": "Non-Fiction", "items": titles, "genres": []any{"Batch Genre"},
	})
	if n := num(t, out["items_updated"], "items_updated"); n != 3 {
		t.Errorf("items_updated = %d, want 3", n)
	}
	if sent := strs(t, out["items"], "items"); len(sent) != 3 {
		t.Errorf("items = %v, want the three titles", sent)
	}
	t.Cleanup(func() {
		call(t, "item_batch_edit", map[string]any{
			"library": "Non-Fiction", "items": titles, "genres": []any{"History"},
		})
	})

	for _, title := range titles {
		item := call(t, "item_get", map[string]any{"item": title})
		if genres := strs(t, item["genres"], "genres"); !slices.Contains(genres, "Batch Genre") {
			t.Errorf("%v genres = %v, want the batch edit applied", title, genres)
		}
	}

	// the server survived, which a flattened payload would not have
	if info := call(t, "server_info", nil); info["version"] == nil {
		t.Error("the server stopped answering after a batch update")
	}
}

func TestItemBatchEditValidation(t *testing.T) {
	if msg := callErr(t, "item_batch_edit", map[string]any{"items": []any{}}); msg == "" {
		t.Error("an empty item list should be refused")
	}
	if msg := callErr(t, "item_batch_edit", map[string]any{"items": []any{"Foundation"}}); msg == "" {
		t.Error("an edit with no fields should be refused")
	}
	if msg := callErr(t, "item_batch_edit", map[string]any{
		"items": []any{"Behind the Bastards"}, "genres": []any{"x"},
	}); msg == "" {
		t.Error("a podcast should be refused")
	}
}

// item_get on a podcast takes three branches a book never does: the episode
// listing, the per-episode audio files, and chapters that live on the episodes
// rather than the item. None were covered.
func TestItemGetPodcast(t *testing.T) {
	out := call(t, "item_get", map[string]any{"item": "Behind the Bastards", "files": true, "chapters": true})

	if out["type"] != "podcast" {
		t.Errorf("type = %v, want podcast", out["type"])
	}
	episodes := rows(t, out["episodes"], "episodes")
	if len(episodes) == 0 {
		t.Fatal("no episodes on a podcast item_get")
	}
	if total := num(t, out["episode_total"], "episode_total"); total != len(episodes) {
		t.Errorf("episode_total = %d but %d episodes returned", total, len(episodes))
	}

	// files=true on a podcast lists the downloaded episodes' audio, which comes
	// from a different field than a book's tracks
	tracks := rows(t, out["track_list"], "track_list")
	if len(tracks) != len(episodes) {
		t.Errorf("track_list = %d, want one per downloaded episode (%d)", len(tracks), len(episodes))
	}
	for _, tr := range tracks {
		if codec, _ := tr["codec"].(string); codec == "" {
			t.Errorf("no codec probed on %v", tr)
		}
	}

	// a podcast's chapters belong to its episodes, so the item's own list is
	// empty rather than missing - the schema says where to look instead
	chapters := out["chapter_list"]
	if chapters == nil {
		t.Error("chapter_list is absent even though chapters was asked for")
	}
	if got := rows(t, chapters, "chapter_list"); len(got) != 0 {
		t.Errorf("chapter_list = %v, want empty on a podcast", got)
	}
}
