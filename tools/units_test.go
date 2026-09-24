package tools

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Every answer gives a length, position or total as a number of seconds in a
// field ending _s, and a size as a number of bytes: "11h 5m" and size_mb
// could not be compared or added up, and a size in whole gigabytes said a
// 50 MB podcast library held nothing. Chapter and bookmark times keep the
// server's fractions, since they are passed back; the rest are whole seconds.

// wantNumbers checks the number at each dotted path in an answer:
// "chapter_list.1.start_s" is the second chapter's start.
func wantNumbers(t *testing.T, tool string, out map[string]any, want map[string]float64) {
	t.Helper()

	for path, n := range want {
		if got, ok := dig(out, path).(float64); !ok || got != n {
			t.Errorf("%s %s = %v, want %v", tool, path, dig(out, path), n)
		}
	}
}

// wantAbsent checks an answer carries nothing at each dotted path.
func wantAbsent(t *testing.T, tool string, out map[string]any, paths ...string) {
	t.Helper()

	for _, path := range paths {
		if v := dig(out, path); v != nil {
			t.Errorf("%s %s = %v, want it absent", tool, path, v)
		}
	}
}

// dig is the value at a dotted path in an answer, nil when there is none.
func dig(out map[string]any, path string) any {
	var v any = out
	for step := range strings.SplitSeq(path, ".") {
		if i, err := strconv.Atoi(step); err == nil {
			l, ok := v.([]any)
			if !ok || i >= len(l) {
				return nil
			}
			v = l[i]
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[step]
	}
	return v
}

func TestItemGetInSecondsAndBytes(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, `{"id":"`+itemID+`","libraryId":"`+libID+`","mediaType":"book","size":734003200,`+
		`"libraryFiles":[{"metadata":{"filename":"cover.jpg","size":20480},"fileType":"image"}],`+
		`"userMediaProgress":{"id":"p1","libraryItemId":"`+itemID+`","currentTime":1800.6,"progress":0.25},`+
		`"media":{"metadata":{"title":"Dune"},"duration":7200.4,`+
		`"audioFiles":[{"index":1,"duration":7200.4,"metadata":{"filename":"01.m4b","size":734003200}}],`+
		`"chapters":[{"id":0,"start":0,"end":1234.567,"title":"One"},{"id":1,"start":1234.567,"end":7200.4,"title":"Two"}]}}`)
	call := toolCaller(t, f)

	out, err := call("item_get", map[string]any{"item": itemID, "chapters": true, "files": true})
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "item_get", out, map[string]float64{
		"duration_s":              7200,
		"size":                    734003200,
		"progress.current_time_s": 1801,
		"chapter_list.0.start_s":  0,
		"chapter_list.0.end_s":    1234.567,
		"chapter_list.1.start_s":  1234.567,
		"chapter_list.1.end_s":    7200.4,
		"track_list.0.duration_s": 7200,
		"track_list.0.size":       734003200,
		"other_files.0.size":      20480,
	})
	wantAbsent(t, "item_get", out, "duration", "size_mb", "progress.current_time", "progress.current_seconds", "chapter_list.0.start_seconds", "track_list.0.size_mb")
}

func TestItemMatchInSeconds(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/search/providers", `{"providers":{"books":[{"value":"audible"}],"podcasts":[]}}`)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", "", `"duration":75600.4`))
	// a provider gives the length in minutes
	f.json("GET /api/search/books", `[{"title":"Dune","author":"Frank Herbert","asin":"B0DUNE","duration":1260}]`)
	call := toolCaller(t, f)

	out, err := call("item_match", map[string]any{"item": itemID, "provider": "audible"})
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "item_match", out, map[string]float64{"item_duration_s": 75600, "candidates.0.duration_s": 75600})
	wantAbsent(t, "item_match", out, "item_duration", "candidates.0.duration")
}

