//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// the libraries exist because library_create made them during setup.
func TestLibraryCreateAndList(t *testing.T) {
	out := call(t, "library_list", nil)

	seen := map[string]string{}
	for _, row := range rows(t, out["libraries"], "libraries") {
		name, _ := row["name"].(string)
		mediaType, _ := row["media_type"].(string)
		seen[name] = mediaType
	}
	for _, want := range libraries {
		if seen[want.Name] != want.MediaType {
			t.Errorf("library %q media type = %q, want %q (have %v)", want.Name, seen[want.Name], want.MediaType, seen)
		}
	}
}

func TestLibraryCreateValidation(t *testing.T) {
	if msg := callErr(t, "library_create", map[string]any{"name": "", "folders": []any{"/fiction"}}); msg == "" {
		t.Error("an empty name should be refused")
	}
	if msg := callErr(t, "library_create", map[string]any{"name": "No Folders", "folders": []any{}}); msg == "" {
		t.Error("no folders should be refused")
	}
	if msg := callErr(t, "library_create", map[string]any{"name": "Bad Type", "media_type": "film", "folders": []any{"/fiction"}}); msg == "" {
		t.Error("an unknown media_type should be refused")
	}
}

func TestLibraryGet(t *testing.T) {
	out := call(t, "library_get", map[string]any{"library": "Fiction"})

	if items := num(t, out["items"], "items"); items != 7 {
		t.Errorf("items = %d, want 7", items)
	}
	if series := num(t, out["series"], "series"); series != 3 {
		t.Errorf("series = %d, want 3 (Foundation, Otherland, The Expanse)", series)
	}
	if authors := num(t, out["authors"], "authors"); authors != 3 {
		t.Errorf("authors = %d, want 3", authors)
	}
}

func TestLibrarySearch(t *testing.T) {
	out := call(t, "library_search", map[string]any{"library": "Fiction", "query": "foundation"})

	if items := rows(t, out["items"], "items"); len(items) == 0 {
		t.Fatalf("library_search found nothing: %v", out)
	}

	// a search that matches a narrator rather than a title
	out = call(t, "library_search", map[string]any{"library": "Fiction", "query": "Jefferson Mays"})
	if narrators := rows(t, out["narrators"], "narrators"); len(narrators) == 0 {
		t.Errorf("no narrator match for Jefferson Mays: %v", out)
	}
}

func TestLibraryItemsFilters(t *testing.T) {
	all := call(t, "library_items", map[string]any{"library": "Fiction"})
	if total := num(t, all["total"], "total"); total != 7 {
		t.Errorf("Fiction total = %d, want 7", total)
	}

	// the filter groups the server understands, encoded as <group>.<base64>
	for _, tc := range []struct {
		name, library, filter string
		want                  int
	}{
		{"by author", "Fiction", "authors:Isaac Asimov", 3},
		{"by narrator", "Fiction", "narrators:Jefferson Mays", 2},
		{"by tag", "Non-Fiction", "tags:history", 2},
		{"by genre", "Fiction", "genres:Science Fiction", 7},
		{"by publisher", "Fiction", "publishers:Orbit", 2},
		{"missing cover", "Fiction", "missing:cover", 7},
		{"not started", "Fiction", "progress:not-started", 7},
	} {
		out := call(t, "library_items", map[string]any{"library": tc.library, "filter": tc.filter})
		if got := num(t, out["total"], "total"); got != tc.want {
			t.Errorf("%s (%s): total = %d, want %d", tc.name, tc.filter, got, tc.want)
		}
	}
}

func TestLibraryItemsPaging(t *testing.T) {
	first := call(t, "library_items", map[string]any{"library": "Fiction", "limit": 3})
	if items := rows(t, first["items"], "items"); len(items) != 3 {
		t.Errorf("page 1 returned %d items, want 3", len(items))
	}

	second := call(t, "library_items", map[string]any{"library": "Fiction", "limit": 3, "offset": 3})
	if off := num(t, second["offset"], "offset"); off != 3 {
		t.Errorf("offset = %d, want 3", off)
	}
	if num(t, second["total"], "total") != 7 {
		t.Error("total should not change with paging")
	}
}

