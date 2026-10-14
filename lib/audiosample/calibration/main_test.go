package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	alice  = recording{ID: 1, Work: "alice", Kind: "solo", Reader: "A reader"}
	island = recording{ID: 2, Work: "island", Kind: "solo", Reader: "B reader"}
	broken = recording{ID: 3, Work: "alice", Kind: "broken", Reader: "C reader"}
)

// books are LibriVox's answers by id: numbers written as text and as numbers, a section with no number, a link with a trailing slash, and books
// that are not there, have no mp3s, or name a file outside their folder.
var books = map[string]string{
	"1": `{"books": [{"title": "Alice", "url_iarchive": "https://www.archive.org/details/alice_one/", "totaltimesecs": 100, "sections": [
		{"section_number": "1", "title": "One", "playtime": "60", "readers": [{"display_name": "A"}]},
		{"section_number": "", "title": null, "playtime": 40, "readers": []}]}]}`,
	"2": `{"books": [{"title": "Island", "url_iarchive": "https://archive.org/details/island_two"}]}`,
	"3": `{"books": [{"title": "Broken", "url_iarchive": "https://archive.org/details/broken_three"}]}`,
	"4": `{"books": []}`,
	"5": `{"books": [{"title": "Silent", "url_iarchive": "https://archive.org/details/silent_five"}]}`,
	"6": `{"books": [{"title": "Escape", "url_iarchive": "https://archive.org/details/escape_six"}]}`,
}

// items are archive.org's answers by item: files out of order, a format not fetched, and sizes and lengths written each way.
var items = map[string]string{
	"alice_one": `{"files": [
		{"name": "b.mp3", "format": "64Kbps MP3", "size": "3", "length": "0:40"},
		{"name": "a.mp3", "format": "64Kbps MP3", "size": "2", "length": "60.5"},
		{"name": "a.ogg", "format": "Ogg Vorbis", "size": "9", "length": "60.5"}]}`,
	"island_two":   `{"files": [{"name": "c.mp3", "format": "64Kbps MP3", "size": 4, "length": "1:00:00"}]}`,
	"broken_three": `{"files": [{"name": "d.mp3", "format": "64Kbps MP3", "size": "1", "length": "1"}]}`,
	"silent_five":  `{"files": [{"name": "e.ogg", "format": "Ogg Vorbis", "size": "1", "length": "1"}]}`,
	"escape_six":   `{"files": [{"name": "../f.mp3", "format": "64Kbps MP3", "size": "1", "length": "1"}]}`,
}

// files are the downloads: b.mp3 fails once before it answers, and d.mp3 never answers.
var files = map[string]string{
	"alice_one/a.mp3":  "aa",
	"alice_one/b.mp3":  "bbb",
	"island_two/c.mp3": "cccc",
}

// fake stands in for LibriVox and archive.org, and counts what each is asked.
type fake struct {
	mu   sync.Mutex
	hits map[string]int
}

func (f *fake) hit(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.hits[kind]++

	return f.hits[kind]
}

func (f *fake) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.hits[kind]
}

