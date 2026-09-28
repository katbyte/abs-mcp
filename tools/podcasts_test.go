package tools

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// twoOfOneTitle is a podcast holding an episode and the server's second
// download of it: the same title, a file with a random suffix.
func twoOfOneTitle() string {
	return podcastWith(
		`{"id":"e1","title":"Episode 12","publishedAt":1000,"addedAt":2000,"audioFile":{"metadata":{"filename":"Episode 12.mp3","path":"/podcasts/Show/Episode 12.mp3"}}}`,
		`{"id":"e2","title":"Episode 12","publishedAt":1000,"addedAt":3000,"audioFile":{"metadata":{"filename":"Episode 12 (a1b2).mp3","path":"/podcasts/Show/Episode 12 (a1b2).mp3"}}}`,
	)
}

// Two episodes titled alike are the original and the server's second download
// of it. A title that names both is refused with their ids and files, rather
// than settled by list order - which deleted the original, not the copy.
func TestAnEpisodeTitleTwoShareIsRefused(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, twoOfOneTitle())
	f.json("DELETE /api/podcasts/"+podcastID+"/episode/e2", `OK`)
	call := toolCaller(t, f)

	for tool, args := range map[string]map[string]any{
		"podcast_episode_get":    {"item": podcastID, "episode": "episode 12"},
		"podcast_episode_edit":   {"item": podcastID, "episode": "Episode 12", "title": "Twelve"},
		"podcast_episode_delete": {"item": podcastID, "episode": "Episode 12", "delete_file": true, "confirm": true},
	} {
		_, err := call(tool, args)
		if err == nil {
			t.Errorf("%s took one of two episodes titled alike", tool)
			continue
		}
		for _, want := range []string{"2 episodes", "e1", "e2", "Episode 12 (a1b2).mp3", "published"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %v, want it to name %q", tool, err, want)
			}
		}
	}
	for _, r := range f.changes() {
		t.Errorf("a refused call reached the server: %s %s", r.Method, r.Path)
	}

	// an id is never ambiguous
	if _, err := call("podcast_episode_delete", map[string]any{"item": podcastID, "episode": "e2", "confirm": true}); err == nil || !strings.Contains(err.Error(), "still in") {
		t.Errorf("deleting by id: %v, want it sent and read back", err)
	}
	if got := f.requests("/api/podcasts/" + podcastID + "/episode/e2"); len(got) != 1 || got[0].Method != http.MethodDelete {
		t.Errorf("deleting by id sent %v, want one DELETE of e2", got)
	}
}

