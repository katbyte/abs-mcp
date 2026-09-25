//go:build integration

// Podcasts the suite serves itself, through the provider proxy, so it decides
// what each feed holds, when each episode came out, and when its audio
// arrives: the podcast audits given shows to find, an episode held twice, and
// a download kept waiting while the queue is read.
package acceptance

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// showFeed is a testFeed whose audio can be held back, so a download stays
// in the queue while a journey looks at it.
type showFeed struct {
	*testFeed

	holdMu sync.Mutex
	held   chan struct{} // closed to let the audio through; nil holds nothing
}

func (f *showFeed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.holdMu.Lock()
	held := f.held
	f.holdMu.Unlock()
	if held != nil && strings.HasPrefix(r.URL.Path, "/audio/") {
		select {
		case <-held:
		case <-r.Context().Done():
			return
		}
	}
	f.testFeed.ServeHTTP(w, r)
}

// hold keeps every episode's audio back until release is called, which the
// end of the test does if the journey does not.
func (f *showFeed) hold(t *testing.T) (release func()) {
	t.Helper()

	ch := make(chan struct{})
	f.holdMu.Lock()
	f.held = ch
	f.holdMu.Unlock()
	var once sync.Once
	release = func() {
		once.Do(func() {
			f.holdMu.Lock()
			f.held = nil
			f.holdMu.Unlock()
			close(ch)
		})
	}
	t.Cleanup(release)

	return release
}

// showServe serves a feed of these episodes, in this order, at host for the
// rest of the test, with a seeded episode's second of audio behind each.
func showServe(t *testing.T, host, title string, items ...feedItem) *showFeed {
	t.Helper()

	requireProviders(t)
	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	audio, err := os.ReadFile(filepath.Join(data, "podcasts", "Behind the Bastards", "Episode 1.mp3"))
	if err != nil {
		t.Fatalf("reading an episode to serve: %v", err)
	}
	feed := &showFeed{testFeed: &testFeed{host: host, title: title, audio: audio, items: items}}
	t.Cleanup(proxy.Serve(host, feed))

	return feed
}

// showSubscribe adds a served feed to Podcasts in a folder of its own, with
// the newest latest episodes queued, and removes the show and its folder at
// the end of the test.
func showSubscribe(t *testing.T, feed *showFeed, folder string, latest int) (id string, added map[string]any) {
	t.Helper()

	args := map[string]any{"feed_url": feed.url(), "library": "Podcasts", "folder": folder}
	if latest > 0 {
		args["download_latest"] = latest
	}
	added = call(t, "podcast_add", args)
	id = text(added["id"])
	if id == "" || added["title"] != feed.title {
		t.Fatalf("podcast_add = %v, want %s", added, feed.title)
	}
	t.Cleanup(func() {
		waitIdle(t)
		eventually(t, "deleting "+feed.title, func() error {
			_, err := invoke("item_delete", map[string]any{"confirm": true, "item": id, "delete_files": true})
			if err != nil && strings.Contains(err.Error(), "not found") {
				return nil
			}
			return err
		})
		if err := os.RemoveAll(filepath.Join(dataDir(), "podcasts", folder)); err != nil {
			t.Errorf("removing %s: %v", folder, err)
		}
	})

	return id, added
}

// showFindings is an audit of Podcasts as title -> detail, checked against
// the count it reports.
func showFindings(t *testing.T, audit string) map[string]string {
	t.Helper()

	out := call(t, audit, map[string]any{"library": "Podcasts"})
	found := map[string]string{}
	for _, f := range rows(t, out["findings"], "findings") {
		found[text(f["title"])] = text(f["detail"])
	}
	if n := num(t, out["total_findings"], "total_findings"); n != len(found) {
		t.Errorf("%s reports %d findings and lists %v", audit, n, found)
	}

	return found
}

// showDate is the date a tool gives for a moment.
func showDate(when time.Time) string { return when.UTC().Format("2006-01-02") }

