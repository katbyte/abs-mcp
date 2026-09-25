//go:build integration

// Journeys: the tools chained the way a real session chains them, against
// state the server changes underneath, across users, and repeated. The
// per-tool tests prove each tool answers; these prove that what one tool
// changed is what the next one sees, and that the server did what the tool
// said it did - a 200 from Audiobookshelf is not proof, so every write here
// is read back.
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/katbyte/abs-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// rawJSON calls the server directly with a token, for the reads a journey
// uses to check the server's own state rather than a tool's account of it.
func rawJSON(method, path, token string, body any) (int, any, error) {
	var in io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		in = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, os.Getenv("ABS_SERVER")+path, in)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, nil, err
	}
	var out any
	if len(raw) > 0 && json.Unmarshal(raw, &out) != nil {
		out = string(raw)
	}

	return res.StatusCode, out, nil
}

// text reads a decoded JSON string, empty when it is anything else.
func text(v any) string {
	s, _ := v.(string)
	return s
}

// adminGet reads a server route as the suite's admin key, failing the test
// on anything but a 200.
func adminGet(t *testing.T, path string) any {
	t.Helper()

	status, out, err := rawJSON(http.MethodGet, path, os.Getenv("ABS_TOKEN"), nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d %v %v", path, status, err, out)
	}

	return out
}

// adminClient is the SDK on the suite's admin key, for the setup a journey
// needs that no tool offers: users, playback sessions, deleting a library.
func adminClient(t *testing.T) *abs.Client {
	t.Helper()

	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}
	c, err := abs.New(os.Getenv("ABS_SERVER"), os.Getenv("ABS_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}

	return c
}

// libraryID names a seeded library's id.
func libraryID(t *testing.T, name string) string {
	t.Helper()

	for _, l := range rows(t, call(t, "library_list", nil)["libraries"], "libraries") {
		if l["name"] == name {
			id, _ := l["id"].(string)
			return id
		}
	}
	t.Fatalf("no library named %s", name)

	return ""
}

// itemID resolves a title in one library to its id through the tools.
func itemID(t *testing.T, library, title string) string {
	t.Helper()

	id, _ := call(t, "item_get", map[string]any{"library": library, "item": title})["id"].(string)
	if id == "" {
		t.Fatalf("no id for %s in %s", title, library)
	}

	return id
}

// eventually retries a cleanup step: a delete can fail while the server is
// mid-scan. It reports, rather than silently leaving state for later tests.
func eventually(t *testing.T, what string, fn func() error) {
	t.Helper()

	var err error
	for range 10 {
		if err = fn(); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Errorf("%s never succeeded: %v", what, err)
}

// waitIdle polls server_tasks until nothing is running, having seen the
// task start when started is true.
func waitIdle(t *testing.T) {
	t.Helper()

	for range 60 {
		running := 0
		for _, task := range rows(t, call(t, "server_tasks", nil)["tasks"], "tasks") {
			if task["status"] == "running" {
				running++
			}
		}
		if running == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("the server never went idle")
}

// userSession is an account of its own with an API key and an MCP session
// wired to that key, so a journey can act as someone other than the admin.
type userSession struct {
	ID, Name, Token string
	session         *mcp.ClientSession
}

// call invokes a tool as this user.
func (u *userSession) call(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()

	out, err := u.invoke(name, args)
	if err != nil {
		t.Fatalf("as %s: %v", u.Name, err)
	}

	return out
}

// callErr invokes a tool as this user, expecting it to fail.
func (u *userSession) callErr(t *testing.T, name string, args map[string]any) string {
	t.Helper()

	out, err := u.invoke(name, args)
	if err == nil {
		t.Fatalf("as %s: %s unexpectedly succeeded: %v", u.Name, name, out)
	}

	return err.Error()
}

func (u *userSession) invoke(name string, args map[string]any) (map[string]any, error) {
	calledMu.Lock()
	called[name] = true
	calledMu.Unlock()

	res, err := u.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}
		return nil, fmt.Errorf("%s: %s", name, strings.Join(msgs, "; "))
	}
	out, _ := res.StructuredContent.(map[string]any)

	return out, nil
}

// newUser creates a non-admin account, activates it with a login, gives it an
// API key and an MCP server of its own, and removes all of it afterwards.
func newUser(t *testing.T, username string, create abs.UserCreate) *userSession {
	t.Helper()

	return newUserWith(t, username, create, tools.Options{})
}

// newUserWith is newUser with the account's MCP server registering the tools
// opts allows, such as the delete tools.
func newUserWith(t *testing.T, username string, create abs.UserCreate, opts tools.Options) *userSession {
	t.Helper()

	admin := adminClient(t)
	existing, err := admin.Users(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range existing {
		if u.Username == username { // left by an earlier run that died
			_ = admin.DeleteUser(ctx, u.ID)
		}
	}
	active := true
	create.Username, create.Password, create.IsActive = username, username+"-password", &active
	if create.Type == "" {
		create.Type = "user"
	}
	user, err := admin.CreateUser(ctx, create)
	if err != nil {
		t.Fatalf("creating %s: %v", username, err)
	}
	t.Cleanup(func() {
		eventually(t, "deleting user "+username, func() error { return admin.DeleteUser(context.WithoutCancel(ctx), user.ID) })
	})
	// an API key for an account that has never logged in is refused
	if err := login(username, username+"-password"); err != nil {
		t.Fatalf("activating %s: %v", username, err)
	}
	key, err := admin.CreateAPIKey(ctx, username, user.ID, 0, true)
	if err != nil {
		t.Fatalf("an api key for %s: %v", username, err)
	}
	t.Cleanup(func() { _ = admin.DeleteAPIKey(context.WithoutCancel(ctx), key.ID) })

	client, err := abs.New(os.Getenv("ABS_SERVER"), key.Key)
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "abs-mcp", Version: "test"}, nil)
	if _, err := tools.RegisterAll(srv, client, opts); err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: username, Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return &userSession{ID: user.ID, Name: username, Token: key.Key, session: cs}
}

