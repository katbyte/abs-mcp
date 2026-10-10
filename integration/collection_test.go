//go:build integration

package integration

import (
	"slices"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// --- collections and playlists ------------------------------------------

func TestCollectionsAndPlaylists(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 2})).Results
	ids := []string{items[0].ID, items[1].ID}

	col := must(client.CreateCollection(ctx, id, "SDK Collection", "", ids))
	t.Cleanup(func() { _ = client.DeleteCollection(t.Context(), col.ID) })
	if len(col.Books) != 2 {
		t.Errorf("collection has %d books, want 2", len(col.Books))
	}

	cols := must(client.Collections(ctx, id))
	if len(cols) == 0 {
		t.Error("Collections returned nothing")
	}

	pl := must(client.CreatePlaylist(ctx, id, "SDK Playlist", "", []abs.PlaylistEntry{{LibraryItemID: ids[0]}}))
	t.Cleanup(func() { _ = client.DeletePlaylist(t.Context(), pl.ID) })
	if len(pl.Items) != 1 {
		t.Errorf("playlist has %d entries, want 1", len(pl.Items))
	}

	fromCol := must(client.CreatePlaylistFromCollection(ctx, col.ID))
	t.Cleanup(func() { _ = client.DeletePlaylist(t.Context(), fromCol.ID) })
	if len(fromCol.Items) != 2 {
		t.Errorf("playlist from collection has %d entries, want 2", len(fromCol.Items))
	}
}

func TestCollectionMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 3})).Results
	ids := []string{items[0].ID, items[1].ID}

	col := must(client.CreateCollection(ctx, id, "SDK Methods", "first", ids))
	t.Cleanup(func() { _ = client.DeleteCollection(t.Context(), col.ID) })

	got := must(client.Collection(ctx, col.ID))
	if got.Name != "SDK Methods" || len(got.Books) != 2 {
		t.Errorf("Collection = %q with %d books", got.Name, len(got.Books))
	}

	desc := "second"
	updated := must(client.UpdateCollection(ctx, col.ID, nil, &desc))
	if updated.Description != desc {
		t.Errorf("description = %q", updated.Description)
	}

	added := must(client.AddToCollection(ctx, col.ID, []string{items[2].ID}))
	if len(added.Books) != 3 {
		t.Errorf("after add = %d books, want 3", len(added.Books))
	}
	removed := must(client.RemoveFromCollection(ctx, col.ID, []string{items[2].ID}))
	if len(removed.Books) != 2 {
		t.Errorf("after remove = %d books, want 2", len(removed.Books))
	}
}

func TestPlaylistMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 2})).Results
	pl := must(client.CreatePlaylist(ctx, id, "SDK Methods", "", []abs.PlaylistEntry{{LibraryItemID: items[0].ID}}))
	t.Cleanup(func() { _ = client.DeletePlaylist(t.Context(), pl.ID) })

	got := must(client.Playlist(ctx, pl.ID))
	if got.Name != "SDK Methods" {
		t.Errorf("Playlist name = %q", got.Name)
	}
	if all := must(client.Playlists(ctx, id)); len(all) == 0 {
		t.Error("Playlists returned nothing")
	}

	desc := "changed"
	if updated := must(client.UpdatePlaylist(ctx, pl.ID, nil, &desc)); updated.Description != desc {
		t.Errorf("description = %q", updated.Description)
	}

	added := must(client.AddToPlaylist(ctx, pl.ID, []abs.PlaylistEntry{{LibraryItemID: items[1].ID}}))
	if len(added.Items) != 2 {
		t.Errorf("after add = %d entries, want 2", len(added.Items))
	}
	removed := must(client.RemoveFromPlaylist(ctx, pl.ID, []abs.PlaylistEntry{{LibraryItemID: items[1].ID}}))
	if len(removed.Items) != 1 {
		t.Errorf("after remove = %d entries, want 1", len(removed.Items))
	}
}

// A collection and a playlist are lists in an order, and each takes a new
// one: what was added last can be put first.
func TestCollectionAndPlaylistOrder(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 3})).Results
	first, second, third := items[0].ID, items[1].ID, items[2].ID

	col := must(client.CreateCollection(ctx, id, "SDK Order", "", []string{first, second, third}))
	t.Cleanup(func() { _ = client.DeleteCollection(t.Context(), col.ID) })
	if _, err := client.OrderCollection(ctx, col.ID, []string{third, first, second}); err != nil {
		t.Fatalf("OrderCollection: %v", err)
	}
	books := make([]string, 0, 3)
	for _, b := range must(client.Collection(ctx, col.ID)).Books {
		books = append(books, b.ID)
	}
	if !slices.Equal(books, []string{third, first, second}) {
		t.Errorf("the collection read back = %v, want the order it was given", books)
	}

	pl := must(client.CreatePlaylist(ctx, id, "SDK Order Playlist", "", []abs.PlaylistEntry{{LibraryItemID: first}, {LibraryItemID: second}, {LibraryItemID: third}}))
	t.Cleanup(func() { _ = client.DeletePlaylist(t.Context(), pl.ID) })
	if _, err := client.OrderPlaylist(ctx, pl.ID, []abs.PlaylistEntry{{LibraryItemID: third}, {LibraryItemID: first}, {LibraryItemID: second}}); err != nil {
		t.Fatalf("OrderPlaylist: %v", err)
	}
	entries := make([]string, 0, 3)
	for _, e := range must(client.Playlist(ctx, pl.ID)).Items {
		entries = append(entries, e.LibraryItemID)
	}
	if !slices.Equal(entries, []string{third, first, second}) {
		t.Errorf("the playlist read back = %v, want the order it was given", entries)
	}
}

// The single-item forms, alongside the batch forms above.
func TestSingleItemCollectionAndPlaylist(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 2})).Results

	col := must(client.CreateCollection(ctx, id, "SDK Single", "", []string{items[0].ID}))
	t.Cleanup(func() { _ = client.DeleteCollection(t.Context(), col.ID) })

	added := must(client.AddBookToCollection(ctx, col.ID, items[1].ID))
	if len(added.Books) != 2 {
		t.Errorf("after AddBookToCollection = %d books, want 2", len(added.Books))
	}
	removed := must(client.RemoveBookFromCollection(ctx, col.ID, items[1].ID))
	if len(removed.Books) != 1 {
		t.Errorf("after RemoveBookFromCollection = %d books, want 1", len(removed.Books))
	}

	pl := must(client.CreatePlaylist(ctx, id, "SDK Single Playlist", "", []abs.PlaylistEntry{{LibraryItemID: items[0].ID}}))
	t.Cleanup(func() { _ = client.DeletePlaylist(t.Context(), pl.ID) })

	plAdded := must(client.AddItemToPlaylist(ctx, pl.ID, abs.PlaylistEntry{LibraryItemID: items[1].ID}))
	if len(plAdded.Items) != 2 {
		t.Errorf("after AddItemToPlaylist = %d entries, want 2", len(plAdded.Items))
	}
	plRemoved := must(client.RemoveItemFromPlaylist(ctx, pl.ID, items[1].ID, ""))
	if len(plRemoved.Items) != 1 {
		t.Errorf("after RemoveItemFromPlaylist = %d entries, want 1", len(plRemoved.Items))
	}
}
