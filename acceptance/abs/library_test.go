//go:build acceptance

package abs_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// --- the library lifecycle ----------------------------------------------

// TestLibraryLifecycle covers create, read, update and scan. Tests run in no promised order, so the fixture library is made by whichever asks
// for it first, through library().
func TestLibraryLifecycle(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	lib := must(client.LibraryFindOne(ctx, id))
	if lib.Name != "SDK Fiction" || lib.MediaType != "book" {
		t.Errorf("library = %s/%s", lib.Name, lib.MediaType)
	}
	if len(lib.Folders) != 1 || lib.Folders[0].FullPath != "/fiction" {
		t.Errorf("folders = %+v", lib.Folders)
	}

	// an empty folder id on create collides on the server's primary key, so a folder is sent without one; a second library proves it
	second := must(client.LibraryCreate(ctx, abs.LibraryCreateRequest{Name: "SDK Second", MediaType: "book", Folders: []abs.LibraryFolder{{FullPath: "/nonfiction"}}}))
	t.Cleanup(func() { _ = errorOf(client.LibraryDelete(afterwards(t), second.Id)) })
	if second.Id == id {
		t.Error("the second library reused the first library's id")
	}

	updated := must(client.LibraryUpdate(ctx, id, abs.LibraryUpdateRequest{Name: "SDK Fiction Renamed"}))
	if updated.Name != "SDK Fiction Renamed" {
		t.Errorf("after update name = %q", updated.Name)
	}
	must(client.LibraryUpdate(ctx, id, abs.LibraryUpdateRequest{Name: "SDK Fiction"}))

	if libs := must(client.LibraryFindAll(ctx)).Libraries; len(libs) < 2 {
		t.Errorf("libraries = %d, want at least the two created here", len(libs))
	}
}

