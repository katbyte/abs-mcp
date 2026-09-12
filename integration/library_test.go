//go:build integration

package integration

import (
	"github.com/katbyte/abs-mcp/lib/abs"
	"strconv"
	"testing"
	"time"
)

// --- the library lifecycle ----------------------------------------------

// TestLibraryLifecycle runs first (by name order within the file is not
// guaranteed, so it seeds lazily via library()) and covers create, read,
// update and scan.
func TestLibraryLifecycle(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	lib := must(client.Library(ctx, id))
	if lib.Name != "SDK Fiction" || lib.MediaType != "book" {
		t.Errorf("library = %s/%s", lib.Name, lib.MediaType)
	}
	if len(lib.Folders) != 1 || lib.Folders[0].FullPath != "/fiction" {
		t.Errorf("folders = %+v", lib.Folders)
	}

	// an empty folder id on create collides on the server's primary key, which
	// is why Folder.ID is omitempty; a second library proves it stays fixed
	second := must(client.CreateLibrary(ctx, abs.LibraryCreate{
		Name: "SDK Second", MediaType: "book", Folders: []abs.Folder{{FullPath: "/nonfiction"}},
	}))
	t.Cleanup(func() { _ = client.DeleteLibrary(t.Context(), second.ID) })
	if second.ID == id {
		t.Error("the second library reused the first library's id")
	}

	name := "SDK Fiction Renamed"
	updated := must(client.UpdateLibrary(ctx, id, abs.LibraryUpdate{Name: &name}))
	if updated.Name != name {
		t.Errorf("after update name = %q", updated.Name)
	}
	original := "SDK Fiction"
	must(client.UpdateLibrary(ctx, id, abs.LibraryUpdate{Name: &original}))

	libs := must(client.Libraries(ctx))
	if len(libs) < 2 {
		t.Errorf("libraries = %d, want at least the two created here", len(libs))
	}
}

// library creates and scans the fixture library once, and returns its id.
func library(t *testing.T) string {
	t.Helper()
	ctx := t.Context()

	if fiction != "" {
		return fiction
	}

	for _, l := range must(client.Libraries(ctx)) {
		if l.Name == "SDK Fiction" {
			fiction = l.ID
			return fiction
		}
	}

	lib := must(client.CreateLibrary(ctx, abs.LibraryCreate{
		Name: "SDK Fiction", MediaType: "book", Provider: "audible",
		Folders: []abs.Folder{{FullPath: "/fiction"}},
	}))
	fiction = lib.ID

	// creating a library does not scan it
	if err := client.ScanLibrary(ctx, fiction, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		res, err := client.Items(ctx, fiction, abs.ItemsOptions{Limit: 1, Minified: true})
		if err == nil && res.Total == 7 {
			seedMetadata(t)
			return fiction
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("the scan never reached 7 items")

	return fiction
}

// podcastLibrary creates and scans the podcast library once. The episode
// endpoints have no book equivalent, so without it a chunk of the client is
// unreachable.
func podcastLibrary(t *testing.T) string {
	t.Helper()
	ctx := t.Context()

	if podcasts != "" {
		return podcasts
	}
	for _, l := range must(client.Libraries(ctx)) {
		if l.Name == "SDK Podcasts" {
			podcasts = l.ID
			return podcasts
		}
	}

	lib := must(client.CreateLibrary(ctx, abs.LibraryCreate{
		Name: "SDK Podcasts", MediaType: "podcast", Provider: "itunes",
		Folders: []abs.Folder{{FullPath: "/podcasts"}},
	}))
	podcasts = lib.ID

	if err := client.ScanLibrary(ctx, podcasts, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		res, err := client.Items(ctx, podcasts, abs.ItemsOptions{Limit: 1, Minified: true})
		if err == nil && res.Total == 2 {
			return podcasts
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("the podcast scan never reached 2 shows")

	return podcasts
}

// seedMetadata gives the scanned items narrators and series, which the scan
// cannot infer from a bare folder name, so the catalogue endpoints have
// something to return.
func seedMetadata(t *testing.T) {
	t.Helper()
	ctx := t.Context()

	items := must(client.Items(ctx, fiction, abs.ItemsOptions{Limit: 100})).Results
	for i := range items {
		upd := abs.MediaUpdate{
			Metadata: &abs.MetadataUpdate{Narrators: []string{"SDK Reader"}},
			Tags:     []string{"sdk"},
		}
		// the first three make a series with a hole in it
		if i < 3 {
			upd.Metadata.Series = []abs.SeriesRef{{Name: "SDK Series", Sequence: strconv.Itoa(i*2 + 1)}}
		}
		if _, err := client.UpdateMedia(ctx, items[i].ID, upd); err != nil {
			t.Fatalf("seeding %s: %v", items[i].Title(), err)
		}
	}
}

func TestItems(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	res := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 100}))
	if res.Total != 7 {
		t.Fatalf("total = %d, want 7", res.Total)
	}
	for i := range res.Results {
		it := &res.Results[i]
		if it.ID == "" || it.Title() == "" {
			t.Errorf("item %d did not decode: %+v", i, it)
		}
		if it.RelPath == "" {
			t.Errorf("%s has no relPath", it.Title())
		}
	}

	// the expanded single-item shape is the only one carrying chapters,
	// audio files and series sequences
	first := must(client.Item(ctx, res.Results[0].ID))
	if first.Media.Duration <= 0 {
		t.Errorf("%s has no duration; was it probed?", first.Title())
	}
	if len(first.Media.AudioFiles) == 0 {
		t.Errorf("%s has no audio files in the expanded shape", first.Title())
	}

	// ItemsAll pages through the whole library
	var seen int
	if err := client.ItemsAll(ctx, id, abs.ItemsOptions{}, func(items []abs.Item) bool {
		seen += len(items)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 7 {
		t.Errorf("ItemsAll saw %d items, want 7", seen)
	}
}

func TestUpdateMediaAndBatch(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 3})).Results
	if len(items) < 3 {
		t.Fatal("need three items")
	}

	year := "1999"
	updated, err := client.UpdateMedia(ctx, items[0].ID, abs.MediaUpdate{
		Metadata: &abs.MetadataUpdate{PublishedYear: &year},
		Tags:     []string{"sdk-test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Error("UpdateMedia reported no change")
	}

	got := must(client.Item(ctx, items[0].ID))
	if string(got.Media.Metadata.PublishedYear) != year {
		t.Errorf("publishedYear = %q, want %q", got.Media.Metadata.PublishedYear, year)
	}

	// BatchUpdate: the same change across several items in one call
	genre := []string{"SDK Genre"}
	n, err := client.BatchUpdate(ctx, []abs.BatchMediaUpdate{
		{ID: items[1].ID, MediaPayload: abs.MediaUpdate{Metadata: &abs.MetadataUpdate{Genres: genre}}},
		{ID: items[2].ID, MediaPayload: abs.MediaUpdate{Metadata: &abs.MetadataUpdate{Genres: genre}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("BatchUpdate updated %d, want 2", n)
	}
}