// --- journey 5: lookups change nothing ------------------------------------

// snapshot is the server's state as its own routes report it: every item in
// full, every user with their progress and bookmarks, collections, playlists,
// series, authors, filter data and the server-wide vocabulary. The volatile
// bookkeeping every request moves (a user's lastSeen) is left out.
func snapshot(t *testing.T) map[string]any {
	t.Helper()

	snap := map[string]any{}
	libs, _ := adminGet(t, "/api/libraries").(map[string]any)["libraries"].([]any)
	snap["libraries"] = libs
	for _, l := range libs {
		lib, _ := l.(map[string]any)
		id, _ := lib["id"].(string)
		name, _ := lib["name"].(string)

		listing, _ := adminGet(t, "/api/libraries/"+id+"/items?limit=500").(map[string]any)
		results, _ := listing["results"].([]any)
		ids := make([]string, 0, len(results))
		for _, r := range results {
			row, _ := r.(map[string]any)
			id, _ := row["id"].(string)
			ids = append(ids, id)
		}
		slices.Sort(ids)
		var expanded any = []any{}
		if len(ids) > 0 {
			status, out, err := rawJSON(http.MethodPost, "/api/items/batch/get", os.Getenv("ABS_TOKEN"), map[string]any{"libraryItemIds": ids})
			if err != nil || status != http.StatusOK {
				t.Fatalf("batch get for %s: HTTP %d %v", name, status, err)
			}
			expanded = out
		}
		snap[name+"/items"] = expanded
		snap[name+"/filterdata"] = adminGet(t, "/api/libraries/"+id+"/filterdata")
		snap[name+"/series"] = adminGet(t, "/api/libraries/"+id+"/series?limit=500")
		snap[name+"/authors"] = adminGet(t, "/api/libraries/"+id+"/authors")
		snap[name+"/collections"] = adminGet(t, "/api/libraries/"+id+"/collections")
		snap[name+"/playlists"] = adminGet(t, "/api/libraries/"+id+"/playlists")
		snap[name+"/narrators"] = adminGet(t, "/api/libraries/"+id+"/narrators")
	}
	users, _ := adminGet(t, "/api/users").(map[string]any)["users"].([]any)
	for _, u := range users {
		id, _ := u.(map[string]any)["id"].(string)
		full, _ := adminGet(t, "/api/users/"+id).(map[string]any)
		delete(full, "lastSeen")
		snap["user/"+id] = full
	}
	snap["tags"] = adminGet(t, "/api/tags")
	snap["genres"] = adminGet(t, "/api/genres")
	snap["sessions"] = adminGet(t, "/api/sessions?itemsPerPage=100")

	return snap
}

// diff lists the paths where two decoded JSON values differ.
func diff(path string, a, b any) []string {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return []string{path}
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		var out []string
		for k := range keys {
			out = append(out, diff(path+"."+k, av[k], bv[k])...)
		}
		slices.Sort(out)
		return out
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return []string{fmt.Sprintf("%s (length %d -> %v)", path, len(av), lenOf(b))}
		}
		var out []string
		for i := range av {
			out = append(out, diff(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i])...)
		}
		return out
	default:
		if fmt.Sprint(a) != fmt.Sprint(b) {
			return []string{fmt.Sprintf("%s: %v -> %v", path, a, b)}
		}
		return nil
	}
}

func lenOf(v any) any {
	if l, ok := v.([]any); ok {
		return len(l)
	}
	return v
}

// readCall is one read-only tool call a journey makes, and whether an error
// from it is expected (a podcast with no feed has no feed to read).
type readCall struct {
	tool   string
	args   map[string]any
	mayErr bool
}