func newFake(t *testing.T) (fetcher, *fake, *bytes.Buffer) {
	t.Helper()

	f := &fake{hits: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /librivox", func(w http.ResponseWriter, r *http.Request) {
		f.hit("librivox")
		if b, ok := books[r.URL.Query().Get("id")]; ok {
			_, _ = w.Write([]byte(b))
			return
		}
		http.Error(w, "down", http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /metadata/{item}", func(w http.ResponseWriter, r *http.Request) {
		f.hit("metadata")
		_, _ = w.Write([]byte(items[r.PathValue("item")]))
	})
	mux.HandleFunc("GET /download/{item}/{name}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("item") + "/" + r.PathValue("name")
		n := f.hit(key)
		body, ok := files[key]
		if !ok || (key == "alice_one/b.mp3" && n == 1) {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(body))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	log := &bytes.Buffer{}

	return fetcher{librivox: srv.URL + "/librivox?id=%d", metadata: srv.URL + "/metadata", download: srv.URL + "/download", unit: time.Millisecond, log: log}, f, log
}

func TestRunFetches(t *testing.T) {
	t.Parallel()

	f, srv, log := newFake(t)
	dir := t.TempDir()
	o := options{recordings: []recording{alice, island}, dir: dir}

	var out bytes.Buffer
	missing, err := f.run(t.Context(), &out, o)
	if err != nil || missing != nil {
		t.Fatalf("run: %v missing, %v\n%s", missing, err, log)
	}

	want := manifest{Recordings: []entry{
		{
			recording: alice, Title: "Alice", Archive: "alice_one", Folder: filepath.Join("alice", "1-solo"), Seconds: 100,
			Sections: []section{{Number: 1, Title: "One", Readers: []string{"A"}, Seconds: 60}, {Number: 2, Readers: []string{}, Seconds: 40}},
			Files:    []file{{Name: "a.mp3", Seconds: 60.5, Bytes: 2}, {Name: "b.mp3", Seconds: 40, Bytes: 3}},
		},
		{recording: island, Title: "Island", Archive: "island_two", Folder: filepath.Join("island", "2-solo"), Files: []file{{Name: "c.mp3", Seconds: 3600, Bytes: 4}}},
	}}
	if got := readManifest(t, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("manifest:\n got %+v\nwant %+v", got, want)
	}

	for name, body := range map[string]string{"alice/1-solo/a.mp3": "aa", "alice/1-solo/b.mp3": "bbb", "island/2-solo/c.mp3": "cccc"} {
		if b, err := fs.ReadFile(os.DirFS(dir), name); err != nil || string(b) != body {
			t.Errorf("%s: %q, %v; want %q", name, b, err, body)
		}
	}

	for _, line := range []string{"Alice (A reader): 2 files, 0 MB, alice_one", "Island (B reader): 1 files, 0 MB, island_two", "2 recordings, 0 MB", "manifest: " + filepath.Join(dir, manifestName), "  fetched a.mp3", "  fetched b.mp3", "  fetched c.mp3"} {
		if !strings.Contains(out.String(), line+"\n") {
			t.Errorf("output has no line %q:\n%s", line, out.String())
		}
	}

	if !strings.Contains(log.String(), "b.mp3: ") || !strings.Contains(log.String(), "retrying") {
		t.Errorf("a failed fetch was not reported as retried:\n%s", log)
	}

	// a second run asks neither service anything: the manifest says what there is, and every file is there at its size
	before := []int{srv.count("librivox"), srv.count("metadata"), srv.count("alice_one/a.mp3")}
	out.Reset()
	if missing, err := f.run(t.Context(), &out, o); err != nil || missing != nil {
		t.Fatalf("second run: %v missing, %v", missing, err)
	}
	if after := []int{srv.count("librivox"), srv.count("metadata"), srv.count("alice_one/a.mp3")}; !reflect.DeepEqual(after, before) {
		t.Errorf("second run asked again: %v, then %v", before, after)
	}
	if strings.Contains(out.String(), "fetched") {
		t.Errorf("second run fetched again:\n%s", out.String())
	}
}

func TestRunListsAndWritesTheManifestOnly(t *testing.T) {
	t.Parallel()

	f, srv, _ := newFake(t)
	dir := t.TempDir()

	// a manifest that cannot be read is described again, not trusted
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if _, err := f.run(t.Context(), &out, options{recordings: []recording{alice, island}, dir: dir, list: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := fs.ReadFile(os.DirFS(dir), manifestName); string(b) != "not json" {
		t.Errorf("list wrote the manifest: %s", b)
	}
	if strings.Contains(out.String(), "manifest:") || !strings.HasSuffix(out.String(), "2 recordings, 0 MB\n") {
		t.Errorf("list output:\n%s", out.String())
	}

	if _, err := f.run(t.Context(), &out, options{recordings: []recording{alice, island}, dir: dir, manifestOnly: true}); err != nil {
		t.Fatal(err)
	}
	if got := readManifest(t, dir); len(got.Recordings) != 2 {
		t.Errorf("manifest-only wrote %d recordings, want 2", len(got.Recordings))
	}
	if _, err := os.Stat(filepath.Join(dir, "alice")); !os.IsNotExist(err) {
		t.Errorf("manifest-only fetched files: %v", err)
	}

	if _, err := f.run(t.Context(), &out, options{recordings: []recording{alice, island}, dir: dir, only: map[int]bool{2: true}}); err != nil {
		t.Fatal(err)
	}
	if srv.count("alice_one/a.mp3") != 0 || srv.count("island_two/c.mp3") != 1 {
		t.Errorf("only 2 fetched alice %d times and island %d times, want 0 and 1", srv.count("alice_one/a.mp3"), srv.count("island_two/c.mp3"))
	}
	if got := readManifest(t, dir); len(got.Recordings) != 2 {
		t.Errorf("only 2 left %d recordings in the manifest, want 2", len(got.Recordings))
	}
}

func TestRunReportsWhatCouldNotBeFetched(t *testing.T) {
	t.Parallel()

	f, srv, log := newFake(t)
	dir := t.TempDir()

	missing, err := f.run(t.Context(), &bytes.Buffer{}, options{recordings: []recording{broken}, dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"broken_three/d.mp3"}; !reflect.DeepEqual(missing, want) {
		t.Errorf("missing %v, want %v", missing, want)
	}
	if n := srv.count("broken_three/d.mp3"); n != 8 {
		t.Errorf("tried %d times, want 8", n)
	}
	if !strings.Contains(log.String(), "  broken_three/d.mp3: ") {
		t.Errorf("the file given up on was not reported:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(dir, "alice", "3-broken", "d.mp3")); !os.IsNotExist(err) {
		t.Errorf("a file that was never fetched is there: %v", err)
	}
}

func TestRunRefuses(t *testing.T) {
	t.Parallel()

	cases := map[int]string{
		4: "LibriVox has no book 4",
		5: "silent_five has no 64 kbps mp3s",
		6: `escape_six names a file outside its folder: "../f.mp3"`,
		7: "answered 500",
	}
	for id, want := range cases {
		t.Run(strconv.Itoa(id), func(t *testing.T) {
			t.Parallel()

			f, srv, _ := newFake(t)
			_, err := f.run(t.Context(), &bytes.Buffer{}, options{recordings: []recording{{ID: id, Work: "w", Kind: "k"}}, dir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("run: %v, want %q", err, want)
			}
			if id == 7 && srv.count("librivox") != 4 {
				t.Errorf("LibriVox asked %d times, want 4", srv.count("librivox"))
			}
		})
	}
}

func TestLoose(t *testing.T) {
	t.Parallel()

	cases := map[string]loose{`"12"`: "12", `12`: "12", `12.5`: "12.5", `null`: "", `""`: ""}
	for in, want := range cases {
		var l loose
		if err := json.Unmarshal([]byte(in), &l); err != nil || l != want {
			t.Errorf("%s: %q, %v; want %q", in, l, err, want)
		}
	}

	var l loose
	if err := json.Unmarshal([]byte(`true`), &l); err == nil {
		t.Errorf("true: %q, no error", l)
	}
}

func TestLooseNumber(t *testing.T) {
	t.Parallel()

	cases := map[loose]int{"": 0, "12": 12, "12.9": 12}
	for in, want := range cases {
		if got, err := in.number(); err != nil || got != want {
			t.Errorf("%q: %d, %v; want %d", in, got, err, want)
		}
	}

	if _, err := loose("twelve").number(); err == nil {
		t.Error("twelve: no error")
	}
}

func TestLooseSeconds(t *testing.T) {
	t.Parallel()

	cases := map[loose]float64{"": 0, "787.02": 787.02, "13:07": 787, "1:00:00": 3600, "01:02:03.5": 3723.5}
	for in, want := range cases {
		if got, err := in.seconds(); err != nil || got != want {
			t.Errorf("%q: %v, %v; want %v", in, got, err, want)
		}
	}

	if _, err := loose("1:xx").seconds(); err == nil {
		t.Error("1:xx: no error")
	}
}

func TestParseIDs(t *testing.T) {
	t.Parallel()

	if got, err := parseIDs(""); err != nil || got != nil {
		t.Errorf("empty: %v, %v; want every recording", got, err)
	}

	if got, err := parseIDs("4511, 200"); err != nil || !reflect.DeepEqual(got, map[int]bool{4511: true, 200: true}) {
		t.Errorf("4511, 200: %v, %v", got, err)
	}

	if _, err := parseIDs("4511,alice"); err == nil {
		t.Error("4511,alice: no error")
	}
}

func readManifest(t *testing.T, dir string) manifest {
	t.Helper()

	b, err := fs.ReadFile(os.DirFS(dir), manifestName)
	if err != nil {
		t.Fatal(err)
	}

	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}

	return m
}
