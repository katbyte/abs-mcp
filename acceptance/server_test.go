//go:build integration

package acceptance

import (
	"slices"
	"testing"
)

func TestServerInfo(t *testing.T) {
	info := call(t, "server_info", nil)

	if v, _ := info["version"].(string); v == "" {
		t.Errorf("no server version: %v", info)
	}
	if info["user"] != "root" {
		t.Errorf("user = %v, want root", info["user"])
	}
	if info["user_type"] != "root" {
		t.Errorf("user_type = %v, want root", info["user_type"])
	}
	for _, perm := range []string{"can_update", "can_delete"} {
		if ok, _ := info[perm].(bool); !ok {
			t.Errorf("root should have %s", perm)
		}
	}
	// the providers the key can match against
	if providers := strs(t, info["book_providers"], "book_providers"); !slices.Contains(providers, "audible") {
		t.Errorf("book_providers = %v, want audible among them", providers)
	}
}

func TestServerStats(t *testing.T) {
	out := call(t, "server_stats", nil)

	if books := num(t, out["books"], "books"); books != 10 {
		t.Errorf("books = %d, want 10", books)
	}
	if podcasts := num(t, out["podcasts"], "podcasts"); podcasts != 2 {
		t.Errorf("podcasts = %d, want 2", podcasts)
	}
	if users := num(t, out["users"], "users"); users != 1 {
		t.Errorf("users = %d, want 1", users)
	}
}

func TestServerTasks(t *testing.T) {
	// the scans in setup ran as tasks; the field must at least decode
	out := call(t, "server_tasks", nil)
	rows(t, out["tasks"], "tasks")
}

// nothing is playing, so this is the empty case - which still has to come back
// as a well-formed list rather than null.
func TestServerSessions(t *testing.T) {
	out := call(t, "server_sessions", nil)

	if sessions := rows(t, out["sessions"], "sessions"); len(sessions) != 0 {
		t.Errorf("sessions = %v, want none open", sessions)
	}
}

func TestServerBackups(t *testing.T) {
	before := call(t, "server_backups", nil)
	n := len(rows(t, before["backups"], "backups"))

	after := call(t, "server_backup_create", nil)
	if got := len(rows(t, after["backups"], "backups")); got != n+1 {
		t.Errorf("backups after create = %d, want %d", got, n+1)
	}
	// the create response carries only the list; the location comes from the
	// list endpoint
	if loc, _ := before["location"].(string); loc == "" {
		t.Error("server_backups reported no location")
	}
}

// server_tag_get is server-wide, so it must see tags from every library at
// once - which is what library_filters, being per-library, cannot do.
func TestServerGetTags(t *testing.T) {
	out := call(t, "server_tag_get", nil)
	tags := strs(t, out["tags"], "tags")
	genres := strs(t, out["genres"], "genres")

	// sf and cyberpunk are Fiction's, history and politics Non-Fiction's
	for _, want := range []string{"sf", "classic", "cyberpunk", "space-opera", "history", "industry", "humour", "war", "politics"} {
		if !slices.Contains(tags, want) {
			t.Errorf("tags %v missing %q", tags, want)
		}
	}
	for _, want := range []string{"Science Fiction", "History"} {
		if !slices.Contains(genres, want) {
			t.Errorf("genres %v missing %q", genres, want)
		}
	}

	if only := call(t, "server_tag_get", map[string]any{"kind": "genres"}); only["tags"] != nil {
		t.Errorf("kind=genres returned tags: %v", only["tags"])
	}
	if only := call(t, "server_tag_get", map[string]any{"kind": "tags"}); only["genres"] != nil {
		t.Errorf("kind=tags returned genres: %v", only["genres"])
	}
}

// rename and rename back, so the rest of the suite still sees the original.
func TestServerRenameTag(t *testing.T) {
	out := call(t, "server_tag_rename", map[string]any{"kind": "tag", "from": "humour", "to": "humor"})
	if n := num(t, out["items_updated"], "items_updated"); n != 1 {
		t.Errorf("items_updated = %d, want 1", n)
	}
	t.Cleanup(func() {
		call(t, "server_tag_rename", map[string]any{"kind": "tag", "from": "humor", "to": "humour"})
	})

	if tags := strs(t, call(t, "server_tag_get", map[string]any{"kind": "tags"})["tags"], "tags"); !slices.Contains(tags, "humor") {
		t.Errorf("tags %v missing the renamed humor", tags)
	}
}