// Every read-only tool, run over every kind of fixture - with the lookups
// that ask a provider, the audits, the stats and the history - and the
// server's state read before and after must be identical. This is what makes
// --read-only trustworthy: a tool annotated read-only that writes would
// change the snapshot. A new read-only tool fails the test until it is added
// here.
func TestJourneyLookupsChangeNothing(t *testing.T) {
	requireProviders(t)

	// something to look at in the organise tools
	call(t, "collection_create", map[string]any{"library": "Fiction", "name": "Zzyzx Lookup Collection", "items": []any{"Foundation"}})
	t.Cleanup(func() { call(t, "collection_delete", map[string]any{"collection": "Zzyzx Lookup Collection"}) })
	call(t, "playlist_create", map[string]any{"library": "Fiction", "name": "Zzyzx Lookup Playlist", "entries": []any{map[string]any{"item": "Foundation"}}})
	t.Cleanup(func() { call(t, "playlist_delete", map[string]any{"playlist": "Zzyzx Lookup Playlist"}) })
	episodes := rows(t, call(t, "podcast_episodes", map[string]any{"item": "Behind the Bastards"})["episodes"], "episodes")
	if len(episodes) == 0 {
		t.Fatal("no podcast episode to look at")
	}
	episode, _ := episodes[0]["id"].(string)
	waitIdle(t)

	calls := []readCall{
		{tool: "server_info"},
		{tool: "library_list"},
		{tool: "library_get", args: map[string]any{"library": "Messy"}},
		{tool: "library_items", args: map[string]any{"library": "Messy", "limit": 50}},
		{tool: "library_search", args: map[string]any{"query": "Foundation"}},
		{tool: "library_filters", args: map[string]any{"library": "Messy"}},
		{tool: "library_recent"},
		{tool: "item_get", args: map[string]any{"library": "Fiction", "item": "Foundation", "files": true, "chapters": true}},
		{tool: "item_get", args: map[string]any{"item": "Behind the Bastards"}},
		// the seeded books are a second long, too short to compare by ear
		{tool: "item_compare_audio", args: map[string]any{"library": "Fiction", "item": "Foundation", "other": "Second Foundation"}, mayErr: true},
		{tool: "author_list", args: map[string]any{"library": "Messy"}},
		{tool: "author_get", args: map[string]any{"library": "Fiction", "author": "Isaac Asimov"}},
		{tool: "author_match", args: map[string]any{"library": "Fiction", "author": "Isaac Asimov", "query": "Isaac Asimov"}},
		{tool: "series_list"},
		{tool: "series_get", args: map[string]any{"library": "Fiction", "series": "Foundation"}},
		{tool: "narrator_list", args: map[string]any{"library": "Messy"}},
		{tool: "server_tags"},
		{tool: "item_match", args: map[string]any{"item": "Second Foundation", "provider": "audible", "title": "Second Foundation", "author": "Isaac Asimov"}},
		{tool: "item_match_batch", args: map[string]any{"library": "Fiction", "filter": "series:Foundation", "providers": []any{"audible"}, "candidates": 2}},
		{tool: "item_cover_search", args: map[string]any{"item": "Foundation and Empire", "provider": "audible", "title": "Foundation and Empire", "author": "Isaac Asimov"}},
		{tool: "audit_all", args: map[string]any{"library": "Messy", "deep": true}},
		{tool: "audit_authors", args: map[string]any{"library": "Messy"}},
		{tool: "audit_chapters", args: map[string]any{"library": "Messy"}},
		{tool: "audit_covers", args: map[string]any{"library": "Messy", "banner": true}},
		{tool: "audit_duplicates", args: map[string]any{"library": "Messy"}},
		{tool: "audit_genres", args: map[string]any{"library": "Messy"}},
		{tool: "audit_issues", args: map[string]any{"library": "Messy"}},
		{tool: "audit_matched", args: map[string]any{"library": "Messy", "providers": []any{"audible"}, "fields": true}},
		{tool: "audit_missing", args: map[string]any{"library": "Messy", "field": "description"}},
		{tool: "audit_narrators", args: map[string]any{"library": "Messy"}},
		{tool: "audit_no_audio", args: map[string]any{"library": "Messy"}},
		{tool: "audit_path", args: map[string]any{"library": "Messy", "files": true}},
		{tool: "audit_podcast_no_episodes", args: map[string]any{"library": "Podcasts"}},
		{tool: "audit_podcast_stale_feed", args: map[string]any{"library": "Podcasts"}},
		{tool: "audit_series", args: map[string]any{"library": "Messy", "articles": true}},
		{tool: "audit_spelling", args: map[string]any{"library": "Messy"}},
		{tool: "audit_unembedded", args: map[string]any{"library": "Messy"}},
		{tool: "audit_unmatched", args: map[string]any{"library": "Messy"}},
		// the seeded books are a second long, too short to search a store for
		{tool: "audit_abridged", args: map[string]any{"library": "Messy"}},
		{tool: "collection_list"},
		{tool: "collection_get", args: map[string]any{"collection": "Zzyzx Lookup Collection"}},
		{tool: "playlist_list"},
		{tool: "playlist_get", args: map[string]any{"playlist": "Zzyzx Lookup Playlist"}},
		{tool: "podcast_episodes", args: map[string]any{"item": "Behind the Bastards"}},
		{tool: "podcast_episode_get", args: map[string]any{"item": "Behind the Bastards", "episode": episode}},
		{tool: "podcast_downloads", args: map[string]any{"library": "Podcasts"}},
		// the seeded shows were laid out on disk, so they have no feed to read
		{tool: "podcast_feed_episodes", args: map[string]any{"item": "Behind the Bastards"}, mayErr: true},
		{tool: "podcast_search", args: map[string]any{"query": "Well There's Your Problem", "limit": 5}},
		{tool: "user_get"},
		{tool: "user_list"},
		{tool: "user_in_progress"},
		{tool: "user_progress_get", args: map[string]any{"library": "Fiction", "item": "Foundation"}},
		{tool: "user_bookmarks"},
		{tool: "user_history"},
		{tool: "user_stats"},
		{tool: "user_stats", args: map[string]any{"year": 2026}},
		{tool: "server_sessions"},
		{tool: "server_tasks"},
		{tool: "server_backups"},
	}
	// previews promise the same: they decide and change nothing
	previews := []readCall{
		{tool: "item_match_apply", args: map[string]any{"library": "Messy", "item": "Foundation (Unabridged)", "provider": "audible", "asin": "B003D8W5VS", "smart": true, "preview": true}},
		{tool: "item_match_apply_batch", args: map[string]any{"matches": []any{map[string]any{"item": "Foundation (Unabridged)", "asin": "B003D8W5VS"}}, "provider": "audible", "smart": true, "preview": true}},
	}

	// every read-only tool the server registers must be in the list
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			continue
		}
		if !slices.ContainsFunc(calls, func(c readCall) bool { return c.tool == tool.Name }) {
			t.Errorf("%s is read-only but this journey does not call it", tool.Name)
		}
	}

	before := snapshot(t)
	for _, c := range slices.Concat(calls, previews) {
		if _, err := invoke(c.tool, c.args); err != nil && !c.mayErr {
			t.Errorf("%s %v: %v", c.tool, c.args, err)
		}
	}
	waitIdle(t)
	after := snapshot(t)

	if changed := diff("", before, after); len(changed) > 0 {
		t.Errorf("the lookups changed the server:\n  %s", strings.Join(changed, "\n  "))
	}
}

// --- journey 1: edits while a library scan runs ------------------------------

