//go:build integration

package acceptance

import (
	"testing"
)

// there is only the root account, which is also the account the API key acts
// as. Omitting user and naming root are the same account, both read through
// /api/me; the admin user record of someone else is the journeys' to check,
// with accounts of their own.

func TestUserList(t *testing.T) {
	users := rows(t, call(t, "user_list", nil)["users"], "users")

	if len(users) != 1 {
		t.Fatalf("users = %d, want 1", len(users))
	}
	if users[0]["username"] != "root" {
		t.Errorf("username = %v, want root", users[0]["username"])
	}
	if users[0]["type"] != "root" {
		t.Errorf("type = %v, want root", users[0]["type"])
	}
}

func TestUserGetSelf(t *testing.T) {
	out := call(t, "user_get", nil)

	if out["username"] != "root" {
		t.Errorf("username = %v, want root", out["username"])
	}
	if out["type"] != "root" {
		t.Errorf("type = %v, want root", out["type"])
	}
	if self, _ := out["self"].(bool); !self {
		t.Error("user_get with no user should report self")
	}
	for _, perm := range []string{"can_update", "can_delete", "can_upload"} {
		if ok, _ := out[perm].(bool); !ok {
			t.Errorf("root should have %s", perm)
		}
	}
}

// naming the API key's own account is still the key's own account
func TestUserGetByName(t *testing.T) {
	out := call(t, "user_get", map[string]any{"user": "root"})

	if self, _ := out["self"].(bool); !self {
		t.Error("naming the API key's own account should report self")
	}
	for _, perm := range []string{"can_update", "can_delete", "can_upload"} {
		if ok, _ := out[perm].(bool); !ok {
			t.Errorf("root should have %s", perm)
		}
	}
	if created, _ := out["created"].(string); created == "" {
		t.Error("no created timestamp")
	}
}

// "me" is not an account here, so it falls through to the API key's own user.
func TestUserGetSelfToken(t *testing.T) {
	for _, token := range []string{"me", "self"} {
		out := call(t, "user_get", map[string]any{"user": token})
		if out["username"] != "root" {
			t.Errorf("user=%q gave username %v, want root", token, out["username"])
		}
		if self, _ := out["self"].(bool); !self {
			t.Errorf("user=%q should report self", token)
		}
	}
}

// the whole progress lifecycle: set, read, appear in-progress, remove.
func TestUserProgress(t *testing.T) {
	set := call(t, "user_progress_set", map[string]any{"item": "Foundation", "percent": 50})
	t.Cleanup(func() {
		call(t, "user_progress_remove", map[string]any{"item": "Foundation"})
	})
	if set["item"] != "Foundation" {
		t.Errorf("item = %v", set["item"])
	}

	got := call(t, "user_progress_get", map[string]any{"item": "Foundation"})
	progress, ok := got["progress"].(map[string]any)
	if !ok {
		t.Fatalf("progress = %T", got["progress"])
	}
	if pct := num(t, progress["percent"], "percent"); pct != 50 {
		t.Errorf("percent = %d, want 50", pct)
	}

	// the same progress read back through the admin user record
	other := call(t, "user_progress_get", map[string]any{"user": "root", "item": "Foundation"})
	op, ok := other["progress"].(map[string]any)
	if !ok {
		t.Fatalf("progress for a named user = %T", other["progress"])
	}
	if pct := num(t, op["percent"], "percent"); pct != 50 {
		t.Errorf("named-user percent = %d, want 50", pct)
	}

	// it now shows on the continue-listening shelf, by either path
	for _, args := range []map[string]any{nil, {"user": "root"}} {
		var onShelf bool
		for _, row := range rows(t, call(t, "user_in_progress", args)["items"], "items") {
			if row["title"] == "Foundation" {
				onShelf = true
			}
		}
		if !onShelf {
			t.Errorf("Foundation is not in user_in_progress (args %v) after setting progress", args)
		}
	}

	// finishing it takes it off the shelf
	call(t, "user_progress_set", map[string]any{"item": "Foundation", "finished": true})
	fin := call(t, "user_progress_get", map[string]any{"item": "Foundation"})
	if p, ok := fin["progress"].(map[string]any); ok {
		if finished, _ := p["finished"].(bool); !finished {
			t.Errorf("finished = %v, want true", p["finished"])
		}
	}
}

