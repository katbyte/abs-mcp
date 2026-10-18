//go:build acceptance

package tools_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	api "github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/katbyte/abs-mcp/tools"
	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerInfo(t *testing.T) {
	info := suite.Call(t, "server_info", nil)

	if v := acctest.Str(info["version"]); v == "" {
		t.Errorf("no server version: %v", info)
	}
	if info["user"] != "root" {
		t.Errorf("user = %v, want root", info["user"])
	}
	if info["user_type"] != "root" {
		t.Errorf("user_type = %v, want root", info["user_type"])
	}
	for _, perm := range []string{"can_update", "can_delete"} {
		if ok := acctest.BoolOf(info[perm]); !ok {
			t.Errorf("root should have %s", perm)
		}
	}
	// the providers the key can match against
	if providers := acctest.Strs(t, info["book_providers"], "book_providers"); !slices.Contains(providers, "audible") {
		t.Errorf("book_providers = %v, want audible among them", providers)
	}
}

// the server-wide totals server_info absorbed from the former server_stats.
// They are admin-only and best-effort, so an admin key must actually see them.
func TestServerInfoTotals(t *testing.T) {
	out := suite.Call(t, "server_info", nil)

	totals, ok := out["totals"].(map[string]any)
	if !ok {
		t.Fatalf("totals = %T, want an object for an admin key: %v", out["totals"], out["note"])
	}
	if got := acctest.Num(t, totals["books"], "books"); got != len(books)+len(messyBooks) {
		t.Errorf("books = %d, want %d", got, len(books)+len(messyBooks))
	}
	if podcasts := acctest.Num(t, totals["podcasts"], "podcasts"); podcasts != 2 {
		t.Errorf("podcasts = %d, want 2", podcasts)
	}
	if users := acctest.Num(t, totals["users"], "users"); users != 1 {
		t.Errorf("users = %d, want 1", users)
	}
	acctest.Num(t, totals["audio_files"], "audio_files")
	// in bytes: a few hundred one-second files were 0 in whole gigabytes
	if size := acctest.Num(t, totals["total_size"], "total_size"); size <= 0 || size < acctest.Num(t, totals["books_size"], "books_size") {
		t.Errorf("total_size = %d bytes, want the books' %v and more", size, totals["books_size"])
	}

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
	out := suite.Call(t, "server_tasks", nil)
	acctest.Rows(t, out["tasks"], "tasks")
}

// nothing is playing, so this is the empty case - which still has to come back
// as a well-formed list rather than null.
func TestServerSessions(t *testing.T) {
	out := suite.Call(t, "server_sessions", nil)

	if sessions := acctest.Rows(t, out["sessions"], "sessions"); len(sessions) != 0 {
		t.Errorf("sessions = %v, want none open", sessions)
	}
}

func TestServerBackups(t *testing.T) {
	before := suite.Call(t, "server_backups", nil)

	// the backup made, by name: a count of the list cannot say, as a backup
	// made in the minute of another replaces it and the oldest are pruned
	after := suite.Call(t, "server_backup_create", nil)
	created := object(after["created"])
	id := acctest.Str(created["id"])
	if id == "" {
		t.Fatalf("server_backup_create named no backup: %v", after)
	}
	t.Cleanup(func() {
		eventually(t, "deleting the backup", func() error { _, err := adminClient(t).BackupDelete(ctx, id); return err })
	})
	var listed bool
	for _, b := range acctest.Rows(t, after["backups"], "backups") {
		listed = listed || b["id"] == id
	}
	if !listed {
		t.Errorf("the backup made, %s, is not in the list: %v", id, after["backups"])
	}
	for _, b := range acctest.Rows(t, after["backups"], "backups") {
		if acctest.Num(t, b["size"], "size") <= 0 {
			t.Errorf("backup %v has no size in bytes", b)
		}
	}
	// the create response carries only the list; the location comes from the
	// list endpoint
	if loc := acctest.Str(before["location"]); loc == "" {
		t.Error("server_backups reported no location")
	}
}