// Without confirm, a delete changes nothing and says what it would take: the
// episode, and the file on disk it would erase. With it, the delete is read
// back rather than trusted.
func TestPodcastEpisodeDeleteNeedsConfirm(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var gone atomic.Bool
	f.mux.HandleFunc("GET /api/items/"+podcastID, func(w http.ResponseWriter, _ *http.Request) {
		if gone.Load() {
			_, _ = io.WriteString(w, podcastWith(`{"id":"e1","title":"Episode 12"}`))
			return
		}
		_, _ = io.WriteString(w, twoOfOneTitle())
	})
	f.mux.HandleFunc("DELETE /api/podcasts/"+podcastID+"/episode/e2", func(w http.ResponseWriter, _ *http.Request) {
		gone.Store(true)
		_, _ = io.WriteString(w, `OK`)
	})
	call := toolCaller(t, f)

	for _, deleteFile := range []bool{true, false} {
		out, err := call("podcast_episode_delete", map[string]any{"item": podcastID, "episode": "e2", "delete_file": deleteFile})
		if err != nil {
			t.Fatal(err)
		}
		note := str(t, out["note"])
		if boolOf(t, out["deleted"]) || str(t, out["file"]) != "/podcasts/Show/Episode 12 (a1b2).mp3" || !strings.Contains(note, "nothing changed") || !strings.Contains(note, "Episode 12 (a1b2).mp3") {
			t.Errorf("delete_file=%v without confirm: %v", deleteFile, out)
		}
		if stays := strings.Contains(note, "stays on disk"); stays == deleteFile {
			t.Errorf("delete_file=%v: the note says the file stays = %v: %s", deleteFile, stays, note)
		}
	}
	if got := f.requests("/api/podcasts/" + podcastID + "/episode/e2"); len(got) != 0 {
		t.Fatalf("an unconfirmed delete sent %v", got)
	}

	out, err := call("podcast_episode_delete", map[string]any{"item": podcastID, "episode": "e2", "delete_file": true, "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["deleted"]) || !boolOf(t, out["file_erased"]) || !strings.HasPrefix(str(t, out["note"]), "removed") {
		t.Errorf("confirmed: %v", out)
	}
	if got := f.requests("/api/podcasts/" + podcastID + "/episode/e2"); len(got) != 1 || got[0].Query != "hard=1" {
		t.Errorf("confirmed with delete_file sent %v, want one DELETE with hard=1", got)
	}
}

// An edit answers with the fields it changed and the episode as saved; a type
// the apps do not know is refused, and a field already so is not sent.
func TestPodcastEpisodeEditSaysWhatChanged(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith(`{"id":"e1","title":"Old","episodeType":"full","season":"1"}`))
	f.json("PATCH /api/podcasts/"+podcastID+"/episode/e1", podcastWith(`{"id":"e1","title":"New","episodeType":"full","season":"1"}`))
	call := toolCaller(t, f)

	if _, err := call("podcast_episode_edit", map[string]any{"item": podcastID, "episode": "e1", "type": "special"}); err == nil || !strings.Contains(err.Error(), "full, trailer, bonus") {
		t.Errorf("an unknown type: %v, want refused naming the three", err)
	}
	out, err := call("podcast_episode_edit", map[string]any{"item": podcastID, "episode": "e1", "type": "Full", "season": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(strs(t, out["changed"])) != 0 || !slices.Equal(strs(t, out["unchanged"]), []string{"season", "type"}) {
		t.Errorf("an edit to what is already so: %v", out)
	}
	if got := f.requests("/api/podcasts/" + podcastID + "/episode/e1"); len(got) != 0 {
		t.Fatalf("nothing to change sent %v", got)
	}

	out, err = call("podcast_episode_edit", map[string]any{"item": podcastID, "episode": "e1", "title": "New", "type": "full"})
	if err != nil {
		t.Fatal(err)
	}
	ep, ok := out["episode"].(map[string]any)
	if !ok || !slices.Equal(strs(t, out["changed"]), []string{"title"}) || !slices.Equal(strs(t, out["unchanged"]), []string{"type"}) || str(t, ep["title"]) != "New" {
		t.Errorf("a retitle: %v", out)
	}
	sent := f.requests("/api/podcasts/" + podcastID + "/episode/e1")
	if len(sent) != 1 || !strings.Contains(sent[0].Body, `"title":"New"`) || strings.Contains(sent[0].Body, "episodeType") {
		t.Errorf("sent %v, want only the title", sent)
	}
}

// The server saves any schedule, and one its scheduler cannot read never
// runs; a negative count means nothing. Both are refused before sending.
func TestPodcastSettingsRefusesWhatCannotRun(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith())
	f.json("PATCH /api/items/"+podcastID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	for _, args := range []map[string]any{
		{"schedule": "hourly"},
		{"schedule": "every day at 3"},
		{"schedule": "61 * * * *"},
		{"schedule": "0 24 * * *"},
		{"schedule": "0 0 0 * *"},
		{"schedule": "*/0 * * * *"},
		{"schedule": "0 5-1 * * *"},
		{"schedule": "0 0 * smarch *"},
		{"keep_episodes": -1},
		{"new_per_check": -2},
	} {
		args["item"] = podcastID
		if _, err := call("podcast_settings", args); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if got := f.requests("/api/items/" + podcastID + "/media"); len(got) != 0 {
		t.Fatalf("a refused setting was sent: %v", got)
	}

	for _, schedule := range []string{"0 * * * *", " 0 3 * * * ", "*/15 9-17 * * mon-fri", "0 0 1 Jan,jul *", "30 0 6 * * sunday", "0 0 * * 0,7"} {
		if _, err := call("podcast_settings", map[string]any{"item": podcastID, "schedule": schedule, "keep_episodes": 0}); err != nil {
			t.Errorf("%q: %v", schedule, err)
		}
	}
	if sent := f.requests("/api/items/" + podcastID + "/media"); len(sent) != 6 || !strings.Contains(sent[1].Body, `"autoDownloadSchedule":"0 3 * * *"`) {
		t.Errorf("sent %v, want six, the schedule trimmed", sent)
	}
}

// A folder is made under the library folder: one that would leave it, or be
// the library folder itself, is refused before anything is created.
func TestPodcastAddKeepsItsFolderInTheLibrary(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	showLibrary(f)
	f.json("POST /api/podcasts/feed", `{"podcast":{"metadata":{"title":"Show","feedUrl":"http://feed.test/show.xml"},"episodes":[]}}`)
	f.json("GET /api/libraries/"+podLibID+"/items", page())
	f.json("POST /api/podcasts", `{"id":"`+podcastID+`","libraryId":"`+podLibID+`","mediaType":"podcast","media":{"metadata":{"title":"Show"}}}`)
	call := toolCaller(t, f)

	for _, folder := range []string{"../x", "..", "a/../../x", "/etc/x", ".", "a/.."} {
		if _, err := call("podcast_add", map[string]any{"feed_url": "http://feed.test/show.xml", "folder": folder}); err == nil || !strings.Contains(err.Error(), "under the library folder") {
			t.Errorf("folder %q: %v, want refused", folder, err)
		}
	}
	if got := f.requests("/api/podcasts"); len(got) != 0 {
		t.Fatalf("a refused folder was created: %v", got)
	}

	if _, err := call("podcast_add", map[string]any{"feed_url": "http://feed.test/show.xml", "folder": "Shows/./Show "}); err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/podcasts"); len(got) != 1 || !strings.Contains(got[0].Body, `"path":"/podcasts/Shows/Show"`) {
		t.Errorf("created %v, want the folder cleaned under /podcasts", got)
	}
}

// A feed lists episodes in its publisher's order and the title search in
// order of how near each title is; both come back newest first, and the
// indexes podcast_episode_download takes are the same ones.
func TestPodcastFeedEpisodesAreNewestFirst(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith())
	f.json("POST /api/podcasts/feed", `{"podcast":{"metadata":{"title":"Show"},"episodes":[`+
		feedEpisode("g1", "Oldest", 1000)+","+feedEpisode("g3", "Newest", 3000)+","+feedEpisode("g2", "Middle", 2000)+`]}}`)
	f.json("GET /api/podcasts/"+podcastID+"/search-episode", `{"episodes":[{"episode":`+feedEpisode("g2", "Middle", 2000)+`},{"episode":`+feedEpisode("g3", "Newest", 3000)+`}]}`)
	f.json("GET /api/libraries/"+podLibID+"/episode-downloads", `{"queue":[]}`)
	f.json("POST /api/podcasts/"+podcastID+"/download-episodes", `OK`)
	call := toolCaller(t, f)

	for title, want := range map[string][]string{"": {"Newest", "Middle", "Oldest"}, "e": {"Newest", "Middle"}} {
		out, err := call("podcast_feed_episodes", map[string]any{"item": podcastID, "title": title})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for i, row := range list(t, out["episodes"]) {
			got = append(got, str(t, row["title"]))
			if num(t, row["index"]) != i {
				t.Errorf("title %q: row %d has index %v", title, i, row["index"])
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("title %q: %v, want %v", title, got, want)
		}
	}

	out, err := call("podcast_episode_download", map[string]any{"item": podcastID, "indexes": []any{0}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strs(t, out["queued"]); !slices.Equal(got, []string{"Newest"}) {
		t.Errorf("index 0 queued %v, want the newest, as listed", got)
	}
}

// What podcast_check_new queued is downloading already, and its place in that
// list is not its place in the feed, so the rows carry no index that
// podcast_episode_download would read as a feed position.
func TestPodcastCheckNewOffersNoIndex(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith())
	f.json("GET /api/podcasts/"+podcastID+"/checknew", `{"episodes":[{"title":"Newest","publishedAt":3000},{"title":"Newer","publishedAt":2000}]}`)
	call := toolCaller(t, f)

	out, err := call("podcast_check_new", map[string]any{"item": podcastID})
	if err != nil {
		t.Fatal(err)
	}
	queued := list(t, out["queued"])
	if len(queued) != 2 {
		t.Fatalf("queued = %v", queued)
	}
	for _, row := range queued {
		if _, ok := row["index"]; ok {
			t.Errorf("a queued episode offers index %v", row["index"])
		}
	}
}

// podcast_settings answers the settings as the server saved them, read back
// after the change, rather than a bare done.
func TestPodcastSettingsReadsTheSettingsBack(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var saved atomic.Bool
	f.mux.HandleFunc("GET /api/items/"+podcastID, func(w http.ResponseWriter, _ *http.Request) {
		if saved.Load() {
			_, _ = io.WriteString(w, `{"id":"`+podcastID+`","libraryId":"`+podLibID+`","mediaType":"podcast","media":{"metadata":{"title":"Show"},"autoDownloadEpisodes":true,"autoDownloadSchedule":"0 3 * * *","maxEpisodesToKeep":5,"maxNewEpisodesToDownload":2,"episodes":[]}}`)
			return
		}
		_, _ = io.WriteString(w, podcastWith())
	})
	f.mux.HandleFunc("PATCH /api/items/"+podcastID+"/media", func(w http.ResponseWriter, _ *http.Request) {
		saved.Store(true)
		_, _ = io.WriteString(w, `{"updated":true}`)
	})
	call := toolCaller(t, f)

	out, err := call("podcast_settings", map[string]any{"item": podcastID, "auto_download": true, "schedule": "0 3 * * *", "keep_episodes": 5, "new_per_check": 2})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["updated"]) || !boolOf(t, out["auto_download"]) || str(t, out["schedule"]) != "0 3 * * *" || num(t, out["keep_episodes"]) != 5 || num(t, out["new_per_check"]) != 2 {
		t.Errorf("podcast_settings = %v, want the saved settings", out)
	}
	if _, ok := out["done"]; ok {
		t.Error("still answers done")
	}
}

func TestEpisodeProgressThatCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith(`{"id":"e1","title":"One","publishedAt":1000}`))
	f.fails("GET /api/me")
	f.fails("GET /api/me/progress/" + podcastID + "/e1")
	call := toolCaller(t, f)

	_, err := call("podcast_episodes", map[string]any{"item": podcastID})
	wantErr(t, "podcast_episodes with the account unreadable", err, "progress", "500")
	_, err = call("podcast_episode_get", map[string]any{"item": podcastID, "episode": "e1"})
	wantErr(t, "podcast_episode_get with its progress unreadable", err, "progress", "500")
}