func TestLibraryRecent(t *testing.T) {
	out := call(t, "library_recent", map[string]any{"library": "Fiction", "limit": 3})

	if items := rows(t, out["items"], "items"); len(items) != 3 {
		t.Errorf("library_recent returned %d items, want 3", len(items))
	}
}

// library_filters is the per-library counterpart to server_tags.
func TestLibraryFilters(t *testing.T) {
	out := call(t, "library_filters", map[string]any{"library": "Fiction"})

	// Authors and series come from the server's relational tables and are
	// always current. Tags, narrators, publishers and languages come from a
	// filter-data cache the server does not invalidate on an edit - not even
	// on a rescan - so they lag behind item_edit and are not asserted here.
	// server_tags reads the live tag and genre endpoints instead.
	if authors := rows(t, out["authors"], "authors"); len(authors) != 3 {
		t.Errorf("authors = %d, want 3", len(authors))
	}
	if series := rows(t, out["series"], "series"); len(series) != 3 {
		t.Errorf("series = %d, want 3", len(series))
	}
}

// the statistics library_get absorbed from the former library_stats: the same
// LibraryStats call it already made, no longer thrown away.
func TestLibraryGetStatistics(t *testing.T) {
	out := call(t, "library_get", map[string]any{"library": "Fiction"})

	if items := num(t, out["items"], "items"); items != 7 {
		t.Errorf("items = %d, want 7", items)
	}
	if top := rows(t, out["top_authors"], "top_authors"); len(top) == 0 {
		t.Error("no top_authors")
	}
	if rows(t, out["top_genres"], "top_genres") == nil {
		t.Error("no top_genres")
	}
	if longest := rows(t, out["longest"], "longest"); len(longest) == 0 || num(t, longest[0]["duration_s"], "duration_s") < 1 {
		t.Errorf("longest = %v, want them with a length in seconds", longest)
	}
	if largest := rows(t, out["largest"], "largest"); len(largest) == 0 || num(t, largest[0]["size"], "size") <= 0 {
		t.Errorf("largest = %v, want them with a size in bytes", largest)
	}
	// seven books of a second or so: seconds and bytes, where whole
	// gigabytes were 0
	if d := num(t, out["total_duration_s"], "total_duration_s"); d < 7 {
		t.Errorf("total_duration_s = %d, want the seven books' seconds", d)
	}
	if num(t, out["total_size"], "total_size") <= 0 {
		t.Errorf("total_size = %v, want bytes", out["total_size"])
	}
}

func TestLibraryNameResolution(t *testing.T) {
	if msg := callErr(t, "library_items", map[string]any{"library": "Nonexistent"}); msg == "" {
		t.Error("an unknown library name should be an error naming the candidates")
	}
}

// Last in this file, because it changes the Fiction item count: drop a book
// folder in, prove library_scan picks it up, then take it away again.
func TestLibraryScanPicksUpANewBook(t *testing.T) {
	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}

	audio, err := os.ReadFile(filepath.Join(data, "fiction", "Isaac Asimov", "Foundation", "01.mp3"))
	if err != nil {
		t.Fatalf("reading a fixture to copy: %v", err)
	}
	dir := filepath.Join(data, "fiction", "Ursula K. Le Guin", "The Dispossessed")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01.mp3"), audio, 0o666); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// a rescan flags a vanished item as missing rather than removing it,
		// so the record has to go explicitly for the count to come back down
		call(t, "item_delete", map[string]any{"confirm": true, "item": "The Dispossessed"})
		// the server usually drops an author its last book leaves, but not
		// always by the time this runs, and a second run would find one more
		// author than the fixtures hold
		if _, err := invoke("author_delete", map[string]any{"library": "Fiction", "author": "Ursula K. Le Guin"}); err != nil && !strings.Contains(err.Error(), "no author named") {
			t.Errorf("removing the added author: %v", err)
		}
		if err := os.RemoveAll(filepath.Dir(dir)); err != nil {
			t.Errorf("removing the added book: %v", err)
		}
		if err := waitForItems("Fiction", 7); err != nil {
			t.Errorf("Fiction did not return to 7 items: %v", err)
		}
	})

	call(t, "library_scan", map[string]any{"library": "Fiction"})
	if err := waitForItems("Fiction", 8); err != nil {
		t.Fatalf("library_scan did not pick up the new book: %v", err)
	}

	if item := call(t, "item_get", map[string]any{"item": "The Dispossessed"}); item["title"] != "The Dispossessed" {
		t.Errorf("item_get title = %v", item["title"])
	}
}

