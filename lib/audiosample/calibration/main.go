// Command calibration fetches the recordings item_compare_audio is calibrated on into a cache outside the repository, with the manifest the
// calibration test reads.
//
// They are LibriVox readings of one text by different readers, the hardest case the by-ear check meets: the same words in another voice. All
// of it is in the public domain and none of it is copied into the repository. docs/CALIBRATION.md has the detail.
//
//	go run ./lib/audiosample/calibration                     # about 925 MB into ~/.cache/abs-mcp/calibration; run again if archive.org drops a file
//	go run ./lib/audiosample/calibration -dir /some/dir
//	go run ./lib/audiosample/calibration -list               # say what would be fetched, fetch nothing
//	go run ./lib/audiosample/calibration -manifest-only      # write the manifest, fetch nothing
//	go run ./lib/audiosample/calibration -only 4511,200      # fetch some of the recordings
//	ABS_CALIBRATION_DIR=~/.cache/abs-mcp/calibration go test -tags calibration -run Calibration -v -timeout 2h ./tools/
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// agent names this program to LibriVox and archive.org.
const agent = "abs-mcp-calibration (https://github.com/katbyte/abs-mcp)"

// manifestName is the manifest's name in the cache, where the calibration test reads it.
const manifestName = "manifest.json"

// recordings are the LibriVox books fetched. Alice's Adventures in Wonderland has more complete solo readings than any other text, and its
// readers recorded other books too.
var recordings = []recording{
	// one text, six readers, unabridged: every pair is two readings
	{ID: 900, Work: "alice", Kind: "solo", Reader: "Peter Yearsley"},
	{ID: 4139, Work: "alice", Kind: "solo", Reader: "Kara Shallenberg"},
	{ID: 4240, Work: "alice", Kind: "solo", Reader: "Eric Leach"},
	{ID: 13477, Work: "alice", Kind: "solo", Reader: "StudioMike"},
	{ID: 15200, Work: "alice", Kind: "solo", Reader: "Craig Franklin"},
	{ID: 16702, Work: "alice", Kind: "solo", Reader: "Vin Cramer"},

	// the same text cut: abridged readings, which the check should call neither the same as a full reading nor the same as each other
	{ID: 1122, Work: "alice", Kind: "abridged", Reader: "Kirsten Ferreri"},
	{ID: 6898, Work: "alice", Kind: "abridged", Reader: "ashleighjane"},

	// a full cast
	{ID: 4511, Work: "alice", Kind: "cast", Reader: "full cast"},

	// the group reading of 2006: several readers, Kara Shallenberg among them reading chapters she recorded again alone in 2010, so the same
	// voice and the same words in another recording
	{ID: 200, Work: "alice", Kind: "group", Reader: "various"},

	// the same reader on another book: her voice without her Alice
	{ID: 10043, Work: "treasure-island", Kind: "solo", Reader: "Kara Shallenberg"},
}

// recording is one LibriVox book to fetch: its id, the text, what kind of reading it is, and who reads it.
type recording struct {
	ID     int    `json:"id"`
	Work   string `json:"work"`
	Kind   string `json:"kind"`
	Reader string `json:"reader"`
}

// manifest is what the calibration test reads: every recording, and where its files are under the cache.
type manifest struct {
	Recordings []entry `json:"recordings"`
}

// entry is one recording in the manifest, with what LibriVox and archive.org say of it.
type entry struct {
	recording

	Title    string    `json:"title"`
	Archive  string    `json:"archive"`
	Folder   string    `json:"folder"`
	Seconds  int       `json:"seconds"`
	Sections []section `json:"sections"`
	Files    []file    `json:"files"`
}

// section is one chapter as LibriVox lists it, with who read it: a group reading names a reader per chapter.
type section struct {
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	Readers []string `json:"readers"`
	Seconds int      `json:"seconds"`
}

// file is one mp3 of a recording on archive.org.
type file struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
	Bytes   int64   `json:"bytes"`
}

