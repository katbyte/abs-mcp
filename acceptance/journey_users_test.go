//go:build integration

// Journeys 3, 6 and 14: other accounts. A listener's playback has to reach
// every tool that reports on listening, whichever account asks; an account
// kept from some libraries or some books has to be kept from them by every
// tool that reads a library on its behalf; and an account without the rights
// to change the library has to be refused by every tool that would.
package acceptance

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/katbyte/abs-mcp/tools"
)

// A non-admin account plays a book the way an app does - open a session,
// sync, close - and every tool that reports on listening agrees about it,
// asked by the listener and asked by an admin naming them. The fixtures are
// one second long, so the times are too; what is asserted is that every field
// the tools promise is filled, and that the two views match.
func TestJourneyPlaybackReachesEveryListeningTool(t *testing.T) {
	requireProviders(t)

	listener := newUser(t, "zzyzx-listener", abs.UserCreate{})
	client, err := abs.New(os.Getenv("ABS_SERVER"), listener.Token)
	if err != nil {
		t.Fatal(err)
	}
	const book = "Sea of Silver Light"
	bookID := itemID(t, "Fiction", book)
	named := map[string]any{"user": listener.Name}

	session, err := client.Play(ctx, bookID, "", abs.PlayRequest{
		MediaPlayer: "zzyzx-player", SupportedMimeTypes: []string{"audio/mpeg"}, ForceDirectPlay: true,
		DeviceInfo: map[string]any{"clientName": "Zzyzx Client", "deviceName": "a journey"},
	})
	if err != nil {
		t.Fatalf("the listener opening a session: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if err := client.CloseSession(ctx, session.ID, nil); err != nil {
				t.Errorf("closing the session the test left open: %v", err)
			}
		}
	})
	if err := client.SyncSession(ctx, session.ID, 0.5, 0.5); err != nil {
		t.Fatalf("syncing: %v", err)
	}

	t.Run("while it plays", func(t *testing.T) {
		// server_sessions is an admin's view of everyone, with who is listening
		var open map[string]any
		for _, s := range rows(t, call(t, "server_sessions", nil)["sessions"], "sessions") {
			if s["id"] == session.ID {
				open = s
			}
		}
		if open == nil {
			t.Fatal("the listener's open session is not in server_sessions")
		}
		if open["user"] != listener.Name || open["user_id"] != listener.ID || open["title"] != book || open["item_id"] != bookID {
			t.Errorf("open session = %v, want %s playing %s", open, listener.Name, book)
		}
		if device, _ := open["device"].(string); !strings.Contains(device, "Zzyzx Client") {
			t.Errorf("device = %q", device)
		}
		if msg := listener.callErr(t, "server_sessions", nil); !strings.Contains(msg, "admin only") {
			t.Errorf("server_sessions for the listener: %s", msg)
		}

		// the continue-listening shelf, from both sides, with a position
		self := rows(t, listener.call(t, "user_in_progress", nil)["items"], "items")
		other := rows(t, call(t, "user_in_progress", named)["items"], "items")
		for who, shelf := range map[string][]map[string]any{"self": self, "admin": other} {
			if len(shelf) != 1 || shelf[0]["id"] != bookID {
				t.Errorf("%s: shelf = %v, want only %s", who, shelf, book)
				continue
			}
			progress, ok := shelf[0]["progress"].(map[string]any)
			if !ok || num(t, progress["percent"], "percent") != 50 {
				t.Errorf("%s: shelf progress = %v, want 50 percent", who, shelf[0]["progress"])
			}
		}

		selfProgress := listener.call(t, "user_progress_get", map[string]any{"library": "Fiction", "item": book})["progress"]
		adminProgress := call(t, "user_progress_get", map[string]any{"user": listener.Name, "library": "Fiction", "item": book})["progress"]
		if !sameJSON(selfProgress, adminProgress) || selfProgress == nil {
			t.Errorf("progress: the listener sees %v, an admin sees %v", selfProgress, adminProgress)
		}

		selfUser, adminUser := listener.call(t, "user_get", nil), call(t, "user_get", named)
		for _, key := range []string{"items_in_progress", "items_finished", "recent_progress"} {
			if !sameJSON(selfUser[key], adminUser[key]) {
				t.Errorf("user_get %s: the listener sees %v, an admin sees %v", key, selfUser[key], adminUser[key])
			}
		}
		if num(t, selfUser["items_in_progress"], "items_in_progress") != 1 {
			t.Errorf("items_in_progress = %v, want 1", selfUser["items_in_progress"])
		}
	})

	if err := client.CloseSession(ctx, session.ID, map[string]any{"currentTime": 0.6, "timeListened": 0.1}); err != nil {
		t.Fatalf("closing: %v", err)
	}
	closed = true

	t.Run("after it closes", func(t *testing.T) {
		for _, s := range rows(t, call(t, "server_sessions", nil)["sessions"], "sessions") {
			if s["id"] == session.ID {
				t.Errorf("the closed session is still open: %v", s)
			}
		}

		// the history, from both sides, naming the listener on every row
		for who, history := range map[string]map[string]any{"self": listener.call(t, "user_history", nil), "admin": call(t, "user_history", named)} {
			sessions := rows(t, history["sessions"], "sessions")
			if num(t, history["total_sessions"], "total_sessions") != 1 || len(sessions) != 1 {
				t.Errorf("%s: history = %v, want the one session", who, history)
				continue
			}
			s := sessions[0]
			if s["id"] != session.ID || s["title"] != book || s["user"] != listener.Name || s["listened_s"] == nil || s["started"] == nil {
				t.Errorf("%s: history row = %v, want the session with its user, time and start", who, s)
			}
		}

		// the stats, from both sides
		selfStats, adminStats := listener.call(t, "user_stats", nil), call(t, "user_stats", named)
		for _, key := range []string{"total_listened_s", "items_listened", "top_items"} {
			if !sameJSON(selfStats[key], adminStats[key]) || selfStats[key] == nil {
				t.Errorf("user_stats %s: the listener sees %v, an admin sees %v", key, selfStats[key], adminStats[key])
			}
		}
		year := listener.call(t, "user_stats", map[string]any{"year": 2026})
		if num(t, year["sessions"], "sessions") != 1 || num(t, year["books_listened"], "books_listened") != 1 {
			t.Errorf("year in review = %v, want one session of one book", year)
		}
		// the server keeps a year in review only for the caller, so an admin
		// asking for the listener's is told so rather than handed their own
		if msg := callErr(t, "user_stats", map[string]any{"user": listener.Name, "year": 2026}); !strings.Contains(msg, "only available") {
			t.Errorf("an admin asking for the listener's year in review: %q", msg)
		}

		// closing a session a second from the end of a one-second book marks
		// it finished: off the shelf, and counted
		if shelf := rows(t, listener.call(t, "user_in_progress", nil)["items"], "items"); len(shelf) != 0 {
			t.Errorf("a finished book is still on the shelf: %v", shelf)
		}
		selfUser, adminUser := listener.call(t, "user_get", nil), call(t, "user_get", named)
		if num(t, selfUser["items_finished"], "items_finished") != 1 || !sameJSON(selfUser["recent_progress"], adminUser["recent_progress"]) {
			t.Errorf("user_get after finishing: the listener sees %v, an admin sees %v", selfUser, adminUser)
		}

		// user_list says what they last listened to
		for _, u := range rows(t, call(t, "user_list", nil)["users"], "users") {
			if u["username"] == listener.Name && u["last_listened"] != book {
				t.Errorf("user_list last_listened = %v, want %s", u["last_listened"], book)
			}
		}
	})
}

