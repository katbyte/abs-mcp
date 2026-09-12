//go:build integration

package integration

import (
	"github.com/katbyte/abs-mcp/lib/abs"
	"testing"
)

func TestItemMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]

	// a batch fetch by id, which user_bookmarks uses to name its items
	batch := must(client.ItemsBatch(ctx, []string{item.ID}))
	if len(batch) != 1 || batch[0].ID != item.ID {
		t.Errorf("ItemsBatch = %d items", len(batch))
	}

	// chapters round-trip
	updated, err := client.SetChapters(ctx, item.ID, []abs.Chapter{
		{ID: 0, Title: "One", Start: 0, End: 0.5},
		{ID: 1, Title: "Two", Start: 0.5, End: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Error("SetChapters reported no change")
	}
	if got := must(client.Item(ctx, item.ID)); len(got.Media.Chapters) != 2 {
		t.Errorf("chapters = %d, want 2", len(got.Media.Chapters))
	}

	// removing a cover that was never set must still succeed
	if err := client.RemoveCover(ctx, item.ID); err != nil {
		t.Errorf("RemoveCover: %v", err)
	}
	// and an item with no cover reports that rather than a decode failure
	if _, _, err := client.CoverSize(ctx, item.ID); err == nil {
		t.Error("CoverSize on a coverless item should fail")
	}

	if _, err := client.ScanItem(ctx, item.ID); err != nil {
		t.Errorf("ScanItem: %v", err)
	}
}

// The batch and single-item forms that the lifecycle tests do not reach.
func TestBatchAndTrackMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	items := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 3})).Results
	ids := []string{items[0].ID, items[1].ID}

	if err := client.BatchScan(ctx, ids); err != nil {
		t.Errorf("BatchScan: %v", err)
	}
	if err := client.BatchEmbedMetadata(ctx, ids, true); err != nil {
		t.Errorf("BatchEmbedMetadata: %v", err)
	}

	// the tags the server would write into the audio files, without writing
	if meta := must(client.MetadataObject(ctx, items[0].ID)); len(meta) == 0 {
		t.Error("MetadataObject returned nothing")
	}

	// reordering needs the inodes the server reports for the audio files
	full := must(client.Item(ctx, items[0].ID))
	order := make([]abs.TrackOrder, 0, len(full.LibraryFiles))
	for _, f := range full.LibraryFiles {
		if f.FileType == "audio" {
			order = append(order, abs.TrackOrder{Ino: f.Ino})
		}
	}
	if len(order) == 0 {
		t.Skip("no audio files to reorder")
	}
	if _, err := client.UpdateTracks(ctx, items[0].ID, order); err != nil {
		t.Errorf("UpdateTracks: %v", err)
	}

	// m4b encoding is a background task; that it starts is what this asserts
	if err := client.EncodeM4B(ctx, items[2].ID, "", "", ""); err != nil {
		t.Logf("EncodeM4B: %v", err) // needs ffmpeg in the container; not fatal
	} else if err := client.CancelEncodeM4B(ctx, items[2].ID); err != nil {
		t.Logf("CancelEncodeM4B: %v", err)
	}
}
