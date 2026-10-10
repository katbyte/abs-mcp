//go:build integration

package acceptance

import (
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
)

func TestPodcastEpisodes(t *testing.T) {
	for _, show := range podcasts {
		out := suite.Call(t, "podcast_episodes", map[string]any{"item": show})
		if out["podcast"] != show {
			t.Errorf("podcast = %v, want %v", out["podcast"], show)
		}
		if total := acc.Num(t, out["total"], "total"); total != 2 {
			t.Errorf("%s total = %d, want 2", show, total)
		}
		if eps := acc.Rows(t, out["episodes"], "episodes"); len(eps) != 2 {
			t.Errorf("%s episodes = %d, want 2", show, len(eps))
		}
	}
}

func TestPodcastEpisodeGetAndEdit(t *testing.T) {
	episodes := acc.Rows(t, suite.Call(t, "podcast_episodes", map[string]any{"item": "Behind the Bastards"})["episodes"], "episodes")
	if len(episodes) == 0 {
		t.Fatal("no episodes to work with")
	}
	id := acc.Str(episodes[0]["id"])
	original := acc.Str(episodes[0]["title"])

	got := suite.Call(t, "podcast_episode_get", map[string]any{"item": "Behind the Bastards", "episode": id})
	if got["title"] != original {
		t.Errorf("episode title = %v, want %v", got["title"], original)
	}

	suite.Call(t, "podcast_episode_edit", map[string]any{
		"item": "Behind the Bastards", "episode": id,
		"title": "Edited Episode", "season": "1", "number": "1",
	})
	t.Cleanup(func() {
		suite.Call(t, "podcast_episode_edit", map[string]any{
			"item": "Behind the Bastards", "episode": id, "title": original,
		})
	})

	after := suite.Call(t, "podcast_episode_get", map[string]any{"item": "Behind the Bastards", "episode": id})
	if after["title"] != "Edited Episode" {
		t.Errorf("after edit title = %v", after["title"])
	}
}

// podcast_episodes with no podcast named is the library-wide view that used to
// be podcast_recent: the same question asked of everything rather than of one
// show, and a different endpoint underneath.
func TestPodcastEpisodesAcrossLibrary(t *testing.T) {
	out := suite.Call(t, "podcast_episodes", map[string]any{"library": "Podcasts"})

	eps := acc.Rows(t, out["episodes"], "episodes")
	if len(eps) != 4 {
		t.Errorf("library-wide episodes = %d, want 4 across both shows", len(eps))
	}
	if total := acc.Num(t, out["total"], "total"); total != 4 {
		t.Errorf("total = %d, want 4", total)
	}

	// naming a podcast narrows it to that show
	one := suite.Call(t, "podcast_episodes", map[string]any{"item": "Behind the Bastards"})
	if got := acc.Rows(t, one["episodes"], "episodes"); len(got) >= len(eps) {
		t.Errorf("one podcast returned %d episodes, want fewer than the %d across the library", len(got), len(eps))
	}
	if one["podcast"] != "Behind the Bastards" {
		t.Errorf("podcast = %v", one["podcast"])
	}
}

// nothing is downloading, so this is the empty case.
func TestPodcastDownloads(t *testing.T) {
	out := suite.Call(t, "podcast_downloads", map[string]any{"library": "Podcasts"})

	if q := acc.Rows(t, out["downloads"], "downloads"); len(q) != 0 {
		t.Errorf("downloads = %v, want an empty queue", q)
	}
}

// a book is not a podcast, and the podcast-only tools must say so.
func TestPodcastBookGuard(t *testing.T) {
	if msg := suite.CallErr(t, "podcast_episodes", map[string]any{"item": "Foundation"}); msg == "" {
		t.Error("podcast_episodes should refuse a book")
	}
}

// last: deleting an episode drops the count, so put it back by rescanning.
func TestPodcastEpisodeDelete(t *testing.T) {
	episodes := acc.Rows(t, suite.Call(t, "podcast_episodes", map[string]any{"item": "Well There's Your Problem"})["episodes"], "episodes")
	if len(episodes) < 2 {
		t.Fatal("expected two episodes to start from")
	}
	id := acc.Str(episodes[0]["id"])

	out := suite.Call(t, "podcast_episode_delete", map[string]any{"item": "Well There's Your Problem", "episode": id, "confirm": true})
	if deleted := acc.BoolOf(out["deleted"]); !deleted {
		t.Errorf("podcast_episode_delete deleted = %v", out["deleted"])
	}
	t.Cleanup(func() {
		// the audio file is still on disk, so a rescan restores the record
		suite.Call(t, "library_scan", map[string]any{"library": "Podcasts"})
	})

	after := suite.Call(t, "podcast_episodes", map[string]any{"item": "Well There's Your Problem"})
	if total := acc.Num(t, after["total"], "total"); total != 1 {
		t.Errorf("after delete total = %d, want 1", total)
	}
}