// The podcast audits, each given a show to find and a show to leave alone.
// A show subscribed with nothing downloaded is found by
// audit_podcast_no_episodes until an episode arrives - and while its episodes
// are held back in the download queue, podcast_downloads shows one
// downloading and one waiting, and a second request for them is not queued
// twice. A show whose newest episode came out 200 days ago is found by
// audit_podcast_stale_feed although its feed was checked a moment earlier,
// and one with yesterday's episode is not; the seeded shows, laid out on
// disk, are found for having no feed. audit_all over the podcasts calls
// every book audit not applicable, and says why. An audit that judged a show
// by when its feed was last read, or a queue read that only ever saw it
// empty, would pass on the seeded fixtures alone.
func TestJourneyPodcastAuditsGivenSomethingToFind(t *testing.T) {
	now := time.Now()
	fresh := showServe(t, "zzyzx-fresh.test", "Zzyzx Fresh Show",
		feedItem{guid: "zzyzx-fresh-2", title: "Zzyzx Fresh Two", published: now.Add(-24 * time.Hour)},
		feedItem{guid: "zzyzx-fresh-1", title: "Zzyzx Fresh One", published: now.Add(-48 * time.Hour)})
	quietNewest := now.AddDate(0, 0, -200)
	quiet := showServe(t, "zzyzx-quiet.test", "Zzyzx Quiet Show",
		feedItem{guid: "zzyzx-quiet-2", title: "Zzyzx Quiet Two", published: quietNewest},
		feedItem{guid: "zzyzx-quiet-1", title: "Zzyzx Quiet One", published: now.AddDate(0, 0, -230)})

	freshID, added := showSubscribe(t, fresh, "Zzyzx Fresh Show", 0)
	if added["queued"] != nil {
		t.Errorf("podcast_add with nothing asked for queued %v", added["queued"])
	}

	t.Run("nothing downloaded, found", func(t *testing.T) {
		found := showFindings(t, "audit_podcast_no_episodes")
		if found["Zzyzx Fresh Show"] != "no episodes downloaded" || len(found) != 1 {
			t.Errorf("audit_podcast_no_episodes = %v, want the new show alone", found)
		}
		// no episode is not a stale one: that is the other audit's finding
		if detail, ok := showFindings(t, "audit_podcast_stale_feed")["Zzyzx Fresh Show"]; ok {
			t.Errorf("audit_podcast_stale_feed finds a show with nothing downloaded: %s", detail)
		}
	})

	t.Run("episodes waiting in the queue, then arrived", func(t *testing.T) {
		release := fresh.hold(t)
		feedList := call(t, "podcast_feed_episodes", map[string]any{"item": freshID})
		if titles := titlesIn(t, feedList["episodes"], "episodes"); !slices.Equal(titles, []string{"Zzyzx Fresh Two", "Zzyzx Fresh One"}) {
			t.Fatalf("podcast_feed_episodes = %v, want newest first", titles)
		}
		queue := func() []string {
			t.Helper()
			var out []string
			for _, d := range rows(t, call(t, "podcast_downloads", map[string]any{"library": "Podcasts"})["downloads"], "downloads") {
				out = append(out, fmt.Sprintf("%s: %s %s", d["podcast"], d["episode"], d["status"]))
			}
			return out
		}

		// the newest, held on its way in
		if got := strs(t, call(t, "podcast_episode_download", map[string]any{"item": freshID, "indexes": []any{0}})["queued"], "queued"); !slices.Equal(got, []string{"Zzyzx Fresh Two"}) {
			t.Fatalf("queued = %v, want Zzyzx Fresh Two", got)
		}
		downloading := []string{"Zzyzx Fresh Show: Zzyzx Fresh Two downloading"}
		var got []string
		for range 50 {
			if got = queue(); slices.Equal(got, downloading) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !slices.Equal(got, downloading) {
			t.Errorf("podcast_downloads while its audio is held = %v, want %v", got, downloading)
		}
		// and the other, waiting behind it
		if got := strs(t, call(t, "podcast_episode_download", map[string]any{"item": freshID, "indexes": []any{1}})["queued"], "queued"); !slices.Equal(got, []string{"Zzyzx Fresh One"}) {
			t.Fatalf("queued = %v, want Zzyzx Fresh One", got)
		}
		if got := queue(); !slices.Equal(got, append(downloading, "Zzyzx Fresh Show: Zzyzx Fresh One queued")) {
			t.Errorf("podcast_downloads with one waiting = %v", got)
		}
		again := call(t, "podcast_episode_download", map[string]any{"item": freshID, "indexes": []any{0, 1}})
		if again["queued"] != nil || !slices.Equal(strs(t, again["already_queued"], "already_queued"), []string{"Zzyzx Fresh Two", "Zzyzx Fresh One"}) {
			t.Errorf("asking again while they download = %v, want both already queued", again)
		}
		if _, ok := showFindings(t, "audit_podcast_no_episodes")["Zzyzx Fresh Show"]; !ok {
			t.Error("the show is off audit_podcast_no_episodes before an episode arrived")
		}

		release()
		if got := waitEpisodes(t, freshID, 2); !slices.Equal(got, []string{"Zzyzx Fresh Two", "Zzyzx Fresh One"}) {
			t.Errorf("episodes = %v, want each once, newest first", got)
		}
		if got := queue(); len(got) != 0 {
			t.Errorf("podcast_downloads once they arrived = %v, want nothing", got)
		}
		if found := showFindings(t, "audit_podcast_no_episodes"); len(found) != 0 {
			t.Errorf("audit_podcast_no_episodes after the downloads = %v, want nothing", found)
		}
	})

	quietID, _ := showSubscribe(t, quiet, "Zzyzx Quiet Show", 1)
	waitEpisodes(t, quietID, 1)

	t.Run("a feed checked just now, with nothing new in 200 days", func(t *testing.T) {
		checked := call(t, "podcast_check_new", map[string]any{"item": quietID})
		if queued := rows(t, checked["queued"], "queued"); len(queued) != 0 {
			t.Errorf("podcast_check_new queued %v from a quiet feed", queued)
		}
		downloads, _ := call(t, "item_get", map[string]any{"item": quietID})["downloads"].(map[string]any)
		if last, err := time.Parse(time.RFC3339, text(downloads["last_check"])); err != nil || time.Since(last) > time.Minute {
			t.Fatalf("last_check = %v (%v), want just now", downloads["last_check"], err)
		}

		found := showFindings(t, "audit_podcast_stale_feed")
		days := int(time.Since(quietNewest).Hours() / 24)
		if want := fmt.Sprintf("no new episode in %d days: the newest came out %s", days, showDate(quietNewest)); found["Zzyzx Quiet Show"] != want {
			t.Errorf("audit_podcast_stale_feed on the quiet show = %q, want %q", found["Zzyzx Quiet Show"], want)
		}
		if detail, ok := found["Zzyzx Fresh Show"]; ok {
			t.Errorf("a show with yesterday's episode is stale: %s", detail)
		}
		for _, show := range podcasts {
			if found[show] != "no feed url" {
				t.Errorf("the seeded %s = %q, want no feed url", show, found[show])
			}
		}
		if len(found) != 1+len(podcasts) {
			t.Errorf("audit_podcast_stale_feed = %v, want the quiet show and the seeded ones", found)
		}
	})

	t.Run("download settings that could never run, refused", func(t *testing.T) {
		before := call(t, "item_get", map[string]any{"item": quietID})["downloads"]
		for _, args := range []map[string]any{
			{"schedule": "61 * * * *"},
			{"schedule": "every day at noon"},
			{"schedule": "0 3 * *"},
			{"keep_episodes": -1},
			{"new_per_check": -2},
			{"auto_download": true, "schedule": "0 25 * * *"},
		} {
			args["item"] = quietID
			if msg := callErr(t, "podcast_settings", args); !strings.Contains(msg, "schedule") && !strings.Contains(msg, "0 or more") {
				t.Errorf("podcast_settings %v: %s", args, msg)
			}
		}
		if after := call(t, "item_get", map[string]any{"item": quietID})["downloads"]; !sameJSON(before, after) {
			t.Errorf("the refused settings changed the show: %v -> %v", before, after)
		}
	})

	t.Run("audit_all says which audits cannot apply, and why", func(t *testing.T) {
		all := call(t, "audit_all", map[string]any{"library": "Podcasts"})
		bookOnly := []string{
			"audit_unmatched", "audit_no_audio", "audit_path",
			"audit_missing narrator", "audit_missing series", "audit_missing year", "audit_missing publisher", "audit_missing chapters",
			"audit_chapters", "audit_authors", "audit_narrators", "audit_series", "audit_genres",
			"audit_covers", "audit_unembedded", "audit_matched", "audit_abridged",
		}
		if got := strs(t, all["not_applicable"], "not_applicable"); !slices.Equal(got, bookOnly) {
			t.Errorf("not_applicable over Podcasts = %v, want the book audits %v", got, bookOnly)
		}
		if all["skipped"] != nil {
			t.Errorf("skipped = %v: over podcasts the deep audits do not apply, rather than wait for deep", all["skipped"])
		}
		reasons := map[string]string{}
		for _, r := range rows(t, all["not_run"], "not_run") {
			reasons[strings.TrimSpace(text(r["audit"])+" "+text(r["field"]))] = text(r["reason"])
		}
		for _, name := range bookOnly {
			if !strings.Contains(reasons[name], "holds podcasts") {
				t.Errorf("not_run %s = %q, want it said the library holds podcasts", name, reasons[name])
			}
		}
		counts := map[string]int{}
		for _, r := range rows(t, all["audits"], "audits") {
			counts[text(r["audit"])] = num(t, r["found"], "found")
		}
		if counts["audit_podcast_stale_feed"] != 1+len(podcasts) || slices.Contains(strs(t, all["clean"], "clean"), "audit_podcast_stale_feed") {
			t.Errorf("audit_all stale feeds = %d, want %d", counts["audit_podcast_stale_feed"], 1+len(podcasts))
		}
		if !slices.Contains(strs(t, all["clean"], "clean"), "audit_podcast_no_episodes") {
			t.Errorf("audit_podcast_no_episodes is not clean in audit_all: %v", all)
		}

		// and the reverse, over the books
		books := call(t, "audit_all", map[string]any{"library": "Fiction"})
		reasons = map[string]string{}
		for _, r := range rows(t, books["not_run"], "not_run") {
			reasons[text(r["audit"])] = text(r["reason"])
		}
		for _, name := range []string{"audit_podcast_stale_feed", "audit_podcast_no_episodes"} {
			if !strings.Contains(reasons[name], "holds books") {
				t.Errorf("not_run over Fiction %s = %q, want it said the library holds books", name, reasons[name])
			}
		}
		for _, name := range []string{"audit_covers", "audit_unembedded", "audit_matched", "audit_abridged"} {
			if !strings.Contains(reasons[name], "pass deep") {
				t.Errorf("not_run over Fiction %s = %q, want it said to pass deep", name, reasons[name])
			}
		}
	})
}

// showEpisode is one of a podcast's downloaded episodes by id: its title,
// publish date and file.
type showEpisode struct{ id, title, published, file string }

// showEpisodes lists a podcast's episodes, newest first, with their files.
func showEpisodes(t *testing.T, podcast string) []showEpisode {
	t.Helper()

	var out []showEpisode
	for _, e := range rows(t, call(t, "podcast_episodes", map[string]any{"item": podcast})["episodes"], "episodes") {
		got := call(t, "podcast_episode_get", map[string]any{"item": podcast, "episode": e["id"]})
		out = append(out, showEpisode{id: text(e["id"]), title: text(e["title"]), published: text(e["published"]), file: text(got["file"])})
	}

	return out
}

// A show whose feed carries one episode twice under one title - a re-upload
// under a new guid, which is also what the server's own second download of
// an episode leaves - and publishes its entries out of date order. The feed
// is listed newest first and its indexes fetch what they list; with both
// copies held, the title is refused by every tool that takes one, naming
// both ids, and nothing is changed; the older copy, deleted by id, is
// previewed and then removed with its file, and the other stays, file and
// all. Its twin then edited, subtitle, description and publish date, reads
// back as edited and moves in the order. A tool that took the first episode
// of a shared title would edit or erase whichever the server listed first.
func TestJourneyAnEpisodeHeldTwice(t *testing.T) {
	now := time.Now()
	const twin = "Zzyzx Twin Episode"
	feed := showServe(t, "zzyzx-twin.test", "Zzyzx Twin Show",
		// the publisher's order, not the date's
		feedItem{guid: "zzyzx-twin-new", title: twin, published: now.Add(-72 * time.Hour)},
		feedItem{guid: "zzyzx-old", title: "Zzyzx Old Episode", published: now.Add(-240 * time.Hour)},
		feedItem{guid: "zzyzx-solo", title: "Zzyzx Solo Episode", published: now.Add(-24 * time.Hour)},
		feedItem{guid: "zzyzx-twin-old", title: twin, published: now.Add(-120 * time.Hour)},
	)
	id, _ := showSubscribe(t, feed, "Zzyzx Twin Show", 0)
	folder := filepath.Join(dataDir(), "podcasts", "Zzyzx Twin Show")
	onDisk := func(file string) bool {
		_, err := os.Stat(filepath.Join(folder, file))
		return err == nil
	}

	t.Run("the feed newest first, and its index what is fetched", func(t *testing.T) {
		listed := rows(t, call(t, "podcast_feed_episodes", map[string]any{"item": id})["episodes"], "episodes")
		var got []string
		for i, e := range listed {
			got = append(got, fmt.Sprintf("%v %s %s", e["index"], e["title"], e["published"]))
			if num(t, e["index"], "index") != i {
				t.Errorf("row %d has index %v", i, e["index"])
			}
		}
		want := []string{
			"0 Zzyzx Solo Episode " + showDate(now.Add(-24*time.Hour)),
			"1 " + twin + " " + showDate(now.Add(-72*time.Hour)),
			"2 " + twin + " " + showDate(now.Add(-120*time.Hour)),
			"3 Zzyzx Old Episode " + showDate(now.Add(-240*time.Hour)),
		}
		if !slices.Equal(got, want) {
			t.Fatalf("podcast_feed_episodes = %v, want %v", got, want)
		}
		// index 0 is the newest, not the feed's first entry
		if q := strs(t, call(t, "podcast_episode_download", map[string]any{"item": id, "indexes": []any{0}})["queued"], "queued"); !slices.Equal(q, []string{"Zzyzx Solo Episode"}) {
			t.Errorf("index 0 queued %v, want the newest", q)
		}
		waitEpisodes(t, id, 1)
		if q := strs(t, call(t, "podcast_episode_download", map[string]any{"item": id, "indexes": []any{1, 2}})["queued"], "queued"); !slices.Equal(q, []string{twin, twin}) {
			t.Errorf("indexes 1 and 2 queued %v, want both copies", q)
		}
		if got := waitEpisodes(t, id, 3); !slices.Equal(got, []string{"Zzyzx Solo Episode", twin, twin}) {
			t.Errorf("episodes = %v, want Solo then both copies", got)
		}
	})

	held := showEpisodes(t, id)
	if len(held) != 3 {
		t.Fatalf("episodes = %v, want 3", held)
	}
	newer, older := held[1], held[2]
	if newer.title != twin || older.title != twin || newer.published != showDate(now.Add(-72*time.Hour)) || older.published != showDate(now.Add(-120*time.Hour)) {
		t.Fatalf("the copies = %v and %v, want the newer then the older", newer, older)
	}
	if newer.file == "" || newer.file == older.file || !onDisk(newer.file) || !onDisk(older.file) {
		t.Fatalf("the copies' files = %q and %q, want two files on disk", newer.file, older.file)
	}

	t.Run("the shared title, refused everywhere, naming both", func(t *testing.T) {
		for tool, args := range map[string]map[string]any{
			"podcast_episode_get":    {"item": id, "episode": twin},
			"podcast_episode_edit":   {"item": id, "episode": twin, "title": "Zzyzx Twin, Retitled"},
			"podcast_episode_delete": {"item": id, "episode": twin, "delete_file": true, "confirm": true},
		} {
			if msg := callErr(t, tool, args); !strings.Contains(msg, newer.id) || !strings.Contains(msg, older.id) || !strings.Contains(msg, "pass an id") {
				t.Errorf("%s by the shared title: %s", tool, msg)
			}
		}
		if after := showEpisodes(t, id); !slices.Equal(after, held) {
			t.Errorf("the refusals changed the episodes: %v -> %v", held, after)
		}
		if !onDisk(newer.file) || !onDisk(older.file) {
			t.Error("a refusal took a file")
		}
	})

	t.Run("the older copy deleted by id, previewed first", func(t *testing.T) {
		preview := call(t, "podcast_episode_delete", map[string]any{"item": id, "episode": older.id, "delete_file": true})
		if deleted, _ := preview["deleted"].(bool); deleted || preview["episode_id"] != older.id || !strings.HasSuffix(text(preview["file"]), "/"+older.file) || !strings.Contains(text(preview["note"]), "not confirmed") {
			t.Errorf("the preview = %v, want nothing deleted and the older copy's file named", preview)
		}
		if after := showEpisodes(t, id); !slices.Equal(after, held) || !onDisk(older.file) {
			t.Fatalf("the preview changed something: %v, file there %v", after, onDisk(older.file))
		}

		done := call(t, "podcast_episode_delete", map[string]any{"item": id, "episode": older.id, "delete_file": true, "confirm": true})
		if deleted, _ := done["deleted"].(bool); !deleted || done["file_erased"] != true || done["episode_id"] != older.id {
			t.Errorf("podcast_episode_delete = %v, want the older copy and its file", done)
		}
		if after := showEpisodes(t, id); !slices.Equal(after, []showEpisode{held[0], newer}) {
			t.Errorf("episodes = %v, want Solo and the newer copy", after)
		}
		if onDisk(older.file) || !onDisk(newer.file) {
			t.Errorf("on disk: the older copy %v, the newer %v; want only the newer", onDisk(older.file), onDisk(newer.file))
		}
		// one left under the title, so the title names it
		if got := call(t, "podcast_episode_get", map[string]any{"item": id, "episode": twin}); got["id"] != newer.id {
			t.Errorf("the title now = %v, want the newer copy", got["id"])
		}
	})

	t.Run("an episode edited, read back", func(t *testing.T) {
		solo := held[0]
		moved := now.AddDate(0, 0, -30).UTC().Truncate(time.Second)
		edit := map[string]any{
			"item": id, "episode": solo.id,
			"subtitle": "Zzyzx: a subtitle", "description": "Zzyzx: what the episode is about, at some length.",
			"pub_date": moved.Format(time.RFC1123Z),
		}
		out := call(t, "podcast_episode_edit", edit)
		if got := strs(t, out["changed"], "changed"); !slices.Equal(got, []string{"subtitle", "description", "pub_date"}) || out["unchanged"] != nil {
			t.Errorf("podcast_episode_edit = %v, want subtitle, description and pub_date changed", out)
		}
		got := call(t, "podcast_episode_get", map[string]any{"item": id, "episode": solo.id})
		if got["subtitle"] != edit["subtitle"] || got["description"] != edit["description"] || got["published"] != showDate(moved) {
			t.Errorf("podcast_episode_get after the edit = %v", got)
		}
		// a month back, it now comes after the newer copy
		if order := showEpisodes(t, id); len(order) != 2 || order[0].id != newer.id || order[1].id != solo.id {
			t.Errorf("episodes after the date moved = %v, want the copy, then Solo", order)
		}

		again := call(t, "podcast_episode_edit", edit)
		if got := strs(t, again["unchanged"], "unchanged"); !slices.Equal(got, []string{"subtitle", "description", "pub_date"}) || len(strs(t, again["changed"], "changed")) != 0 {
			t.Errorf("the same edit again = %v, want everything unchanged", again)
		}
		if msg := callErr(t, "podcast_episode_edit", map[string]any{"item": id, "episode": solo.id, "type": "minisode"}); !strings.Contains(msg, "full, trailer, bonus") {
			t.Errorf("an unknown type: %s", msg)
		}
		if got := call(t, "podcast_episode_get", map[string]any{"item": id, "episode": solo.id}); got["type"] == "minisode" {
			t.Errorf("the refused type was stored: %v", got)
		}
	})

	t.Run("a new episode, checked for, offered with no index", func(t *testing.T) {
		feed.publish(feedItem{guid: "zzyzx-twin-news", title: "Zzyzx News Episode", published: time.Now().Add(2 * time.Second)})
		queued := rows(t, call(t, "podcast_check_new", map[string]any{"item": id})["queued"], "queued")
		if len(queued) != 1 || queued[0]["title"] != "Zzyzx News Episode" {
			t.Fatalf("podcast_check_new queued %v, want the new episode", queued)
		}
		if index, ok := queued[0]["index"]; ok {
			t.Errorf("a check_new row carries index %v, which is no place in the feed", index)
		}
		if got := waitEpisodes(t, id, 3); got[0] != "Zzyzx News Episode" {
			t.Errorf("episodes = %v, want the new one first", got)
		}
	})
}