// sameJSON reports whether two decoded values encode the same.
func sameJSON(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// An account limited to one library, and an account limited to books
// carrying one tag. user_get says so, and every tool that reads a library on
// their behalf shows them only what they may see.
func TestJourneyRestrictedAccounts(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}

	fiction := libraryID(t, "Fiction")
	// something in Non-Fiction a Fiction-only account must not see
	nf := call(t, "collection_create", map[string]any{"library": "Non-Fiction", "name": "Zzyzx Hidden Shelf", "items": []any{"War Is a Racket"}})
	t.Cleanup(func() { call(t, "collection_delete", map[string]any{"collection": nf["id"]}) })
	racket := itemID(t, "Non-Fiction", "War Is a Racket")

	t.Run("one library", func(t *testing.T) {
		no := false
		shelf := newUser(t, "zzyzx-one-library", abs.UserCreate{
			Permissions:         map[string]bool{"accessAllLibraries": no, "accessAllTags": true, "accessExplicitContent": true},
			LibrariesAccessible: []string{fiction},
		})

		for who, u := range map[string]map[string]any{"self": shelf.call(t, "user_get", nil), "admin": call(t, "user_get", map[string]any{"user": shelf.Name})} {
			if all, _ := u["all_libraries"].(bool); all {
				t.Errorf("%s: all_libraries = true for an account limited to Fiction", who)
			}
			if libs := strs(t, u["libraries"], "libraries"); !slices.Equal(libs, []string{fiction}) {
				t.Errorf("%s: libraries = %v, want only Fiction", who, libs)
			}
		}

		libs := rows(t, shelf.call(t, "library_list", nil)["libraries"], "libraries")
		if len(libs) != 1 || libs[0]["name"] != "Fiction" {
			t.Errorf("library_list = %v, want only Fiction", libs)
		}
		for _, tc := range []struct {
			tool string
			args map[string]any
		}{
			{"library_items", map[string]any{"library": "Non-Fiction"}},
			{"library_get", map[string]any{"library": libraryID(t, "Non-Fiction")}},
			{"library_filters", map[string]any{"library": "Non-Fiction"}},
			{"item_get", map[string]any{"item": "War Is a Racket"}},
			{"item_get", map[string]any{"item": racket}},
			{"audit_missing", map[string]any{"library": "Non-Fiction", "field": "cover"}},
			{"collection_get", map[string]any{"collection": "Zzyzx Hidden Shelf"}},
		} {
			shelf.callErr(t, tc.tool, tc.args)
		}
		if items := rows(t, shelf.call(t, "library_search", map[string]any{"query": "War"})["items"], "items"); len(items) != 0 {
			t.Errorf("library_search War = %v, want nothing from Non-Fiction", items)
		}
		for _, row := range rows(t, shelf.call(t, "collection_list", nil)["collections"], "collections") {
			if row["name"] == "Zzyzx Hidden Shelf" {
				t.Errorf("collection_list shows a Non-Fiction collection: %v", row)
			}
		}
		for _, it := range rows(t, shelf.call(t, "library_recent", nil)["items"], "items") {
			if it["id"] == racket {
				t.Error("library_recent shows a Non-Fiction book")
			}
		}
		if scanned := num(t, shelf.call(t, "audit_all", nil)["items_scanned"], "items_scanned"); scanned != 7 {
			t.Errorf("audit_all scanned %d, want Fiction's 7", scanned)
		}
	})

	t.Run("one tag", func(t *testing.T) {
		no := false
		tagged := newUser(t, "zzyzx-one-tag", abs.UserCreate{
			Permissions:      map[string]bool{"accessAllLibraries": true, "accessAllTags": no, "accessExplicitContent": true},
			ItemTagsSelected: []string{"cyberpunk"},
		})
		// Otherland is the only series tagged cyberpunk
		visible := []string{"City of Golden Shadow", "Sea of Silver Light"}

		u := tagged.call(t, "user_get", nil)
		if all, _ := u["all_tags"].(bool); all {
			t.Error("all_tags = true for an account limited to one tag")
		}
		if tags := strs(t, u["tags"], "tags"); !slices.Equal(tags, []string{"cyberpunk"}) {
			t.Errorf("tags = %v, want [cyberpunk]", tags)
		}

		var titles []string
		for _, it := range rows(t, tagged.call(t, "library_items", map[string]any{"library": "Fiction", "limit": 50})["items"], "items") {
			titles = append(titles, text(it["title"]))
		}
		slices.Sort(titles)
		if !slices.Equal(titles, visible) {
			t.Errorf("Fiction for the account = %v, want %v", titles, visible)
		}

		// the vocabulary the server caches per library names hidden books'
		// authors and narrators; the tools do not pass it on
		hidden := []string{"Isaac Asimov", "James S. A. Corey", "Scott Brick", "Jefferson Mays", "Foundation", "Leviathan Wakes", "Robert Evans", "Grover Gardner", "Warbreaker", "History"}
		for _, tc := range []struct {
			tool string
			args map[string]any
		}{
			{"library_filters", map[string]any{"library": "Fiction"}},
			{"library_filters", map[string]any{"library": "Non-Fiction"}},
			{"narrator_list", map[string]any{"library": "Fiction"}},
			{"library_get", map[string]any{"library": "Fiction"}},
			{"library_search", map[string]any{"query": "war"}},
			{"library_search", map[string]any{"query": "a"}},
			{"series_list", nil},
			{"author_list", map[string]any{"library": "Fiction"}},
		} {
			raw, err := json.Marshal(tagged.call(t, tc.tool, tc.args))
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range hidden {
				if strings.Contains(string(raw), `"`+h+`"`) {
					t.Errorf("%s %v names %q, which the account cannot see: %s", tc.tool, tc.args, h, raw)
				}
			}
		}
		get := tagged.call(t, "library_get", map[string]any{"library": "Fiction"})
		if num(t, get["items"], "items") != 2 || num(t, get["authors"], "authors") != 1 {
			t.Errorf("library_get Fiction = %v, want 2 items by 1 author", get)
		}
		if narrators := rows(t, tagged.call(t, "narrator_list", map[string]any{"library": "Fiction"})["narrators"], "narrators"); len(narrators) != 1 || narrators[0]["name"] != "George Newbern" || num(t, narrators[0]["books"], "books") != 2 {
			t.Errorf("narrator_list = %v, want George Newbern on 2", narrators)
		}
		tagged.callErr(t, "item_get", map[string]any{"library": "Fiction", "item": "Foundation"})
		if scanned := num(t, tagged.call(t, "audit_all", map[string]any{"library": "Fiction"})["items_scanned"], "items_scanned"); scanned != 2 {
			t.Errorf("audit_all scanned %d, want the 2 visible books", scanned)
		}
	})
}

