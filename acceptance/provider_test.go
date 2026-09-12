//go:build integration

// The tools that reach a metadata provider. Audiobookshelf, not abs-mcp, makes
// these calls, so they are intercepted at its edge by the record/replay proxy
// in lib/providerproxy: by default they replay from
// testdata/cassettes and touch no network, and `make record` refreshes them
// against the real Audible, Audnexus and iTunes.
//
// The multi-step flows are ordered subtests rather than separate tests,
// because the second step needs an asin or a feed url the first one returns -
// which is also how a real curation session runs.
package acceptance

import (
	"strings"
	"testing"
	"time"
)

// requireProviders skips when the proxy is not up, so these fail for a real
// reason rather than a confusing connection error.
func requireProviders(t *testing.T) {
	t.Helper()

	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set; run: eval \"$(scripts/abs-testenv.sh up)\"")
	}
	if !providersReady() {
		t.Skip("the provider proxy is not running")
	}
}

// restoreBook puts a seeded book's metadata back after a test has matched over
// it, so the rest of the suite still sees the fixture it expects.
func restoreBook(t *testing.T, title string) {
	t.Helper()

	for _, b := range books {
		if b.Title != title {
			continue
		}
		args := map[string]any{
			"item": title, "title": b.Title, "authors": []any{b.Author},
			"narrators": []any{b.Narrator}, "publisher": b.Publisher,
			"year": b.Year, "language": b.Language,
			"tags": toAny(b.Tags), "genres": toAny(b.Genres),
		}
		if len(b.Series) > 0 {
			args["series"] = toAny(b.Series)
		}
		call(t, "item_edit", args)

		return
	}
	t.Fatalf("no fixture named %q to restore", title)
}

// item_match asks a provider for candidates and changes nothing;
// item_match_apply then pulls one in.
func TestItemMatchAndApply(t *testing.T) {
	requireProviders(t)

	const book = "Second Foundation"
	var asin string

	t.Run("item_match", func(t *testing.T) {
		out := call(t, "item_match", map[string]any{
			"item": book, "provider": "audible", "title": "Second Foundation", "author": "Isaac Asimov",
		})
		if out["provider"] != "audible" {
			t.Errorf("provider = %v, want audible", out["provider"])
		}

		candidates := rows(t, out["candidates"], "candidates")
		if len(candidates) == 0 {
			t.Fatal("audible returned no candidates for Second Foundation")
		}
		// a real catalogue entry, not just a well-formed empty shell
		first := candidates[0]
		title, _ := first["title"].(string)
		if !strings.Contains(strings.ToLower(title), "foundation") {
			t.Errorf("first candidate title = %q, want it to mention Foundation", title)
		}
		if idx := num(t, first["index"], "index"); idx != 0 {
			t.Errorf("first candidate index = %d, want 0", idx)
		}
		asin, _ = first["asin"].(string)
		if asin == "" {
			t.Error("no asin on the first candidate; item_match_apply has nothing to match on")
		}

		// nothing may have changed yet: these are suggestions
		if item := call(t, "item_get", map[string]any{"item": book}); item["asin"] != nil {
			t.Errorf("item_match set an asin (%v); it must only suggest", item["asin"])
		}
	})

	t.Run("item_match_apply", func(t *testing.T) {
		if asin == "" {
			t.Skip("item_match produced no asin")
		}
		t.Cleanup(func() { restoreBook(t, book) })

		out := call(t, "item_match_apply", map[string]any{
			"item": book, "provider": "audible", "asin": asin, "override_details": true,
		})
		if updated, _ := out["updated"].(bool); !updated {
			t.Errorf("item_match_apply reported no update: %v", out)
		}

		item := call(t, "item_get", map[string]any{"item": book})
		if got, _ := item["asin"].(string); got != asin {
			t.Errorf("asin = %q, want the matched %q", got, asin)
		}
	})
}

// item_cover_search returns urls; item_cover_edit has the server fetch one.
func TestItemCoverSearchAndSet(t *testing.T) {
	requireProviders(t)

	const book = "Foundation and Empire"
	var coverURL string

	t.Run("item_cover_search", func(t *testing.T) {
		out := call(t, "item_cover_search", map[string]any{
			"item": book, "provider": "audible",
			"title": "Foundation and Empire", "author": "Isaac Asimov",
		})
		if out["item"] != book {
			t.Errorf("item = %v", out["item"])
		}

		covers := strs(t, out["covers"], "covers")
		if len(covers) == 0 {
			t.Fatal("no cover candidates returned")
		}
		if !strings.HasPrefix(covers[0], "http") {
			t.Errorf("cover = %q, want a url", covers[0])
		}
		coverURL = covers[0]
	})

	t.Run("item_cover_edit", func(t *testing.T) {
		if coverURL == "" {
			t.Skip("item_cover_search produced no url")
		}
		t.Cleanup(func() { call(t, "item_cover_edit", map[string]any{"item": book}) })

		out := call(t, "item_cover_edit", map[string]any{"item": book, "url": coverURL})
		if done, _ := out["done"].(bool); !done {
			t.Errorf("item_cover_edit done = %v", out["done"])
		}

		// the fixtures start with no cover, so this is visible on the item
		if item := call(t, "item_get", map[string]any{"item": book}); item["no_cover"] != nil {
			t.Errorf("no_cover = %v, want the cover to have been set", item["no_cover"])
		}
	})
}

