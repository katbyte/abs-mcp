//go:build integration

package acceptance

import (
	"context"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
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
	if self := truth(out["self"]); !self {
		t.Error("user_get with no user should report self")
	}
	for _, perm := range []string{"can_update", "can_delete", "can_upload"} {
		if ok := truth(out[perm]); !ok {
			t.Errorf("root should have %s", perm)
		}
	}
}

// naming the API key's own account is still the key's own account
func TestUserGetByName(t *testing.T) {
	out := call(t, "user_get", map[string]any{"user": "root"})

	if self := truth(out["self"]); !self {
		t.Error("naming the API key's own account should report self")
	}
	for _, perm := range []string{"can_update", "can_delete", "can_upload"} {
		if ok := truth(out[perm]); !ok {
			t.Errorf("root should have %s", perm)
		}
	}
	if created := text(out["created"]); created == "" {
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
		if self := truth(out["self"]); !self {
			t.Errorf("user=%q should report self", token)
		}
	}
}

// the whole progress lifecycle: set, read, appear in-progress, remove.
func TestUserProgress(t *testing.T) {
	set := call(t, "user_progress_set", map[string]any{"item": "Foundation", "percent": 50})
	t.Cleanup(func() {
		call(t, "user_progress_set", map[string]any{"remove": true, "item": "Foundation"})
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
		if finished := truth(p["finished"]); !finished {
			t.Errorf("finished = %v, want true", p["finished"])
		}
	}
}

func TestUserProgressRemove(t *testing.T) {
	call(t, "user_progress_set", map[string]any{"item": "Second Foundation", "percent": 10})
	out := call(t, "user_progress_set", map[string]any{"remove": true, "item": "Second Foundation"})

	if removed := truth(out["removed"]); !removed {
		t.Errorf("user_progress_set remove: removed = %v", out["removed"])
	}
	got := call(t, "user_progress_get", map[string]any{"item": "Second Foundation"})
	if got["progress"] != nil {
		t.Errorf("progress = %v, want none after removal", got["progress"])
	}
}

func TestUserBookmarks(t *testing.T) {
	added := call(t, "user_bookmark_edit", map[string]any{
		"item": "Leviathan Wakes", "add_bookmarks": []any{map[string]any{"time_s": 0.25, "title": "A good bit"}},
	})
	t.Cleanup(func() {
		_, _ = invoke("user_bookmark_edit", map[string]any{"item": "Leviathan Wakes", "remove_bookmarks": []any{0.25}})
	})
	if marks := rows(t, added["added"], "added"); len(marks) != 1 || marks[0]["title"] != "A good bit" {
		t.Fatalf("added = %v, want the one bookmark", added)
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

	// several in one call: one added, the one held renamed, then both removed;
	// a removal of one that is not there changes nothing
	several := call(t, "user_bookmark_edit", map[string]any{"item": "Leviathan Wakes", "add_bookmarks": []any{
		map[string]any{"time_s": 0.5, "title": "Another bit"}, map[string]any{"time_s": 0.25, "title": "A better name"},
	}})
	if added, renamed := rows(t, several["added"], "added"), rows(t, several["renamed"], "renamed"); len(added) != 1 || added[0]["title"] != "Another bit" || len(renamed) != 1 || renamed[0]["title"] != "A better name" || renamed[0]["was_titled"] != "A good bit" {
		t.Errorf("an add and a rename in one call = %v", several)
	}
	if msg := callErr(t, "user_bookmark_edit", map[string]any{"item": "Leviathan Wakes", "remove_bookmarks": []any{0.25, 0.75}}); !strings.Contains(msg, "no bookmark at 0.75") {
		t.Errorf("a removal naming one that is not there: %s", msg)
	}
	if left := rows(t, call(t, "user_bookmarks", map[string]any{"item": "Leviathan Wakes"})["bookmarks"], "bookmarks"); len(left) != 2 {
		t.Fatalf("after a refused removal the book has %d bookmarks, want both", len(left))
	}
	gone := call(t, "user_bookmark_edit", map[string]any{"item": "Leviathan Wakes", "remove_bookmarks": []any{0.25, 0.5}})
	if removed := rows(t, gone["removed"], "removed"); len(removed) != 2 {
		t.Errorf("two removed in one call = %v", gone)
	}
	if left := rows(t, call(t, "user_bookmarks", map[string]any{"item": "Leviathan Wakes"})["bookmarks"], "bookmarks"); len(left) != 0 {
		t.Errorf("bookmarks left after both were removed: %v", left)
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

// An account made for someone: active, with a password made for it that
// signs in, opening only the library named; then changed - every library,
// upload allowed, and stopped from signing in - each change read back.
func TestUserCreateAndEdit(t *testing.T) {
	const name = "zzyzx-newcomer"
	admin := adminClient(t)
	existing, err := admin.Users(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range existing {
		if u.Username == name { // left by a run that died
			_ = admin.DeleteUser(ctx, u.ID)
		}
	}

	out := call(t, "user_create", map[string]any{"username": name, "libraries": []any{"Fiction"}})
	id := text(out["id"])
	t.Cleanup(func() {
		eventually(t, "deleting "+name, func() error { return admin.DeleteUser(context.WithoutCancel(ctx), id) })
	})
	password := text(out["password"])
	if len(password) != 16 || !truth(out["active"]) || out["type"] != "user" || !isFalse(out["all_libraries"]) ||
		!slices.Equal(strs(t, out["libraries"], "libraries"), []string{libraryID(t, "Fiction")}) {
		t.Fatalf("user_create = %v", out)
	}
	if err := login(name, password); err != nil {
		t.Fatalf("the new account cannot sign in with the password made for it: %v", err)
	}
	if msg := callErr(t, "user_create", map[string]any{"username": name, "password": "x"}); !strings.Contains(msg, "taken") {
		t.Errorf("a second account of that name: %s", msg)
	}

	out = call(t, "user_edit", map[string]any{"user": name, "libraries": []any{"all"}, "can_upload": true, "email": "newcomer@zzyzx.test"})
	if !truth(out["all_libraries"]) || !truth(out["can_upload"]) || out["email"] != "newcomer@zzyzx.test" {
		t.Errorf("user_edit = %v", out)
	}
	got, err := admin.User(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Permissions.AccessAllLibraries || !got.Permissions.Upload {
		t.Errorf("the server holds %+v", got.Permissions)
	}

	out = call(t, "user_edit", map[string]any{"user": name, "active": false})
	if truth(out["active"]) {
		t.Errorf("user_edit active false = %v", out)
	}
	if err := login(name, password); err == nil {
		t.Error("an account stopped from signing in still signs in")
	}
	out = call(t, "user_edit", map[string]any{"user": name, "active": true, "password": "new"})
	if err := login(name, text(out["password"])); err != nil {
		t.Errorf("the new password does not sign in: %v", err)
	}

	if msg := callErr(t, "user_edit", map[string]any{"user": "root", "active": false}); !strings.Contains(msg, "lock the key out") {
		t.Errorf("stopping the key's own account: %s", msg)
	}
}

// Sessions taken out of an account's history, one of the key's own and one
// of another account's: previewed, removed, and gone from user_history.
func TestUserHistoryRemove(t *testing.T) {
	listener := newUser(t, "zzyzx-forgetful", abs.UserCreate{})
	lc, err := abs.New(os.Getenv("ABS_SERVER"), listener.Token)
	if err != nil {
		t.Fatal(err)
	}
	bookID := itemID(t, "Fiction", "Foundation and Empire")
	play := func(c *abs.Client) string {
		t.Helper()
		s, err := c.Play(ctx, bookID, "", abs.PlayRequest{MediaPlayer: "zzyzx-player", SupportedMimeTypes: []string{"audio/mpeg"}, ForceDirectPlay: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.SyncSession(ctx, s.ID, 0.3, 0.3); err != nil {
			t.Fatal(err)
		}
		if err := c.CloseSession(ctx, s.ID, map[string]any{"currentTime": 0.3, "timeListened": 0.3}); err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	theirs, mine := play(lc), play(adminClient(t))

	for _, c := range []struct {
		user    map[string]any
		session string
	}{{map[string]any{"user": listener.Name}, theirs}, {map[string]any{}, mine}} {
		args := func(extra map[string]any) map[string]any {
			out := map[string]any{"sessions": []any{c.session}}
			maps.Copy(out, c.user)
			maps.Copy(out, extra)
			return out
		}
		history := func() []string {
			return valuesIn(t, call(t, "user_history", c.user)["sessions"], "sessions", "id")
		}
		if !slices.Contains(history(), c.session) {
			t.Fatalf("user_history %v does not hold %s", c.user, c.session)
		}
		out := call(t, "user_history_remove", args(nil))
		if got := valuesIn(t, out["would_remove"], "would_remove", "id"); !slices.Equal(got, []string{c.session}) || !slices.Contains(history(), c.session) {
			t.Fatalf("the preview = %v", out)
		}
		out = call(t, "user_history_remove", args(map[string]any{"confirm": true}))
		if got := valuesIn(t, out["removed"], "removed", "id"); !slices.Equal(got, []string{c.session}) || slices.Contains(history(), c.session) {
			t.Errorf("the removal = %v; history holds %v", out, history())
		}
	}
	// one account's session is not another's to name
	other := play(lc)
	if msg := callErr(t, "user_history_remove", map[string]any{"sessions": []any{other}, "confirm": true}); !strings.Contains(msg, "has no session") {
		t.Errorf("removing another account's session as the key's own: %s", msg)
	}
}

// A series taken off the Continue Series shelf and put back, as the account
// then holds it.
func TestUserProgressSetSeries(t *testing.T) {
	admin := adminClient(t)
	hidden := func() bool {
		me, err := admin.Me(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s := call(t, "series_get", map[string]any{"series": "Foundation", "library": "Fiction"})
		return slices.Contains(me.SeriesHidden, text(s["id"]))
	}
	t.Cleanup(func() {
		_, _ = invoke("user_progress_set", map[string]any{"series": "Foundation", "library": "Fiction", "hide_from_continue": false})
	})

	out := call(t, "user_progress_set", map[string]any{"series": "Foundation", "library": "Fiction", "hide_from_continue": true})
	if out["series"] != "Foundation" || !truth(out["series_hidden"]) || !hidden() {
		t.Errorf("hide = %v", out)
	}
	out = call(t, "user_progress_set", map[string]any{"series": "Foundation", "library": "Fiction", "hide_from_continue": false})
	if !isFalse(out["series_hidden"]) || hidden() {
		t.Errorf("show = %v", out)
	}
}

// The whole server's year, in the shape the server gives it: the seeded
// books were all added this year.
func TestUserStatsServerYear(t *testing.T) {
	out := call(t, "user_stats", map[string]any{"year": time.Now().Year(), "server": true})
	if out["user"] != "every account" || number(out["books_added"]) < 10 || number(out["books"]) < number(out["books_added"]) || number(out["added_s"]) <= 0 {
		t.Errorf("user_stats server = %v", out)
	}
	if msg := callErr(t, "user_stats", map[string]any{"year": 1999, "server": true}); !strings.Contains(strings.ToLower(msg), "year") {
		t.Errorf("a year the server refuses: %s", msg)
	}
}

// An account of another type that sees only some books: a guest kept to
// the books carrying a tag, then kept from them, then shown every book
// again, each as the account itself then finds the library; and made an
// admin, which gives it no right to delete.
func TestUserCreateTagsAndType(t *testing.T) {
	const name = "zzyzx-guest"
	admin := adminClient(t)
	existing, err := admin.Users(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range existing {
		if u.Username == name { // left by a run that died
			_ = admin.DeleteUser(ctx, u.ID)
		}
	}

	for _, c := range []struct {
		args map[string]any
		says string
	}{
		{map[string]any{"type": "owner"}, "user, guest or admin"},
		{map[string]any{"tags": []any{"classic"}, "denied_tags": []any{"sf"}}, "one or the other"},
		{map[string]any{"all_tags": true}, "all_tags is for user_edit"},
	} {
		if msg := callErr(t, "user_create", withArgs(map[string]any{"username": name}, c.args)); !strings.Contains(msg, c.says) {
			t.Errorf("user_create %v: %s", c.args, msg)
		}
	}

	out := call(t, "user_create", map[string]any{"username": name, "password": name + "-password", "type": "guest", "tags": []any{"classic"}})
	id := text(out["id"])
	t.Cleanup(func() {
		eventually(t, "deleting "+name, func() error { return admin.DeleteUser(context.WithoutCancel(ctx), id) })
	})
	if out["type"] != "guest" || !isFalse(out["all_tags"]) || !slices.Equal(strs(t, out["tags"], "tags"), []string{"classic"}) || out["denied_tags"] != nil || out["password"] != nil {
		t.Fatalf("user_create = %v, want a guest seeing only classic, its password not repeated", out)
	}

	// the library as the account finds it
	if err := login(name, name+"-password"); err != nil {
		t.Fatalf("the guest signing in: %v", err)
	}
	key, err := admin.CreateAPIKey(ctx, name, id, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.DeleteAPIKey(context.WithoutCancel(ctx), key.ID) })
	own, err := abs.New(os.Getenv("ABS_SERVER"), key.Key)
	if err != nil {
		t.Fatal(err)
	}
	fiction := libraryID(t, "Fiction")
	sees := func() []string {
		t.Helper()
		page, err := own.Items(ctx, fiction, abs.ItemsOptions{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		titles := make([]string, 0, len(page.Results))
		for i := range page.Results {
			titles = append(titles, page.Results[i].Title())
		}
		slices.Sort(titles)
		return titles
	}
	classic := []string{"Foundation", "Foundation and Empire", "Second Foundation"}
	others := []string{"Abaddon's Gate", "City of Golden Shadow", "Leviathan Wakes", "Sea of Silver Light"}
	if got := sees(); !slices.Equal(got, classic) {
		t.Errorf("kept to classic, the guest sees %v, want %v", got, classic)
	}

	out = call(t, "user_edit", map[string]any{"user": name, "denied_tags": []any{"classic"}})
	if !isFalse(out["all_tags"]) || !slices.Equal(strs(t, out["denied_tags"], "denied_tags"), []string{"classic"}) || out["tags"] != nil {
		t.Errorf("user_edit denied_tags = %v", out)
	}
	if got := sees(); !slices.Equal(got, others) {
		t.Errorf("kept from classic, the guest sees %v, want %v", got, others)
	}

	out = call(t, "user_edit", map[string]any{"user": name, "all_tags": true})
	if !truth(out["all_tags"]) || out["tags"] != nil || out["denied_tags"] != nil {
		t.Errorf("user_edit all_tags = %v", out)
	}
	if got := sees(); len(got) != len(classic)+len(others) {
		t.Errorf("shown every book again, the guest sees %v", got)
	}

	out = call(t, "user_edit", map[string]any{"user": name, "type": "admin"})
	if out["type"] != "admin" || truth(out["can_delete"]) {
		t.Errorf("user_edit type admin = %v, want an admin that may not delete", out)
	}
	if got, err := admin.User(ctx, id); err != nil || got.Type != "admin" || got.Permissions.Delete {
		t.Errorf("the server holds %+v (%v)", got, err)
	}
	if msg := callErr(t, "user_edit", map[string]any{"user": "root", "type": "user"}); !strings.Contains(msg, "root account") {
		t.Errorf("changing the root account's type: %s", msg)
	}
}