// A scan that re-reads every book in a library, with a collection, a playlist
// and item edits made while it runs. In Emby a scan saved playlists as it had
// found them and lost the adds; here the final state is read back once the
// server is idle, entry by entry.
func TestJourneyEditsDuringAScan(t *testing.T) {
	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}

	// every file looks changed, so the scan updates each book rather than
	// skipping it as untouched
	future := time.Now().Add(time.Minute)
	if err := filepath.WalkDir(filepath.Join(data, "messy"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return os.Chtimes(p, future, future)
	}); err != nil {
		t.Fatal(err)
	}

	ids := map[string]string{}
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy", "limit": 100})["items"], "items") {
		path, _ := it["path"].(string)
		ids[path], _ = it["id"].(string)
	}
	id := func(relPath string) string {
		if ids[relPath] == "" {
			t.Fatalf("no book at %s", relPath)
		}
		return ids[relPath]
	}
	mort, sourcery := id("Terry Pratchett/Discworld - 04 - Mort"), id("Terry Pratchett/Discworld - 05 - Sourcery")
	eric, reaper := id("Discworld - 09 - Eric.m4b"), id("Terry Pratchett/Discworld - 11 - Reaper Man")
	wyrd, witches, rites := id("Terry Pratchett/Discworld - 06 - Wyrd Sisters"), id("Terry Pratchett/Discworld - 12 - Witches Abroad"), id("Terry Pratchett/Discworld - 03 - Equal Rites")
	colour := id("Terry Pratchett/Discworld - 01 - The Colour of Magic")

	t.Cleanup(func() {
		waitIdle(t)
		eventually(t, "deleting the scan collection", func() error {
			_, err := invoke("collection_delete", map[string]any{"collection": "Zzyzx Scan Shelf"})
			return err
		})
		eventually(t, "deleting the scan playlist", func() error {
			_, err := invoke("playlist_delete", map[string]any{"playlist": "Zzyzx Scan Queue"})
			return err
		})
		call(t, "item_edit", map[string]any{"item": colour, "remove_tags": []any{"zzyzx-scan"}, "remove_series": []any{"Zzyzx Scan Saga"}})
		call(t, "series_edit", map[string]any{"library": "Messy", "series": "Discworld", "description": " "})
	})

	call(t, "library_scan", map[string]any{"library": "Messy"})

	// while it runs
	call(t, "collection_create", map[string]any{"library": "Messy", "name": "Zzyzx Scan Shelf", "items": []any{mort, sourcery}})
	call(t, "collection_books_edit", map[string]any{"collection": "Zzyzx Scan Shelf", "action": "add", "items": []any{eric, reaper}})
	call(t, "collection_books_edit", map[string]any{"collection": "Zzyzx Scan Shelf", "action": "remove", "items": []any{sourcery}})
	call(t, "playlist_create", map[string]any{"library": "Messy", "name": "Zzyzx Scan Queue", "entries": []any{map[string]any{"item": wyrd}}})
	call(t, "playlist_entries_edit", map[string]any{"playlist": "Zzyzx Scan Queue", "action": "add", "entries": []any{map[string]any{"item": witches}, map[string]any{"item": rites}}})
	call(t, "playlist_entries_edit", map[string]any{"playlist": "Zzyzx Scan Queue", "action": "remove", "entries": []any{map[string]any{"item": wyrd}}})
	call(t, "item_edit", map[string]any{"item": colour, "add_tags": []any{"zzyzx-scan"}, "add_series": []any{"Zzyzx Scan Saga #1"}})
	call(t, "series_edit", map[string]any{"library": "Messy", "series": "Discworld", "description": "Zzyzx: edited mid-scan"})

	waitIdle(t)

	// and afterwards, everything is as it was left
	col := call(t, "collection_get", map[string]any{"collection": "Zzyzx Scan Shelf"})
	var colIDs, colTitles []string
	for _, b := range rows(t, col["items"], "items") {
		colIDs = append(colIDs, text(b["id"]))
		colTitles = append(colTitles, text(b["title"]))
	}
	// the created book first; a batch add lands in the server's own order,
	// not the order it was asked in
	if len(colIDs) != 3 || colIDs[0] != mort || !slices.Contains(colIDs, eric) || !slices.Contains(colIDs, reaper) {
		t.Errorf("collection = %v, want Mort, then Eric and Reaper Man", colTitles)
	}
	pl := call(t, "playlist_get", map[string]any{"playlist": "Zzyzx Scan Queue"})
	var plIDs []string
	for _, e := range rows(t, pl["entries"], "entries") {
		it, _ := e["item"].(map[string]any)
		plIDs = append(plIDs, text(it["id"]))
	}
	if !slices.Equal(plIDs, []string{witches, rites}) {
		t.Errorf("playlist = %v, want Witches Abroad then Equal Rites", plIDs)
	}
	book := call(t, "item_get", map[string]any{"item": colour})
	if tags := strs(t, book["tags"], "tags"); !slices.Contains(tags, "zzyzx-scan") {
		t.Errorf("tags = %v, want the tag added mid-scan", tags)
	}
	if series := strs(t, book["series"], "series"); !slices.Contains(series, "Zzyzx Scan Saga #1") || !slices.Contains(series, "Discworld #01") {
		t.Errorf("series = %v, want the added series beside Discworld #01", series)
	}
	if d, _ := call(t, "series_get", map[string]any{"library": "Messy", "series": "Discworld"})["description"].(string); d != "Zzyzx: edited mid-scan" {
		t.Errorf("series description = %q", d)
	}
	// the scan re-read every book without re-titling any from its folder
	if total := num(t, call(t, "library_items", map[string]any{"library": "Messy", "limit": 1})["total"], "total"); total != len(messyBooks) {
		t.Errorf("Messy has %d books after the scan, want %d", total, len(messyBooks))
	}
	if title := call(t, "item_get", map[string]any{"item": id("Terry Pratchett/Discworld - 07 - Pyramids")})["title"]; title != "Small Gods" {
		t.Errorf("the scan re-titled Small Gods from its folder: %v", title)
	}
}

// --- journey 4: audit_all is each audit ----------------------------------------