// library creates and scans the fixture library once, and returns its id.
func library(t *testing.T) abs.LibraryID {
	t.Helper()
	ctx := t.Context()

	if fiction != "" {
		return fiction
	}

	for _, l := range must(client.LibraryFindAll(ctx)).Libraries {
		if l.Name == "SDK Fiction" {
			fiction = l.Id
			return fiction
		}
	}

	fiction = must(client.LibraryCreate(ctx, abs.LibraryCreateRequest{Name: "SDK Fiction", MediaType: "book", Provider: "audible", Folders: []abs.LibraryFolder{{FullPath: "/fiction"}}})).Id

	// creating a library does not scan it
	if err := errorOf(client.LibraryScan(ctx, fiction)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		res, err := record(client.LibraryGetLibraryItems(ctx, fiction, abs.LibraryGetLibraryItemsOptions{Limit: new(100), Minified: "1"}))
		if err == nil && res.Total == 7 {
			seedMetadata(t)
			return fiction
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("the scan never reached 7 items; found: " + scanned(t, fiction))

	return fiction
}

// podcastLibrary creates and scans the podcast library once. The episode
// endpoints have no book equivalent, so without it a chunk of the client is
// unreachable.
func podcastLibrary(t *testing.T) abs.LibraryID {
	t.Helper()
	ctx := t.Context()

	if podcasts != "" {
		return podcasts
	}
	for _, l := range must(client.LibraryFindAll(ctx)).Libraries {
		if l.Name == "SDK Podcasts" {
			podcasts = l.Id
			return podcasts
		}
	}

	podcasts = must(client.LibraryCreate(ctx, abs.LibraryCreateRequest{Name: "SDK Podcasts", MediaType: "podcast", Provider: "itunes", Folders: []abs.LibraryFolder{{FullPath: "/podcasts"}}})).Id

	if err := errorOf(client.LibraryScan(ctx, podcasts)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		res, err := record(client.LibraryGetLibraryItems(ctx, podcasts, abs.LibraryGetLibraryItemsOptions{Limit: new(100), Minified: "1"}))
		if err == nil && res.Total == 2 {
			return podcasts
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("the podcast scan never reached 2 shows; found: " + scanned(t, podcasts))

	return podcasts
}

// seedMetadata gives the scanned items narrators and series, which the scan
// cannot infer from a bare folder name, so the catalogue endpoints have
// something to return.
func seedMetadata(t *testing.T) {
	t.Helper()
	ctx := t.Context()

	for i, it := range firstItems(ctx, fiction, 100) {
		upd := abs.MediaUpdate{Metadata: &abs.MediaUpdateMetadata{Narrators: []string{"SDK Reader"}}, Tags: []string{"sdk"}}
		// the first three make a series with a hole in it
		if i < 3 {
			upd.Metadata.Series = []abs.SeriesMinimal{{Name: "SDK Series", Sequence: strconv.Itoa(i*2 + 1)}}
		}
		if err := errorOf(client.LibraryItemUpdateMedia(ctx, it.Id, upd)); err != nil {
			t.Fatalf("seeding %s: %v", it.Title(), err)
		}
	}
}

// scanned lists what a library actually holds, so a scan that comes up short
// names the titles it did find rather than only the count.
func scanned(t *testing.T, lib abs.LibraryID) string {
	t.Helper()

	res, err := record(client.LibraryGetLibraryItems(t.Context(), lib, abs.LibraryGetLibraryItemsOptions{Limit: new(100), Minified: "1"}))
	if err != nil {
		return "could not list items: " + err.Error()
	}
	titles := make([]string, 0, len(res.Results))
	for _, it := range res.Results {
		titles = append(titles, strconv.Quote(it.Title()))
	}

	return fmt.Sprintf("%d of them: %s", res.Total, strings.Join(titles, ", "))
}

func TestUpdateMediaAndBatch(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := firstItems(ctx, id, 3)
	if len(items) < 3 {
		t.Fatal("need three items")
	}

	updated := must(client.LibraryItemUpdateMedia(ctx, items[0].Id, abs.MediaUpdate{Metadata: &abs.MediaUpdateMetadata{PublishedYear: new("1999")}, Tags: []string{"sdk-test"}}))
	if !updated.Updated {
		t.Error("the media update reported no change")
	}
	if got := whole(ctx, items[0].Id).Media.Metadata.PublishedYear; got != "1999" {
		t.Errorf("publishedYear = %q, want 1999", got)
	}

	// the same change across several items in one call
	genre := &abs.MediaUpdate{Metadata: &abs.MediaUpdateMetadata{Genres: []string{"SDK Genre"}}}
	batch := must(client.LibraryItemBatchUpdate(ctx, []abs.LibraryItemBatchUpdateRequest{{Id: string(items[1].Id), MediaPayload: genre}, {Id: string(items[2].Id), MediaPayload: genre}}))
	if batch.Updates != 2 {
		t.Errorf("the batch update changed %d, want 2", batch.Updates)
	}
}

// Putting the libraries in a new order, the last first, and back again.
func TestLibraryReorder(t *testing.T) {
	ctx := skipUnlessLive(t)
	library(t)
	podcastLibrary(t)

	libs := must(client.LibraryFindAll(ctx)).Libraries
	ids := make([]abs.LibraryID, 0, len(libs))
	for _, l := range libs {
		ids = append(ids, l.Id)
	}
	reorder := func(ctx context.Context, ids []abs.LibraryID) ([]abs.LibraryID, error) {
		order := make([]abs.LibraryReorderRequest, 0, len(ids))
		for i, id := range ids {
			order = append(order, abs.LibraryReorderRequest{Id: string(id), NewOrder: float64(i + 1)})
		}
		got, err := record(client.LibraryReorder(ctx, order))
		answered := make([]abs.LibraryID, 0, len(got.Libraries))
		for _, l := range got.Libraries {
			answered = append(answered, l.Id)
		}

		return answered, err
	}

	moved := append([]abs.LibraryID{ids[len(ids)-1]}, ids[:len(ids)-1]...)
	got, err := reorder(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = reorder(afterwards(t), ids) })
	if !slices.Equal(got, moved) {
		t.Errorf("the libraries after reordering = %v, want %v", got, moved)
	}
}

// Taking away the metadata files the server wrote beside the audio, which runs on a library of its own rather than the shared fixture.
func TestRemoveLibraryMetadata(t *testing.T) {
	ctx := skipUnlessLive(t)

	scratch := must(client.LibraryCreate(ctx, abs.LibraryCreateRequest{Name: "SDK Metadata Scratch", MediaType: "book", Folders: []abs.LibraryFolder{{FullPath: "/nonfiction"}}}))
	t.Cleanup(func() { _ = errorOf(client.LibraryDelete(afterwards(t), scratch.Id)) })

	if err := errorOf(client.LibraryRemoveAllMetadataFiles(ctx, scratch.Id, abs.LibraryRemoveAllMetadataFilesOptions{Ext: "json"})); err != nil {
		t.Errorf("removing the metadata files: %v", err)
	}
}
