package audiosample

import (
	"bytes"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
)

const (
	testItem = "li_1"
	bookType = "book"
)

// fakeFiles is Audiobookshelf's file route over files made at runtime:
// /api/items/li_1/file/<ino>, served with ranges the way the server's own
// static file handler serves them, noting every request.
type fakeFiles struct {
	files map[string][]byte // by ino
	srv   *httptest.Server

	mu   sync.Mutex
	seen []http.Header
}

func newFakeFiles(t *testing.T, files map[string][]byte) *fakeFiles {
	t.Helper()

	f := &fakeFiles{files: files}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.Header.Clone())
		f.mu.Unlock()
		ino := strings.TrimPrefix(r.URL.Path, "/api/items/"+testItem+"/file/")
		body, ok := f.files[ino]
		switch {
		case r.Header.Get("Authorization") != "Bearer secret-key":
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		case ino == "locked":
			http.Error(w, "Forbidden", http.StatusForbidden)
		case !ok:
			http.NotFound(w, r)
		default:
			http.ServeContent(w, r, ino, time.Time{}, bytes.NewReader(body))
		}
	}))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeFiles) sampler(t *testing.T) *Sampler {
	t.Helper()

	client, err := abs.New(f.srv.URL, "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

func (f *fakeFiles) ranges() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, 0, len(f.seen))
	for _, h := range f.seen {
		out = append(out, h.Get("Range"))
	}
	return out
}

func needFFmpeg(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
}

// generate runs ffmpeg over a lavfi source and returns the file it wrote.
func generate(t *testing.T, name, source string, seconds int, codec ...string) []byte {
	t.Helper()

	out := filepath.Join(t.TempDir(), name)
	args := append([]string{"-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", source, "-t", strconv.Itoa(seconds), "-ac", "1"}, codec...)
	if b, err := exec.CommandContext(t.Context(), "ffmpeg", append(args, out)...).CombinedOutput(); err != nil { //nolint:gosec // ffmpeg over this test's own lavfi sources
		t.Fatalf("ffmpeg %s: %v\n%s", name, err, b)
	}
	b, err := os.ReadFile(out) //nolint:gosec // a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func rms(pcm []int16) float64 {
	var sum float64
	for _, v := range pcm {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(max(len(pcm), 1)))
}

// A stretch across the join of two files comes back whole, in play order:
// the end of a loud mp3 and then the start of a quiet m4b, at the rate asked
// for, every request carrying the key the sampler added.
func TestReadCrossesFromFileToFile(t *testing.T) {
	t.Parallel()
	needFFmpeg(t)

	f := newFakeFiles(t, map[string][]byte{
		"101": generate(t, "loud.mp3", "sine=frequency=440:sample_rate=22050", 4, "-b:a", "64k"),
		"102": generate(t, "quiet.m4b", "sine=frequency=440:sample_rate=22050,volume=0.02", 4, "-c:a", "aac", "-b:a", "32k"),
	})
	s := f.sampler(t)
	book := &Book{ItemID: testItem, Tracks: []Track{{FileID: "101", Start: 0, Duration: 4}, {FileID: "102", Start: 4, Duration: 4}}}

	pcm, err := s.Read(t.Context(), book, 3, 2, 8000)
	if err != nil {
		t.Fatal(err)
	}
	// an mp3's fast seek lands a few milliseconds late, so the mp3's part is
	// a little short
	if len(pcm) < 15500 || len(pcm) > 16000 {
		t.Fatalf("2 s at 8 kHz = %d samples, want about 16000", len(pcm))
	}
	loud, quiet := rms(pcm[500:7500]), rms(pcm[8500:15500])
	if loud < 20*quiet {
		t.Errorf("first second RMS %.0f, second %.0f: want the loud file then the quiet one", loud, quiet)
	}
	if s.BytesRead() == 0 {
		t.Error("no bytes counted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.seen {
		if h.Get("Authorization") != "Bearer secret-key" {
			t.Errorf("a request reached the server without the key: %v", h)
		}
	}
}

// A stretch near the end of a long mp3 is one ranged request to near where
// it lies, not the book read from the start: over a network that is the
// difference between a few hundred kilobytes and the whole file.
func TestReadSeeksRatherThanReadingThrough(t *testing.T) {
	t.Parallel()
	needFFmpeg(t)

	const seconds = 1200
	mp3 := generate(t, "long.mp3", "anoisesrc=r=8000:c=pink:a=0.3", seconds, "-b:a", "32k")
	f := newFakeFiles(t, map[string][]byte{"7": mp3})
	s := f.sampler(t)
	book := &Book{ItemID: testItem, Tracks: []Track{{FileID: "7", Duration: seconds}}}

	pcm, err := s.Read(t.Context(), book, 1100, 5, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 20000 {
		t.Errorf("5 s at 4 kHz = %d samples, want 20000", len(pcm))
	}
	var jumped bool
	for _, r := range f.ranges() {
		if at, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r, "bytes="), "-")); err == nil && at > len(mp3)*8/10 {
			jumped = true
		}
	}
	t.Logf("ranges %v, read %d of %d bytes", f.ranges(), s.BytesRead(), len(mp3))
	if !jumped || s.BytesRead() > int64(len(mp3)/2) {
		t.Errorf("ranges %v read %d of %d bytes: want a jump past 80%% and under half the file read", f.ranges(), s.BytesRead(), len(mp3))
	}
}

