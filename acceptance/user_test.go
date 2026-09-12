//go:build integration

package acceptance

import (
	"testing"
)

// there is only the root account, so these cover the admin read path against
// a real permissions payload rather than a stub.
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

func TestUserGet(t *testing.T) {
	out := call(t, "user_get", map[string]any{"user": "root"})

	for _, perm := range []string{"can_update", "can_delete", "can_upload"} {
		if ok, _ := out[perm].(bool); !ok {
			t.Errorf("root should have %s", perm)
		}
	}
	if created, _ := out["created"].(string); created == "" {
		t.Error("no created timestamp")
	}
}

func TestUserHistoryAndStats(t *testing.T) {
	history := call(t, "user_history", map[string]any{"user": "root"})
	if history["user"] != "root" {
		t.Errorf("user = %v", history["user"])
	}
	rows(t, history["sessions"], "sessions")

	stats := call(t, "user_stats", map[string]any{"user": "root"})
	if stats["user"] != "root" {
		t.Errorf("user = %v", stats["user"])
	}
	if _, ok := stats["total_listened"].(string); !ok {
		t.Errorf("total_listened = %T, want a formatted duration", stats["total_listened"])
	}
}

func TestUserUnknown(t *testing.T) {
	if msg := callErr(t, "user_get", map[string]any{"user": "nobody"}); msg == "" {
		t.Error("an unknown user should be an error")
	}
}
