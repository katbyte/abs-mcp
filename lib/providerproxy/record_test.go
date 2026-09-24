package providerproxy

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	// loopback is where a proxy under test listens: any free port, this
	// machine only
	loopback    = "127.0.0.1:0"
	contentJSON = "application/json"
	// recordedAnswer is what a cassette holds before a re-record replaces it
	recordedAnswer = `{"runtime":117}`
)

// get fetches path from upstream through the proxy, asking for gzip the way
// Audiobookshelf's axios does.
func get(t *testing.T, p *Proxy, upstream, path string) (status int, body string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip, compress, deflate, br")
	resp, err := clientThrough(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(raw)
}

// recorded reads a cassette back, by path.
func recorded(t *testing.T, dir, host string) map[string][]*interaction {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(dir, hostFile(host))) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	var c cassette
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	out := map[string][]*interaction{}
	for _, i := range c.Interactions {
		out[i.Path] = append(out[i.Path], i)
	}

	return out
}

// A gzipped answer is stored as the text it decodes to, so a cassette can be
// read, grepped and compared; a binary under octet-stream is elided, text
// under it kept; and a rate limit is passed on but never stored, since a
// replay of one would read as the provider's answer.
func TestRecordDecodesGzipAndKeepsNoRateLimit(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.xml":
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || strings.Contains(r.Header.Get("Accept-Encoding"), "br") {
				t.Errorf("the provider was offered %q, want gzip alone", r.Header.Get("Accept-Encoding"))
			}
			w.Header().Set("Content-Type", "application/rss+xml")
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			_, _ = zw.Write([]byte(`<rss><channel><title>Show</title></channel></rss>`))
			_ = zw.Close()
		case "/episode.mp3":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0x49, 0x44, 0x33, 0x00, 0xff})
		case "/list.txt":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("one\ntwo\n"))
		case "/books/v1/volumes":
			w.Header().Set("Content-Type", contentJSON)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":429}}`)
		}
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")

	dir := t.TempDir()
	p, err := New(Options{Mode: Record, CassetteDir: dir, Addr: loopback, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, body := get(t, p, upstream.URL, "/feed.xml"); !strings.Contains(body, "<title>Show</title>") {
		t.Errorf("the feed came back as %q", body)
	}
	get(t, p, upstream.URL, "/episode.mp3")
	get(t, p, upstream.URL, "/list.txt")
	if status, _ := get(t, p, upstream.URL, "/books/v1/volumes"); status != http.StatusTooManyRequests {
		t.Errorf("the rate limit was answered %d, want it passed on", status)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	got := recorded(t, dir, host)
	feed := got["/feed.xml"]
	if len(feed) != 1 || feed[0].BodyBase64 != "" || !strings.Contains(feed[0].Body, "<title>Show</title>") || feed[0].Headers["Content-Encoding"] != "" {
		t.Errorf("the gzipped feed = %+v, want it decoded and stored as text", feed)
	}
	if ep := got["/episode.mp3"]; len(ep) != 1 || !ep[0].Elided || ep[0].ElidedSize != 5 {
		t.Errorf("a binary = %+v, want it elided", ep)
	}
	if txt := got["/list.txt"]; len(txt) != 1 || txt[0].Elided || txt[0].Body != "one\ntwo\n" {
		t.Errorf("text under octet-stream = %+v, want it kept", txt)
	}
	if limited := got["/books/v1/volumes"]; len(limited) != 0 {
		t.Errorf("a 429 was recorded: %+v", limited[0])
	}

	// and the feed replays, decoded, without the upstream
	upstream.Close()
	replay, err := New(Options{CassetteDir: dir, Addr: loopback, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()
	if _, body := get(t, replay, "http://"+host, "/feed.xml"); !strings.Contains(body, "<title>Show</title>") || len(replay.Misses()) != 0 {
		t.Errorf("replayed %q, misses %v", body, replay.Misses())
	}
}

// Record fills in only what the cassettes lack, so recording a new test's
// lookups leaves every other recording as it was; Rerecord (make record)
// refreshes what is recorded too: a request is fetched live the first time
// the proxy sees it, its recording replaced, and repeats in the same run are
// served that fresh answer without another fetch.
func TestRecordFillsInAndRerecordRefreshes(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	fetched := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", contentJSON)
		_, _ = io.WriteString(w, `{"runtime":118}`)
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")
	fetches := func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return fetched[path]
	}
	dir := t.TempDir()
	writeCassette(t, dir, cassette{Host: host, Interactions: []*interaction{{
		Key: "GET " + host + "/1.0/book/B00", Method: "GET", Host: host, Path: "/1.0/book/B00",
		Status: 200, Headers: map[string]string{"Content-Type": contentJSON}, Body: recordedAnswer,
	}}})
	read := func() map[string][]string {
		out := map[string][]string{}
		for path, is := range recorded(t, dir, host) {
			for _, i := range is {
				out[path] = append(out[path], i.Body)
			}
		}
		return out
	}

	// Record: the recording answers, and only the request it lacks goes out
	p, err := New(Options{Mode: Record, CassetteDir: dir, Addr: loopback, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, got := get(t, p, upstream.URL, "/1.0/book/B00"); got != recordedAnswer || fetches("/1.0/book/B00") != 0 {
		t.Errorf("Record answered a recorded request with %s after %d fetches, want the recording", got, fetches("/1.0/book/B00"))
	}
	if _, got := get(t, p, upstream.URL, "/1.0/book/B01"); !strings.Contains(got, `"runtime":118`) || fetches("/1.0/book/B01") != 1 {
		t.Errorf("Record answered a new request with %s", got)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read(); !slices.Equal(got["/1.0/book/B00"], []string{recordedAnswer}) || len(got["/1.0/book/B01"]) != 1 {
		t.Errorf("after Record the cassette holds %v", got)
	}

	// Rerecord: each request fetched once, the recording replaced in place
	p, err = New(Options{Mode: Rerecord, CassetteDir: dir, Addr: loopback, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, got := get(t, p, upstream.URL, "/1.0/book/B00"); !strings.Contains(got, `"runtime":118`) {
			t.Errorf("Rerecord answered %s, want the fresh answer", got)
		}
	}
	if n := fetches("/1.0/book/B00"); n != 1 {
		t.Errorf("Rerecord fetched a request %d times in one run, want once", n)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got["/1.0/book/B00"]) != 1 || !strings.Contains(got["/1.0/book/B00"][0], `"runtime":118`) || len(got["/1.0/book/B01"]) != 1 {
		t.Errorf("after Rerecord the cassette holds %v, want each request once, refreshed", got)
	}
}

// The suites run the proxy in Record mode whenever ABS_TEST_RECORD is set;
// set to all, as make record sets it, that is a full refresh. (Not parallel:
// it sets the environment.)
func TestRecordAllInTheEnvironmentRerecords(t *testing.T) {
	for value, want := range map[string]Mode{"all": Rerecord, "ALL": Rerecord, "1": Record} {
		t.Setenv(RecordEnv, value)
		p, err := New(Options{Mode: Record, CassetteDir: t.TempDir(), Addr: loopback, Logger: log.New(io.Discard, "", 0)})
		if err != nil {
			t.Fatal(err)
		}
		if p.mode != want {
			t.Errorf("%s=%s runs in mode %d, want %d", RecordEnv, value, p.mode, want)
		}
		_ = p.Close()
	}
	// and it never turns a replay into a recording
	t.Setenv(RecordEnv, "all")
	p, err := New(Options{CassetteDir: t.TempDir(), Addr: loopback, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	if p.mode != Replay {
		t.Errorf("a replay proxy runs in mode %d", p.mode)
	}
	_ = p.Close()
}