func TestLibraryEdit(t *testing.T) {
	// put back what it was, not a guess: a later test that matches in this
	// library would otherwise depend on the order the tests ran in
	was, _ := call(t, "library_get", map[string]any{"library": "Non-Fiction"})["provider"].(string)
	out := call(t, "library_edit", map[string]any{"library": "Non-Fiction", "provider": "audible"})
	lib, ok := out["library"].(map[string]any)
	if !ok {
		t.Fatalf("library = %T", out["library"])
	}
	if lib["provider"] != "audible" {
		t.Errorf("provider = %v, want audible", lib["provider"])
	}
	if was != "" && was != "audible" {
		t.Cleanup(func() {
			call(t, "library_edit", map[string]any{"library": "Non-Fiction", "provider": was})
		})
	}

	if msg := callErr(t, "library_edit", map[string]any{"library": "Non-Fiction"}); msg == "" {
		t.Error("an edit with no fields should be refused")
	}
}

// library_issues_remove deletes the records of items whose folder has gone.
// Last in this file: it needs a broken item, which means disturbing the
// library and putting it back.
func TestLibraryRemoveIssues(t *testing.T) {
	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}

	audio, err := os.ReadFile(filepath.Join(data, "nonfiction", "Robert Evans", "A Brief History of Vice", "01.mp3"))
	if err != nil {
		t.Fatalf("reading a fixture to copy: %v", err)
	}
	dir := filepath.Join(data, "nonfiction", "Doomed Author", "Doomed Book")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01.mp3"), audio, 0o666); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Dir(dir))
		if err := waitForItems("Non-Fiction", 3); err != nil {
			t.Errorf("Non-Fiction did not return to 3 items: %v", err)
		}
	})

	call(t, "library_scan", map[string]any{"library": "Non-Fiction"})
	if err := waitForItems("Non-Fiction", 4); err != nil {
		t.Fatalf("the added book was not picked up: %v", err)
	}

	// take the folder away and rescan: the record survives, flagged missing
	if err := os.RemoveAll(filepath.Dir(dir)); err != nil {
		t.Fatal(err)
	}
	call(t, "library_scan", map[string]any{"library": "Non-Fiction"})

	var flagged bool
	for range 30 {
		out := call(t, "audit_issues", map[string]any{"library": "Non-Fiction"})
		if num(t, out["total_findings"], "total_findings") > 0 {
			flagged = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !flagged {
		t.Fatal("audit_issues never reported the removed folder")
	}

	// without confirm it names what it would delete and deletes nothing
	out := call(t, "library_issues_remove", map[string]any{"library": "Non-Fiction"})
	if found, removed := num(t, out["found"], "found"), num(t, out["removed"], "removed"); found < 1 || removed != 0 {
		t.Errorf("preview found %d removed %d, want the broken item found and nothing removed", found, removed)
	}
	// by path: the copied file carries the tags an earlier embed wrote into
	// it, so the record is titled after the book it was copied from
	var named bool
	for _, row := range rows(t, out["items"], "items") {
		if row["path"] == "Doomed Author/Doomed Book" {
			named = true
		}
	}
	if !named {
		t.Errorf("preview items = %v, want the Doomed Book folder", out["items"])
	}
	if again := call(t, "audit_issues", map[string]any{"library": "Non-Fiction"}); num(t, again["total_findings"], "total_findings") < 1 {
		t.Error("the preview removed the broken record")
	}

	out = call(t, "library_issues_remove", map[string]any{"library": "Non-Fiction", "confirm": true})
	if removed := num(t, out["removed"], "removed"); removed < 1 {
		t.Errorf("removed = %d, want at least the broken item", removed)
	}
	if err := waitForItems("Non-Fiction", 3); err != nil {
		t.Errorf("the broken record was not removed: %v", err)
	}
}