// A file the server will not hand over says why, in the server's words,
// rather than as ffmpeg's report of an HTTP error.
func TestReadSaysWhyTheServerRefused(t *testing.T) {
	t.Parallel()
	needFFmpeg(t)

	s := newFakeFiles(t, map[string][]byte{}).sampler(t)
	_, err := s.Read(t.Context(), &Book{ItemID: testItem, Tracks: []Track{{FileID: "locked", Duration: 60}}}, 0, 5, 4000)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "lacks permission") {
		t.Errorf("a refused file = %v, want the server's 403 and what it means", err)
	}
}

// The proxy is no open door to the library while it runs: only its own
// random path, only the items it was asked to read, only GET.
func TestTheProxyServesOnlyWhatItWasAskedFor(t *testing.T) {
	t.Parallel()
	needFFmpeg(t)

	f := newFakeFiles(t, map[string][]byte{"9": []byte("not really audio")})
	s := f.sampler(t)
	root := strings.TrimSuffix(s.base, "/"+s.secret)
	get := func(method, url string) int {
		req, _ := http.NewRequestWithContext(t.Context(), method, url, http.NoBody)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if got := get(http.MethodGet, s.base+"/li_1/9"); got != http.StatusNotFound {
		t.Errorf("an item nothing asked for = %d, want 404", got)
	}
	_, _ = s.Read(t.Context(), &Book{ItemID: testItem, Tracks: []Track{{FileID: "9", Duration: 1}}}, 0, 1, 4000)
	if got := get(http.MethodGet, s.base+"/li_1/9"); got != http.StatusOK {
		t.Errorf("the item read = %d, want 200", got)
	}
	if got := get(http.MethodGet, root+"/wrong/li_1/9"); got != http.StatusNotFound {
		t.Errorf("another path = %d, want 404", got)
	}
	if got := get(http.MethodPost, s.base+"/li_1/9"); got != http.StatusMethodNotAllowed {
		t.Errorf("a POST = %d, want 405", got)
	}
	if strings.Contains(s.base, "secret-key") {
		t.Errorf("the url ffmpeg is given, %s, carries the key", s.base)
	}
}

// Without ffmpeg the sampler says so by name, rather than failing somewhere
// inside a decode.
func TestNoFFmpegIsNamed(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	client, err := abs.New("http://abs.invalid", "k")
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(t.Context(), client)
	if !errors.Is(err, ErrNoFFmpeg) || !strings.Contains(err.Error(), "ffmpeg is not installed") {
		t.Errorf("New with no ffmpeg = %v, want ErrNoFFmpeg", err)
	}
}

// The play order is the server's track list, file ids from the track or
// its url; without one, the included audio files in index order.
func TestBookOfReadsThePlayOrder(t *testing.T) {
	t.Parallel()

	tracks := &abs.Item{ID: testItem, MediaType: bookType, Media: abs.Media{Tracks: []abs.AudioTrack{
		{Index: 1, Ino: "11", StartOffset: 0, Duration: 100},
		{Index: 2, ContentURL: "/audiobookshelf/api/items/li_1/file/12", StartOffset: 100, Duration: 50.5},
	}}}
	b, err := BookOf(tracks)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Tracks) != 2 || b.Tracks[0].FileID != "11" || b.Tracks[1].FileID != "12" || b.Tracks[1].Start != 100 || b.Duration() != 150.5 {
		t.Errorf("from tracks: %+v, duration %g", b.Tracks, b.Duration())
	}

	files := &abs.Item{ID: "li_2", MediaType: bookType, Media: abs.Media{AudioFiles: []abs.AudioFile{
		{Index: 3, Ino: "33", Duration: 30},
		{Index: 1, Ino: "31", Duration: 10},
		{Index: 2, Ino: "32", Duration: 20, Exclude: true},
	}}}
	b, err = BookOf(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Tracks) != 2 || b.Tracks[0].FileID != "31" || b.Tracks[1].FileID != "33" || b.Tracks[1].Start != 10 || b.Duration() != 40 {
		t.Errorf("from audio files: %+v, want 31 then 33 at 10 s, the excluded one left out", b.Tracks)
	}

	if _, err := BookOf(&abs.Item{MediaType: "podcast", Media: abs.Media{Metadata: abs.Metadata{Title: "Pod"}}}); err == nil || !strings.Contains(err.Error(), "podcast") {
		t.Errorf("a podcast = %v, want refused", err)
	}
	if _, err := BookOf(&abs.Item{MediaType: bookType, Media: abs.Media{Metadata: abs.Metadata{Title: "Ebook"}}}); err == nil || !strings.Contains(err.Error(), "no audio") {
		t.Errorf("an ebook = %v, want refused", err)
	}
}
