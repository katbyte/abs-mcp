//go:build integration

package integration

import (
	"github.com/katbyte/abs-mcp/lib/abs"
	"testing"
)

// --- progress and bookmarks ---------------------------------------------

func TestProgressAndBookmarks(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]

	half, at, duration := 0.5, 0.5, 1.0
	if err := client.SetProgress(ctx, item.ID, "", abs.ProgressUpdate{
		Progress: &half, CurrentTime: &at, Duration: &duration,
	}); err != nil {
		t.Fatal(err)
	}
	p := must(client.Progress(ctx, item.ID, ""))
	if p == nil {
		t.Fatal("no progress after setting it")
	}
	if p.Progress < 0.4 || p.Progress > 0.6 {
		t.Errorf("progress = %v, want ~0.5", p.Progress)
	}

	inProgress := must(client.ItemsInProgress(ctx, 10))
	if len(inProgress) == 0 {
		t.Error("ItemsInProgress is empty after setting progress")
	}

	// HideFromContinueListening had no caller before this suite
	if err := client.HideFromContinueListening(ctx, p.ID); err != nil {
		t.Errorf("HideFromContinueListening: %v", err)
	}

	// bookmarks come back wrapped in an object, unlike the other /api/me routes
	if _, err := client.CreateBookmark(ctx, item.ID, 0.25, "SDK bookmark"); err != nil {
		t.Fatal(err)
	}
	bookmarks := must(client.Bookmarks(ctx))
	if len(bookmarks) == 0 {
		t.Fatal("Bookmarks returned nothing after creating one")
	}
	if bookmarks[0].Title == "" {
		t.Errorf("bookmark did not decode: %+v", bookmarks[0])
	}
	if err := client.DeleteBookmark(ctx, item.ID, 0.25); err != nil {
		t.Error(err)
	}

	if err := client.RemoveProgress(ctx, p.ID); err != nil {
		t.Error(err)
	}
}

func TestBookmarkUpdateAndGenreRename(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	if _, err := client.CreateBookmark(ctx, item.ID, 0.4, "first"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.DeleteBookmark(t.Context(), item.ID, 0.4) })

	renamed, err := client.UpdateBookmark(ctx, item.ID, 0.4, "second")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "second" {
		t.Errorf("bookmark title = %q", renamed.Title)
	}

	genres := must(client.Genres(ctx))
	if len(genres) == 0 {
		t.Skip("no genres set")
	}
	n, err := client.RenameGenre(ctx, genres[0], "SDK Genre Renamed")
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("RenameGenre reported no items updated")
	}
	if _, err := client.RenameGenre(ctx, "SDK Genre Renamed", genres[0]); err != nil {
		t.Error(err)
	}
}