// the from_asin path pulls Audible's chapter list, as opposed to the explicit
// list covered in item_test.go.
func TestItemChaptersSetFromASIN(t *testing.T) {
	requireProviders(t)

	const book = "Foundation"

	match := call(t, "item_match", map[string]any{
		"item": book, "provider": "audible", "title": "Foundation", "author": "Isaac Asimov",
	})
	candidates := rows(t, match["candidates"], "candidates")
	if len(candidates) == 0 {
		t.Skip("no audible candidate to take an asin from")
	}
	asin, _ := candidates[0]["asin"].(string)
	if asin == "" {
		t.Skip("no asin on the candidate")
	}

	t.Cleanup(func() {
		call(t, "item_chapters_set", map[string]any{
			"item":     book,
			"chapters": []any{map[string]any{"title": "Chapter One", "start": 0}},
		})
	})

	out := call(t, "item_chapters_set", map[string]any{"item": book, "from_asin": asin, "region": "us"})
	n := num(t, out["chapters"], "chapters")
	if n < 2 {
		t.Fatalf("chapters = %d, want Audible's list", n)
	}

	got := rows(t, call(t, "item_get", map[string]any{"item": book, "chapters": true})["chapter_list"], "chapter_list")
	if len(got) != n {
		t.Errorf("read back %d chapters, want %d", len(got), n)
	}
	if title, _ := got[0]["title"].(string); title == "" {
		t.Error("first chapter has no title")
	}
}

// author_match fills in asin, description and photo from Audnexus.
func TestAuthorMatch(t *testing.T) {
	requireProviders(t)

	t.Cleanup(func() {
		call(t, "author_edit", map[string]any{
			"library": "Fiction", "author": "Isaac Asimov", "description": " ", "asin": " ",
		})
	})

	out := call(t, "author_match", map[string]any{
		"library": "Fiction", "author": "Isaac Asimov", "query": "Isaac Asimov", "region": "us",
	})
	if updated, _ := out["updated"].(bool); !updated {
		t.Errorf("author_match reported no update: %v", out)
	}

	author, ok := out["author"].(map[string]any)
	if !ok {
		t.Fatalf("author = %T", out["author"])
	}
	if name, _ := author["name"].(string); name != "Isaac Asimov" {
		t.Errorf("name = %q", name)
	}
	if asin, _ := author["asin"].(string); asin == "" {
		t.Error("author_match set no asin")
	}
	if desc, _ := author["description"].(string); desc == "" {
		t.Error("author_match set no description")
	}
}

// author_image_set hands the server a url to download.
func TestAuthorImageSet(t *testing.T) {
	requireProviders(t)

	// a cover url is a real image the proxy already has to serve; reusing one
	// keeps this test from depending on a second provider
	covers := strs(t, call(t, "item_cover_search", map[string]any{
		"item": "Leviathan Wakes", "provider": "audible",
		"title": "Leviathan Wakes", "author": "James S. A. Corey",
	})["covers"], "covers")
	if len(covers) == 0 {
		t.Skip("no image url available to set")
	}

	out := call(t, "author_image_set", map[string]any{
		"library": "Fiction", "author": "James S. A. Corey", "url": covers[0],
	})
	author, ok := out["author"].(map[string]any)
	if !ok {
		t.Fatalf("author = %T", out["author"])
	}
	if name, _ := author["name"].(string); name != "James S. A. Corey" {
		t.Errorf("name = %q", name)
	}
	if img, _ := author["has_image"].(bool); !img {
		t.Errorf("has_image = %v, want the downloaded image to be reported: %v", author["has_image"], author)
	}
}

func TestAuthorImageSetValidation(t *testing.T) {
	requireProviders(t)

	if msg := callErr(t, "author_image_set", map[string]any{
		"library": "Fiction", "author": "Isaac Asimov", "url": "  ",
	}); msg == "" {
		t.Error("an empty url should be refused")
	}
}

