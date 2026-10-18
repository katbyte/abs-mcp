//go:build acceptance

package abs_test

import (
	"slices"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// --- collections and playlists ------------------------------------------

func TestCollectionsAndPlaylists(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := firstItems(ctx, id, 2)

	col := must(client.CollectionCreate(ctx, abs.CollectionCreateRequest{LibraryId: id, Name: "SDK Collection", Books: bookIDs(items...)}))
	t.Cleanup(func() { _ = errorOf(client.CollectionDelete(afterwards(t), col.Id)) })
	if len(col.Books) != 2 {
		t.Errorf("collection has %d books, want 2", len(col.Books))
	}
	listed := func(c abs.Collection) bool { return c.Id == col.Id }
	if !slices.ContainsFunc(must(client.CollectionFindAll(ctx)).Collections, listed) {
		t.Error("the new collection is not listed")
	}

	pl := must(client.PlaylistCreate(ctx, abs.PlaylistCreateRequest{LibraryId: id, Name: "SDK Playlist", Items: entries(items[0])}))
	t.Cleanup(func() { _ = errorOf(client.PlaylistDelete(afterwards(t), pl.Id)) })
	if len(pl.Items) != 1 {
		t.Errorf("playlist has %d entries, want 1", len(pl.Items))
	}

	fromCol := must(client.PlaylistCreateFromCollection(ctx, col.Id))
	t.Cleanup(func() { _ = errorOf(client.PlaylistDelete(afterwards(t), fromCol.Id)) })
	if len(fromCol.Items) != 2 {
		t.Errorf("playlist from collection has %d entries, want 2", len(fromCol.Items))
	}
}

func TestCollectionMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := firstItems(ctx, id, 3)

	col := must(client.CollectionCreate(ctx, abs.CollectionCreateRequest{LibraryId: id, Name: "SDK Methods", Description: "first", Books: bookIDs(items[:2]...)}))
	t.Cleanup(func() { _ = errorOf(client.CollectionDelete(afterwards(t), col.Id)) })

	if got := must(client.CollectionFindOne(ctx, col.Id)); got.Name != "SDK Methods" || len(got.Books) != 2 {
		t.Errorf("collection = %q with %d books", got.Name, len(got.Books))
	}

	if updated := must(client.CollectionUpdate(ctx, col.Id, abs.CollectionUpdateRequest{Description: "second"})); updated.Description != "second" {
		t.Errorf("description = %q", updated.Description)
	}

	if added := must(client.CollectionAddBatch(ctx, col.Id, abs.CollectionAddBatchRequest{Books: bookIDs(items[2])})); len(added.Books) != 3 {
		t.Errorf("after add = %d books, want 3", len(added.Books))
	}
	if removed := must(client.CollectionRemoveBatch(ctx, col.Id, abs.CollectionRemoveBatchRequest{Books: bookIDs(items[2])})); len(removed.Books) != 2 {
		t.Errorf("after remove = %d books, want 2", len(removed.Books))
	}
}

func TestPlaylistMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := firstItems(ctx, id, 2)
	pl := must(client.PlaylistCreate(ctx, abs.PlaylistCreateRequest{LibraryId: id, Name: "SDK Methods", Items: entries(items[0])}))
	t.Cleanup(func() { _ = errorOf(client.PlaylistDelete(afterwards(t), pl.Id)) })

	if got := must(client.PlaylistFindOne(ctx, pl.Id)); got.Name != "SDK Methods" {
		t.Errorf("playlist name = %q", got.Name)
	}
	listed := func(p abs.Playlist) bool { return p.Id == pl.Id }
	if !slices.ContainsFunc(must(client.PlaylistFindAllForUser(ctx)).Playlists, listed) {
		t.Error("the new playlist is not listed")
	}

	if updated := must(client.PlaylistUpdate(ctx, pl.Id, abs.PlaylistUpdateRequest{Description: "changed"})); updated.Description != "changed" {
		t.Errorf("description = %q", updated.Description)
	}

	if added := must(client.PlaylistAddBatch(ctx, pl.Id, abs.PlaylistAddBatchRequest{Items: entries(items[1])})); len(added.Items) != 2 {
		t.Errorf("after add = %d entries, want 2", len(added.Items))
	}
	if removed := must(client.PlaylistRemoveBatch(ctx, pl.Id, abs.PlaylistRemoveBatchRequest{Items: entries(items[1])})); len(removed.Items) != 1 {
		t.Errorf("after remove = %d entries, want 1", len(removed.Items))
	}
}