func TestLibraryGetInSecondsAndBytes(t *testing.T) {
	t.Parallel()

	t.Run("from the server's stats", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		oneLibrary(f)
		f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"accessAllTags":true,"accessExplicitContent":true}}`)
		f.json("GET /api/libraries/"+libID, `{"library":{"id":"`+libID+`","name":"Books","mediaType":"book"},"filterdata":{},"issues":0}`)
		f.json("GET /api/libraries/"+libID+"/stats", `{"totalItems":2,"totalDuration":90000.6,"totalSize":1610612736,"numAudioTracks":2,`+
			`"longestItems":[{"id":"i1","title":"Long","duration":60000.4}],"largestItems":[{"id":"i1","title":"Long","size":1073741824}]}`)
		call := toolCaller(t, f)

		out, err := call("library_get", map[string]any{"library": "Books"})
		if err != nil {
			t.Fatal(err)
		}
		// 1.5 GB, which total_size_gb gave as 1
		wantNumbers(t, "library_get", out, map[string]float64{
			"total_duration_s": 90001, "total_size": 1610612736, "longest.0.duration_s": 60000, "largest.0.size": 1073741824,
		})
		wantAbsent(t, "library_get", out, "total_duration", "total_size_gb", "longest.0.duration", "largest.0.size_mb")
	})

	t.Run("from the books a restricted key sees", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		oneLibrary(f)
		f.json("GET /api/me", `{"id":"u1","username":"kid","type":"user","permissions":{"accessAllLibraries":true,"accessAllTags":false,"accessExplicitContent":true}}`)
		// the listing gives the size beside the id, not in the media
		visible := strings.Replace(item(bookB1, "Visible Book", "", `"duration":600.6,"numAudioFiles":1`), `{"id":`, `{"size":52428800,"id":`, 1)
		f.json("GET /api/libraries/"+libID+"/items", page(visible))
		f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
		f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
		call := toolCaller(t, f)

		out, err := call("library_get", map[string]any{"library": "Books"})
		if err != nil {
			t.Fatal(err)
		}
		wantNumbers(t, "library_get", out, map[string]float64{
			"total_duration_s": 601, "total_size": 52428800, "longest.0.duration_s": 601, "largest.0.size": 52428800,
		})
	})
}

func TestSeriesListInSeconds(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"Dune","books":[],"totalDuration":75600.4}],"total":1}`)
	call := toolCaller(t, f)

	out, err := call("series_list", map[string]any{"library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "series_list", out, map[string]float64{"series.0.duration_s": 75600})
	wantAbsent(t, "series_list", out, "series.0.duration")
}

func TestServerSizesInBytes(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /status", `{"serverVersion":"2.30.0"}`)
	f.json("GET /api/me", `{"id":"u1","username":"root","type":"root"}`)
	f.json("GET /api/libraries", `{"libraries":[]}`)
	f.json("GET /api/stats/server", `{"books":{"totalSize":1610612736,"numItems":2},"podcasts":{"totalSize":52428800,"numItems":1},"total":{"totalSize":1663041536,"numItems":3,"numAudioFiles":4}}`)
	f.json("GET /api/backups", `{"backups":[{"id":"b1","filename":"b1.audiobookshelf","fileSize":5242880,"createdAt":1700000000000}],"backupLocation":"/metadata/backups"}`)
	call := toolCaller(t, f)

	out, err := call("server_info", nil)
	if err != nil {
		t.Fatal(err)
	}
	// the podcasts' 50 MB was 0 in whole gigabytes
	wantNumbers(t, "server_info", out, map[string]float64{"totals.total_size": 1663041536, "totals.books_size": 1610612736, "totals.podcasts_size": 52428800})
	wantAbsent(t, "server_info", out, "totals.total_size_gb", "totals.books_size_gb", "totals.podcasts_size_gb")

	out, err = call("server_backups", nil)
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "server_backups", out, map[string]float64{"backups.0.size": 5242880})
	wantAbsent(t, "server_backups", out, "backups.0.size_mb")
}

func TestListeningTimesInSeconds(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	accounts(f)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", `"duration":3600.6`))
	f.json("GET /api/me/progress/"+bookB1, `{"id":"mp1","libraryItemId":"`+bookB1+`","currentTime":1799.5,"duration":3600.6,"progress":0.5}`)
	f.json("GET /api/me/bookmarks", `{"bookmarks":[{"libraryItemId":"`+bookB1+`","title":"Mark","time":12.345}]}`)
	today := time.Now().UTC().Format("2006-01-02")
	f.json("GET /api/me/listening-stats", `{"totalTime":7200.4,"today":600.4,"days":{"`+today+`":600.4},`+
		`"items":{"`+bookB1+`":{"id":"`+bookB1+`","timeListening":7200.4,"mediaMetadata":{"title":"First"}}}}`)
	f.json("GET /api/me/stats/year/2025", `{"totalListeningTime":7200.4,"topAuthors":[{"name":"A","time":3600.4}],"topNarrators":[{"name":"N","time":1800.4}],`+
		`"topGenres":[{"genre":"G","time":900.4}],"booksFinished":[{"id":"`+bookB1+`","title":"First","duration":3600.6}]}`)
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	f.mux.HandleFunc("GET /api/me/listening-sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total":2,"sessions":[{"id":"s1","userId":"` + meID + `","libraryItemId":"` + bookB1 + `","displayTitle":"First",` +
			`"duration":3600.6,"timeListening":300.4,"currentTime":1799.5,"updatedAt":` + now + `},` +
			// a session barely begun has a time and a position all the same
			`{"id":"s0","userId":"` + meID + `","libraryItemId":"` + bookB1 + `","displayTitle":"First","duration":3600.6,"timeListening":0.1,"updatedAt":` + now + `}]}`))
	})
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool   string
		args   map[string]any
		want   map[string]float64
		absent []string
	}{
		{"user_progress_get", map[string]any{"item": bookB1}, map[string]float64{"duration_s": 3601, "progress.current_time_s": 1800}, []string{"duration", "progress.current_time"}},
		// a bookmark is removed by its exact time, so the fraction stays
		{"user_bookmarks", nil, map[string]float64{"bookmarks.0.time_s": 12.345}, []string{"bookmarks.0.time", "bookmarks.0.seconds"}},
		{"user_stats", nil, map[string]float64{
			"total_listened_s": 7200, "today_s": 600, "last_7_days_s": 600, "last_30_days_s": 600, "top_items.0.time_s": 7200,
		}, []string{"total_listened", "today", "top_items.0.time"}},
		{"user_stats", map[string]any{"year": 2025}, map[string]float64{
			"total_listened_s": 7200, "top_authors.0.time_s": 3600, "top_narrators.0.time_s": 1800, "top_genres.0.time_s": 900, "finished.0.time_s": 3601,
		}, []string{"total_listened", "top_authors.0.time", "finished.0.time"}},
		{"user_history", nil, map[string]float64{
			"sessions.0.listened_s": 300, "sessions.0.position_s": 1800, "sessions.1.listened_s": 0, "sessions.1.position_s": 0,
		}, []string{"sessions.0.listened", "sessions.0.position"}},
	} {
		out, err := call(tc.tool, tc.args)
		if err != nil {
			t.Fatalf("%s %v: %v", tc.tool, tc.args, err)
		}
		wantNumbers(t, tc.tool, out, tc.want)
		wantAbsent(t, tc.tool, out, tc.absent...)
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

// The length compared against the provider's recording, on both sides.
func TestMatchDurationsInSeconds(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("11111111-1111-4111-8111-000000000001", "Killingly", `"authorName":"Katharine Beutner"`, `"duration":44580.4`),
		item("11111111-1111-4111-8111-000000000002", "Horus Rising", `"authorName":"Dan Abnett","asin":"B0UK"`, `"duration":40000.6`),
	))
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("title") {
		case "Killingly":
			_, _ = w.Write([]byte(`[{"title":"Killingly","author":"Katharine Beutner","asin":"B0BZ","duration":743}]`))
		case "B0UK":
			_, _ = w.Write([]byte(`[{"title":"Horus Rising","author":"Dan Abnett","asin":"B0UK","duration":755}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	call := toolCaller(t, f)

	out, err := call("item_match_batch", nil)
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "item_match_batch", out, map[string]float64{"rows.0.duration_s": 44580, "rows.0.best.duration_s": 44580})
	wantAbsent(t, "item_match_batch", out, "rows.0.duration", "rows.0.best.duration")

	out, err = call("audit_matched", map[string]any{"providers": []any{"audible"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := dig(out, "findings.0.problems.0"); got != "duration_off" {
		t.Fatalf("audit_matched = %v, want Horus Rising's recording a different length", out["findings"])
	}
	wantNumbers(t, "audit_matched", out, map[string]float64{"findings.0.duration_s": 40001, "findings.0.provider.duration_s": 45300})
	wantAbsent(t, "audit_matched", out, "findings.0.provider.duration")
}
