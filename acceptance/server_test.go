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
	if got := num(t, totals["books"], "books"); got != len(books)+len(messyBooks) {
		t.Errorf("books = %d, want %d", got, len(books)+len(messyBooks))
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
// Tags are server-wide, which is why the rename lives in metadata_rename
// rather than under server_.
func TestServerRenameTag(t *testing.T) {
	out := call(t, "metadata_rename", map[string]any{"field": "tags", "from": "humour", "to": "humor"})
	if n := num(t, out["items_updated"], "items_updated"); n != 1 {
		t.Errorf("items_updated = %d, want 1", n)
	}
	t.Cleanup(func() {
		call(t, "metadata_rename", map[string]any{"field": "tags", "from": "humor", "to": "humour"})
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

// Nothing in the fixtures has ever been played, so every session-shaped answer
// was only ever tested empty - which left summarizeSession and
// DeviceInfo.Describe, the projection behind server_sessions and user_history,
// never executed by any test. This opens a real playback session so both are
// asserted against actual data.
func TestServerSessionsWithAPlaybackSession(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}

	client, err := abs.New(os.Getenv("ABS_SERVER"), os.Getenv("ABS_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	// a book no other test asserts on: opening a session and syncing time
	// against it writes progress, which would knock it off another test's
	// continue-listening shelf
	const book = "City of Golden Shadow"
	item := call(t, "item_get", map[string]any{"item": book})
	id, _ := item["id"].(string)
	if id == "" {
		t.Fatalf("no id for %s: %v", book, item)
	}

	session, err := client.Play(ctx, id, "", abs.PlayRequest{
		MediaPlayer:        "acceptance-player",
		SupportedMimeTypes: []string{"audio/mpeg"},
		DeviceInfo:         map[string]any{"clientName": "abs-mcp tests", "deviceName": "a test runner"},
		ForceDirectPlay:    true,
	})
	if err != nil {
		t.Fatalf("opening a playback session: %v", err)
	}
	t.Cleanup(func() {
		_ = client.CloseSession(ctx, session.ID, nil)
		call(t, "user_progress_remove", map[string]any{"item": book})
	})

	// it is open, so server_sessions must project it rather than return nothing
	rows := rows(t, call(t, "server_sessions", nil)["sessions"], "sessions")
	if len(rows) == 0 {
		t.Fatal("server_sessions is empty while a session is open")
	}
	var found map[string]any
	for _, r := range rows {
		if r["id"] == session.ID {
			found = r
		}
	}
	if found == nil {
		t.Fatalf("the open session %s is not in server_sessions: %v", session.ID, rows)
	}
	if found["title"] != book {
		t.Errorf("title = %v, want %s", found["title"], book)
	}
	// an open session carries userId but not the expanded user object, so the
	// name is absent here and only appears on a finished one
	if uid, _ := found["user_id"].(string); uid == "" {
		t.Errorf("no user_id on the open session: %v", found)
	}
	// DeviceInfo.Describe builds this from clientName and deviceName
	if device, _ := found["device"].(string); !strings.Contains(device, "abs-mcp tests") {
		t.Errorf("device = %q, want the client name in it", device)
	}

	// a session only reaches the history once it has listened time against it.
	// The fixtures are one second long, so stay inside that: syncing past the
	// end marks the book finished.
	if err := client.SyncSession(ctx, session.ID, 0.3, 0.3); err != nil {
		t.Fatalf("syncing listened time: %v", err)
	}

	// and it reaches the history, which reads a different endpoint
	history := call(t, "user_history", nil)
	if num(t, history["total_sessions"], "total_sessions") == 0 {
		t.Error("user_history reports no sessions after one was opened")
	}
	var inHistory bool
	for _, r := range rows2(t, history["sessions"]) {
		if r["id"] == session.ID {
			inHistory = true
			if r["title"] != book {
				t.Errorf("history title = %v, want %s", r["title"], book)
			}
		}
	}
	if !inHistory {
		t.Errorf("session %s is not in user_history", session.ID)
	}
}

// rows2 is rows without the t.Helper fatal, for a list that may legitimately
// be short.
func rows2(t *testing.T, v any) []map[string]any {
	t.Helper()

	list, _ := v.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}

	return out
}