// audit_all's counts, for every library and for the whole server, match what
// each audit reports when run alone, row by row; every audit it calls clean
// finds nothing alone; and what it scanned is every item there is.
func TestJourneyAuditAllIsEachAudit(t *testing.T) {
	requireProviders(t)

	for _, library := range []string{"Fiction", "Non-Fiction", "Podcasts", "Messy", ""} {
		t.Run("library "+strings.Trim(library+" ", " ")+"/", func(t *testing.T) {
			args := func(extra map[string]any) map[string]any {
				out := map[string]any{}
				if library != "" {
					out["library"] = library
				}
				for k, v := range extra {
					out[k] = v
				}
				return out
			}
			all := call(t, "audit_all", args(map[string]any{"deep": true}))

			items := 0
			for _, l := range libraries {
				if library == "" || l.Name == library {
					items += num(t, call(t, "library_items", map[string]any{"library": l.Name, "limit": 1})["total"], "total")
				}
			}
			if scanned := num(t, all["items_scanned"], "items_scanned"); scanned != items {
				t.Errorf("items_scanned = %d, want every item, %d", scanned, items)
			}

			total := 0
			for _, row := range rows(t, all["audits"], "audits") {
				name, _ := row["audit"].(string)
				field, _ := row["field"].(string)
				found := num(t, row["found"], "found")
				total += found
				extra := map[string]any{}
				if field != "" {
					extra["field"] = field
				}
				out, err := invoke(name, args(extra))
				if err != nil {
					t.Errorf("%s %s: audit_all counts %d, the audit alone fails: %v", name, field, found, err)
					continue
				}
				if got := num(t, out["total_findings"], "total_findings"); got != found {
					t.Errorf("%s %s: audit_all says %d, the audit says %d", name, field, found, got)
				}
			}
			if sum := num(t, all["total_findings"], "total_findings"); sum != total {
				t.Errorf("total_findings = %d, the rows add up to %d", sum, total)
			}
			for _, name := range strs(t, all["clean"], "clean") {
				// a clean audit_missing field is listed as "audit_missing <field>"
				tool, field, _ := strings.Cut(name, " ")
				extra := map[string]any{}
				if field != "" {
					extra["field"] = field
				}
				out, err := invoke(tool, args(extra))
				if err != nil {
					t.Errorf("%s is clean in audit_all, the audit alone fails: %v", name, err)
					continue
				}
				if got := num(t, out["total_findings"], "total_findings"); got != 0 {
					t.Errorf("%s is clean in audit_all, the audit finds %d", name, got)
				}
			}
		})
	}
}

// --- journey 8: writes done twice ----------------------------------------------

