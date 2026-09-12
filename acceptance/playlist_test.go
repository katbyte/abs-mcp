//go:build integration

package acceptance

import (
	"testing"
)

func TestPlaylistLifecycle(t *testing.T) {
	created := call(t, "playlist_create", map[string]any{
		"library": "Fiction", "name": "Integration Playlist",
		"description": "made by the integration suite",
		"entries":     []any{map[string]any{"item": "Foundation"}},
	})
	if id, _ := created["id"].(string); id == "" {
		t.Fatalf("no playlist id: %v", created)
	}
	t.Cleanup(func() {
		call(t, "playlist_delete", map[string]any{"playlist": "Integration Playlist"})
	})

	var found bool
	for _, row := range rows(t, call(t, "playlist_list", nil)["playlists"], "playlists") {
		if row["name"] == "Integration Playlist" {
			found = true
		}
	}
	if !found {
		t.Error("the new playlist is not in playlist_list")
	}

	added := call(t, "playlist_add", map[string]any{
		"playlist": "Integration Playlist",
		"entries":  []any{map[string]any{"item": "Foundation and Empire"}},
	})
	if n := num(t, added["entries"], "entries"); n != 2 {
		t.Errorf("after add = %d entries, want 2", n)
	}

	got := call(t, "playlist_get", map[string]any{"playlist": "Integration Playlist"})
	if entries := rows(t, got["entries"], "entries"); len(entries) != 2 {
		t.Errorf("playlist_get returned %d entries, want 2", len(entries))
	}

	removed := call(t, "playlist_remove", map[string]any{
		"playlist": "Integration Playlist",
		"entries":  []any{map[string]any{"item": "Foundation"}},
	})
	if n := num(t, removed["entries"], "entries"); n != 1 {
		t.Errorf("after remove = %d entries, want 1", n)
	}

	edited := call(t, "playlist_edit", map[string]any{
		"playlist": "Integration Playlist", "description": "renamed description",
	})
	if edited["description"] != "renamed description" {
		t.Errorf("description = %v", edited["description"])
	}
}

// a playlist can be seeded from a collection in one call.
func TestPlaylistFromCollection(t *testing.T) {
	call(t, "collection_create", map[string]any{
		"library": "Fiction", "name": "Source Collection",
		"items": []any{"Foundation", "Second Foundation"},
	})
	t.Cleanup(func() {
		call(t, "collection_delete", map[string]any{"collection": "Source Collection"})
	})

	created := call(t, "playlist_create", map[string]any{
		"library": "Fiction", "name": "From Collection", "from_collection": "Source Collection",
	})
	t.Cleanup(func() {
		call(t, "playlist_delete", map[string]any{"playlist": "From Collection"})
	})

	if n := num(t, created["entries"], "entries"); n != 2 {
		t.Errorf("playlist from collection has %d entries, want 2", n)
	}
}

// a playlist can hold podcast episodes as well as books.
func TestPlaylistWithEpisode(t *testing.T) {
	episodes := rows(t, call(t, "podcast_episodes", map[string]any{"item": "Behind the Bastards"})["episodes"], "episodes")
	if len(episodes) == 0 {
		t.Skip("no episodes to add")
	}
	episodeID, _ := episodes[0]["id"].(string)

	created := call(t, "playlist_create", map[string]any{
		"library": "Podcasts", "name": "Episode Playlist",
		"entries": []any{map[string]any{"item": "Behind the Bastards", "episode": episodeID}},
	})
	t.Cleanup(func() {
		call(t, "playlist_delete", map[string]any{"playlist": "Episode Playlist"})
	})

	if n := num(t, created["entries"], "entries"); n != 1 {
		t.Errorf("entries = %d, want 1", n)
	}
}