// A negative offset used to index past the end of the episode list, and the
// MCP transport has no recover: one bad argument took the whole server down.
func TestPodcastEpisodesOffsetBelowZero(t *testing.T) {
	t.Parallel()

	const podID = "33333333-3333-4333-8333-333333333333"
	f := newFakeABS(t)
	f.json("GET /api/items/"+podID, `{"id":"`+podID+`","libraryId":"`+libID+`","mediaType":"podcast","media":{"metadata":{"title":"Pod"},"episodes":[{"id":"e1","title":"One"},{"id":"e2","title":"Two"}]}}`)
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","mediaProgress":[]}`)
	call := toolCaller(t, f)

	out, err := call("podcast_episodes", map[string]any{"item": podID, "offset": -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := list(t, out["episodes"]); len(got) != 2 {
		t.Errorf("episodes = %d, want both from the start", len(got))
	}
	if past, err := call("podcast_episodes", map[string]any{"item": podID, "offset": 5}); err != nil || len(list(t, past["episodes"])) != 0 {
		t.Errorf("offset past the end = %v, %v; want none", past, err)
	}

	// a page says where the next starts, and the last says nothing
	out, err = call("podcast_episodes", map[string]any{"item": podID, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["offset"]) != 0 || num(t, out["next_offset"]) != 1 {
		t.Errorf("first page = %v, want offset 0 and next_offset 1", out)
	}
	if out, err = call("podcast_episodes", map[string]any{"item": podID, "limit": 1, "offset": 1}); err != nil || out["next_offset"] != nil {
		t.Errorf("last page = %v, %v; want no next_offset", out, err)
	}
}

// showLibrary is a podcast library with one folder.
func showLibrary(f *fakeABS) {
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+podLibID+`","name":"Shows","mediaType":"podcast","folders":[{"id":"f1","fullPath":"/podcasts"}]}]}`)
}

// feedEpisode is one entry of a canned feed.
func feedEpisode(guid, title string, published int) string {
	return fmt.Sprintf(`{"title":%q,"guid":%q,"publishedAt":%d,"enclosure":{"url":"http://feed.test/%s.mp3"}}`, title, guid, published, guid)
}

// The server takes no episodes with a new podcast, and dropped the ones sent
// there, so download_latest queues the newest by publication once the
// podcast exists; and a feed the library already has is refused before
// anything is made.
func TestPodcastAddQueuesTheNewestAndRefusesAHeldFeed(t *testing.T) {
	t.Parallel()

	feed := `{"podcast":{"metadata":{"title":"Show","feedUrl":"http://feed.test/show.xml"},"episodes":[` +
		feedEpisode("g1", "Oldest", 1000) + "," + feedEpisode("g3", "Newest", 3000) + "," + feedEpisode("g2", "Middle", 2000) + `]}}`

	t.Run("queued once it exists", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		showLibrary(f)
		f.json("POST /api/podcasts/feed", feed)
		f.json("GET /api/libraries/"+podLibID+"/items", page())
		f.json("POST /api/podcasts", `{"id":"`+podcastID+`","libraryId":"`+podLibID+`","mediaType":"podcast","media":{"metadata":{"title":"Show"}}}`)
		f.json("POST /api/podcasts/"+podcastID+"/download-episodes", `OK`)
		call := toolCaller(t, f)

		out, err := call("podcast_add", map[string]any{"feed_url": "http://feed.test/show.xml", "download_latest": 2})
		if err != nil {
			t.Fatal(err)
		}
		if got := out["queued"]; fmt.Sprint(got) != "[Newest Middle]" {
			t.Errorf("queued = %v, want [Newest Middle]", got)
		}
		if created := f.requests("/api/podcasts"); len(created) != 1 || strings.Contains(created[0].Body, "episodesToDownload") {
			t.Errorf("create = %v, want no episodes sent with it", created)
		}
		queued := f.requests("/api/podcasts/" + podcastID + "/download-episodes")
		if len(queued) != 1 || !strings.Contains(queued[0].Body, `"Newest"`) || !strings.Contains(queued[0].Body, `"Middle"`) || strings.Contains(queued[0].Body, `"Oldest"`) {
			t.Errorf("download-episodes = %v, want Newest and Middle", queued)
		}
	})

	t.Run("a feed already held", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		showLibrary(f)
		f.json("POST /api/podcasts/feed", feed)
		f.json("GET /api/libraries/"+podLibID+"/items", page(`{"id":"`+podcastID+`","libraryId":"`+podLibID+`","mediaType":"podcast","media":{"metadata":{"title":"Show","feedUrl":"HTTP://feed.test/show.xml"}}}`))
		call := toolCaller(t, f)

		if _, err := call("podcast_add", map[string]any{"feed_url": "http://feed.test/show.xml", "folder": "Show Again"}); err == nil || !strings.Contains(err.Error(), podcastID) {
			t.Errorf("a second subscription: %v, want refused naming %s", err, podcastID)
		}
		if created := f.requests("/api/podcasts"); len(created) != 0 {
			t.Errorf("the podcast was created: %v", created)
		}
	})
}