// librivoxBook is the part of LibriVox's record of a book used here.
type librivoxBook struct {
	Title    string `json:"title"`
	Archive  string `json:"url_iarchive"`
	Seconds  loose  `json:"totaltimesecs"`
	Sections []struct {
		Number  loose `json:"section_number"`
		Title   loose `json:"title"`
		Seconds loose `json:"playtime"`
		Readers []struct {
			Name string `json:"display_name"`
		} `json:"readers"`
	} `json:"sections"`
}

// archiveFile is the part of archive.org's record of a file used here.
type archiveFile struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Size   loose  `json:"size"`
	Length loose  `json:"length"`
}

// loose is a value written as text one time and as a number another, kept as text.
type loose string

func (l *loose) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*l = ""
		return nil
	}

	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*l = loose(s)
		return nil
	}

	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*l = loose(n)

	return nil
}

// number is the value as a whole number, or 0 when it is empty.
func (l loose) number() (int, error) {
	if l == "" {
		return 0, nil
	}

	f, err := strconv.ParseFloat(string(l), 64)

	return int(f), err
}

// seconds is a length archive.org writes as seconds, or as mm:ss or h:mm:ss.
func (l loose) seconds() (float64, error) {
	var total float64
	if l == "" {
		return total, nil
	}

	for part := range strings.SplitSeq(string(l), ":") {
		f, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, fmt.Errorf("length %q: %w", l, err)
		}
		total = total*60 + f
	}

	return total, nil
}

// fetcher talks to LibriVox and archive.org. The addresses and the unit of every wait are fields, so a test can stand in for both.
type fetcher struct {
	librivox string        // a book's record, with %d for its id
	metadata string        // an archive.org item's record, with the item appended
	download string        // an archive.org item's files, with the item and the file appended
	unit     time.Duration // a second, or less in a test
	log      io.Writer     // retries, and files that could not be fetched
}

// options are what one run is asked to do.
type options struct {
	recordings   []recording  // the books to describe and fetch
	dir          string       // where the recordings and the manifest go
	list         bool         // say what would be fetched, fetch nothing
	manifestOnly bool         // write the manifest, fetch nothing
	only         map[int]bool // the recordings to fetch, or every one when nil
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "calibration:", err)
		os.Exit(1)
	}

	dir := flag.String("dir", filepath.Join(home, ".cache", "abs-mcp", "calibration"), "where the recordings and the manifest go")
	list := flag.Bool("list", false, "say what would be fetched, fetch nothing")
	manifestOnly := flag.Bool("manifest-only", false, "write the manifest, fetch nothing")
	only := flag.String("only", "", "LibriVox ids to fetch, comma-separated; the manifest still lists them all")
	flag.Parse()

	ids, err := parseIDs(*only)
	if err != nil {
		fmt.Fprintln(os.Stderr, "calibration:", err)
		os.Exit(1)
	}

	f := fetcher{
		librivox: "https://librivox.org/api/feed/audiobooks/?id=%d&format=json&extended=1",
		metadata: "https://archive.org/metadata",
		download: "https://archive.org/download",
		unit:     time.Second,
		log:      os.Stderr,
	}
	missing, err := f.run(context.Background(), os.Stdout, options{recordings: recordings, dir: *dir, list: *list, manifestOnly: *manifestOnly, only: ids})
	if err != nil {
		fmt.Fprintln(os.Stderr, "calibration:", err)
		os.Exit(1)
	}

	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "%d files could not be fetched; run again for them:\n", len(missing))
		for _, m := range missing {
			fmt.Fprintln(os.Stderr, " ", m)
		}
		os.Exit(1)
	}
}

// parseIDs reads -only: LibriVox ids, comma-separated, or none for every recording.
func parseIDs(s string) (map[int]bool, error) {
	if s == "" {
		return nil, nil
	}

	ids := map[int]bool{}
	for part := range strings.SplitSeq(s, ",") {
		id, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("-only %q: %w", s, err)
		}
		ids[id] = true
	}

	return ids, nil
}

