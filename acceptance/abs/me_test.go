//go:build acceptance

package abs_test

import (
	"slices"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// --- progress and bookmarks ---------------------------------------------

func TestProgressAndBookmarks(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]

	if err := errorOf(client.MeCreateUpdateMediaProgress2(ctx, item.Id, abs.ProgressUpdatePayload{Progress: new(0.5), CurrentTime: new(0.5), Duration: new(1.0)})); err != nil {
		t.Fatal(err)
	}
	p := must(client.MeGetMediaProgress2(ctx, item.Id))
	if p.Progress < 0.4 || p.Progress > 0.6 {
		t.Errorf("progress = %v, want ~0.5", p.Progress)
	}

	inProgress := func(it abs.LibraryItemMinified) bool { return it.Id == item.Id }
	if !slices.ContainsFunc(must(client.MeGetAllLibraryItemsInProgress(ctx, abs.MeGetAllLibraryItemsInProgressOptions{Limit: new(10)})).LibraryItems, inProgress) {
		t.Error("the items in progress do not hold the one just set")
	}

	if err := errorOf(client.MeRemoveItemFromContinueListening(ctx, p.Id)); err != nil {
		t.Errorf("taking the book off the shelf where listening continues: %v", err)
	}

	// bookmarks come back wrapped in an object, unlike the other /api/me routes
	if err := errorOf(client.MeCreateBookmark(ctx, item.Id, abs.MeCreateBookmarkRequest{Time: 0.25, Title: "SDK bookmark"})); err != nil {
		t.Fatal(err)
	}
	bookmarks := must(client.MeGetAllBookmarks(ctx)).Bookmarks
	if len(bookmarks) == 0 {
		t.Fatal("no bookmarks listed after making one")
	}
	if bookmarks[0].Title == "" {
		t.Errorf("bookmark did not decode: %+v", bookmarks[0])
	}
	if err := errorOf(client.MeRemoveBookmark(ctx, item.Id, "0.25")); err != nil {
		t.Error(err)
	}

	if err := errorOf(client.MeRemoveMediaProgress(ctx, p.Id)); err != nil {
		t.Error(err)
	}
}

// Progress carried from one record to another keeps when the book was
// finished and when it was last listened to, which are otherwise stamped with
// the moment of the call. The position goes first and the finish after it: in
// one call the server takes a new position on a finished book for a restart.
func TestProgressKeepsTheDatesItIsGiven(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]
	finished, listened := abs.UnixMilli{Time: time.UnixMilli(1700000000000)}, abs.UnixMilli{Time: time.UnixMilli(1700000500000)}
	if err := errorOf(client.MeCreateUpdateMediaProgress2(ctx, item.Id, abs.ProgressUpdatePayload{CurrentTime: new(0.5), Duration: new(1.0)})); err != nil {
		t.Fatal(err)
	}
	if err := errorOf(client.MeCreateUpdateMediaProgress2(ctx, item.Id, abs.ProgressUpdatePayload{IsFinished: new(true), FinishedAt: finished, LastUpdate: listened})); err != nil {
		t.Fatal(err)
	}
	p := must(client.MeGetMediaProgress2(ctx, item.Id))
	t.Cleanup(func() { _ = errorOf(client.MeRemoveMediaProgress(afterwards(t), p.Id)) })
	if !p.IsFinished || !p.FinishedAt.Equal(finished.Time) || !p.LastUpdate.Equal(listened.Time) || p.CurrentTime != 0.5 {
		t.Errorf("progress = finished %v at %v, last update %v, position %v; want finished at %v, last update %v, position 0.5", p.IsFinished, p.FinishedAt, p.LastUpdate, p.CurrentTime, finished, listened)
	}
}

func TestBookmarkUpdateAndGenreRename(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]
	if err := errorOf(client.MeCreateBookmark(ctx, item.Id, abs.MeCreateBookmarkRequest{Time: 0.4, Title: "first"})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errorOf(client.MeRemoveBookmark(afterwards(t), item.Id, "0.4")) })

	if renamed := must(client.MeUpdateBookmark(ctx, item.Id, abs.MeUpdateBookmarkRequest{Time: 0.4, Title: "second"})); renamed.Title != "second" {
		t.Errorf("bookmark title = %q", renamed.Title)
	}

	genres := must(client.MiscGetAllGenres(ctx)).Genres
	if len(genres) == 0 {
		t.Skip("no genres set")
	}
	renamed, err := record(client.MiscRenameGenre(ctx, abs.MiscRenameGenreRequest{Genre: genres[0], NewGenre: "SDK Genre Renamed"}))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.NumItemsUpdated == 0 {
		t.Error("the genre rename reported no items updated")
	}
	if err := errorOf(client.MiscRenameGenre(ctx, abs.MiscRenameGenreRequest{Genre: "SDK Genre Renamed", NewGenre: genres[0]})); err != nil {
		t.Error(err)
	}
}

// The progress and bookmark forms the lifecycle test does not reach.
func TestProgressAndBookmarkExtras(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := firstItems(ctx, id, 2)

	// several items in one call, rather than one call per book
	batch := []abs.ProgressUpdatePayload{{LibraryItemId: items[0].Id, IsFinished: new(true)}, {LibraryItemId: items[1].Id, IsFinished: new(true)}}
	if err := errorOf(client.MeBatchUpdateMediaProgress(ctx, batch)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, it := range items {
			if p, err := record(client.MeGetMediaProgress2(afterwards(t), it.Id)); err == nil {
				_ = errorOf(client.MeRemoveMediaProgress(afterwards(t), p.Id))
			}
		}
	})

	if all := must(client.MeGetAllMediaProgress(ctx)).MediaProgress; len(all) < 2 {
		t.Errorf("%d progress records, want at least the two just set", len(all))
	}

	if err := errorOf(client.MeCreateBookmark(ctx, items[0].Id, abs.MeCreateBookmarkRequest{Time: 0.3, Title: "extras"})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errorOf(client.MeRemoveBookmark(afterwards(t), items[0].Id, "0.3")) })
	if bs := must(client.MeGetBookmarksForLibraryItem(ctx, items[0].Id)).Bookmarks; len(bs) == 0 {
		t.Error("an item with a bookmark lists none")
	}

	if series := must(client.LibraryGetAllSeriesForLibrary(ctx, id, abs.LibraryGetAllSeriesForLibraryOptions{Limit: new(1)})).Results; len(series) > 0 {
		if err := errorOf(client.MeRemoveSeriesFromContinueListening(ctx, series[0].Id)); err != nil {
			t.Errorf("taking the series off the shelf where listening continues: %v", err)
		}
		if err := errorOf(client.MeReaddSeriesFromContinueListening(ctx, series[0].Id)); err != nil {
			t.Errorf("putting the series back on the shelf: %v", err)
		}
	}
}