// The server downloads an episode it holds a second time, and drops a
// request for one it is already fetching without a word: held is matched by
// guid or audio url, not by a title an edit changed, and neither is sent.
func TestPodcastEpisodeDownloadSkipsHeldAndQueued(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith(`{"id":"e1","title":"Retitled","guid":"g1","enclosure":{"url":"http://feed.test/g1.mp3"}}`))
	f.json("POST /api/podcasts/feed", `{"podcast":{"metadata":{"title":"Show"},"episodes":[`+
		feedEpisode("g3", "Three", 3000)+","+feedEpisode("g2", "Two", 2000)+","+feedEpisode("g1", "One", 1000)+`]}}`)
	f.json("GET /api/libraries/"+podLibID+"/episode-downloads", `{"currentDownload":{"url":"http://feed.test/g2.mp3","libraryItemId":"`+podcastID+`"},"queue":[]}`)
	f.json("POST /api/podcasts/"+podcastID+"/download-episodes", `OK`)
	call := toolCaller(t, f)

	out, err := call("podcast_episode_download", map[string]any{"item": podcastID, "indexes": []any{0, 1, 2, 2}})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"queued": "[Three]", "already_queued": "[Two]", "already_held": "[One]"} {
		if got := fmt.Sprint(out[key]); got != want {
			t.Errorf("%s = %s, want %s", key, got, want)
		}
	}
	sent := f.requests("/api/podcasts/" + podcastID + "/download-episodes")
	if len(sent) != 1 || strings.Contains(sent[0].Body, `"One"`) || strings.Contains(sent[0].Body, `"Two"`) {
		t.Errorf("sent %v, want only Three", sent)
	}

	// nothing left to send is not a request
	f2 := newFakeABS(t)
	f2.json("GET /api/items/"+podcastID, podcastWith(`{"id":"e1","title":"One","guid":"g1"}`))
	f2.json("POST /api/podcasts/feed", `{"podcast":{"metadata":{"title":"Show"},"episodes":[`+feedEpisode("g1", "One", 1000)+`]}}`)
	f2.json("GET /api/libraries/"+podLibID+"/episode-downloads", `{"queue":[]}`)
	if _, err := toolCaller(t, f2)("podcast_episode_download", map[string]any{"item": podcastID, "indexes": []any{0}}); err != nil {
		t.Fatal(err)
	}
	if sent := f2.requests("/api/podcasts/" + podcastID + "/download-episodes"); len(sent) != 0 {
		t.Errorf("sent %v for an episode already held", sent)
	}
}