func TestUserProgressRemove(t *testing.T) {
	call(t, "user_progress_set", map[string]any{"item": "Second Foundation", "percent": 10})
	out := call(t, "user_progress_remove", map[string]any{"item": "Second Foundation"})

	if removed, _ := out["removed"].(bool); !removed {
		t.Errorf("user_progress_remove removed = %v", out["removed"])
	}
	got := call(t, "user_progress_get", map[string]any{"item": "Second Foundation"})
	if got["progress"] != nil {
		t.Errorf("progress = %v, want none after removal", got["progress"])
	}
}

func TestUserBookmarks(t *testing.T) {
	added := call(t, "user_bookmark_edit", map[string]any{
		"item": "Leviathan Wakes", "action": "add", "time_s": 0.25, "title": "A good bit",
	})
	t.Cleanup(func() {
		call(t, "user_bookmark_edit", map[string]any{
			"item": "Leviathan Wakes", "action": "remove", "time_s": 0.25,
		})
	})
	bookmark, ok := added["bookmark"].(map[string]any)
	if !ok {
		t.Fatalf("bookmark = %T", added["bookmark"])
	}
	if bookmark["title"] != "A good bit" {
		t.Errorf("title = %v", bookmark["title"])
	}

	out := call(t, "user_bookmarks", map[string]any{"item": "Leviathan Wakes"})
	bookmarks := rows(t, out["bookmarks"], "bookmarks")
	if len(bookmarks) != 1 {
		t.Fatalf("bookmarks = %d, want 1", len(bookmarks))
	}
	if bookmarks[0]["title"] != "A good bit" {
		t.Errorf("bookmark title = %v", bookmarks[0]["title"])
	}

	// the same bookmark through the admin user record
	named := call(t, "user_bookmarks", map[string]any{"user": "root", "item": "Leviathan Wakes"})
	if got := rows(t, named["bookmarks"], "bookmarks"); len(got) != 1 {
		t.Errorf("named-user bookmarks = %d, want 1", len(got))
	}

	// and all of them, unfiltered
	if all := call(t, "user_bookmarks", nil); len(rows(t, all["bookmarks"], "bookmarks")) == 0 {
		t.Error("user_bookmarks with no item returned nothing")
	}
}

// nothing has been played, so history and stats are the empty case - which
// still has to decode as the right shape, by either path.
func TestUserHistoryAndStats(t *testing.T) {
	for _, args := range []map[string]any{nil, {"user": "root"}} {
		history := call(t, "user_history", args)
		if history["user"] != "root" {
			t.Errorf("user = %v (args %v)", history["user"], args)
		}
		rows(t, history["sessions"], "sessions")
		num(t, history["total_sessions"], "total_sessions")

		stats := call(t, "user_stats", args)
		if stats["user"] != "root" {
			t.Errorf("user = %v (args %v)", stats["user"], args)
		}
		if _, ok := stats["total_listened_s"].(float64); !ok {
			t.Errorf("total_listened_s = %T, want a number of seconds (args %v)", stats["total_listened_s"], args)
		}
	}

	// the year-in-review variant takes a different endpoint entirely
	year := call(t, "user_stats", map[string]any{"year": 2026})
	if year == nil {
		t.Error("user_stats year returned nothing")
	}
}

// Audiobookshelf keeps a year in review only for the caller, and the caller
// named by username is the caller: it was refused as someone else's. Asking
// for another account's is refused in the playback journey.
func TestUserStatsYearByOwnName(t *testing.T) {
	if year := call(t, "user_stats", map[string]any{"user": "root", "year": 2026}); year["user"] != "root" {
		t.Errorf("a year in review for the caller by name = %v", year)
	}
}

func TestUserUnknown(t *testing.T) {
	if msg := callErr(t, "user_get", map[string]any{"user": "nobody"}); msg == "" {
		t.Error("an unknown user should be an error")
	}
}
