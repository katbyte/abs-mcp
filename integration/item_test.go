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

	// a batch fetch by id, which me_bookmarks uses to name its items
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
