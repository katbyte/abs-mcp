//go:build integration

package integration

import (
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// The episode endpoints have no book equivalent: a podcast is one item whose
// episodes are nested inside it, addressed by their own ids.
func TestEpisodeMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := podcastLibrary(t)

	shows := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 10})).Results
	if len(shows) == 0 {
		t.Fatal("no podcasts scanned")
	}
	show := must(client.Item(ctx, shows[0].ID))
	if len(show.Media.Episodes) == 0 {
		t.Fatalf("%s has no episodes in the expanded shape", show.Title())
	}
	episode := show.Media.Episodes[0]

	got := must(client.Episode(ctx, show.ID, episode.ID))
	if got.ID != episode.ID {
		t.Errorf("Episode(%s) = %s", episode.ID, got.ID)
	}

	title := "SDK Episode"
	// UpdateEpisode answers with the parent item, so read the episode back
	if _, err := client.UpdateEpisode(ctx, show.ID, episode.ID, abs.EpisodeUpdate{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if after := must(client.Episode(ctx, show.ID, episode.ID)); after.Title != title {
		t.Errorf("after update title = %q, want %q", after.Title, title)
	}
	t.Cleanup(func() {
		original := episode.Title
		_, _ = client.UpdateEpisode(t.Context(), show.ID, episode.ID, abs.EpisodeUpdate{Title: &original})
	})
}

// The download queue is empty, which still has to decode as an empty list
// rather than null.
func TestDownloadQueueMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := podcastLibrary(t)

	shows := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results
	if len(shows) == 0 {
		t.Fatal("no podcasts scanned")
	}

	if _, err := client.PodcastDownloads(ctx, shows[0].ID); err != nil {
		t.Errorf("PodcastDownloads: %v", err)
	}
	if _, _, err := client.EpisodeDownloads(ctx, id); err != nil {
		t.Errorf("EpisodeDownloads: %v", err)
	}
	if err := client.ClearDownloadQueue(ctx, shows[0].ID); err != nil {
		t.Errorf("ClearDownloadQueue: %v", err)
	}

	recent := must(client.RecentEpisodes(ctx, id, 10, 0))
	if len(recent) == 0 {
		t.Error("RecentEpisodes returned nothing for a library with episodes")
	}
}

// Deleting an episode is destructive, so it runs against a copy the test makes
// and then restores by rescanning: the audio file is still on disk.
func TestDeleteEpisode(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := podcastLibrary(t)

	shows := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 10})).Results
	show := must(client.Item(ctx, shows[0].ID))
	if len(show.Media.Episodes) < 2 {
		t.Skip("need two episodes to delete one")
	}
	episode := show.Media.Episodes[0]

	if err := client.DeleteEpisode(ctx, show.ID, episode.ID, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.ScanLibrary(t.Context(), id, false)
	})

	after := must(client.Item(ctx, show.ID))
	if len(after.Media.Episodes) != len(show.Media.Episodes)-1 {
		t.Errorf("episodes = %d, want one fewer than %d", len(after.Media.Episodes), len(show.Media.Episodes))
	}
}
