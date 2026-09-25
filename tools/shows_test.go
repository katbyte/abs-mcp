package tools

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

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
	if out["queued"] != nil || !slices.Equal(anyStrings(out["already_queued"]), []string{"One"}) {
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
	if !ok || !slices.Equal(anyStrings(out["changed"]), []string{"pub_date"}) || str(t, ep["published"]) != "2024-03-01" {
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
	if len(anyStrings(out["changed"])) != 0 || !slices.Equal(anyStrings(out["unchanged"]), []string{"pub_date"}) {
		t.Errorf("the date it has = %v, want unchanged", out)
	}
}