// run writes the manifest and fetches what is not there yet, and returns the files that could not be fetched.
func (f fetcher) run(ctx context.Context, w io.Writer, o options) ([]string, error) {
	known := readKnown(o.dir)

	type job struct {
		item   string
		folder string
		files  []file
	}

	var m manifest
	var total int64
	var jobs []job
	for _, rec := range o.recordings {
		e, ok := known[rec.ID]
		if !ok || e.Kind != rec.Kind || e.Work != rec.Work {
			var err error
			if e, err = f.describe(ctx, rec); err != nil {
				return nil, err
			}
		}
		m.Recordings = append(m.Recordings, e)

		var size int64
		for _, fl := range e.Files {
			size += fl.Bytes
		}
		total += size

		_, _ = fmt.Fprintf(w, "%s (%s): %d files, %.0f MB, %s\n", e.Title, rec.Reader, len(e.Files), float64(size)/1e6, e.Archive)
		if o.only == nil || o.only[rec.ID] {
			jobs = append(jobs, job{item: e.Archive, folder: e.Folder, files: e.Files})
		}
	}

	_, _ = fmt.Fprintf(w, "%d recordings, %.0f MB\n", len(m.Recordings), float64(total)/1e6)
	if o.list {
		return nil, nil
	}

	// everything is written through root, so no name LibriVox or archive.org gives can put a file outside the cache
	if err := os.MkdirAll(o.dir, 0o750); err != nil {
		return nil, err
	}

	root, err := os.OpenRoot(o.dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	// the manifest first, so a partial fetch can be read as far as it got
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return nil, err
	}

	if err := root.WriteFile(manifestName, b, 0o600); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintln(w, "manifest:", filepath.Join(o.dir, manifestName))

	if o.manifestOnly {
		return nil, nil
	}

	var missing []string
	for _, j := range jobs {
		if err := root.MkdirAll(j.folder, 0o750); err != nil {
			return nil, err
		}

		// one file failing does not stop the rest
		for _, fl := range j.files {
			fetched, err := f.fetchFile(ctx, root, j.item, fl.Name, filepath.Join(j.folder, fl.Name), fl.Bytes)
			if err != nil {
				_, _ = fmt.Fprintf(f.log, "  %s/%s: %v\n", j.item, fl.Name, err)
				missing = append(missing, j.item+"/"+fl.Name)
				continue
			}

			if fetched {
				_, _ = fmt.Fprintln(w, "  fetched", fl.Name)
			}
		}
	}

	return missing, nil
}

// readKnown is what an earlier run wrote to the manifest, by id. A rerun for the files one of the services dropped then needs neither to answer.
// A manifest that is not there or cannot be read is nothing known.
func readKnown(dir string) map[int]entry {
	known := map[int]entry{}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return known
	}
	defer func() { _ = root.Close() }()

	b, err := root.ReadFile(manifestName)
	if err != nil {
		return known
	}

	var m manifest
	if json.Unmarshal(b, &m) != nil {
		return known
	}

	for _, e := range m.Recordings {
		known[e.ID] = e
	}

	return known
}