// server_tags is server-wide, so it must see tags from every library at
// once - which is what library_filters, being per-library, cannot do.
func TestServerTags(t *testing.T) {
	out := suite.Call(t, "server_tags", nil)
	tags := acctest.Strs(t, out["tags"], "tags")
	genres := acctest.Strs(t, out["genres"], "genres")

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

	if only := suite.Call(t, "server_tags", map[string]any{"kind": "genres"}); only["tags"] != nil {
		t.Errorf("kind=genres returned tags: %v", only["tags"])
	}
	if only := suite.Call(t, "server_tags", map[string]any{"kind": "tags"}); only["genres"] != nil {
		t.Errorf("kind=tags returned genres: %v", only["genres"])
	}
}

// rename and rename back, so the rest of the suite still sees the original.
// Tags are server-wide, which is why the rename lives in metadata_rename
// rather than under server_.
func TestServerRenameTag(t *testing.T) {
	out := suite.Call(t, "metadata_rename", map[string]any{"field": "tags", "from": "humour", "to": "humor"})
	if n := acctest.Num(t, out["items_updated"], "items_updated"); n != 1 {
		t.Errorf("items_updated = %d, want 1", n)
	}
	t.Cleanup(func() {
		suite.Call(t, "metadata_rename", map[string]any{"field": "tags", "from": "humor", "to": "humour"})
	})

	if tags := acctest.Strs(t, suite.Call(t, "server_tags", map[string]any{"kind": "tags"})["tags"], "tags"); !slices.Contains(tags, "humor") {
		t.Errorf("tags %v missing the renamed humor", tags)
	}
}

