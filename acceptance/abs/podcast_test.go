//go:build acceptance

package abs_test

import (
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// The episode endpoints have no book equivalent: a podcast is one item whose
// episodes are nested inside it, addressed by their own ids.
func TestEpisodeMethods(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := podcastLibrary(t)

	shows := firstItems(ctx, id, 10)
	if len(shows) == 0 {
		t.Fatal("no podcasts scanned")
	}
	show := whole(ctx, shows[0].Id)
	if len(show.Media.Episodes) == 0 {
		t.Fatalf("%s has no episodes in the whole item", show.Title())
	}
	episode := show.Media.Episodes[0]

	// the update answers with the show, so the episode is read back on its own
	if err := errorOf(client.PodcastUpdateEpisode(ctx, show.Id, episode.Id, abs.PodcastUpdateEpisodeRequest{Title: "SDK Episode"})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = errorOf(client.PodcastUpdateEpisode(afterwards(t), show.Id, episode.Id, abs.PodcastUpdateEpisodeRequest{Title: episode.Title}))
	})
	if after := must(client.PodcastGetEpisode(ctx, show.Id, episode.Id)); after.Title != "SDK Episode" {
		t.Errorf("after update title = %q, want SDK Episode", after.Title)
	}
}

// Clearing a show's download queue, which is empty: the read sweep leaves it alone, since it changes what is queued.
func TestClearDownloadQueue(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := podcastLibrary(t)

	shows := firstItems(ctx, id, 1)
	if len(shows) == 0 {
		t.Fatal("no podcasts scanned")
	}
	if err := errorOf(client.PodcastClearEpisodeDownloadQueue(ctx, shows[0].Id)); err != nil {
		t.Errorf("clearing the download queue: %v", err)
	}
}

// Deleting an episode is destructive, so it runs against a copy the test makes
// and then restores by rescanning: the audio file is still on disk.
func TestDeleteEpisode(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := podcastLibrary(t)

	show := whole(ctx, firstItems(ctx, id, 10)[0].Id)
	if len(show.Media.Episodes) < 2 {
		t.Skip("need two episodes to delete one")
	}

	if err := errorOf(client.PodcastRemoveEpisode(ctx, show.Id, show.Media.Episodes[0].Id)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errorOf(client.LibraryScan(afterwards(t), id)) })

	if after := whole(ctx, show.Id); len(after.Media.Episodes) != len(show.Media.Episodes)-1 {
		t.Errorf("episodes = %d, want one fewer than %d", len(after.Media.Episodes), len(show.Media.Episodes))
	}
}