// Every write that can be repeated is repeated, and the second call changes
// nothing, or says exactly what it did: the server answers 200 to adding what
// a collection holds and to removing what it does not, and changes nothing.
func TestJourneyWritesDoneTwice(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}

	t.Run("progress finished twice", func(t *testing.T) {
		t.Cleanup(func() {
			_, _ = invoke("user_progress_remove", map[string]any{"library": "Fiction", "item": "City of Golden Shadow"})
		})
		first := call(t, "user_progress_set", map[string]any{"library": "Fiction", "item": "City of Golden Shadow", "finished": true})
		second := call(t, "user_progress_set", map[string]any{"library": "Fiction", "item": "City of Golden Shadow", "finished": true})
		p1, _ := first["progress"].(map[string]any)
		p2, _ := second["progress"].(map[string]any)
		if p1["progress_id"] != p2["progress_id"] || p1["finished_at"] != p2["finished_at"] {
			t.Errorf("finishing a finished book changed its record: %v then %v", p1, p2)
		}
		if done, _ := call(t, "user_progress_remove", map[string]any{"library": "Fiction", "item": "City of Golden Shadow"})["removed"].(bool); !done {
			t.Error("the first removal removed nothing")
		}
		if done, _ := call(t, "user_progress_remove", map[string]any{"library": "Fiction", "item": "City of Golden Shadow"})["removed"].(bool); done {
			t.Error("the second removal said it removed something")
		}
	})

	t.Run("a book a collection holds, added and removed twice", func(t *testing.T) {
		call(t, "collection_create", map[string]any{"library": "Fiction", "name": "Zzyzx Twice Shelf", "items": []any{"Foundation", "Second Foundation"}})
		t.Cleanup(func() { call(t, "collection_delete", map[string]any{"collection": "Zzyzx Twice Shelf"}) })

		out := call(t, "collection_books_edit", map[string]any{"collection": "Zzyzx Twice Shelf", "action": "add", "items": []any{"Foundation"}})
		if got := strs(t, out["already_held"], "already_held"); !slices.Equal(got, []string{"Foundation"}) || out["added"] != nil {
			t.Errorf("adding a held book: %v", out)
		}
		removed := call(t, "collection_books_edit", map[string]any{"collection": "Zzyzx Twice Shelf", "action": "remove", "items": []any{"Second Foundation"}})
		again := call(t, "collection_books_edit", map[string]any{"collection": "Zzyzx Twice Shelf", "action": "remove", "items": []any{"Second Foundation"}})
		if !slices.Equal(strs(t, removed["removed"], "removed"), []string{"Second Foundation"}) || !slices.Equal(strs(t, again["not_held"], "not_held"), []string{"Second Foundation"}) {
			t.Errorf("removing twice: %v then %v", removed, again)
		}
		if n := len(rows(t, call(t, "collection_get", map[string]any{"collection": "Zzyzx Twice Shelf"})["items"], "items")); n != 1 {
			t.Errorf("the collection holds %d books, want 1", n)
		}
	})

	t.Run("a playlist entry, added and removed twice", func(t *testing.T) {
		call(t, "playlist_create", map[string]any{"library": "Fiction", "name": "Zzyzx Twice Queue", "entries": []any{map[string]any{"item": "Foundation"}, map[string]any{"item": "Leviathan Wakes"}}})
		t.Cleanup(func() { call(t, "playlist_delete", map[string]any{"playlist": "Zzyzx Twice Queue"}) })

		first := call(t, "playlist_entries_edit", map[string]any{"playlist": "Zzyzx Twice Queue", "action": "add", "entries": []any{map[string]any{"item": "Abaddon's Gate"}}})
		second := call(t, "playlist_entries_edit", map[string]any{"playlist": "Zzyzx Twice Queue", "action": "add", "entries": []any{map[string]any{"item": "Abaddon's Gate"}}})
		if !slices.Equal(strs(t, first["added"], "added"), []string{"Abaddon's Gate"}) || !slices.Equal(strs(t, second["already_held"], "already_held"), []string{"Abaddon's Gate"}) {
			t.Errorf("adding twice: %v then %v", first, second)
		}
		if n := num(t, second["entries"], "entries"); n != 3 {
			t.Errorf("entries = %d after the second add, want 3", n)
		}
		call(t, "playlist_entries_edit", map[string]any{"playlist": "Zzyzx Twice Queue", "action": "remove", "entries": []any{map[string]any{"item": "Foundation"}}})
		again := call(t, "playlist_entries_edit", map[string]any{"playlist": "Zzyzx Twice Queue", "action": "remove", "entries": []any{map[string]any{"item": "Foundation"}}})
		if !slices.Equal(strs(t, again["not_held"], "not_held"), []string{"Foundation"}) {
			t.Errorf("the second removal: %v", again)
		}
		if n := len(rows(t, call(t, "playlist_get", map[string]any{"playlist": "Zzyzx Twice Queue"})["entries"], "entries")); n != 2 {
			t.Errorf("the playlist holds %d entries, want 2", n)
		}
	})

	t.Run("a tag and a series added twice", func(t *testing.T) {
		t.Cleanup(func() {
			call(t, "item_edit", map[string]any{"library": "Fiction", "item": "Foundation", "remove_tags": []any{"zzyzx-twice"}, "remove_series": []any{"Zzyzx Twice Saga"}})
		})
		for i, want := range []bool{true, false} {
			out := call(t, "item_edit", map[string]any{"library": "Fiction", "item": "Foundation", "add_tags": []any{"zzyzx-twice"}, "add_series": []any{"Zzyzx Twice Saga #1"}})
			if updated, _ := out["updated"].(bool); updated != want {
				t.Errorf("call %d: updated = %v, want %v", i+1, updated, want)
			}
		}
		book := call(t, "item_get", map[string]any{"library": "Fiction", "item": "Foundation"})
		tags, series := strs(t, book["tags"], "tags"), strs(t, book["series"], "series")
		if countOf(tags, "zzyzx-twice") != 1 || countOf(series, "Zzyzx Twice Saga #1") != 1 {
			t.Errorf("tags %v, series %v: want each once", tags, series)
		}
		out := call(t, "item_batch_edit", map[string]any{"library": "Fiction", "items": []any{"Foundation"}, "add_tags": []any{"zzyzx-twice"}})
		if n := num(t, out["items_updated"], "items_updated"); n != 0 {
			t.Errorf("item_batch_edit add_tags of a held tag updated %d", n)
		}
	})

	t.Run("a rename done twice", func(t *testing.T) {
		t.Cleanup(func() {
			call(t, "metadata_rename", map[string]any{"field": "tags", "from": "zzyzx-space-opera", "to": "space-opera"})
		})
		first := call(t, "metadata_rename", map[string]any{"field": "tags", "from": "space-opera", "to": "zzyzx-space-opera"})
		if n := num(t, first["items_updated"], "items_updated"); n != 2 {
			t.Errorf("first rename updated %d, want the 2 Expanse books", n)
		}
		second := call(t, "metadata_rename", map[string]any{"field": "tags", "from": "space-opera", "to": "zzyzx-space-opera"})
		if n := num(t, second["items_updated"], "items_updated"); n != 0 {
			t.Errorf("second rename updated %d, want 0", n)
		}
		if tags := strs(t, call(t, "server_tags", map[string]any{"kind": "tags"})["tags"], "tags"); slices.Contains(tags, "space-opera") || !slices.Contains(tags, "zzyzx-space-opera") {
			t.Errorf("tags after the renames = %v", tags)
		}
	})

	t.Run("a bookmark added twice", func(t *testing.T) {
		t.Cleanup(func() {
			call(t, "user_bookmark_edit", map[string]any{"library": "Fiction", "item": "Leviathan Wakes", "action": "remove", "time_s": 0.5})
		})
		call(t, "user_bookmark_edit", map[string]any{"library": "Fiction", "item": "Leviathan Wakes", "action": "add", "time_s": 0.5, "title": "Zzyzx once"})
		call(t, "user_bookmark_edit", map[string]any{"library": "Fiction", "item": "Leviathan Wakes", "action": "add", "time_s": 0.5, "title": "Zzyzx twice"})
		marks := rows(t, call(t, "user_bookmarks", map[string]any{"library": "Fiction", "item": "Leviathan Wakes"})["bookmarks"], "bookmarks")
		if len(marks) != 1 || marks[0]["title"] != "Zzyzx twice" {
			t.Errorf("bookmarks = %v, want one, renamed", marks)
		}
	})

	t.Run("a series merged twice", func(t *testing.T) {
		t.Cleanup(func() { restoreBook(t, "Foundation") })
		call(t, "item_edit", map[string]any{"library": "Fiction", "item": "Foundation", "add_series": []any{"Zzyzx Foundation Saga #1"}})
		call(t, "series_merge", map[string]any{"library": "Fiction", "from": "Zzyzx Foundation Saga", "into": "Foundation"})
		if msg := callErr(t, "series_merge", map[string]any{"library": "Fiction", "from": "Zzyzx Foundation Saga", "into": "Foundation"}); !strings.Contains(msg, "no series named") {
			t.Errorf("merging a series that is gone: %s", msg)
		}
	})
}

// countOf counts a value in a list.
func countOf(list []string, v string) int {
	n := 0
	for _, s := range list {
		if s == v {
			n++
		}
	}
	return n
}

// --- journey 9: resolve by id and by name, everywhere ------------------------