// The degradation path, which nothing covered before: a key for a non-admin
// account cannot read the server-wide totals, and has to be told so. A missing
// count read as zero would have the model report an empty server.
func TestServerInfoNonAdmin(t *testing.T) {
	if !suite.Ready {
		t.Skip(suite.NotReady)
	}

	admin, err := connect(os.Getenv("ABS_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	// isActive has to be set explicitly: an account created without it cannot
	// log in, and so can never activate an API key either
	made, err := record(admin.UserCreate(ctx, api.UserCreateRequest{
		Username: "plainuser", Password: "plainuser-password", Type: "user", IsActive: new(true),
	}))
	if err != nil {
		t.Fatalf("creating a non-admin user: %v", err)
	}
	user := made.User.Id
	t.Cleanup(func() { _, _ = admin.UserDelete(ctx, user) })

	// Audiobookshelf will not honour an API key for an account that has never
	// logged in: the key is created, and then every authenticated route answers
	// 401 rather than 403. One login activates it. /login is a root-level route
	// rather than an /api/ one, so it is outside ApiRouter and the generated client.
	if err := login("plainuser", "plainuser-password"); err != nil {
		t.Fatalf("activating the account with a login: %v", err)
	}

	key, err := record(admin.ApiKeyCreate(ctx, api.ApiKeyCreateRequest{Name: "acceptance-nonadmin", UserId: user, IsActive: new(true)}))
	if err != nil {
		t.Fatalf("creating an api key for them: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.ApiKeyDelete(ctx, string(key.ApiKey.Id)) })
	if key.ApiKey.ApiKey == "" {
		t.Fatal("no token on the new api key")
	}

	// a second server, wired to their key rather than root's
	plain, err := connect(key.ApiKey.ApiKey)
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
	defer func() { _ = cs.Close() }()

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
	out := object(res.StructuredContent)

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
	note := acctest.Str(out["note"])
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
	defer func() { _ = res.Body.Close() }()
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
	if !suite.Ready {
		t.Skip(suite.NotReady)
	}

	client, err := connect(os.Getenv("ABS_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	// a book no other test asserts on: opening a session and syncing time
	// against it writes progress, which would knock it off another test's
	// continue-listening shelf
	const book = "City of Golden Shadow"
	item := suite.Call(t, "item_get", map[string]any{"item": book})
	id := acctest.Str(item["id"])
	if id == "" {
		t.Fatalf("no id for %s: %v", book, item)
	}

	session, err := record(client.LibraryItemStartPlaybackSession(ctx, api.LibraryItemID(id), api.LibraryItemStartPlaybackSessionRequest{
		MediaPlayer:        "acceptance-player",
		SupportedMimeTypes: []string{"audio/mpeg"},
		DeviceInfo:         &api.LibraryItemStartPlaybackSessionRequestDeviceInfo{ClientName: "abs-mcp tests", DeviceName: "a test runner"},
		ForceDirectPlay:    new(true),
	}))
	if err != nil {
		t.Fatalf("opening a playback session: %v", err)
	}
	t.Cleanup(func() {
		// a close that failed left the session playing for the next run
		if _, err := client.SessionClose(ctx, session.Id, api.SessionCloseRequest{}); err != nil {
			t.Errorf("closing the session: %v", err)
		}
		suite.Call(t, "user_progress_set", map[string]any{"remove": true, "item": book})
	})

	// it is open, so server_sessions must project it rather than return nothing
	rows := acctest.Rows(t, suite.Call(t, "server_sessions", nil)["sessions"], "sessions")
	if len(rows) == 0 {
		t.Fatal("server_sessions is empty while a session is open")
	}
	var found map[string]any
	for _, r := range rows {
		if r["id"] == session.Id {
			found = r
		}
	}
	if found == nil {
		t.Fatalf("the open session %s is not in server_sessions: %v", session.Id, rows)
	}
	if found["title"] != book {
		t.Errorf("title = %v, want %s", found["title"], book)
	}
	// an open session carries userId but not the expanded user object; the
	// tool looks the name up
	if uid := acctest.Str(found["user_id"]); uid == "" {
		t.Errorf("no user_id on the open session: %v", found)
	}
	if found["user"] != "root" {
		t.Errorf("user = %v, want root named on the open session", found["user"])
	}
	// DeviceInfo.Describe builds this from clientName and deviceName
	if device := acctest.Str(found["device"]); !strings.Contains(device, "abs-mcp tests") {
		t.Errorf("device = %q, want the client name in it", device)
	}

	// a session only reaches the history once it has listened time against it.
	// The fixtures are one second long, so stay inside that: syncing past the
	// end marks the book finished.
	if _, err := client.SessionSync(ctx, session.Id, api.SessionSyncRequest{CurrentTime: 0.3, TimeListened: 0.3}); err != nil {
		t.Fatalf("syncing listened time: %v", err)
	}

	// and it reaches the history, which reads a different endpoint
	history := suite.Call(t, "user_history", nil)
	if acctest.Num(t, history["total_sessions"], "total_sessions") == 0 {
		t.Error("user_history reports no sessions after one was opened")
	}
	var inHistory bool
	for _, r := range rows2(t, history["sessions"]) {
		if r["id"] == session.Id {
			inHistory = true
			if r["title"] != book {
				t.Errorf("history title = %v, want %s", r["title"], book)
			}
		}
	}
	if !inHistory {
		t.Errorf("session %s is not in user_history", session.Id)
	}
}

// rows2 is rows without the t.Helper fatal, for a list that may legitimately
// be short.
func rows2(t *testing.T, v any) []map[string]any {
	t.Helper()

	list := acctest.RowsOfAny(v)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}

	return out
}

// Today's log, newest first: the server logs every scan, and a match picks
// lines out of it.
func TestServerTasksLog(t *testing.T) {
	out := suite.Call(t, "server_tasks", map[string]any{"log": true, "level": "info", "limit": 5})
	lines := acctest.Rows(t, out["log"], "log")
	if len(lines) == 0 || len(lines) > 5 || acctest.DecimalOr0(out["log_matched"]) < float64(len(lines)) {
		t.Fatalf("server_tasks log = %v", out)
	}
	if lines[0]["time"] == nil || lines[0]["level"] == nil || lines[0]["message"] == nil || acctest.Str(lines[0]["time"]) < acctest.Str(lines[len(lines)-1]["time"]) {
		t.Errorf("lines = %v, want time, level and message, newest first", lines)
	}
	out = suite.Call(t, "server_tasks", map[string]any{"log": true, "level": "info", "match": "scan"})
	for _, l := range acctest.Rows(t, out["log"], "log") {
		if !strings.Contains(strings.ToLower(acctest.Str(l["message"])+" "+acctest.Str(l["source"])), "scan") {
			t.Errorf("a line that does not match: %v", l)
		}
	}
	if acctest.DecimalOr0(out["log_matched"]) == 0 {
		t.Error("no line of today's log mentions a scan, though the fixtures were scanned")
	}
}
