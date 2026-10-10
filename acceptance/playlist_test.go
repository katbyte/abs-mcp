//go:build integration

package acceptance

import (
	"slices"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
)

func TestPlaylistLifecycle(t *testing.T) {
	created := suite.Call(t, "playlist_create", map[string]any{
		"library": "Fiction", "name": "Integration Playlist",
		"description": "made by the integration suite",
		"entries":     []any{map[string]any{"item": "Foundation"}},
	})
	if id := acc.Str(created["id"]); id == "" {
		t.Fatalf("no playlist id: %v", created)
	}
	t.Cleanup(func() {
		suite.Call(t, "playlist_delete", map[string]any{"playlist": "Integration Playlist"})
	})

	var found bool
	for _, row := range acc.Rows(t, suite.Call(t, "playlist_list", nil)["playlists"], "playlists") {
		if row["name"] == "Integration Playlist" {
			found = true
		}
	}
	if !found {
		t.Error("the new playlist is not in playlist_list")
	}

	added := suite.Call(t, "playlist_edit", map[string]any{
		"playlist":    "Integration Playlist",
		"add_entries": []any{map[string]any{"item": "Foundation and Empire"}},
	})
	if n := acc.Num(t, added["entries"], "entries"); n != 2 {
		t.Errorf("after add = %d entries, want 2", n)
	}

	got := suite.Call(t, "playlist_get", map[string]any{"playlist": "Integration Playlist"})
	if entries := acc.Rows(t, got["entries"], "entries"); len(entries) != 2 {
		t.Errorf("playlist_get returned %d entries, want 2", len(entries))
	}

	removed := suite.Call(t, "playlist_edit", map[string]any{
		"playlist":       "Integration Playlist",
		"remove_entries": []any{map[string]any{"item": "Foundation"}},
	})
	if n := acc.Num(t, removed["entries"], "entries"); n != 1 {
		t.Errorf("after remove = %d entries, want 1", n)
	}

	edited := suite.Call(t, "playlist_edit", map[string]any{
		"playlist": "Integration Playlist", "description": "renamed description",
	})
	if edited["description"] != "renamed description" {
		t.Errorf("description = %v", edited["description"])
	}

	// all of it in one call: a description, an entry in and an entry out
	both := suite.Call(t, "playlist_edit", map[string]any{
		"playlist": "Integration Playlist", "description": "Zzyzx: three things at once",
		"add_entries":    []any{map[string]any{"item": "Foundation"}},
		"remove_entries": []any{map[string]any{"item": "Foundation and Empire"}},
	})
	if both["description"] != "Zzyzx: three things at once" || acc.Num(t, both["entries"], "entries") != 1 ||
		!slices.Equal(acc.Strs(t, both["added"], "added"), []string{"Foundation"}) || !slices.Equal(acc.Strs(t, both["removed"], "removed"), []string{"Foundation and Empire"}) {
		t.Errorf("three changes in one call = %v", both)
	}
	entries := acc.Rows(t, suite.Call(t, "playlist_get", map[string]any{"playlist": "Integration Playlist"})["entries"], "entries")
	if len(entries) != 1 || acc.Str(object(entries[0]["item"])["title"]) != "Foundation" {
		t.Errorf("the playlist holds %v, want Foundation alone", entries)
	}
}

// a playlist can be seeded from a collection in one call.
func TestPlaylistFromCollection(t *testing.T) {
	suite.Call(t, "collection_create", map[string]any{
		"library": "Fiction", "name": "Source Collection",
		"items": []any{"Foundation", "Second Foundation"},
	})
	t.Cleanup(func() {
		suite.Call(t, "collection_delete", map[string]any{"collection": "Source Collection"})
	})

	created := suite.Call(t, "playlist_create", map[string]any{
		"library": "Fiction", "name": "From Collection", "from_collection": "Source Collection",
	})
	t.Cleanup(func() {
		suite.Call(t, "playlist_delete", map[string]any{"playlist": "From Collection"})
	})

	if n := acc.Num(t, created["entries"], "entries"); n != 2 {
		t.Errorf("playlist from collection has %d entries, want 2", n)
	}
}

// a playlist can hold podcast episodes as well as books.
func TestPlaylistWithEpisode(t *testing.T) {
	episodes := acc.Rows(t, suite.Call(t, "podcast_episodes", map[string]any{"item": "Behind the Bastards"})["episodes"], "episodes")
	if len(episodes) == 0 {
		t.Skip("no episodes to add")
	}
	episodeID := acc.Str(episodes[0]["id"])

	created := suite.Call(t, "playlist_create", map[string]any{
		"library": "Podcasts", "name": "Episode Playlist",
		"entries": []any{map[string]any{"item": "Behind the Bastards", "episode": episodeID}},
	})
	t.Cleanup(func() {
		suite.Call(t, "playlist_delete", map[string]any{"playlist": "Episode Playlist"})
	})

	if n := acc.Num(t, created["entries"], "entries"); n != 1 {
		t.Errorf("entries = %d, want 1", n)
	}
}