// A collection and a playlist are lists in an order, and each takes a new
// one: what was added last can be put first.
func TestCollectionAndPlaylistOrder(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := firstItems(ctx, id, 3)
	first, second, third := items[0], items[1], items[2]
	reordered := []abs.LibraryItemID{third.Id, first.Id, second.Id}

	col := must(client.CollectionCreate(ctx, abs.CollectionCreateRequest{LibraryId: id, Name: "SDK Order", Books: bookIDs(first, second, third)}))
	t.Cleanup(func() { _ = errorOf(client.CollectionDelete(afterwards(t), col.Id)) })
	if err := errorOf(client.CollectionUpdate(ctx, col.Id, abs.CollectionUpdateRequest{Books: bookIDs(third, first, second)})); err != nil {
		t.Fatalf("ordering the collection: %v", err)
	}
	books := make([]abs.LibraryItemID, 0, 3)
	for _, b := range must(client.CollectionFindOne(ctx, col.Id)).Books {
		books = append(books, b.Id)
	}
	if !slices.Equal(books, reordered) {
		t.Errorf("the collection read back = %v, want the order it was given", books)
	}

	pl := must(client.PlaylistCreate(ctx, abs.PlaylistCreateRequest{LibraryId: id, Name: "SDK Order Playlist", Items: entries(first, second, third)}))
	t.Cleanup(func() { _ = errorOf(client.PlaylistDelete(afterwards(t), pl.Id)) })
	if err := errorOf(client.PlaylistUpdate(ctx, pl.Id, abs.PlaylistUpdateRequest{Items: entries(third, first, second)})); err != nil {
		t.Fatalf("ordering the playlist: %v", err)
	}
	listed := make([]abs.LibraryItemID, 0, 3)
	for _, e := range must(client.PlaylistFindOne(ctx, pl.Id)).Items {
		listed = append(listed, e.LibraryItemId)
	}
	if !slices.Equal(listed, reordered) {
		t.Errorf("the playlist read back = %v, want the order it was given", listed)
	}
}

// The single-item forms, alongside the batch forms above.
func TestSingleItemCollectionAndPlaylist(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := firstItems(ctx, id, 2)

	col := must(client.CollectionCreate(ctx, abs.CollectionCreateRequest{LibraryId: id, Name: "SDK Single", Books: bookIDs(items[0])}))
	t.Cleanup(func() { _ = errorOf(client.CollectionDelete(afterwards(t), col.Id)) })

	if added := must(client.CollectionAddBook(ctx, col.Id, abs.CollectionAddBookRequest{Id: string(items[1].Id)})); len(added.Books) != 2 {
		t.Errorf("after adding a book = %d books, want 2", len(added.Books))
	}
	if removed := must(client.CollectionRemoveBook(ctx, col.Id, items[1].Id)); len(removed.Books) != 1 {
		t.Errorf("after removing a book = %d books, want 1", len(removed.Books))
	}

	pl := must(client.PlaylistCreate(ctx, abs.PlaylistCreateRequest{LibraryId: id, Name: "SDK Single Playlist", Items: entries(items[0])}))
	t.Cleanup(func() { _ = errorOf(client.PlaylistDelete(afterwards(t), pl.Id)) })

	if added := must(client.PlaylistAddItem(ctx, pl.Id, abs.PlaylistEntry{LibraryItemId: items[1].Id})); len(added.Items) != 2 {
		t.Errorf("after adding an item = %d entries, want 2", len(added.Items))
	}
	if removed := must(client.PlaylistRemoveItem2(ctx, pl.Id, items[1].Id)); len(removed.Items) != 1 {
		t.Errorf("after removing an item = %d entries, want 1", len(removed.Items))
	}
}

// bookIDs are items' ids as a collection takes them.
func bookIDs(items ...abs.LibraryItemMinified) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, string(it.Id))
	}

	return out
}

// entries are items as a playlist takes them.
func entries(items ...abs.LibraryItemMinified) []abs.PlaylistEntry {
	out := make([]abs.PlaylistEntry, 0, len(items))
	for _, it := range items {
		out = append(out, abs.PlaylistEntry{LibraryItemId: it.Id})
	}

	return out
}