// the whole podcast flow: find one on iTunes, subscribe, read its feed, change
// its download settings, and queue an episode.
//
// Every step after the subscribe addresses the podcast by the id podcast_add
// returned, not by title: the folder name we pass does not become the title -
// that comes from the feed, and here it is the same show the fixtures already
// contain, so a name would be ambiguous.
func TestPodcastProviderFlow(t *testing.T) {
	requireProviders(t)

	const show = "Well There's Your Problem"
	var feedURL, podcastID string

	t.Run("podcast_search", func(t *testing.T) {
		out := call(t, "podcast_search", map[string]any{"query": show, "limit": 5})

		results := rows(t, out["results"], "results")
		if len(results) == 0 {
			t.Fatalf("iTunes returned nothing for %q", show)
		}
		for _, r := range results {
			if u, _ := r["feed_url"].(string); strings.HasPrefix(u, "http") {
				feedURL = u
				break
			}
		}
		if feedURL == "" {
			t.Fatalf("no feed url among the results: %v", results)
		}
	})

	t.Run("podcast_add", func(t *testing.T) {
		if feedURL == "" {
			t.Skip("podcast_search produced no feed url")
		}

		out := call(t, "podcast_add", map[string]any{
			"feed_url": feedURL, "library": "Podcasts", "folder": "Provider Test Podcast",
			"auto_download": false,
		})
		podcastID, _ = out["id"].(string)
		if podcastID == "" {
			t.Fatalf("podcast_add returned no id: %v", out)
		}
		if episodes := num(t, out["feed_episodes"], "feed_episodes"); episodes == 0 {
			t.Error("the feed reported no episodes")
		}
		if title, _ := out["title"].(string); title == "" {
			t.Error("the new podcast has no title")
		}
	})

	// registered after podcast_add's subtest so it runs once the flow is done,
	// keeping the Podcasts library at the two shows the rest of the suite counts on
	t.Cleanup(func() {
		if podcastID != "" {
			call(t, "item_delete", map[string]any{"item": podcastID, "delete_files": true})
		}
	})

	t.Run("podcast_feed_episodes", func(t *testing.T) {
		if podcastID == "" {
			t.Skip("nothing subscribed")
		}
		out := call(t, "podcast_feed_episodes", map[string]any{"item": podcastID, "limit": 5})

		episodes := rows(t, out["episodes"], "episodes")
		if len(episodes) == 0 {
			t.Fatal("the feed listed no episodes")
		}
		if title, _ := episodes[0]["title"].(string); title == "" {
			t.Error("the first feed episode has no title")
		}
		if total := num(t, out["total"], "total"); total == 0 {
			t.Error("total = 0")
		}
	})

	t.Run("podcast_settings", func(t *testing.T) {
		if podcastID == "" {
			t.Skip("nothing subscribed")
		}
		out := call(t, "podcast_settings", map[string]any{
			"item": podcastID, "auto_download": true,
			"schedule": "0 * * * *", "keep_episodes": 3, "new_per_check": 1,
		})
		if done, _ := out["done"].(bool); !done {
			t.Errorf("podcast_settings done = %v", out["done"])
		}
	})

	t.Run("podcast_episode_download", func(t *testing.T) {
		if podcastID == "" {
			t.Skip("nothing subscribed")
		}
		// the download runs in the background; the tool returns once queued,
		// which is what is asserted here
		out := call(t, "podcast_episode_download", map[string]any{
			"item": podcastID, "indexes": []any{0},
		})
		if queued := strs(t, out["queued"], "queued"); len(queued) != 1 {
			t.Errorf("queued = %v, want one episode", queued)
		}
	})

	t.Run("podcast_check_new", func(t *testing.T) {
		if podcastID == "" {
			t.Skip("nothing subscribed")
		}
		out := call(t, "podcast_check_new", map[string]any{"item": podcastID, "limit": 1})
		if out["podcast"] == nil {
			t.Errorf("podcast_check_new returned no podcast: %v", out)
		}
		// queued may legitimately be empty when the episode is already held
		rows(t, out["queued"], "queued")
	})
}

// library_match_all kicks off a background quick-match over a whole library.
//
// The test waits for the task to finish rather than just asserting it started:
// the match looks up every unmatched book against the provider, and if those
// requests are still in flight when a recording run ends they never reach a
// cassette, and the next replay misses them.
func TestLibraryMatchAll(t *testing.T) {
	requireProviders(t)

	t.Cleanup(func() {
		for _, b := range books {
			switch b.Author {
			case "William Manchester", "Robert Evans", "Smedley D. Butler":
				restoreBook(t, b.Title)
			}
		}
	})

	out := call(t, "library_match_all", map[string]any{"library": "Non-Fiction"})
	if started, _ := out["started"].(string); started == "" {
		t.Errorf("library_match_all started = %v", out["started"])
	}

	if !waitForTasks(t) {
		t.Error("the match-all task did not finish; its provider calls may be unrecorded")
	}
}

// waitForTasks polls until nothing is running, so a background job's provider
// traffic has all been made before the run ends.
func waitForTasks(t *testing.T) bool {
	t.Helper()

	for range 30 {
		var running int
		for _, task := range rows(t, call(t, "server_tasks", nil)["tasks"], "tasks") {
			if status, _ := task["status"].(string); status == "running" {
				running++
			}
		}
		if running == 0 {
			// the match issues its lookups as it goes; give the last ones a
			// moment to land before the proxy stops accepting them
			time.Sleep(2 * time.Second)

			return true
		}
		time.Sleep(time.Second)
	}

	return false
}