// writeCall is one write a journey makes as another account. refused, when
// set, judges a reply that is not an error: the batch tools report a failed
// row rather than failing the call. mayPass is a call that has nothing to
// write with these fixtures, which the snapshot still has to agree with.
type writeCall struct {
	tool    string
	args    map[string]any
	refused func(out map[string]any) bool
	mayPass bool
}

// An account with no update, delete, upload or admin rights, kept out of
// Non-Fiction, calls every tool that changes the server with arguments that
// would work for an admin. Each is refused, and the server read back
// afterwards is as it was. Then its own listening and its own playlists,
// which need no rights, work in the libraries it can open. A new write tool
// fails the test until it is added to one list or the other.
func TestJourneyWritesAnAccountMayNotMake(t *testing.T) {
	requireProviders(t)

	fiction, messy, podcastsID := libraryID(t, "Fiction"), libraryID(t, "Messy"), libraryID(t, "Podcasts")
	no := false
	account := newUserWith(t, "zzyzx-no-rights", abs.UserCreate{
		Permissions: map[string]bool{
			"download": true, "update": no, "delete": no, "upload": no,
			"accessAllLibraries": no, "accessAllTags": true, "accessExplicitContent": true,
		},
		LibrariesAccessible: []string{fiction, messy, podcastsID},
	}, tools.Options{EnableDelete: true})

	racket := itemID(t, "Non-Fiction", "War Is a Racket")
	shelf := text(call(t, "collection_create", map[string]any{"library": "Fiction", "name": "Zzyzx Rights Shelf", "items": []any{"Foundation"}})["id"])
	t.Cleanup(func() { call(t, "collection_delete", map[string]any{"collection": shelf}) })
	episode := text(rows(t, call(t, "podcast_episodes", map[string]any{"item": "Behind the Bastards"})["episodes"], "episodes")[0]["id"])
	noneApplied := func(out map[string]any) bool { return num(t, out["applied"], "applied") == 0 }
	noneTagged := func(out map[string]any) bool { return num(t, out["tagged"], "tagged") == 0 }

	refused := []writeCall{
		{tool: "item_edit", args: map[string]any{"library": "Fiction", "item": "Foundation", "add_tags": []any{"zzyzx-refused"}}},
		{tool: "item_edit", args: map[string]any{"item": racket, "add_tags": []any{"zzyzx-refused"}}},
		{tool: "item_batch_edit", args: map[string]any{"library": "Fiction", "items": []any{"Foundation", "Second Foundation"}, "add_tags": []any{"zzyzx-refused"}}},
		{tool: "item_chapters_set", args: map[string]any{"library": "Fiction", "item": "Foundation", "chapters": []any{map[string]any{"title": "Zzyzx", "start_s": 0}}}},
		{tool: "item_cover_edit", args: map[string]any{"library": "Messy", "item": "Moving Pictures", "remove": true}},
		// Foundation has no asin to look a cover up by
		{tool: "item_cover_upgrade", args: map[string]any{"library": "Fiction", "items": []any{"Foundation"}}, mayPass: true},
		{tool: "item_match_apply", args: map[string]any{"library": "Messy", "item": "Foundation (Unabridged)", "provider": "audible", "asin": "B003D8W5VS", "override_details": true}},
		{tool: "item_match_apply_batch", args: map[string]any{"matches": []any{map[string]any{"item": "Foundation (Unabridged)", "asin": "B003D8W5VS"}}, "provider": "audible"}, refused: noneApplied},
		{tool: "item_match_tag", args: map[string]any{"library": "Messy", "overwrite": true}, refused: noneTagged},
		{tool: "item_rescan", args: map[string]any{"library": "Fiction", "item": "Foundation"}},
		{tool: "item_embed_metadata", args: map[string]any{"library": "Fiction", "item": "Foundation"}},
		{tool: "item_delete", args: map[string]any{"confirm": true, "library": "Fiction", "item": "Foundation"}},
		{tool: "library_create", args: map[string]any{"name": "Zzyzx Refused", "folders": []any{"/scratch"}}},
		{tool: "library_edit", args: map[string]any{"library": "Fiction", "name": "Zzyzx Refused Fiction"}},
		{tool: "library_scan", args: map[string]any{"library": "Fiction"}},
		// Fiction has no missing books, so there is nothing to send
		{tool: "library_issues_remove", args: map[string]any{"library": "Fiction", "confirm": true}, mayPass: true},
		{tool: "metadata_rename", args: map[string]any{"field": "tags", "from": "sf", "to": "zzyzx-refused-sf"}},
		{tool: "metadata_rename", args: map[string]any{"field": "publishers", "library": "Fiction", "from": "Bantam", "to": "Zzyzx Refused"}},
		{tool: "series_edit", args: map[string]any{"library": "Fiction", "series": "Foundation", "description": "Zzyzx refused"}},
		{tool: "series_merge", args: map[string]any{"library": "Fiction", "from": "Otherland", "into": "The Expanse"}},
		{tool: "author_edit", args: map[string]any{"library": "Fiction", "author": "Isaac Asimov", "description": "Zzyzx refused"}},
		{tool: "author_match_apply", args: map[string]any{"library": "Fiction", "author": "Isaac Asimov", "asin": "B000AP9A6K", "region": "us"}},
		{tool: "author_image_set", args: map[string]any{"library": "Fiction", "author": "Isaac Asimov", "url": "https://m.media-amazon.com/images/I/zzyzx-refused.jpg"}},
		{tool: "author_delete", args: map[string]any{"library": "Fiction", "author": "Tad Williams"}},
		{tool: "collection_create", args: map[string]any{"library": "Fiction", "name": "Zzyzx Refused Shelf", "items": []any{"Foundation"}}},
		{tool: "collection_edit", args: map[string]any{"collection": shelf, "name": "Zzyzx Refused Rename"}},
		{tool: "collection_books_edit", args: map[string]any{"collection": shelf, "action": "add", "items": []any{"Second Foundation"}}},
		{tool: "collection_delete", args: map[string]any{"collection": shelf}},
		{tool: "podcast_add", args: map[string]any{"feed_url": "http://zzyzx-refused.test/show.xml", "library": "Podcasts"}},
		{tool: "podcast_settings", args: map[string]any{"item": "Behind the Bastards", "auto_download": true}},
		{tool: "podcast_episode_edit", args: map[string]any{"item": "Behind the Bastards", "episode": episode, "title": "Zzyzx Refused"}},
		{tool: "podcast_episode_download", args: map[string]any{"item": "Behind the Bastards", "indexes": []any{0}}},
		{tool: "podcast_check_new", args: map[string]any{"item": "Behind the Bastards"}},
		{tool: "podcast_episode_delete", args: map[string]any{"item": "Behind the Bastards", "episode": episode, "confirm": true}},
		{tool: "server_backup_create"},
		// its own things, but on a book in a library it cannot open
		{tool: "user_progress_set", args: map[string]any{"item": racket, "percent": 50}},
		{tool: "user_progress_remove", args: map[string]any{"item": racket}},
		{tool: "user_bookmark_edit", args: map[string]any{"item": racket, "action": "add", "time_s": 0.5, "title": "Zzyzx Refused"}},
		{tool: "playlist_create", args: map[string]any{"library": "Non-Fiction", "name": "Zzyzx Refused Queue", "entries": []any{map[string]any{"item": racket}}}},
	}
	// what any account may do with its own listening, in its own libraries
	own := []string{"user_progress_set", "user_progress_remove", "user_bookmark_edit", "playlist_create", "playlist_edit", "playlist_entries_edit", "playlist_delete"}

	res, err := account.session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
			continue
		}
		if !slices.Contains(own, tool.Name) && !slices.ContainsFunc(refused, func(c writeCall) bool { return c.tool == tool.Name }) {
			t.Errorf("%s changes the server but this journey does not call it", tool.Name)
		}
	}

	waitIdle(t)
	before := snapshot(t)
	for _, c := range refused {
		out, err := account.invoke(c.tool, c.args)
		switch {
		case err != nil:
		case c.refused != nil && c.refused(out):
		case c.mayPass:
		default:
			t.Errorf("%s %v was not refused: %v", c.tool, c.args, out)
		}
	}
	waitIdle(t)
	if changed := diff("", before, snapshot(t)); len(changed) > 0 {
		t.Errorf("the refused writes changed the server:\n  %s", strings.Join(changed, "\n  "))
	}

	t.Run("its own listening and playlists", func(t *testing.T) {
		t.Cleanup(func() {
			_, _ = account.invoke("user_progress_remove", map[string]any{"library": "Fiction", "item": "Foundation"})
			_, _ = account.invoke("user_bookmark_edit", map[string]any{"library": "Fiction", "item": "Foundation", "action": "remove", "time_s": 0.5})
			_, _ = account.invoke("playlist_delete", map[string]any{"playlist": "Zzyzx Own Queue"})
		})
		account.call(t, "user_progress_set", map[string]any{"library": "Fiction", "item": "Foundation", "percent": 50})
		account.call(t, "user_bookmark_edit", map[string]any{"library": "Fiction", "item": "Foundation", "action": "add", "time_s": 0.5, "title": "Zzyzx Own Mark"})
		account.call(t, "playlist_create", map[string]any{"library": "Fiction", "name": "Zzyzx Own Queue", "entries": []any{map[string]any{"item": "Foundation"}}})
		account.call(t, "playlist_entries_edit", map[string]any{"playlist": "Zzyzx Own Queue", "action": "add", "entries": []any{map[string]any{"item": "Second Foundation"}}})
		account.call(t, "playlist_edit", map[string]any{"playlist": "Zzyzx Own Queue", "description": "Zzyzx: the account's own"})
		if got := account.call(t, "playlist_get", map[string]any{"playlist": "Zzyzx Own Queue"}); len(rows(t, got["entries"], "entries")) != 2 || got["description"] != "Zzyzx: the account's own" {
			t.Errorf("the account's playlist = %v", got)
		}
		if p, _ := account.call(t, "user_progress_get", map[string]any{"library": "Fiction", "item": "Foundation"})["progress"].(map[string]any); p == nil || num(t, p["percent"], "percent") != 50 {
			t.Errorf("the account's progress = %v", p)
		}
		account.call(t, "playlist_delete", map[string]any{"playlist": "Zzyzx Own Queue"})
		if done, _ := account.call(t, "user_progress_remove", map[string]any{"library": "Fiction", "item": "Foundation"})["removed"].(bool); !done {
			t.Error("the account's progress was not removed")
		}
		account.call(t, "user_bookmark_edit", map[string]any{"library": "Fiction", "item": "Foundation", "action": "remove", "time_s": 0.5})
		// and none of it is the admin's
		for _, p := range rows(t, call(t, "playlist_list", nil)["playlists"], "playlists") {
			if p["name"] == "Zzyzx Own Queue" {
				t.Errorf("the account's playlist is in the admin's list: %v", p)
			}
		}
	})
}