// describe is one recording's manifest entry: what LibriVox and archive.org say of it.
func (f fetcher) describe(ctx context.Context, rec recording) (entry, error) {
	var lv struct {
		Books []librivoxBook `json:"books"`
	}
	if err := f.fetchJSON(ctx, fmt.Sprintf(f.librivox, rec.ID), &lv); err != nil {
		return entry{}, err
	}
	if len(lv.Books) == 0 {
		return entry{}, fmt.Errorf("LibriVox has no book %d", rec.ID)
	}
	book := lv.Books[0]

	// https://www.archive.org/details/<item> or https://archive.org/details/<item>
	item := strings.TrimRight(book.Archive, "/")
	item = item[strings.LastIndex(item, "/")+1:]

	addr, err := url.JoinPath(f.metadata, item)
	if err != nil {
		return entry{}, err
	}

	var meta struct {
		Files []archiveFile `json:"files"`
	}
	if err := f.fetchJSON(ctx, addr, &meta); err != nil {
		return entry{}, err
	}

	e := entry{recording: rec, Title: book.Title, Archive: item, Folder: filepath.Join(rec.Work, fmt.Sprintf("%d-%s", rec.ID, rec.Kind))}

	if e.Seconds, err = book.Seconds.number(); err != nil {
		return entry{}, fmt.Errorf("LibriVox book %d's length: %w", rec.ID, err)
	}

	for _, s := range book.Sections {
		n := len(e.Sections) + 1
		if s.Number != "" {
			if n, err = s.Number.number(); err != nil {
				return entry{}, fmt.Errorf("LibriVox book %d's section number: %w", rec.ID, err)
			}
		}

		secs, err := s.Seconds.number()
		if err != nil {
			return entry{}, fmt.Errorf("LibriVox book %d's section %d length: %w", rec.ID, n, err)
		}

		readers := make([]string, 0, len(s.Readers))
		for _, r := range s.Readers {
			readers = append(readers, r.Name)
		}

		e.Sections = append(e.Sections, section{Number: n, Title: string(s.Title), Readers: readers, Seconds: secs})
	}

	for _, af := range meta.Files {
		if af.Format != "64Kbps MP3" {
			continue
		}

		if !filepath.IsLocal(af.Name) {
			return entry{}, fmt.Errorf("%s names a file outside its folder: %q", item, af.Name)
		}

		size, err := strconv.ParseInt(string(af.Size), 10, 64)
		if err != nil {
			return entry{}, fmt.Errorf("%s/%s size: %w", item, af.Name, err)
		}

		secs, err := af.Length.seconds()
		if err != nil {
			return entry{}, fmt.Errorf("%s/%s: %w", item, af.Name, err)
		}

		e.Files = append(e.Files, file{Name: af.Name, Seconds: secs, Bytes: size})
	}

	if len(e.Files) == 0 {
		return entry{}, fmt.Errorf("%s has no 64 kbps mp3s", item)
	}
	slices.SortFunc(e.Files, func(a, b file) int { return strings.Compare(a.Name, b.Name) })

	return e, nil
}

// fetchJSON reads a record into v, trying four times.
func (f fetcher) fetchJSON(ctx context.Context, addr string, v any) error {
	const attempts = 4

	var err error
	for attempt := range attempts {
		if err = readJSON(ctx, addr, v); err == nil {
			return nil
		}

		if attempt < attempts-1 {
			_, _ = fmt.Fprintf(f.log, "  %s: %v, retrying\n", addr, err)
			if err := f.wait(ctx, 5*(attempt+1)); err != nil {
				return err
			}
		}
	}

	return err
}

// readJSON reads one answer into v.
func readJSON(ctx context.Context, addr string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	resp, err := get(ctx, addr)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return json.NewDecoder(resp.Body).Decode(v)
}

// fetchFile fetches one file of an item to dest under root, unless it is there already at the size archive.org gives, and says whether it
// fetched it. archive.org answers 500 now and then and recovers in a minute, so a file is tried for a while before it is given up on.
func (f fetcher) fetchFile(ctx context.Context, root *os.Root, item, name, dest string, size int64) (bool, error) {
	if st, err := root.Stat(dest); err == nil && st.Size() == size {
		return false, nil
	}

	addr, err := url.JoinPath(f.download, item, name)
	if err != nil {
		return false, err
	}

	const attempts = 8
	for attempt := range attempts {
		if err = save(ctx, root, addr, dest); err == nil {
			return true, nil
		}

		if attempt < attempts-1 {
			_, _ = fmt.Fprintf(f.log, "  %s: %v, retrying\n", addr, err)
			if err := f.wait(ctx, min(10<<attempt, 120)); err != nil {
				return false, err
			}
		}
	}

	return false, err
}

// save writes what addr answers to dest under root, by way of dest.part, so an interrupted fetch never leaves a file that looks whole.
func save(ctx context.Context, root *os.Root, addr, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	resp, err := get(ctx, addr)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	tmp := dest + ".part"
	out, err := root.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return err
	}

	if err := out.Close(); err != nil {
		return err
	}

	return root.Rename(tmp, dest)
}

// get sends a request and refuses an answer that is not 200 OK. The caller closes the body.
func get(ctx context.Context, addr string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", agent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s answered %s", addr, resp.Status)
	}

	return resp, nil
}

// wait pauses for a number of units, or until ctx is done.
func (f fetcher) wait(ctx context.Context, units int) error {
	t := time.NewTimer(time.Duration(units) * f.unit)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
