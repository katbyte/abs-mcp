//go:build acceptance

package abs_test

import (
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

func TestItemMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]

	// a batch fetch by id, which user_bookmarks uses to name its items
	if batch := must(client.LibraryItemBatchGet(ctx, abs.LibraryItemBatchGetRequest{LibraryItemIds: []abs.LibraryItemID{item.Id}})).LibraryItems; len(batch) != 1 || batch[0].Id != item.Id {
		t.Errorf("the batch fetch answered %d items", len(batch))
	}

	// chapters round-trip
	chapters := []abs.Chapter{{Id: 0, Title: "One", Start: 0, End: 0.5}, {Id: 1, Title: "Two", Start: 0.5, End: 1}}
	if updated := must(client.LibraryItemUpdateMediaChapters(ctx, item.Id, abs.LibraryItemUpdateMediaChaptersRequest{Chapters: chapters})); !updated.Updated {
		t.Error("setting the chapters reported no change")
	}
	if got := whole(ctx, item.Id).Media.Chapters; len(got) != 2 {
		t.Errorf("chapters = %d, want 2", len(got))
	}

	// removing a cover that was never set must still succeed
	if err := errorOf(client.LibraryItemRemoveCover(ctx, item.Id)); err != nil {
		t.Errorf("removing the cover: %v", err)
	}
	// and an item with no cover says so rather than sending something that is not a picture
	if _, _, err := coverSize(ctx, item.Id); !abs.IsNotFound(err) {
		t.Errorf("the cover of an item with none = %v, want a 404", err)
	}

	if err := errorOf(client.LibraryItemScan(ctx, item.Id)); err != nil {
		t.Errorf("scanning the item: %v", err)
	}
}

// The batch and single-item forms that the lifecycle tests do not reach.
func TestBatchAndTrackMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := firstItems(ctx, id, 2)
	ids := []abs.LibraryItemID{items[0].Id, items[1].Id}

	if err := errorOf(client.LibraryItemBatchScan(ctx, abs.LibraryItemBatchScanRequest{LibraryItemIds: ids})); err != nil {
		t.Errorf("scanning a batch: %v", err)
	}
	if err := errorOf(client.ToolsBatchEmbedMetadata(ctx, abs.ToolsBatchEmbedMetadataRequest{LibraryItemIds: ids}, abs.ToolsBatchEmbedMetadataOptions{Backup: "1"})); err != nil {
		t.Errorf("embedding the metadata of a batch: %v", err)
	}

	// reordering needs the inodes the server reports for the audio files
	order := []abs.LibraryItemUpdateTracksRequestOrderedFileData{}
	for _, f := range whole(ctx, items[0].Id).LibraryFiles {
		if f.FileType == "audio" {
			order = append(order, abs.LibraryItemUpdateTracksRequestOrderedFileData{Ino: f.Ino})
		}
	}
	if len(order) == 0 {
		t.Skip("no audio files to reorder")
	}
	if err := errorOf(client.LibraryItemUpdateTracks(ctx, items[0].Id, abs.LibraryItemUpdateTracksRequest{OrderedFileData: order})); err != nil {
		t.Errorf("ordering the tracks: %v", err)
	}
}