// The server lists a podcast's episodes in the order they were downloaded;
// newest first is by publication.
func TestPodcastEpisodesAreNewestByPublication(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith(
		`{"id":"e3","title":"Newest","publishedAt":3000}`,
		`{"id":"e1","title":"Oldest","publishedAt":1000}`,
		`{"id":"e2","title":"Middle","publishedAt":2000}`,
	))
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","mediaProgress":[]}`)
	call := toolCaller(t, f)

	want := []string{"Newest", "Middle", "Oldest"}
	for tool, key := range map[string]string{"podcast_episodes": "episodes", "item_get": "episodes"} {
		out, err := call(tool, map[string]any{"item": podcastID})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range list(t, out[key]) {
			got = append(got, str(t, e["title"]))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", tool, got, want)
		}
	}
}

// Audiobookshelf caches every read under /api/libraries until the database
// is next written, and the download queue lives in memory: the queue read
// before a download started was the answer until the episode arrived, so
// podcast_downloads showed nothing downloading, and a second request for the
// same episode was reported queued while the server dropped it. A sort of
// random is the one request its cache lets through.
func TestTheDownloadQueueIsReadPastTheServersCache(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	showLibrary(f)
	f.json("GET /api/items/"+podcastID, podcastWith())
	f.json("POST /api/podcasts/feed", `{"podcast":{"metadata":{"title":"Show"},"episodes":[`+feedEpisode("g1", "One", 1000)+`]}}`)
	f.json("POST /api/podcasts/"+podcastID+"/download-episodes", `OK`)
	var mu sync.Mutex
	current := ""
	cache := map[string]string{}
	f.mux.HandleFunc("GET /api/libraries/"+podLibID+"/episode-downloads", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body := `{"currentDownload":` + current + `,"queue":[]}`
		if current == "" {
			body = `{"queue":[]}`
		}
		if r.URL.Query().Get("sort") != "random" {
			if cached, ok := cache[r.URL.String()]; ok {
				body = cached
			} else {
				cache[r.URL.String()] = body
			}
		}
		_, _ = io.WriteString(w, body)
	})
	call := toolCaller(t, f)

	out, err := call("podcast_downloads", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := list(t, out["downloads"]); len(got) != 0 {
		t.Fatalf("downloads = %v, want none yet", got)
	}

	mu.Lock()
	current = `{"url":"http://feed.test/g1.mp3","libraryItemId":"` + podcastID + `","libraryId":"` + podLibID + `","podcastTitle":"Show","episodeDisplayTitle":"One"}`
	mu.Unlock()

	out, err = call("podcast_downloads", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := list(t, out["downloads"]); len(got) != 1 || str(t, got[0]["episode"]) != "One" || str(t, got[0]["status"]) != "downloading" {
		t.Errorf("downloads once One started = %v, want it downloading", got)
	}
	out, err = call("podcast_episode_download", map[string]any{"item": podcastID, "indexes": []any{0}})
	if err != nil {
		t.Fatal(err)
	}
	if out["queued"] != nil || !slices.Equal(strs(t, out["already_queued"]), []string{"One"}) {
		t.Errorf("asking for One while it downloads = %v, want it already queued", out)
	}
	if sent := f.requests("/api/podcasts/" + podcastID + "/download-episodes"); len(sent) != 0 {
		t.Errorf("sent %v for an episode downloading", sent)
	}
}

// The server keeps an episode's publish date twice: the feed's text, and the
// time its apps sort by, which the tools read as published and order by.
// Sent the text alone, the date read back unmoved.
func TestPodcastEpisodeEditMovesThePublishDate(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith(`{"id":"e1","title":"One","pubDate":"Mon, 01 Jan 2024 00:00:00 +0000","publishedAt":1704067200000}`))
	f.json("PATCH /api/podcasts/"+podcastID+"/episode/e1", podcastWith(`{"id":"e1","title":"One","pubDate":"2024-03-01","publishedAt":1709251200000}`))
	call := toolCaller(t, f)

	if _, err := call("podcast_episode_edit", map[string]any{"item": podcastID, "episode": "e1", "pub_date": "next tuesday"}); err == nil || !strings.Contains(err.Error(), "not a date") {
		t.Errorf("a pub_date that is no date: %v, want refused", err)
	}
	if sent := f.requests("/api/podcasts/" + podcastID + "/episode/e1"); len(sent) != 0 {
		t.Fatalf("a refused pub_date sent %v", sent)
	}

	out, err := call("podcast_episode_edit", map[string]any{"item": podcastID, "episode": "e1", "pub_date": "2024-03-01"})
	if err != nil {
		t.Fatal(err)
	}
	ep, ok := out["episode"].(map[string]any)
	if !ok || !slices.Equal(strs(t, out["changed"]), []string{"pub_date"}) || str(t, ep["published"]) != "2024-03-01" {
		t.Errorf("podcast_episode_edit pub_date = %v, want it changed and published 2024-03-01", out)
	}
	sent := f.requests("/api/podcasts/" + podcastID + "/episode/e1")
	if len(sent) != 1 || !strings.Contains(sent[0].Body, `"pubDate":"2024-03-01"`) || !strings.Contains(sent[0].Body, `"publishedAt":1709251200000`) {
		t.Errorf("sent %v, want the text and the time", sent)
	}

	// the same date written the way the feed writes it is no change
	out, err = call("podcast_episode_edit", map[string]any{"item": podcastID, "episode": "e1", "pub_date": "Mon, 01 Jan 2024 00:00:00 +0000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(strs(t, out["changed"])) != 0 || !slices.Equal(strs(t, out["unchanged"]), []string{"pub_date"}) {
		t.Errorf("the date it has = %v, want unchanged", out)
	}
}

// safeFolderName builds the directory a subscribed podcast lands in, so a show
// with a slash or a colon in its title cannot escape the library folder or
// produce a path the server refuses.
func TestSafeFolderName(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ in, want string }{
		{"Behind the Bastards", "Behind the Bastards"},
		{"Well There's Your Problem", "Well There's Your Problem"},
		{"AC/DC: The Podcast", "AC DC The Podcast"},
		{"../../etc/passwd", ".. .. etc passwd"},
		{`a\b:c*d?e"f<g>h|i`, "a b c d e f g h i"},
		{"  spaced   out  ", "spaced out"},
		{"", ""},
	} {
		if got := safeFolderName(c.in); got != c.want {
			t.Errorf("safeFolderName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPodcastTimesInSecondsAndSizesInBytes(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+podcastID, podcastWith(`{"id":"e1","libraryItemId":"`+podcastID+`","title":"Pilot","publishedAt":1000,`+
		`"audioFile":{"duration":2700.6,"metadata":{"filename":"pilot.mp3","size":43200000}},`+
		`"chapters":[{"id":0,"start":0,"end":90.5,"title":"Intro"},{"id":1,"start":90.5,"end":2700.6,"title":"Main"}]}`))
	// a record at the very start is a position all the same
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","mediaProgress":[{"id":"mp1","libraryItemId":"`+podcastID+`","episodeId":"e1","currentTime":0,"progress":0}]}`)
	// the server parses the feed's own duration, and leaves an unreadable one
	// unparsed
	f.json("POST /api/podcasts/feed", `{"podcast":{"metadata":{"title":"Show"},"episodes":[`+
		`{"title":"Next","guid":"g2","publishedAt":2000,"duration":"45:00","durationSeconds":2700.4},`+
		`{"title":"Odd","guid":"g3","publishedAt":1500,"duration":"about an hour"}]}}`)
	call := toolCaller(t, f)

	out, err := call("podcast_episodes", map[string]any{"item": podcastID})
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "podcast_episodes", out, map[string]float64{"episodes.0.duration_s": 2701, "episodes.0.size": 43200000, "episodes.0.progress.current_time_s": 0})
	wantAbsent(t, "podcast_episodes", out, "episodes.0.duration", "episodes.0.size_mb")

	out, err = call("podcast_episode_get", map[string]any{"item": podcastID, "episode": "e1"})
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "podcast_episode_get", out, map[string]float64{"duration_s": 2701, "chapters.0.start_s": 0, "chapters.1.start_s": 90.5})
	wantAbsent(t, "podcast_episode_get", out, "chapters.1.start")

	out, err = call("podcast_feed_episodes", map[string]any{"item": podcastID})
	if err != nil {
		t.Fatal(err)
	}
	if str(t, dig(out, "episodes.0.title")) != "Next" || str(t, dig(out, "episodes.1.title")) != "Odd" {
		t.Fatalf("feed = %v, want Next then Odd", out["episodes"])
	}
	wantNumbers(t, "podcast_feed_episodes", out, map[string]float64{"episodes.0.duration_s": 2700})
	wantAbsent(t, "podcast_feed_episodes", out, "episodes.1.duration_s", "episodes.1.duration")
}