// Every list tool's rows are got again by id and by name, and both come back
// as the same record. A name two records share is refused with both ids,
// and a create under a taken name is refused rather than making a second.
func TestJourneyResolveByIDAndName(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}
	admin := adminClient(t)

	t.Run("libraries", func(t *testing.T) {
		for _, l := range rows(t, call(t, "library_list", nil)["libraries"], "libraries") {
			byID, byName := call(t, "library_get", map[string]any{"library": l["id"]}), call(t, "library_get", map[string]any{"library": l["name"]})
			if byID["id"] != l["id"] || byName["id"] != l["id"] {
				t.Errorf("%v: by id %v, by name %v", l["name"], byID["id"], byName["id"])
			}
		}
	})

	t.Run("items", func(t *testing.T) {
		for _, lib := range []string{"Fiction", "Non-Fiction", "Messy"} {
			listed := rows(t, call(t, "library_items", map[string]any{"library": lib, "limit": 100})["items"], "items")
			seen := map[string]int{}
			for _, it := range listed {
				seen[text(it["title"])]++
			}
			for _, it := range listed {
				title := text(it["title"])
				if byID := call(t, "item_get", map[string]any{"item": it["id"]}); byID["id"] != it["id"] {
					t.Errorf("%s by id = %v", title, byID["id"])
				}
				if seen[title] > 1 {
					if msg := callErr(t, "item_get", map[string]any{"library": lib, "item": title}); !strings.Contains(msg, text(it["id"])) {
						t.Errorf("%s is two books; the refusal does not list %v: %s", title, it["id"], msg)
					}
					continue
				}
				if byName := call(t, "item_get", map[string]any{"library": lib, "item": title}); byName["id"] != it["id"] {
					t.Errorf("%s by title = %v, want %v", title, byName["id"], it["id"])
				}
			}
		}
	})

	t.Run("authors and series", func(t *testing.T) {
		for _, lib := range []string{"Fiction", "Messy"} {
			for _, a := range rows(t, call(t, "author_list", map[string]any{"library": lib, "limit": 100})["authors"], "authors") {
				if got := call(t, "author_get", map[string]any{"author": a["id"]}); got["id"] != a["id"] {
					t.Errorf("author %v by id = %v", a["name"], got["id"])
				}
				if got := call(t, "author_get", map[string]any{"library": lib, "author": a["name"]}); got["id"] != a["id"] {
					t.Errorf("author %v by name = %v", a["name"], got["id"])
				}
			}
			for _, s := range rows(t, call(t, "series_list", map[string]any{"library": lib, "limit": 100})["series"], "series") {
				if got := call(t, "series_get", map[string]any{"series": s["id"]}); got["id"] != s["id"] {
					t.Errorf("series %v by id = %v", s["name"], got["id"])
				}
				if got := call(t, "series_get", map[string]any{"library": lib, "series": s["name"]}); got["id"] != s["id"] {
					t.Errorf("series %v by name = %v", s["name"], got["id"])
				}
			}
		}
		// Isaac Asimov writes in two libraries: by name alone that is two
		// records, named
		if msg := callErr(t, "author_get", map[string]any{"author": "Isaac Asimov"}); !strings.Contains(msg, "pass an id") {
			t.Errorf("an author in two libraries: %s", msg)
		}
	})

	t.Run("a series renamed, found by the name it has now", func(t *testing.T) {
		// the server's filter data keeps a series under its old name for half
		// an hour after a rename; every lookup by name has to see the new one
		id := text(call(t, "series_get", map[string]any{"library": "Fiction", "series": "Otherland"})["id"])
		t.Cleanup(func() { call(t, "series_edit", map[string]any{"series": id, "name": "Otherland"}) })

		call(t, "series_edit", map[string]any{"library": "Fiction", "series": "Otherland", "name": "Zzyzx Otherland"})
		if got := call(t, "series_get", map[string]any{"library": "Fiction", "series": "Zzyzx Otherland"}); got["id"] != id {
			t.Errorf("by the new name = %v, want %s", got["id"], id)
		}
		if msg := callErr(t, "series_get", map[string]any{"library": "Fiction", "series": "Otherland"}); !strings.Contains(msg, "no series named") {
			t.Errorf("the old name still resolves: %s", msg)
		}
		if total := num(t, call(t, "library_items", map[string]any{"library": "Fiction", "filter": "series:Zzyzx Otherland"})["total"], "total"); total != 2 {
			t.Errorf("a filter by the new name finds %d books, want 2", total)
		}
		names := valuesIn(t, call(t, "library_filters", map[string]any{"library": "Fiction"})["series"], "series", "name")
		if !slices.Contains(names, "Zzyzx Otherland") || slices.Contains(names, "Otherland") {
			t.Errorf("library_filters series = %v, want the new name only", names)
		}
		// and renamed back by the name it has now
		call(t, "series_edit", map[string]any{"library": "Fiction", "series": "Zzyzx Otherland", "name": "Otherland"})
	})

	t.Run("users", func(t *testing.T) {
		for _, u := range rows(t, call(t, "user_list", nil)["users"], "users") {
			if got := call(t, "user_get", map[string]any{"user": u["id"]}); got["id"] != u["id"] {
				t.Errorf("user %v by id = %v", u["username"], got["id"])
			}
			if got := call(t, "user_get", map[string]any{"user": u["username"]}); got["id"] != u["id"] {
				t.Errorf("user %v by name = %v", u["username"], got["id"])
			}
		}
	})

	t.Run("podcast episodes", func(t *testing.T) {
		for _, show := range podcasts {
			for _, e := range rows(t, call(t, "podcast_episodes", map[string]any{"item": show})["episodes"], "episodes") {
				if got := call(t, "podcast_episode_get", map[string]any{"item": show, "episode": e["id"]}); got["id"] != e["id"] {
					t.Errorf("%s episode %v by id = %v", show, e["title"], got["id"])
				}
			}
		}
	})

	t.Run("collections sharing a name", func(t *testing.T) {
		made := call(t, "collection_create", map[string]any{"library": "Fiction", "name": "Zzyzx Shared", "items": []any{"Foundation"}})
		madeID, _ := made["id"].(string)
		t.Cleanup(func() {
			eventually(t, "deleting a collection", func() error { return admin.DeleteCollection(context.WithoutCancel(ctx), madeID) })
		})

		if msg := callErr(t, "collection_create", map[string]any{"library": "Fiction", "name": "zzyzx shared", "items": []any{"Second Foundation"}}); !strings.Contains(msg, madeID) {
			t.Errorf("a create under a taken name: %s", msg)
		}
		// the server itself makes a second happily; with two, a name is refused
		twin, err := admin.CreateCollection(ctx, libraryID(t, "Fiction"), "Zzyzx Shared", "", []string{itemID(t, "Fiction", "Second Foundation")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			eventually(t, "deleting a collection", func() error { return admin.DeleteCollection(context.WithoutCancel(ctx), twin.ID) })
		})
		msg := callErr(t, "collection_get", map[string]any{"collection": "Zzyzx Shared"})
		if !strings.Contains(msg, madeID) || !strings.Contains(msg, twin.ID) {
			t.Errorf("a name two collections share: %s", msg)
		}
		if msg := callErr(t, "collection_books_edit", map[string]any{"collection": "Zzyzx Shared", "action": "add", "items": []any{"Leviathan Wakes"}}); !strings.Contains(msg, "pass an id") {
			t.Errorf("an edit by a shared name: %s", msg)
		}
		for _, id := range []string{madeID, twin.ID} {
			if got := call(t, "collection_get", map[string]any{"collection": id}); got["id"] != id {
				t.Errorf("collection by id %s = %v", id, got["id"])
			}
		}
	})

	t.Run("a collection renamed, found by the name it has now", func(t *testing.T) {
		made := text(call(t, "collection_create", map[string]any{"library": "Fiction", "name": "Zzyzx Renamed Shelf", "items": []any{"Foundation"}})["id"])
		other := text(call(t, "collection_create", map[string]any{"library": "Fiction", "name": "Zzyzx Other Shelf", "items": []any{"Second Foundation"}})["id"])
		t.Cleanup(func() {
			for _, id := range []string{made, other} {
				eventually(t, "deleting a collection", func() error { return admin.DeleteCollection(context.WithoutCancel(ctx), id) })
			}
		})

		if msg := callErr(t, "collection_edit", map[string]any{"collection": other, "name": "zzyzx renamed shelf"}); !strings.Contains(msg, made) {
			t.Errorf("renaming onto a taken name: %s", msg)
		}
		call(t, "collection_edit", map[string]any{"collection": "Zzyzx Renamed Shelf", "name": "Zzyzx Shelf Anew"})
		if got := call(t, "collection_get", map[string]any{"collection": "Zzyzx Shelf Anew"}); got["id"] != made {
			t.Errorf("by the new name = %v, want %s", got["id"], made)
		}
		if msg := callErr(t, "collection_get", map[string]any{"collection": "Zzyzx Renamed Shelf"}); !strings.Contains(msg, "no collection named") {
			t.Errorf("the old name still resolves: %s", msg)
		}
		// renamed to the name it already has, in another case, is not a clash with itself
		call(t, "collection_edit", map[string]any{"collection": made, "name": "ZZYZX SHELF ANEW"})
	})

	t.Run("playlists sharing a name", func(t *testing.T) {
		made := call(t, "playlist_create", map[string]any{"library": "Fiction", "name": "Zzyzx Shared List", "entries": []any{map[string]any{"item": "Foundation"}}})
		madeID, _ := made["id"].(string)
		t.Cleanup(func() {
			eventually(t, "deleting a playlist", func() error { return admin.DeletePlaylist(context.WithoutCancel(ctx), madeID) })
		})

		if msg := callErr(t, "playlist_create", map[string]any{"library": "Fiction", "name": "Zzyzx Shared List"}); !strings.Contains(msg, madeID) {
			t.Errorf("a create under a taken name: %s", msg)
		}
		twin, err := admin.CreatePlaylist(ctx, libraryID(t, "Fiction"), "Zzyzx Shared List", "", []abs.PlaylistEntry{{LibraryItemID: itemID(t, "Fiction", "Second Foundation")}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			eventually(t, "deleting a playlist", func() error { return admin.DeletePlaylist(context.WithoutCancel(ctx), twin.ID) })
		})
		msg := callErr(t, "playlist_get", map[string]any{"playlist": "Zzyzx Shared List"})
		if !strings.Contains(msg, madeID) || !strings.Contains(msg, twin.ID) {
			t.Errorf("a name two playlists share: %s", msg)
		}
		if msg := callErr(t, "playlist_edit", map[string]any{"playlist": twin.ID, "name": "Zzyzx Shared List"}); !strings.Contains(msg, madeID) {
			t.Errorf("renaming onto a taken name: %s", msg)
		}
	})

	t.Run("libraries sharing a name", func(t *testing.T) {
		// Audiobookshelf takes a second library by an existing name too
		original := libraryID(t, "Podcasts")
		twin, err := admin.CreateLibrary(ctx, abs.LibraryCreate{Name: "Podcasts", MediaType: "book", Folders: []abs.Folder{{FullPath: "/scratch"}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			eventually(t, "deleting the twin library", func() error { return admin.DeleteLibrary(context.WithoutCancel(ctx), twin.ID) })
		})
		msg := callErr(t, "library_get", map[string]any{"library": "podcasts"})
		if !strings.Contains(msg, twin.ID) || !strings.Contains(msg, original) {
			t.Errorf("a name two libraries share: %s", msg)
		}
		if got := call(t, "library_get", map[string]any{"library": twin.ID}); got["id"] != twin.ID {
			t.Errorf("the twin by id = %v", got["id"])
		}
		if msg := callErr(t, "library_create", map[string]any{"name": "Fiction", "folders": []any{"/scratch"}}); !strings.Contains(msg, "already exists") {
			t.Errorf("a create under a taken name: %s", msg)
		}
	})
}
