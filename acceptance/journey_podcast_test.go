//go:build integration

// Journey 10: a podcast's whole life, over a feed the suite serves itself
// through the provider proxy, so episodes can be published, and their audio
// taken away, while the journey runs. Every download is waited for and read
// back: podcast_episode_download returning is only the queue accepting it.
package acceptance

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// testFeed is an RSS feed the proxy answers for, newest episode first.
type testFeed struct {
	host, title string
	audio       []byte

	mu    sync.Mutex
	items []feedItem
}

type feedItem struct {
	guid, title string
	published   time.Time
	gone        bool // the enclosure answers 404
}

func (f *testFeed) url() string { return "http://" + f.host + "/show.xml" }

// publish puts an episode at the top of the feed.
func (f *testFeed) publish(item feedItem) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.items = append([]feedItem{item}, f.items...)
}

func (f *testFeed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	switch {
	case r.URL.Path == "/show.xml":
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
		b.WriteString(`<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel>`)
		fmt.Fprintf(&b, `<title>%s</title><itunes:author>Zzyzx Host</itunes:author><description>A feed the acceptance suite serves.</description><language>en</language>`, esc(f.title))
		for _, it := range f.items {
			fmt.Fprintf(&b, `<item><title>%s</title><guid isPermaLink="false">%s</guid><pubDate>%s</pubDate><enclosure url="http://%s/audio/%s.mp3" length="%d" type="audio/mpeg"/><itunes:duration>1</itunes:duration></item>`,
				esc(it.title), esc(it.guid), it.published.UTC().Format(time.RFC1123Z), f.host, it.guid, len(f.audio))
		}
		b.WriteString(`</channel></rss>`)
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	case strings.HasPrefix(r.URL.Path, "/audio/"):
		guid := strings.TrimSuffix(path.Base(r.URL.Path), ".mp3")
		for _, it := range f.items {
			if it.guid == guid && !it.gone {
				w.Header().Set("Content-Type", "audio/mpeg")
				_, _ = w.Write(f.audio)
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

// episodeTitles lists a podcast's downloaded episodes, newest first.
func episodeTitles(t *testing.T, podcast string) []string {
	t.Helper()

	return titlesIn(t, call(t, "podcast_episodes", map[string]any{"item": podcast})["episodes"], "episodes")
}

// waitEpisodes waits for a podcast to hold want episodes and the downloads
// to have stopped.
func waitEpisodes(t *testing.T, podcast string, want int) []string {
	t.Helper()

	var got []string
	for range 60 {
		got = episodeTitles(t, podcast)
		if len(got) == want && len(rows(t, call(t, "podcast_downloads", map[string]any{"library": "Podcasts"})["downloads"], "downloads")) == 0 {
			waitIdle(t)
			return episodeTitles(t, podcast)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the podcast holds %v, want %d episodes", got, want)

	return nil
}

func TestJourneyPodcastLife(t *testing.T) {
	requireProviders(t)
	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}

	audio, err := os.ReadFile(filepath.Join(data, "podcasts", "Behind the Bastards", "Episode 1.mp3"))
	if err != nil {
		t.Fatalf("reading an episode to serve: %v", err)
	}
	// published before the show is subscribed to, so none of them is new
	start := time.Now().Add(-time.Hour)
	feed := &testFeed{host: "zzyzx-feed.test", title: "Zzyzx Feed Show", audio: audio}
	feed.publish(feedItem{guid: "zzyzx-ep-1", title: "Zzyzx Episode One", published: start.Add(-72 * time.Hour)})
	feed.publish(feedItem{guid: "zzyzx-ep-2", title: "Zzyzx Episode Two", published: start.Add(-48 * time.Hour)})
	feed.publish(feedItem{guid: "zzyzx-ep-3", title: "Zzyzx Episode Three", published: start.Add(-24 * time.Hour)})
	t.Cleanup(proxy.Serve(feed.host, feed))

	folder := filepath.Join(data, "podcasts", "Zzyzx Feed Show")
	var id string
	t.Cleanup(func() {
		if id != "" {
			_, _ = invoke("item_delete", map[string]any{"confirm": true, "item": id, "delete_files": true})
		}
		_, _ = invoke("playlist_delete", map[string]any{"playlist": "Zzyzx Feed Queue"})
		if err := os.RemoveAll(folder); err != nil {
			t.Errorf("removing %s: %v", folder, err)
		}
	})
	shows := num(t, call(t, "library_items", map[string]any{"library": "Podcasts", "limit": 1})["total"], "total")

	t.Run("subscribed, with the newest episode", func(t *testing.T) {
		out := call(t, "podcast_add", map[string]any{"feed_url": feed.url(), "library": "Podcasts", "folder": "Zzyzx Feed Show", "download_latest": 1})
		id = text(out["id"])
		if id == "" || out["title"] != feed.title || num(t, out["feed_episodes"], "feed_episodes") != 3 {
			t.Fatalf("podcast_add = %v, want %s with 3 feed episodes", out, feed.title)
		}
		if queued := strs(t, out["queued"], "queued"); !slices.Equal(queued, []string{"Zzyzx Episode Three"}) {
			t.Errorf("queued = %v, want the newest", queued)
		}
		if got := waitEpisodes(t, id, 1); !slices.Equal(got, []string{"Zzyzx Episode Three"}) {
			t.Errorf("episodes = %v, want the newest", got)
		}
		if got := call(t, "item_get", map[string]any{"item": id}); got["feed_url"] != feed.url() || got["path"] != "Zzyzx Feed Show" {
			t.Errorf("item_get = %v", got)
		}
		files, _ := filepath.Glob(filepath.Join(folder, "*.mp3"))
		if len(files) != 1 {
			t.Errorf("files on disk = %v, want the one episode", files)
		}
	})
	if id == "" {
		t.FailNow()
	}

	t.Run("the same feed again, refused", func(t *testing.T) {
		again, err := invoke("podcast_add", map[string]any{"feed_url": feed.url(), "library": "Podcasts", "folder": "Zzyzx Feed Show Again"})
		switch {
		case err == nil:
			t.Errorf("a second subscription to the feed was made: %v", again)
			_, _ = invoke("item_delete", map[string]any{"confirm": true, "item": again["id"], "delete_files": true})
		case !strings.Contains(err.Error(), id):
			t.Errorf("a second subscription to the feed: %v", err)
		}
		if n := num(t, call(t, "library_items", map[string]any{"library": "Podcasts", "limit": 1})["total"], "total"); n != shows+1 {
			t.Errorf("Podcasts holds %d shows, want %d", n, shows+1)
		}
		if _, err := os.Stat(filepath.Join(data, "podcasts", "Zzyzx Feed Show Again")); !os.IsNotExist(err) {
			t.Errorf("the refused subscription made its folder: %v", err)
			_ = os.RemoveAll(filepath.Join(data, "podcasts", "Zzyzx Feed Show Again"))
		}
	})

	t.Run("one fetched from the back catalogue", func(t *testing.T) {
		out := call(t, "podcast_feed_episodes", map[string]any{"item": id})
		if titles := titlesIn(t, out["episodes"], "episodes"); !slices.Equal(titles, []string{"Zzyzx Episode Three", "Zzyzx Episode Two", "Zzyzx Episode One"}) {
			t.Fatalf("feed = %v, want newest first", titles)
		}
		queued := call(t, "podcast_episode_download", map[string]any{"item": id, "indexes": []any{2}})
		if got := strs(t, queued["queued"], "queued"); !slices.Equal(got, []string{"Zzyzx Episode One"}) {
			t.Errorf("queued = %v", got)
		}
		if got := waitEpisodes(t, id, 2); !slices.Equal(got, []string{"Zzyzx Episode Three", "Zzyzx Episode One"}) {
			t.Errorf("episodes = %v, want Three and One", got)
		}
	})

	t.Run("an episode already held, asked for again", func(t *testing.T) {
		out := call(t, "podcast_episode_download", map[string]any{"item": id, "indexes": []any{0, 1}})
		if got := strs(t, out["queued"], "queued"); !slices.Equal(got, []string{"Zzyzx Episode Two"}) {
			t.Errorf("queued = %v, want only the one not held", got)
		}
		if got := strs(t, out["already_held"], "already_held"); !slices.Equal(got, []string{"Zzyzx Episode Three"}) {
			t.Errorf("already_held = %v", got)
		}
		if got := waitEpisodes(t, id, 3); countOf(got, "Zzyzx Episode Three") != 1 {
			t.Errorf("episodes = %v, want Three once", got)
		}
	})

	var two string
	t.Run("an episode edited, and still known by its feed entry", func(t *testing.T) {
		for _, e := range rows(t, call(t, "podcast_episodes", map[string]any{"item": id})["episodes"], "episodes") {
			if e["title"] == "Zzyzx Episode Two" {
				two = text(e["id"])
			}
		}
		call(t, "podcast_episode_edit", map[string]any{"item": id, "episode": two, "title": "Zzyzx Episode Two, Retitled", "season": "1", "number": "2", "type": "bonus"})
		got := call(t, "podcast_episode_get", map[string]any{"item": id, "episode": two})
		if got["title"] != "Zzyzx Episode Two, Retitled" || got["season"] != "1" || got["episode"] != "2" || got["type"] != "bonus" {
			t.Errorf("podcast_episode_get after the edit = %v", got)
		}
		// held by its guid, whatever it is called now
		out := call(t, "podcast_episode_download", map[string]any{"item": id, "indexes": []any{1}})
		if out["queued"] != nil || !slices.Equal(strs(t, out["already_held"], "already_held"), []string{"Zzyzx Episode Two"}) {
			t.Errorf("a retitled episode asked for again: %v", out)
		}
	})

	t.Run("a new episode published, found by check_new", func(t *testing.T) {
		feed.publish(feedItem{guid: "zzyzx-ep-4", title: "Zzyzx Episode Four", published: time.Now().Add(2 * time.Second)})
		out := call(t, "podcast_check_new", map[string]any{"item": id})
		if got := titlesIn(t, out["queued"], "queued"); !slices.Equal(got, []string{"Zzyzx Episode Four"}) {
			t.Errorf("queued = %v, want the new episode", got)
		}
		if got := waitEpisodes(t, id, 4); !slices.Contains(got, "Zzyzx Episode Four") {
			t.Errorf("episodes = %v", got)
		}
		if again := titlesIn(t, call(t, "podcast_check_new", map[string]any{"item": id})["queued"], "queued"); len(again) != 0 {
			t.Errorf("a second check queued %v", again)
		}
	})

	t.Run("download settings, read back", func(t *testing.T) {
		call(t, "podcast_settings", map[string]any{"item": id, "auto_download": true, "schedule": "0 3 * * *", "keep_episodes": 10, "new_per_check": 2})
		want := map[string]any{"auto_download": true, "schedule": "0 3 * * *", "keep_episodes": float64(10), "new_per_check": float64(2)}
		got, _ := call(t, "item_get", map[string]any{"item": id})["downloads"].(map[string]any)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("downloads.%s = %v, want %v (%v)", k, got[k], v, got)
			}
		}
		if got["last_check"] == nil {
			t.Errorf("downloads.last_check is unset after two checks: %v", got)
		}
		call(t, "podcast_settings", map[string]any{"item": id, "auto_download": false})
		if got, _ := call(t, "item_get", map[string]any{"item": id})["downloads"].(map[string]any); got["auto_download"] == true {
			t.Errorf("auto_download still on: %v", got)
		}
	})

	t.Run("an episode queued and in progress, deleted with its file", func(t *testing.T) {
		var one string
		for _, e := range rows(t, call(t, "podcast_episodes", map[string]any{"item": id})["episodes"], "episodes") {
			if e["title"] == "Zzyzx Episode One" {
				one = text(e["id"])
			}
		}
		file := text(call(t, "podcast_episode_get", map[string]any{"item": id, "episode": one})["file"])
		if _, err := os.Stat(filepath.Join(folder, file)); err != nil {
			t.Fatalf("the episode's file %q is not on disk: %v", file, err)
		}
		call(t, "playlist_create", map[string]any{"library": "Podcasts", "name": "Zzyzx Feed Queue", "entries": []any{
			map[string]any{"item": id, "episode": one}, map[string]any{"item": id, "episode": two},
		}})
		call(t, "user_progress_set", map[string]any{"item": id, "episode": one, "percent": 50})
		t.Cleanup(func() { _, _ = invoke("user_progress_remove", map[string]any{"item": id, "episode": one}) })

		// unconfirmed, it says what it would erase and erases nothing
		preview := call(t, "podcast_episode_delete", map[string]any{"item": id, "episode": one, "delete_file": true})
		if deleted, _ := preview["deleted"].(bool); deleted || !strings.HasSuffix(text(preview["file"]), "/"+file) || !strings.Contains(text(preview["note"]), file) {
			t.Errorf("podcast_episode_delete without confirm = %v, want nothing deleted and the file named", preview)
		}
		if _, err := os.Stat(filepath.Join(folder, file)); err != nil {
			t.Fatalf("an unconfirmed delete took the file: %v", err)
		}
		if got := episodeTitles(t, id); len(got) != 4 {
			t.Fatalf("an unconfirmed delete took the episode: %v", got)
		}

		call(t, "podcast_episode_delete", map[string]any{"item": id, "episode": one, "delete_file": true, "confirm": true})

		if _, err := os.Stat(filepath.Join(folder, file)); !os.IsNotExist(err) {
			t.Errorf("the deleted episode's file is still on disk: %v", err)
		}
		if got := episodeTitles(t, id); len(got) != 3 || slices.Contains(got, "Zzyzx Episode One") {
			t.Errorf("episodes = %v, want three without One", got)
		}
		entries := rows(t, call(t, "playlist_get", map[string]any{"playlist": "Zzyzx Feed Queue"})["entries"], "entries")
		if len(entries) != 1 {
			t.Errorf("playlist entries = %v, want only Two", entries)
		}
		for _, it := range rows(t, call(t, "user_in_progress", nil)["items"], "items") {
			if ep, _ := it["episode"].(map[string]any); ep != nil && ep["id"] == one {
				t.Errorf("the deleted episode is still being listened to: %v", it)
			}
		}
		if msg := callErr(t, "user_progress_get", map[string]any{"item": id, "episode": one}); !strings.Contains(msg, "no episode") {
			t.Errorf("progress on the deleted episode: %s", msg)
		}
		// it is still in the feed, and no longer held
		out := call(t, "podcast_episode_download", map[string]any{"item": id, "indexes": []any{3}})
		if got := strs(t, out["queued"], "queued"); !slices.Equal(got, []string{"Zzyzx Episode One"}) {
			t.Errorf("the deleted episode asked for again: %v", out)
		}
		waitEpisodes(t, id, 4)
	})

	t.Run("an episode whose audio is gone", func(t *testing.T) {
		// after Four, which went out two seconds ahead: the feed is listed by
		// publication, so this is what index 0 is
		feed.publish(feedItem{guid: "zzyzx-ep-5", title: "Zzyzx Episode Five", published: time.Now().Add(3 * time.Second), gone: true})
		call(t, "podcast_episode_download", map[string]any{"item": id, "indexes": []any{0}})
		if got := waitEpisodes(t, id, 4); slices.Contains(got, "Zzyzx Episode Five") {
			t.Errorf("an episode with no audio was added: %v", got)
		}
		// not held, so it can be asked for again once it is back
		out := call(t, "podcast_episode_download", map[string]any{"item": id, "indexes": []any{0}})
		if got := strs(t, out["queued"], "queued"); !slices.Equal(got, []string{"Zzyzx Episode Five"}) {
			t.Errorf("a failed episode asked for again: %v", out)
		}
		waitEpisodes(t, id, 4)
	})

	t.Run("not stale, and unsubscribed", func(t *testing.T) {
		for _, f := range rows(t, call(t, "audit_podcast_stale_feed", map[string]any{"library": "Podcasts"})["findings"], "findings") {
			if f["id"] == id {
				t.Errorf("a show published today is stale: %v", f)
			}
		}
		call(t, "item_delete", map[string]any{"confirm": true, "item": id, "delete_files": true})
		deleted := id
		id = ""
		if _, err := os.Stat(folder); !os.IsNotExist(err) {
			t.Errorf("the show's folder is still on disk: %v", err)
		}
		if n := num(t, call(t, "library_items", map[string]any{"library": "Podcasts", "limit": 1})["total"], "total"); n != shows {
			t.Errorf("Podcasts holds %d shows, want %d", n, shows)
		}
		for _, e := range rows(t, call(t, "podcast_episodes", map[string]any{"library": "Podcasts"})["episodes"], "episodes") {
			if e["podcast_id"] == deleted {
				t.Errorf("an episode of the deleted show is still listed: %v", e)
			}
		}
		for _, p := range rows(t, call(t, "playlist_list", nil)["playlists"], "playlists") {
			if p["name"] == "Zzyzx Feed Queue" {
				t.Errorf("the playlist of the deleted show's episodes is still listed: %v", p)
			}
		}
	})
}
