//go:build acceptance

package abs_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
	pclient "github.com/katbyte/pandorest/client"
)

// --- error handling -----------------------------------------------------

func TestBadToken(t *testing.T) {
	if client == nil {
		t.Skip("not live")
	}

	bad, err := connect("not-a-real-key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = bad.MeGetCurrentUser(t.Context())
	if code := pclient.StatusCode(err); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Errorf("error for a bad key = %v, want a 401 or 403", err)
	}
}

// The task list with the queue beside it, which the server sends only when asked: an embed waiting its turn is in the queue and not among
// the tasks. An embed of a one-second file is over too soon to be sure of seeing it pending, so what is asserted is that the answer decodes,
// and that an embed that was started comes to an end.
func TestEmbedPending(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]
	settled := func(what string) {
		t.Helper()

		deadline := time.Now().Add(30 * time.Second)
		for {
			pending, err := embedPending(ctx, item.Id)
			if err != nil {
				t.Fatalf("reading the tasks %s: %v", what, err)
			}
			if !pending {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the embed is still pending %s, after 30s", what)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	settled("before an embed")
	if err := errorOf(client.ToolsEmbedAudioFileMetadata(ctx, item.Id, abs.ToolsEmbedAudioFileMetadataOptions{ForceEmbedChapters: "1"})); err != nil {
		t.Fatalf("embedding the metadata: %v", err)
	}
	settled("after an embed")

	if pending, err := embedPending(ctx, "no-such-item"); err != nil || pending {
		t.Errorf("an item that does not exist = %v, %v; want not pending", pending, err)
	}
}

// embedPending is whether the server is tagging an item's files, or has the item waiting to be: it lists a running embed as a task and a
// waiting one in its queue.
func embedPending(ctx context.Context, id abs.LibraryItemID) (bool, error) {
	got, err := record(client.MiscGetTasks(ctx, abs.MiscGetTasksOptions{Include: "queue"}))
	if err != nil {
		return false, err
	}
	for _, t := range got.Tasks {
		if t.Action == "embed-metadata" && !t.IsFinished && t.Data != nil && t.Data.LibraryItemId == id {
			return true, nil
		}
	}
	if got.QueuedTaskData != nil {
		for _, q := range got.QueuedTaskData.EmbedMetadata {
			if q.LibraryItemId == id {
				return true, nil
			}
		}
	}

	return false, nil
}
