//go:build integration

package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/katbyte/abs-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// the server-wide totals server_info absorbed from the former server_stats.
// They are admin-only and best-effort, so an admin key must actually see them.
func TestServerInfoTotals(t *testing.T) {
	out := call(t, "server_info", nil)

	totals, ok := out["totals"].(map[string]any)
	if !ok {
		t.Fatalf("totals = %T, want an object for an admin key: %v", out["totals"], out["note"])
	}
	if books := num(t, totals["books"], "books"); books != 10 {
		t.Errorf("books = %d, want 10", books)
	}
	if podcasts := num(t, totals["podcasts"], "podcasts"); podcasts != 2 {
		t.Errorf("podcasts = %d, want 2", podcasts)
	}
	if users := num(t, totals["users"], "users"); users != 1 {
		t.Errorf("users = %d, want 1", users)
	}
	num(t, totals["audio_files"], "audio_files")

	// nobody is playing anything, and that is a real answer rather than a
	// missing one: the field has to be there and say zero
	if _, present := totals["open_sessions"]; !present {
		t.Error("open_sessions is absent; zero sessions is an answer, not a gap")
	}
	if out["note"] != nil {
		t.Errorf("an admin key should get no note, got %v", out["note"])
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

// server_tags is server-wide, so it must see tags from every library at
// once - which is what library_filters, being per-library, cannot do.
func TestServerTags(t *testing.T) {
	out := call(t, "server_tags", nil)
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

	if only := call(t, "server_tags", map[string]any{"kind": "genres"}); only["tags"] != nil {
		t.Errorf("kind=genres returned tags: %v", only["tags"])
	}
	if only := call(t, "server_tags", map[string]any{"kind": "tags"}); only["genres"] != nil {
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

	if tags := strs(t, call(t, "server_tags", map[string]any{"kind": "tags"})["tags"], "tags"); !slices.Contains(tags, "humor") {
		t.Errorf("tags %v missing the renamed humor", tags)
	}
}

// The degradation path, which nothing covered before: a key for a non-admin
// account cannot read the server-wide totals, and has to be told so. A missing
// count read as zero would have the model report an empty server.
func TestServerInfoNonAdmin(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}

	admin, err := abs.New(os.Getenv("ABS_SERVER"), os.Getenv("ABS_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	// isActive has to be set explicitly: an account created without it cannot
	// log in, and so can never activate an API key either
	active := true
	user, err := admin.CreateUser(ctx, abs.UserCreate{
		Username: "plainuser", Password: "plainuser-password", Type: "user", IsActive: &active,
	})
	if err != nil {
		t.Fatalf("creating a non-admin user: %v", err)
	}
	t.Cleanup(func() { _ = admin.DeleteUser(ctx, user.ID) })

	// Audiobookshelf will not honour an API key for an account that has never
	// logged in: the key is created, and then every authenticated route answers
	// 401 rather than 403. One login activates it. /login is a root-level route
	// rather than an /api/ one, so it is outside ApiRouter and lib/abs.
	if err := login("plainuser", "plainuser-password"); err != nil {
		t.Fatalf("activating the account with a login: %v", err)
	}

	key, err := admin.CreateAPIKey(ctx, "acceptance-nonadmin", user.ID, 0, true)
	if err != nil {
		t.Fatalf("creating an api key for them: %v", err)
	}
	t.Cleanup(func() { _ = admin.DeleteAPIKey(ctx, key.ID) })
	if key.Key == "" {
		t.Fatal("no token on the new api key")
	}

	// a second server, wired to their key rather than root's
	plain, err := abs.New(os.Getenv("ABS_SERVER"), key.Key)
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "abs-mcp", Version: "test"}, nil)
	if _, err := tools.RegisterAll(srv, plain, tools.Options{}); err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "server_info"})
	if err != nil {
		t.Fatalf("server_info must answer for a non-admin key: %v", err)
	}
	if res.IsError {
		var msg []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msg = append(msg, tc.Text)
			}
		}
		t.Fatalf("server_info errored for a non-admin key: %s", strings.Join(msg, "; "))
	}
	out, _ := res.StructuredContent.(map[string]any)

	// the part that works for any key is still complete
	if out["user"] != "plainuser" {
		t.Errorf("user = %v, want plainuser", out["user"])
	}
	if out["user_type"] != "user" {
		t.Errorf("user_type = %v, want user", out["user_type"])
	}
	if out["version"] == nil || out["version"] == "" {
		t.Error("no version for a non-admin key")
	}

	// and the part that does not is absent and explained
	if out["totals"] != nil {
		t.Errorf("totals = %v, want none for a non-admin key", out["totals"])
	}
	note, _ := out["note"].(string)
	if note == "" {
		t.Fatal("no note: a non-admin key is told nothing about why the totals are missing")
	}
	for _, want := range []string{"admin", "plainuser", "unknown"} {
		if !strings.Contains(strings.ToLower(note), want) {
			t.Errorf("note does not mention %q: %s", want, note)
		}
	}
}

// login posts to the root-level /login route to activate an account, which
// Audiobookshelf requires before any API key issued for it is accepted.
func login(username, password string) error {
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, os.Getenv("ABS_SERVER")+"/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("/login: HTTP %d", res.StatusCode)
	}

	return nil
}
