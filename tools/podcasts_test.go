package tools

import (
	"io"
	"net/http"
	"slices"
	"strings"
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
	if len(anyStrings(out["changed"])) != 0 || !slices.Equal(anyStrings(out["unchanged"]), []string{"season", "type"}) {
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
	if !ok || !slices.Equal(anyStrings(out["changed"]), []string{"title"}) || !slices.Equal(anyStrings(out["unchanged"]), []string{"type"}) || str(t, ep["title"]) != "New" {
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
	if got := anyStrings(out["queued"]); !slices.Equal(got, []string{"Newest"}) {
		t.Errorf("index 0 queued %v, want the newest, as listed", got)
	}
}

// changes are the requests made so far that were not reads.
func (f *fakeABS) changes() []request {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []request
	for _, r := range f.seen {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}

	return out
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
