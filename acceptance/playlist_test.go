//go:build integration

package acceptance

import (
	"slices"
	"testing"
)

func TestPlaylistLifecycle(t *testing.T) {
	created := call(t, "playlist_create", map[string]any{
		"library": "Fiction", "name": "Integration Playlist",
		"description": "made by the integration suite",
		"entries":     []any{map[string]any{"item": "Foundation"}},
	})
	if id := text(created["id"]); id == "" {
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

	added := call(t, "playlist_edit", map[string]any{
		"playlist":    "Integration Playlist",
		"add_entries": []any{map[string]any{"item": "Foundation and Empire"}},
	})
	if n := num(t, added["entries"], "entries"); n != 2 {
		t.Errorf("after add = %d entries, want 2", n)
	}

	got := call(t, "playlist_get", map[string]any{"playlist": "Integration Playlist"})
	if entries := rows(t, got["entries"], "entries"); len(entries) != 2 {
		t.Errorf("playlist_get returned %d entries, want 2", len(entries))
	}

	removed := call(t, "playlist_edit", map[string]any{
		"playlist":       "Integration Playlist",
		"remove_entries": []any{map[string]any{"item": "Foundation"}},
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

	// all of it in one call: a description, an entry in and an entry out
	both := call(t, "playlist_edit", map[string]any{
		"playlist": "Integration Playlist", "description": "Zzyzx: three things at once",
		"add_entries":    []any{map[string]any{"item": "Foundation"}},
		"remove_entries": []any{map[string]any{"item": "Foundation and Empire"}},
	})
	if both["description"] != "Zzyzx: three things at once" || num(t, both["entries"], "entries") != 1 ||
		!slices.Equal(strs(t, both["added"], "added"), []string{"Foundation"}) || !slices.Equal(strs(t, both["removed"], "removed"), []string{"Foundation and Empire"}) {
		t.Errorf("three changes in one call = %v", both)
	}
	entries := rows(t, call(t, "playlist_get", map[string]any{"playlist": "Integration Playlist"})["entries"], "entries")
	if len(entries) != 1 || text(object(entries[0]["item"])["title"]) != "Foundation" {
		t.Errorf("the playlist holds %v, want Foundation alone", entries)
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
	episodeID := text(episodes[0]["id"])

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
