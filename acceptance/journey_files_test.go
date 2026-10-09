//go:build integration

// Journeys over the files themselves: books deleted from disk, folders
// renamed and merged under a scan, a share that goes away and comes back,
// edits that a forced scan must not undo, and a folder with no audio in it.
// Each builds what it needs on disk, under a scratch library of its own where
// it can, and reads both the server and the disk back after every step,
// because a delete that answers 200 has not been shown to delete the right
// thing until the folder is looked at.
package acceptance

import (
	"archive/zip"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/katbyte/abs-mcp/tools"
)

// diskShelf is a library a journey owns, over a folder of its own under
// ABS_TEST_DATA/scratch, so books can be laid out, moved and deleted on disk
// without disturbing the seeded libraries. The library and the folder go
// when the test ends.
type diskShelf struct {
	name, id string
	root     string // the folder on this machine
	folder   string // the same folder as the server sees it
}

// newDiskShelf makes the shelf's folder, empty; write lays books out in it
// and open creates the library over it.
func newDiskShelf(t *testing.T, name, dir string) *diskShelf {
	t.Helper()

	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	admin := adminClient(t)
	s := &diskShelf{name: name, root: filepath.Join(data, "scratch", dir), folder: "/scratch/" + dir}
	if err := os.RemoveAll(s.root); err != nil { // left by a run that died
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.root, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s.id != "" {
			eventually(t, "deleting the library "+name, func() error {
				if err := admin.DeleteLibrary(ctx, s.id); err != nil && !isNotFound(err) {
					return err
				}
				return nil
			})
		}
		if err := os.RemoveAll(s.root); err != nil {
			t.Errorf("removing %s: %v", s.root, err)
		}
	})

	return s
}

// write lays files out under the shelf, each made by its extension: a
// second of silence for audio, a 200-pixel cover for a jpg (too small for
// audit_covers), a minimal epub titled after its file.
func (s *diskShelf) write(t *testing.T, rels ...string) {
	t.Helper()

	for _, rel := range rels {
		p := filepath.Join(s.root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			t.Fatal(err)
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".mp3", ".m4b":
			diskSilence(t, p, 1)
		case ".jpg":
			writeJPEG(t, p, 200)
		case ".epub":
			diskEpub(t, p)
		default:
			t.Fatalf("no fixture for %s", rel)
		}
	}
}

// open creates the library over the shelf's folder, scans it and waits for
// want books.
func (s *diskShelf) open(t *testing.T, want int) {
	t.Helper()

	lib := object(call(t, "library_create", map[string]any{"name": s.name, "folders": []any{s.folder}})["library"])
	if s.id = text(lib["id"]); s.id == "" {
		t.Fatalf("library_create gave no id: %v", lib)
	}
	s.scan(t, false)
	if got := len(s.items(t)); got != want {
		t.Fatalf("the first scan of %s found %d books, want %d", s.name, got, want)
	}
}

// scan runs library_scan and waits for the server to finish it: the scan
// task is registered before the call returns, so idle means done.
func (s *diskShelf) scan(t *testing.T, force bool) {
	t.Helper()

	args := map[string]any{"library": s.name}
	if force {
		args["force"] = true
	}
	call(t, "library_scan", args)
	waitIdle(t)
}

// items is every record in the library by its path in the library folder.
func (s *diskShelf) items(t *testing.T) map[string]map[string]any {
	t.Helper()

	out := map[string]map[string]any{}
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": s.name, "limit": 100})["items"], "items") {
		out[text(it["path"])] = it
	}
	return out
}

// ids is every record's id by its path.
func (s *diskShelf) ids(t *testing.T) map[string]string {
	t.Helper()

	out := map[string]string{}
	for p, it := range s.items(t) {
		out[p] = text(it["id"])
	}
	return out
}

// onDisk is every file and folder under the shelf, folders ending in /,
// sorted: what the server actually left, rather than what it said it did.
func (s *diskShelf) onDisk(t *testing.T) []string {
	t.Helper()

	return diskTree(t, s.root)
}

// diskTree lists everything under root, relative to it, folders ending in /.
func diskTree(t *testing.T, root string) []string {
	t.Helper()

	var out []string
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// diskSilence writes seconds of silence, mp3 or aac by the extension. The aac
// is mono at 8kHz, so two and a half hours of it is half a megabyte and takes
// ffmpeg about two seconds.
func diskSilence(t *testing.T, path string, seconds int) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	// -nostdin: ffmpeg otherwise reads the test's stdin for keystrokes
	args := []string{"-nostdin", "-loglevel", "error", "-y", "-f", "lavfi"}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		args = append(args, "-i", "anullsrc=r=44100:cl=mono", "-t", strconv.Itoa(seconds), "-q:a", "9", path)
	default:
		args = append(args, "-i", "anullsrc=r=8000:cl=mono", "-t", strconv.Itoa(seconds), "-c:a", "aac", "-b:a", "8k", path)
	}
	if out, err := exec.CommandContext(t.Context(), "ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", path, err, out)
	}
}

// diskEpub writes a minimal epub titled after its file, built the way
// scripts/abs-testenv.sh builds the fixture's: a zip whose first entry is an
// uncompressed mimetype.
func diskEpub(t *testing.T, path string) {
	t.Helper()

	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	entries := []struct {
		name, body string
		method     uint16
	}{
		{"mimetype", "application/epub+zip", zip.Store},
		{"META-INF/container.xml", `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`, zip.Deflate},
		{"content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">zzyzx</dc:identifier><dc:title>` + title + `</dc:title><dc:language>en</dc:language></metadata><manifest/><spine/></package>`, zip.Deflate},
	}
	for _, e := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: e.method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// diskUntil polls cond until it holds, failing with the last thing it said
// when it never does: a scan's effects land on the server's time, not the
// test's.
func diskUntil(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()

	var last string
	for range 120 {
		var ok bool
		if ok, last = cond(); ok {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s never happened: %s", what, last)
}

// diskSettle waits out the server's file watcher. Where file events reach
// the container (Docker on Linux, as in CI), every file a journey moves
// queues a scan of its folder that runs ten seconds after the last change,
// long after the journey's own scan: left alone it would rescan Messy under
// whichever test comes next. The watcher holds events up to two seconds to
// pair them into renames, so this looks for three before deciding none is
// coming; where no events arrive (Docker Desktop on a Mac) that is all it
// costs.
func diskSettle(t *testing.T) {
	t.Helper()

	for range 12 {
		for _, task := range rows(t, call(t, "server_tasks", nil)["tasks"], "tasks") {
			if task["status"] == "running" {
				waitIdle(t)
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// diskFindingIDs lists the id of every row of an audit's findings.
func diskFindingIDs(t *testing.T, out map[string]any) []string {
	t.Helper()

	ids := valuesIn(t, out["findings"], "findings", "id")
	slices.Sort(ids)
	return ids
}

// --- a book deleted with its files --------------------------------------------

// A session clearing books off a shelf for good: each delete is previewed,
// then a book's folder goes with delete_files, then a book that is one m4b
// at the library root. The disk is listed after every step. It would catch a
// preview that deletes, a delete that takes the author folder or the book
// beside it, a single-file book whose delete takes the folder it sits in
// (which is the library itself), and a record a later scan brings back.
func TestJourneyABookDeletedWithItsFiles(t *testing.T) {
	const author = "Zzyzx Delete Author"
	one, two := author+"/Zzyzx Book One", author+"/Zzyzx Book Two"
	const rootOne, rootTwo = "Zzyzx Root One.m4b", "Zzyzx Root Two.m4b"

	s := newDiskShelf(t, "Zzyzx Delete Shelf", "zzyzx-delete")
	s.write(t, one+"/01.mp3", one+"/cover.jpg", two+"/01.mp3", rootOne, rootTwo)
	s.open(t, 4)
	ids := s.ids(t)
	start := s.onDisk(t)
	gone := func(names ...string) []string {
		return slices.DeleteFunc(slices.Clone(start), func(p string) bool {
			return slices.ContainsFunc(names, func(n string) bool { return p == n || strings.HasPrefix(p, n+"/") })
		})
	}
	call(t, "user_bookmark_edit", map[string]any{"item": ids[one], "add_bookmarks": []any{map[string]any{"time_s": 0.5, "title": "Zzyzx Delete Mark"}}})

	t.Run("previewed, nothing goes", func(t *testing.T) {
		out := call(t, "item_delete", map[string]any{"item": ids[one], "delete_files": true})
		if out["would_delete"] != "Zzyzx Book One" || out["deleted"] != nil || !isFalse(out["files_removed"]) {
			t.Errorf("preview = %v, want Book One named and nothing deleted", out)
		}
		if want := "the folder " + s.folder + "/" + one + " and everything in it"; out["files"] != want {
			t.Errorf("files = %q, want %q", out["files"], want)
		}
		if marks := strs(t, out["bookmarks"], "bookmarks"); len(marks) != 1 || !strings.Contains(marks[0], `"Zzyzx Delete Mark"`) {
			t.Errorf("bookmarks = %v, want the one on it", marks)
		}
		// without delete_files the record is all that would go
		if files := call(t, "item_delete", map[string]any{"item": ids[one]})["files"]; files != nil {
			t.Errorf("a record-only preview names files: %v", files)
		}
		if got := s.onDisk(t); !slices.Equal(got, start) {
			t.Errorf("the previews changed the disk:\n got  %v\n want %v", got, start)
		}
		if n := len(s.items(t)); n != 4 {
			t.Errorf("the library holds %d books after the previews, want 4", n)
		}
		if marks := rows(t, call(t, "user_bookmarks", map[string]any{"item": ids[one]})["bookmarks"], "bookmarks"); len(marks) != 1 {
			t.Errorf("the previews took the bookmark: %v", marks)
		}
	})

	t.Run("a book's folder", func(t *testing.T) {
		out := call(t, "item_delete", map[string]any{"item": ids[one], "delete_files": true, "confirm": true})
		if out["deleted"] != "Zzyzx Book One" || !truth(out["files_removed"]) || num(t, out["bookmarks_removed"], "bookmarks_removed") != 1 {
			t.Errorf("delete = %v", out)
		}
		// the folder, cover and all; the author folder and the book beside it stay
		if got, want := s.onDisk(t), gone(one); !slices.Equal(got, want) {
			t.Errorf("on disk after the delete:\n got  %v\n want %v", got, want)
		}
		if msg := callErr(t, "item_get", map[string]any{"item": ids[one]}); msg == "" {
			t.Error("the deleted book still resolves")
		}
	})

	t.Run("a book that is one file at the library root", func(t *testing.T) {
		out := call(t, "item_delete", map[string]any{"item": ids[rootOne], "delete_files": true})
		if want := "the file " + s.folder + "/" + rootOne; out["files"] != want || out["would_delete"] != "Zzyzx Root One" {
			t.Errorf("preview = %v, want the one file, %q", out, want)
		}
		if got, want := s.onDisk(t), gone(one); !slices.Equal(got, want) {
			t.Errorf("the preview changed the disk:\n got  %v\n want %v", got, want)
		}
		call(t, "item_delete", map[string]any{"item": ids[rootOne], "delete_files": true, "confirm": true})
		if got, want := s.onDisk(t), gone(one, rootOne); !slices.Equal(got, want) {
			t.Errorf("on disk after the delete:\n got  %v\n want %v", got, want)
		}
	})

	t.Run("a scan brings nothing back", func(t *testing.T) {
		s.scan(t, false)
		left := slices.Sorted(func(yield func(string) bool) {
			for p := range s.items(t) {
				if !yield(p) {
					return
				}
			}
		})
		if !slices.Equal(left, []string{two, rootTwo}) {
			t.Errorf("after a scan the library holds %v, want Book Two and Root Two", left)
		}
		if n := num(t, call(t, "audit_issues", map[string]any{"library": s.name})["total_findings"], "total_findings"); n != 0 {
			t.Errorf("audit_issues finds %d, want none: nothing was left pointing at a deleted file", n)
		}
	})
}

// --- a folder renamed, and one book held twice put back together -------------

// A session acting on audit_path and audit_duplicates by moving files rather
// than editing records. First a Messy book whose folder names another book
// has its folder renamed to its title and the library scanned: the server
// follows a moved folder by its inode, so the record has to come through
// with the listening progress, the bookmark, the collection, the playlist
// entry and the series it had. Then the copy of Mort filed outside the series
// has its file moved in beside the other and its empty folder removed: the
// scan leaves its record behind as missing, audit_issues names it, and
// library_issues_remove clears it, previewed first. It would catch a rename
// that orphans a record (and everything hung on it) or makes a second one,
// and an issues removal that takes more than the orphan.
func TestJourneyAFolderRenamedKeepsItsBook(t *testing.T) {
	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	messy := filepath.Join(data, "messy")
	// last of all, once both subtests have put their files back
	t.Cleanup(func() { diskSettle(t) })

	t.Run("renamed to its title", func(t *testing.T) {
		const from, to = "Terry Pratchett/Discworld - 07 - Pyramids", "Terry Pratchett/Discworld - 07 - Small Gods"
		id := messyID(t, from)
		rename := func(t *testing.T, a, b string) {
			t.Helper()
			if err := os.Rename(filepath.Join(messy, a), filepath.Join(messy, b)); err != nil {
				t.Fatal(err)
			}
			call(t, "library_scan", map[string]any{"library": "Messy"})
			waitIdle(t)
			diskUntil(t, "the record following its folder to "+b, func() (bool, string) {
				got := call(t, "item_get", map[string]any{"item": id})
				return got["path"] == b && got["missing"] == nil, fmt.Sprintf("path %v, missing %v", got["path"], got["missing"])
			})
		}
		t.Cleanup(func() {
			if _, err := os.Stat(filepath.Join(messy, to)); err == nil {
				rename(t, to, from)
			}
		})

		// everything a listener and a curator hang on a book
		call(t, "user_progress_set", map[string]any{"item": id, "percent": 50})
		t.Cleanup(func() { call(t, "user_progress_set", map[string]any{"remove": true, "item": id}) })
		call(t, "user_bookmark_edit", map[string]any{"item": id, "add_bookmarks": []any{map[string]any{"time_s": 0.5, "title": "Zzyzx Moved Mark"}}})
		t.Cleanup(func() {
			call(t, "user_bookmark_edit", map[string]any{"item": id, "remove_bookmarks": []any{0.5}})
		})
		call(t, "collection_create", map[string]any{"library": "Messy", "name": "Zzyzx Moved Shelf", "items": []any{id}})
		t.Cleanup(func() { call(t, "collection_delete", map[string]any{"collection": "Zzyzx Moved Shelf"}) })
		call(t, "playlist_create", map[string]any{"library": "Messy", "name": "Zzyzx Moved Queue", "entries": []any{map[string]any{"item": id}}})
		t.Cleanup(func() { call(t, "playlist_delete", map[string]any{"playlist": "Zzyzx Moved Queue"}) })
		call(t, "item_edit", map[string]any{"item": id, "add_series": []any{"Zzyzx Moved Saga #1"}})
		t.Cleanup(func() {
			call(t, "item_edit", map[string]any{"item": id, "remove_series": []any{"Zzyzx Moved Saga"}})
		})

		if !slices.Contains(diskFindingIDs(t, call(t, "audit_path", withMessy(nil))), id) {
			t.Fatal("audit_path does not report Small Gods in the Pyramids folder, so there is nothing to rename")
		}

		rename(t, from, to)

		book := call(t, "item_get", map[string]any{"item": id})
		if book["title"] != "Small Gods" {
			t.Errorf("title = %v, want Small Gods kept through the move", book["title"])
		}
		if series := strs(t, book["series"], "series"); !slices.Contains(series, "Discworld #07") || !slices.Contains(series, "Zzyzx Moved Saga #1") {
			t.Errorf("series = %v, want both kept", series)
		}
		if n := num(t, call(t, "library_items", withMessy(map[string]any{"limit": 1}))["total"], "total"); n != len(messyBooks) {
			t.Errorf("Messy holds %d books after the rename, want %d: the move made a second record", n, len(messyBooks))
		}
		progress := object(call(t, "user_progress_get", map[string]any{"item": id})["progress"])
		if progress == nil || num(t, progress["percent"], "percent") != 50 {
			t.Errorf("progress = %v, want the 50%% set before the move", progress)
		}
		if marks := titlesIn(t, call(t, "user_bookmarks", map[string]any{"item": id})["bookmarks"], "bookmarks"); !slices.Equal(marks, []string{"Zzyzx Moved Mark"}) {
			t.Errorf("bookmarks = %v", marks)
		}
		if held := valuesIn(t, call(t, "collection_get", map[string]any{"collection": "Zzyzx Moved Shelf"})["items"], "items", "id"); !slices.Equal(held, []string{id}) {
			t.Errorf("the collection holds %v, want the moved book", held)
		}
		entries := rows(t, call(t, "playlist_get", map[string]any{"playlist": "Zzyzx Moved Queue"})["entries"], "entries")
		queued := make([]string, 0, len(entries))
		for _, e := range entries {
			queued = append(queued, text(object(e["item"])["id"]))
		}
		if !slices.Equal(queued, []string{id}) {
			t.Errorf("the playlist holds %v, want the moved book", queued)
		}
		if books := valuesIn(t, call(t, "series_get", map[string]any{"library": "Messy", "series": "Zzyzx Moved Saga"})["books"], "books", "id"); !slices.Equal(books, []string{id}) {
			t.Errorf("the series holds %v, want the moved book", books)
		}
		if slices.Contains(diskFindingIDs(t, call(t, "audit_path", withMessy(nil))), id) {
			t.Error("audit_path still reports the book once its folder is named after it")
		}
		if n := num(t, call(t, "audit_issues", withMessy(nil))["total_findings"], "total_findings"); n != 0 {
			t.Errorf("audit_issues finds %d after the move, want none", n)
		}

		// and back, under the same record again
		rename(t, to, from)
	})

	t.Run("one book held twice, put together", func(t *testing.T) {
		const outsidePath, insidePath = "Terry Pratchett/Mort", "Terry Pratchett/Discworld - 04 - Mort"
		outside, inside := messyID(t, outsidePath), messyID(t, insidePath)
		src := filepath.Join(messy, outsidePath, "01.mp3")
		dst := filepath.Join(messy, insidePath, "02.mp3")
		tracks := func(id string) int {
			return num(t, call(t, "item_get", map[string]any{"item": id})["audio_tracks"], "audio_tracks")
		}
		grouped := func(t *testing.T) bool {
			t.Helper()
			for _, g := range rows(t, call(t, "audit_duplicates", withMessy(nil))["groups"], "groups") {
				if slices.Contains(valuesIn(t, g["items"], "items", "id"), inside) {
					return true
				}
			}
			return false
		}
		// the file goes back where it was, and the copy outside the series is
		// scanned back and seeded as harness_test.go seeds it
		putBack := func(t *testing.T) {
			t.Helper()
			if err := os.MkdirAll(filepath.Dir(src), 0o777); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(dst, src); err != nil {
				t.Fatal(err)
			}
			call(t, "library_scan", map[string]any{"library": "Messy"})
			waitIdle(t)
			diskUntil(t, "Mort scanned back outside the series", func() (bool, string) {
				n := num(t, call(t, "library_items", withMessy(map[string]any{"limit": 1}))["total"], "total")
				return n == len(messyBooks) && tracks(inside) == 1, fmt.Sprintf("%d books, the series copy has %d tracks", n, tracks(inside))
			})
			// the server built the series copy's chapters from its two files,
			// one each, and does not rebuild them when one goes: it is left
			// with a chapter starting past the end of its audio, which
			// audit_chapters finds and item_chapters_set fit repairs. The
			// fixture had none at all, which no tool sets, so they are put
			// back through the client here
			if chapters := call(t, "item_get", map[string]any{"item": inside})["chapters"]; chapters != nil {
				t.Logf("with its second file gone, the series copy of Mort still has %v chapters", chapters)
				if _, err := adminClient(t).SetChapters(ctx, inside, []abs.Chapter{}); err != nil {
					t.Fatal(err)
				}
			}
			id := messyID(t, outsidePath)
			if id == outside {
				return // never removed: the record kept its metadata
			}
			for _, b := range messyBooks {
				if b.Path == outsidePath {
					call(t, "item_edit", map[string]any{
						"item": id, "title": b.Title, "authors": []any{b.Author}, "narrators": toAny(b.Narrators),
						"genres": toAny(b.Genres), "description": b.Description, "language": "English", "clear": []any{"series"},
					})
				}
			}
		}
		t.Cleanup(func() {
			if _, err := os.Stat(dst); err == nil {
				putBack(t)
			}
		})

		if !grouped(t) {
			t.Fatal("audit_duplicates does not group the two Morts, so there is nothing to put together")
		}

		// the file moves in beside the other copy, and its empty folder goes
		if err := os.Rename(src, dst); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Dir(src)); err != nil {
			t.Fatal(err)
		}
		call(t, "library_scan", map[string]any{"library": "Messy"})
		waitIdle(t)
		diskUntil(t, "the scan seeing the move", func() (bool, string) {
			missing := call(t, "item_get", map[string]any{"item": outside})["missing"]
			return truth(missing) && tracks(inside) == 2, fmt.Sprintf("the outside copy missing %v, the series copy has %d tracks", missing, tracks(inside))
		})

		if got := diskFindingIDs(t, call(t, "audit_issues", withMessy(nil))); !slices.Equal(got, []string{outside}) {
			t.Errorf("audit_issues = %v, want the record left behind, %s", got, outside)
		}
		// and it still pairs with the copy its file went to, so neither audit
		// loses sight of it
		if !grouped(t) {
			t.Error("audit_duplicates no longer groups the record left behind with the series copy")
		}

		preview := call(t, "library_issues_remove", withMessy(nil))
		if num(t, preview["found"], "found") != 1 || num(t, preview["removed"], "removed") != 0 || num(t, preview["remaining"], "remaining") != 1 {
			t.Errorf("preview = %v, want one found and none removed", preview)
		}
		if items := rows(t, preview["items"], "items"); len(items) != 1 || items[0]["id"] != outside || items[0]["title"] != "Mort" || items[0]["path"] != outsidePath || items[0]["full_path"] != "/messy/"+outsidePath {
			t.Errorf("preview items = %v, want the orphan by id, title and path", items)
		}
		if _, err := invoke("item_get", map[string]any{"item": outside}); err != nil {
			t.Errorf("the preview removed the record: %v", err)
		}

		done := call(t, "library_issues_remove", withMessy(map[string]any{"confirm": true}))
		if num(t, done["removed"], "removed") != 1 || num(t, done["remaining"], "remaining") != 0 {
			t.Errorf("removal = %v, want the one removed and none left", done)
		}
		if msg := callErr(t, "item_get", map[string]any{"item": outside}); msg == "" {
			t.Error("the removed record still resolves")
		}
		if n := num(t, call(t, "library_items", withMessy(map[string]any{"limit": 1}))["total"], "total"); n != len(messyBooks)-1 {
			t.Errorf("Messy holds %d books, want %d: the removal took more than the orphan", n, len(messyBooks)-1)
		}
		if tracks(inside) != 2 {
			t.Error("the copy in the series lost the track moved into it")
		}
		if grouped(t) {
			t.Error("audit_duplicates still groups Mort with a copy that is gone")
		}

		putBack(t)
		if messyID(t, outsidePath) == outside {
			t.Errorf("the removed record %s came back", outside)
		}
		if got := call(t, "item_get", map[string]any{"item": inside}); got["chapters"] != nil || num(t, got["audio_tracks"], "audio_tracks") != 1 {
			t.Errorf("the series copy has %v chapters in %v tracks after the put back, want none in one", got["chapters"], got["audio_tracks"])
		}
		if !grouped(t) {
			t.Error("the put back did not bring the two Morts back")
		}
	})
}

// --- edits survive a forced scan, and a rescan picks up files -----------------

// A curation pass followed by the scans a server runs on its own: two books
// are edited one by one and together, one is embedded and then edited again,
// and a forced scan re-reads every file. The edits must stay - the embedded
// tags are older than them - and audit_unembedded must still call the book
// stale. Then a bigger cover and a second track are dropped into its folder,
// and item_rescan has to pick both up. It would catch a forced scan that
// re-titles a book from its old tags, an edit field the server does not
// keep (isbn, explicit, abridged, a batch edit's year or language), and a
// rescan that misses a new file.
func TestJourneyEditsSurviveAForcedScan(t *testing.T) {
	const author = "Zzyzx Rescan Author"
	bookPath, otherPath := author+"/Zzyzx Rescan Book", author+"/Zzyzx Rescan Other"

	s := newDiskShelf(t, "Zzyzx Rescan Shelf", "zzyzx-rescan")
	s.write(t, bookPath+"/01.mp3", bookPath+"/cover.jpg", otherPath+"/01.mp3")
	s.open(t, 2)
	ids := s.ids(t)
	book, other := ids[bookPath], ids[otherPath]

	// what each book should read back as, from here on
	want := map[string]map[string]any{
		book: {
			"title": "Zzyzx Rescan Title", "isbn": "9780306406157", "explicit": true, "abridged": true,
			"narrator": "Zzyzx Rescan Reader", "year": "1999", "publisher": "Zzyzx Press", "language": "English",
		},
		other: {"title": "Zzyzx Rescan Other", "year": "1999", "publisher": "Zzyzx Press", "language": "English"},
	}
	wantList := map[string]map[string][]string{
		// a genre with a comma of its own, which the embed joins with "; "
		book:  {"genres": {"Mystery, Thriller & Suspense", "Zzyzx Genre"}, "series": {"Zzyzx Rescan Saga #1"}, "tags": {"zzyzx-keep"}},
		other: {"series": {"Zzyzx Rescan Saga #2"}, "tags": {"zzyzx-keep"}},
	}
	check := func(t *testing.T) {
		t.Helper()
		for id, fields := range want {
			got := call(t, "item_get", map[string]any{"item": id})
			for k, v := range fields {
				if got[k] != v {
					t.Errorf("%v %s = %v, want %v", got["title"], k, got[k], v)
				}
			}
			for k, v := range wantList[id] {
				var have []string
				if got[k] != nil {
					have = strs(t, got[k], k)
				}
				if !slices.Equal(have, v) {
					t.Errorf("%v %s = %v, want %v", got["title"], k, have, v)
				}
			}
		}
	}

	t.Run("edited one by one and together", func(t *testing.T) {
		call(t, "item_edit", map[string]any{
			"item": book, "title": want[book]["title"], "genres": toAny(wantList[book]["genres"]), "isbn": want[book]["isbn"],
			"explicit": true, "abridged": true, "narrators": []any{want[book]["narrator"]}, "series": toAny(wantList[book]["series"]),
		})
		call(t, "item_edit", map[string]any{"item": other, "series": toAny(wantList[other]["series"])})
		both := []any{book, other}
		call(t, "item_edit", map[string]any{
			"library": s.name, "items": both, "tags": []any{"zzyzx-keep", "zzyzx-drop"},
			"year": "1999", "publisher": "Zzyzx Press", "language": "English", "add_series": []any{"Zzyzx Rescan Omnibus"},
		})
		for _, id := range []string{book, other} {
			if series := strs(t, call(t, "item_get", map[string]any{"item": id})["series"], "series"); !slices.Contains(series, "Zzyzx Rescan Omnibus") || len(series) != 2 {
				t.Errorf("series after add_series = %v, want the omnibus beside the book's own", series)
			}
		}
		out := call(t, "item_edit", map[string]any{"library": s.name, "items": both, "remove_tags": []any{"zzyzx-drop"}, "remove_series": []any{"Zzyzx Rescan Omnibus"}})
		if n := num(t, out["items_updated"], "items_updated"); n != 2 {
			t.Errorf("items_updated = %d, want 2", n)
		}
		check(t)
	})

	t.Run("embedded, then edited: stale", func(t *testing.T) {
		// the embed reads clean: a genre holding a comma is one genre, and an
		// mp3 carries the publisher
		embed(t, s.name, book, map[string]any{"item": book})
		want[book]["title"] = "Zzyzx Retitled"
		wantList[book]["genres"] = []string{"Zzyzx Other Genre"}
		call(t, "item_edit", map[string]any{"item": book, "title": want[book]["title"], "genres": toAny(wantList[book]["genres"])})
		if detail, listed := unembedded(t, s.name, book); !listed || !strings.Contains(detail, "title") || !strings.Contains(detail, "genres") {
			t.Errorf("audit_unembedded = %q, %v; want the book, its title and genres stale", detail, listed)
		}
	})

	t.Run("a forced scan keeps the edits", func(t *testing.T) {
		s.scan(t, true)
		if result := text(call(t, "item_rescan", map[string]any{"item": book})["result"]); result != "UPTODATE" {
			t.Errorf("item_rescan after a forced scan = %s, want UPTODATE", result)
		}
		check(t)
		// the tags are still the old ones, so it is still due an embed
		if detail, listed := unembedded(t, s.name, book); !listed || !strings.Contains(detail, "title") || !strings.Contains(detail, "genres") {
			t.Errorf("audit_unembedded after the forced scan = %q, %v; want the book still stale", detail, listed)
		}
	})

	t.Run("a new track and a bigger cover, rescanned", func(t *testing.T) {
		small := func() bool {
			for _, row := range rows(t, call(t, "audit_covers", map[string]any{"library": s.name})["findings"], "findings") {
				if row["id"] == book && row["problem"] == "small" {
					return true
				}
			}
			return false
		}
		if !small() {
			t.Fatal("audit_covers does not call the 200-pixel cover small")
		}
		before := call(t, "item_get", map[string]any{"item": book})

		writeJPEG(t, filepath.Join(s.root, bookPath, "cover.jpg"), 600)
		diskSilence(t, filepath.Join(s.root, bookPath, "02.mp3"), 1)
		if result := text(call(t, "item_rescan", map[string]any{"item": book})["result"]); result != "UPDATED" {
			t.Errorf("item_rescan after adding a track = %s, want UPDATED", result)
		}

		after := call(t, "item_get", map[string]any{"item": book, "files": true})
		if n := num(t, after["audio_tracks"], "audio_tracks"); n != 2 {
			t.Errorf("audio_tracks = %d, want 2", n)
		}
		if files := valuesIn(t, after["track_list"], "track_list", "filename"); !slices.Equal(files, []string{"01.mp3", "02.mp3"}) {
			t.Errorf("tracks = %v, want 01.mp3 then 02.mp3", files)
		}
		if num(t, after["duration_s"], "duration_s") <= num(t, before["duration_s"], "duration_s") {
			t.Errorf("duration_s = %v before and %v after a second track", before["duration_s"], after["duration_s"])
		}
		if small() {
			t.Error("audit_covers still calls the cover small once it is 600 pixels")
		}
		check(t)
	})
}

// --- removing issues is scoped --------------------------------------------------

// Two libraries each lose a book's folder, and library_issues_remove is run
// on one: its preview and its removal must stay inside that library. Then
// every folder under one library goes at once, as a share that was not
// mounted for a scan: the preview lists every book, nothing goes without
// confirm, and when the folders come back a scan finds the same records with
// the listening progress still on them. It would catch a removal that reaches
// into another library, and a preview that deletes - here, everyone's
// progress on every book.
func TestJourneyRemovingIssuesIsScoped(t *testing.T) {
	const author = "Zzyzx Scope Author"
	gonePath, keptPath, alsoPath := author+"/Zzyzx Scope Gone", author+"/Zzyzx Scope Kept", author+"/Zzyzx Scope Also"
	otherGonePath, otherKeptPath := author+"/Zzyzx Scope Other Gone", author+"/Zzyzx Scope Other Kept"

	one := newDiskShelf(t, "Zzyzx Scope One", "zzyzx-scope-one")
	one.write(t, gonePath+"/01.mp3", keptPath+"/01.mp3", alsoPath+"/01.mp3")
	one.open(t, 3)
	two := newDiskShelf(t, "Zzyzx Scope Two", "zzyzx-scope-two")
	two.write(t, otherGonePath+"/01.mp3", otherKeptPath+"/01.mp3")
	two.open(t, 2)
	oneIDs, twoIDs := one.ids(t), two.ids(t)
	missing := func(s *diskShelf) []string {
		var out []string
		for p, it := range s.items(t) {
			if truth(it["missing"]) {
				out = append(out, p)
			}
		}
		slices.Sort(out)
		return out
	}

	for _, gone := range []string{filepath.Join(one.root, gonePath), filepath.Join(two.root, otherGonePath)} {
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
	}
	one.scan(t, false)
	two.scan(t, false)
	diskUntil(t, "both removed folders flagged missing", func() (bool, string) {
		a, b := missing(one), missing(two)
		return slices.Equal(a, []string{gonePath}) && slices.Equal(b, []string{otherGonePath}), fmt.Sprintf("missing %v and %v", a, b)
	})

	t.Run("one library's issues, removed", func(t *testing.T) {
		preview := call(t, "library_issues_remove", map[string]any{"library": one.name})
		items := rows(t, preview["items"], "items")
		if num(t, preview["found"], "found") != 1 || num(t, preview["removed"], "removed") != 0 || len(items) != 1 ||
			items[0]["id"] != oneIDs[gonePath] || items[0]["path"] != gonePath || items[0]["full_path"] != one.folder+"/"+gonePath {
			t.Errorf("preview = %v, want only this library's missing book", preview)
		}
		if got := missing(one); !slices.Equal(got, []string{gonePath}) {
			t.Errorf("after the preview this library's missing books are %v", got)
		}

		done := call(t, "library_issues_remove", map[string]any{"library": one.name, "confirm": true})
		if num(t, done["removed"], "removed") != 1 || num(t, done["remaining"], "remaining") != 0 {
			t.Errorf("removal = %v, want the one removed", done)
		}
		if got := slices.Sorted(func(yield func(string) bool) {
			for p := range one.items(t) {
				if !yield(p) {
					return
				}
			}
		}); !slices.Equal(got, []string{alsoPath, keptPath}) {
			t.Errorf("this library holds %v, want the two whose folders are there", got)
		}
		// the other library's missing book is untouched, and is now the only
		// issue on the server
		if got := missing(two); !slices.Equal(got, []string{otherGonePath}) {
			t.Errorf("the other library's missing books are %v, want its own still there", got)
		}
		if got := diskFindingIDs(t, call(t, "audit_issues", nil)); !slices.Equal(got, []string{twoIDs[otherGonePath]}) {
			t.Errorf("audit_issues across the server = %v, want only the other library's book", got)
		}
	})

	t.Run("every folder gone, then back", func(t *testing.T) {
		kept := oneIDs[keptPath]
		call(t, "user_progress_set", map[string]any{"item": kept, "percent": 40})
		away := one.root + "-away"
		if err := os.Rename(filepath.Join(one.root, author), away); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(away) })

		one.scan(t, false)
		diskUntil(t, "every book flagged missing", func() (bool, string) {
			got := missing(one)
			return slices.Equal(got, []string{alsoPath, keptPath}), fmt.Sprintf("missing %v", got)
		})
		preview := call(t, "library_issues_remove", map[string]any{"library": one.name})
		paths := valuesIn(t, preview["items"], "items", "path")
		slices.Sort(paths)
		if num(t, preview["found"], "found") != 2 || num(t, preview["removed"], "removed") != 0 ||
			!slices.Equal(paths, []string{alsoPath, keptPath}) {
			t.Errorf("preview = %v, want both books listed and nothing removed", preview)
		}
		if n := len(one.items(t)); n != 2 {
			t.Errorf("the library holds %d records after the preview, want both", n)
		}

		// the share comes back
		if err := os.Rename(away, filepath.Join(one.root, author)); err != nil {
			t.Fatal(err)
		}
		one.scan(t, false)
		diskUntil(t, "the books found again", func() (bool, string) {
			got := missing(one)
			return len(got) == 0, fmt.Sprintf("missing %v", got)
		})
		if got := one.ids(t); got[keptPath] != kept || got[alsoPath] != oneIDs[alsoPath] || len(got) != 2 {
			t.Errorf("after the folders came back the records are %v, want the same two", got)
		}
		progress := object(call(t, "user_progress_get", map[string]any{"item": kept})["progress"])
		if progress == nil || num(t, progress["percent"], "percent") != 40 {
			t.Errorf("progress = %v, want the 40%% it had before the folders went", progress)
		}
		if n := num(t, call(t, "audit_issues", map[string]any{"library": one.name})["total_findings"], "total_findings"); n != 0 {
			t.Errorf("audit_issues finds %d once the folders are back", n)
		}
	})
}

// --- a folder with no audio ------------------------------------------------------

// An ebook dropped into an audiobook library, as a folder of its own with no
// audio beside it. audit_no_audio has to name it, and leave alone the
// audiobook next to it and the seeded book that carries an ebook beside its
// audio; deleting it with its files, the fix, has to clear the finding and
// the disk. Until now no fixture gave the audit anything to find.
func TestJourneyAnEbookOnlyFolder(t *testing.T) {
	const author = "Zzyzx Ebook Author"
	audioPath, ebookPath := author+"/Zzyzx Audio Book", author+"/Zzyzx Ebook Only"

	s := newDiskShelf(t, "Zzyzx Ebook Shelf", "zzyzx-ebook")
	s.write(t, audioPath+"/01.mp3", ebookPath+"/Zzyzx Ebook Only.epub")
	s.open(t, 2)
	ids := s.ids(t)

	book := call(t, "item_get", map[string]any{"item": ids[ebookPath]})
	if book["ebook"] != "epub" || book["audio_tracks"] != nil {
		t.Errorf("the ebook-only book reads as ebook %v with %v tracks", book["ebook"], book["audio_tracks"])
	}
	out := call(t, "audit_no_audio", map[string]any{"library": s.name})
	findings := rows(t, out["findings"], "findings")
	if len(findings) != 1 || findings[0]["title"] != "Zzyzx Ebook Only" || findings[0]["detail"] != "ebook only (epub)" || num(t, out["total_findings"], "total_findings") != 1 {
		t.Errorf("audit_no_audio = %v, want the ebook alone, as an ebook", out)
	}
	// across every library it is still the only one: Foundation's epub sits
	// beside its audio
	if got := titlesIn(t, call(t, "audit_no_audio", nil)["findings"], "findings"); !slices.Equal(got, []string{"Zzyzx Ebook Only"}) {
		t.Errorf("audit_no_audio across the server = %v", got)
	}

	call(t, "item_delete", map[string]any{"item": ids[ebookPath], "delete_files": true, "confirm": true})
	if got := s.onDisk(t); !slices.Equal(got, []string{author + "/", audioPath + "/", audioPath + "/01.mp3"}) {
		t.Errorf("on disk after the delete: %v", got)
	}
	s.scan(t, false)
	if n := num(t, call(t, "audit_no_audio", map[string]any{"library": s.name})["total_findings"], "total_findings"); n != 0 {
		t.Errorf("audit_no_audio finds %d after the delete and a scan", n)
	}
	if got := s.ids(t); len(got) != 1 || got[audioPath] != ids[audioPath] {
		t.Errorf("the library holds %v, want the audiobook alone", got)
	}
}

// --- folders that name their books ------------------------------------------------

// A shelf laid out the way collectors lay one out - Author/Series/NN - Title,
// Author/Title, and an author written in Cyrillic - read by the two audits
// that compare folders with metadata. audit_series has to take the series
// from the folder a book sits in, not from the whole path above it, to see a
// book filed in the series folder but not linked to the series; audit_path
// has to compare whole words, so a book retitled "It" in a folder called
// "The Zzyzx Institute" is a mismatch, and has to keep Cyrillic letters, so
// a correctly filed Russian book is not flagged and a misfiled one is. Each
// finding is fixed the way it says and the audits read again.
func TestJourneyFoldersNameTheirBooks(t *testing.T) {
	const author, saga = "Zzyzx Folder Author", "Zzyzx Folder Saga"
	first, second, third := author+"/"+saga+"/01 - Zzyzx First Voyage", author+"/"+saga+"/02 - Zzyzx Second Voyage", author+"/"+saga+"/03 - Zzyzx Third Voyage"
	institute := author + "/The Zzyzx Institute"
	sisters, vanya := "Антон Чехов/Три сестры", "Антон Чехов/Дядя Ваня"
	dated := author + "/Zzyzx Dated Book (1965)"

	s := newDiskShelf(t, "Zzyzx Folder Shelf", "zzyzx-folders")
	s.write(t, first+"/01.mp3", second+"/01.mp3", third+"/01.mp3", institute+"/01.mp3", sisters+"/01.mp3", vanya+"/01.mp3", dated+"/01.mp3")
	s.open(t, 7)
	ids := s.ids(t)
	if got := call(t, "item_get", map[string]any{"item": ids[sisters]}); got["title"] != "Три сестры" || got["author"] != "Антон Чехов" {
		t.Fatalf("the Cyrillic book scanned as %v by %v", got["title"], got["author"])
	}

	t.Run("a book in the series folder, not in the series", func(t *testing.T) {
		call(t, "item_edit", map[string]any{"item": ids[first], "series": []any{saga + " #1"}})
		call(t, "item_edit", map[string]any{"item": ids[third], "series": []any{saga + " #3"}})
		call(t, "item_edit", map[string]any{"item": ids[second], "clear": []any{"series"}})

		out := call(t, "audit_series", map[string]any{"library": s.name})
		got := rows(t, out["numbering"], "numbering")
		if len(got) != 1 || got[0]["id"] != ids[second] || got[0]["problem"] != "unlinked" || got[0]["suggest"] != saga+" #2" {
			t.Fatalf("numbering = %v, want the second voyage unlinked, with %s #2 to add", got, saga)
		}
		// the gap it leaves names it too, rather than calling #2 missing
		gaps := rows(t, out["gaps"], "gaps")
		if len(gaps) != 1 || gaps[0]["name"] != saga || !slices.Equal(valuesIn(t, gaps[0]["unlinked"], "unlinked", "id"), []string{ids[second]}) {
			t.Errorf("gaps = %v, want the saga's #2 found unlinked on the shelf", gaps)
		}
		call(t, "item_edit", map[string]any{"item": ids[second], "add_series": []any{got[0]["suggest"]}})
		out = call(t, "audit_series", map[string]any{"library": s.name})
		if n, g := len(rows(t, out["numbering"], "numbering")), len(rows(t, out["gaps"], "gaps")); n+g != 0 {
			t.Errorf("after linking it: numbering %v, gaps %v; want both clear", out["numbering"], out["gaps"])
		}
	})

	t.Run("titles the folders do not name", func(t *testing.T) {
		call(t, "item_edit", map[string]any{"item": ids[institute], "title": "It"})
		call(t, "item_edit", map[string]any{"item": ids[vanya], "title": "Палата номер шесть"})

		want := []string{ids[institute], ids[vanya]}
		slices.Sort(want)
		if got := diskFindingIDs(t, call(t, "audit_path", map[string]any{"library": s.name})); !slices.Equal(got, want) {
			t.Errorf("audit_path = %v, want It and the misfiled Chekhov (%v), and not the one filed right", got, want)
		}

		call(t, "item_edit", map[string]any{"item": ids[institute], "title": "The Zzyzx Institute"})
		call(t, "item_edit", map[string]any{"item": ids[vanya], "title": "Дядя Ваня"})
		if got := diskFindingIDs(t, call(t, "audit_path", map[string]any{"library": s.name})); len(got) != 0 {
			t.Errorf("audit_path = %v once every title is its folder's", got)
		}
	})

	// a folder carries its first printing's year, and a recording is never
	// older than that: a year before it is a wrong match or a slip
	t.Run("a year earlier than the folder's", func(t *testing.T) {
		call(t, "item_edit", map[string]any{"item": ids[dated], "title": "Zzyzx Dated Book", "year": "1959"})
		out := call(t, "audit_path", map[string]any{"library": s.name})
		found := rows(t, out["findings"], "findings")
		if len(found) != 1 || found[0]["id"] != ids[dated] || !strings.Contains(text(found[0]["detail"]), "says 1965 but the year is 1959") {
			t.Fatalf("audit_path = %v, want the dated book, its year before its folder's", found)
		}
		// audit_all counts it, as it counts every audit_path finding
		counts := rows(t, call(t, "audit_all", map[string]any{"library": s.name})["audits"], "audits")
		i := slices.IndexFunc(counts, func(r map[string]any) bool { return r["audit"] == "audit_path" })
		if i < 0 || number(counts[i]["found"]) != 1 {
			t.Errorf("audit_all's audit_path = %v", counts)
		}

		// a later year is what a recording of a book first printed then is
		call(t, "item_edit", map[string]any{"item": ids[dated], "year": "2007"})
		if got := diskFindingIDs(t, call(t, "audit_path", map[string]any{"library": s.name})); len(got) != 0 {
			t.Errorf("audit_path = %v for a recording later than the folder's year", got)
		}
	})

	// a book the record places in a series is filed where the series shows:
	// the three voyages are, under the saga's own folder, and a book filed by
	// its title alone is not
	t.Run("a series the folder does not say", func(t *testing.T) {
		call(t, "item_edit", map[string]any{"item": ids[institute], "series": []any{saga + " #4"}})
		found := rows(t, call(t, "audit_path", map[string]any{"library": s.name})["findings"], "findings")
		if len(found) != 1 || found[0]["id"] != ids[institute] || !strings.Contains(text(found[0]["detail"]), `does not say it is "`+saga+` #4"`) {
			t.Fatalf("audit_path = %v, want the institute alone, placed in the saga by its record and not by its folder", found)
		}

		// filing by series is one collector's way, and a server told to leave
		// the rule out says nothing of it, here or in audit_all's count
		other := newUserWith(t, "zzyzx-files-another-way", abs.UserCreate{}, tools.Options{AuditSkip: []string{"path-series"}})
		if got := diskFindingIDs(t, other.call(t, "audit_path", map[string]any{"library": s.name})); len(got) != 0 {
			t.Errorf("audit_path = %v on a server that leaves the series rule out", got)
		}
		counts := rows(t, other.call(t, "audit_all", map[string]any{"library": s.name})["audits"], "audits")
		if slices.ContainsFunc(counts, func(r map[string]any) bool { return r["audit"] == "audit_path" }) {
			t.Errorf("audit_all = %v on a server that leaves the series rule out", counts)
		}

		// a series with no place in it is a collection, which nobody files by
		call(t, "item_edit", map[string]any{"item": ids[institute], "series": []any{saga}})
		if got := diskFindingIDs(t, call(t, "audit_path", map[string]any{"library": s.name})); len(got) != 0 {
			t.Errorf("audit_path = %v for a series the record gives no place in", got)
		}
	})
}

// --- the files inside a book --------------------------------------------------

// writeText writes a file of text into the shelf, for the files a book
// carries beside its audio.
func (s *diskShelf) writeText(t *testing.T, rel, body string) {
	t.Helper()

	p := filepath.Join(s.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
		t.Fatal(err)
	}
}

// chapterSpans reads a book's chapters as "title start-end", in whole seconds.
func chapterSpans(t *testing.T, id string) []string {
	t.Helper()

	chapters := rows(t, call(t, "item_get", map[string]any{"item": id, "chapters": true})["chapter_list"], "chapter_list")
	out := make([]string, 0, len(chapters))
	for _, ch := range chapters {
		out = append(out, fmt.Sprintf("%s %.0f-%.0f", text(ch["title"]), number(ch["start_s"]), number(ch["end_s"])))
	}
	return out
}

// trackOrder reads a book's audio files in play order.
func trackOrder(t *testing.T, id string) []string {
	t.Helper()

	return valuesIn(t, call(t, "item_get", map[string]any{"item": id, "files": true})["track_list"], "track_list", "filename")
}

// A book whose files play in the wrong order, and whose two ebooks open the
// wrong one: item_edit puts the tracks in order, the chapters, one to a file
// as the scanner cut them, going with their files, and chooses the ebook
// readers open. A plain rescan keeps it all.
func TestJourneyTracksReorderedAndEbookChosen(t *testing.T) {
	const book = "Zzyzx Track Author/Zzyzx Track Book"

	s := newDiskShelf(t, "Zzyzx Track Shelf", "zzyzx-tracks")
	for i, secs := range []int{2, 3, 4} {
		diskSilence(t, filepath.Join(s.root, book, fmt.Sprintf("0%d.mp3", i+1)), secs)
	}
	s.write(t, book+"/Zzyzx Track Book.epub", book+"/Zzyzx Track Book Extras.epub")
	s.open(t, 1)
	id := s.ids(t)[book]

	if got := chapterSpans(t, id); !slices.Equal(got, []string{"01 0-2", "02 2-5", "03 5-9"}) {
		t.Fatalf("the scan chaptered it %v, want one chapter to a file", got)
	}
	msg := callErr(t, "item_edit", map[string]any{"item": id, "tracks": []any{"03.mp3", "01.mp3"}})
	if !strings.Contains(msg, "leaves out") || !strings.Contains(msg, "02.mp3") {
		t.Errorf("a list short of a file: %s", msg)
	}
	if got := trackOrder(t, id); !slices.Equal(got, []string{"01.mp3", "02.mp3", "03.mp3"}) {
		t.Fatalf("a refused list changed the order to %v", got)
	}

	out := call(t, "item_edit", map[string]any{"item": id, "tracks": []any{"03.mp3", "01.mp3", "02.mp3"}})
	if out["chapters"] != "moved" || !slices.Equal(strs(t, out["tracks"], "tracks"), []string{"03.mp3", "01.mp3", "02.mp3"}) {
		t.Errorf("item_edit tracks = %v", out)
	}
	if got := trackOrder(t, id); !slices.Equal(got, []string{"03.mp3", "01.mp3", "02.mp3"}) {
		t.Errorf("plays %v", got)
	}
	if got := chapterSpans(t, id); !slices.Equal(got, []string{"03 0-4", "01 4-6", "02 6-9"}) {
		t.Errorf("chapters %v, want each with its file", got)
	}
	if found := rows(t, call(t, "audit_chapters", map[string]any{"library": s.name})["findings"], "findings"); len(found) != 0 {
		t.Errorf("audit_chapters = %v after the chapters moved", found)
	}
	call(t, "item_rescan", map[string]any{"item": id})
	if got, ch := trackOrder(t, id), chapterSpans(t, id); !slices.Equal(got, []string{"03.mp3", "01.mp3", "02.mp3"}) || ch[0] != "03 0-4" {
		t.Errorf("after a rescan: plays %v, chapters %v", got, ch)
	}

	// the ebooks: the scan chose one, and the other is supplementary
	mainOf := func() string {
		for _, f := range rows(t, call(t, "item_get", map[string]any{"item": id})["other_files"], "other_files") {
			if truth(f["main"]) {
				return text(f["filename"])
			}
		}
		return "none"
	}
	chosen := mainOf()
	other := "Zzyzx Track Book.epub"
	if chosen == other {
		other = "Zzyzx Track Book Extras.epub"
	}
	if chosen == "none" {
		t.Fatal("the scan made neither ebook the main one")
	}
	out = call(t, "item_edit", map[string]any{"item": id, "ebook": other})
	if out["ebook"] != other || mainOf() != other {
		t.Errorf("item_edit ebook = %v; main is now %s", out, mainOf())
	}
	// asked again, it is already so: one more flip would leave none
	if out = call(t, "item_edit", map[string]any{"item": id, "ebook": other}); truth(out["updated"]) || mainOf() != other {
		t.Errorf("a second call = %v; main is now %s", out, mainOf())
	}
	if out = call(t, "item_edit", map[string]any{"item": id, "ebook": "none"}); out["ebook"] != "none" || mainOf() != "none" {
		t.Errorf("item_edit ebook none = %v; main is now %s", out, mainOf())
	}
}

// Stray files beside a book's audio - a note, a second ebook - taken out one
// at a time and off the disk, while the audio and the cover, whose removal
// the server would not carry through the book, are refused.
func TestJourneyOneFileDeleted(t *testing.T) {
	const book = "Zzyzx Stray Author/Zzyzx Stray Book"

	s := newDiskShelf(t, "Zzyzx Stray Shelf", "zzyzx-stray")
	s.write(t, book+"/01.mp3", book+"/02.mp3", book+"/cover.jpg", book+"/Zzyzx Stray Book.epub")
	s.writeText(t, book+"/notes .txt", "notes\n")
	s.open(t, 1)
	id := s.ids(t)[book]

	out := call(t, "item_delete", map[string]any{"item": id, "file": "notes .txt"})
	if out["would_delete"] != `"notes .txt" from "Zzyzx Stray Book"` || !slices.Contains(s.onDisk(t), book+"/notes .txt") {
		t.Fatalf("the preview = %v, and the disk %v", out, s.onDisk(t))
	}
	out = call(t, "item_delete", map[string]any{"item": id, "file": "notes .txt", "confirm": true})
	if !truth(out["files_removed"]) || slices.Contains(s.onDisk(t), book+"/notes .txt") {
		t.Errorf("the delete = %v, and the disk %v", out, s.onDisk(t))
	}
	for _, f := range rows(t, call(t, "item_get", map[string]any{"item": id})["other_files"], "other_files") {
		if f["filename"] == "notes .txt" {
			t.Error("the book still lists the note")
		}
	}

	for file, says := range map[string]string{"02.mp3": "audio file", "cover.jpg": "cover"} {
		if msg := callErr(t, "item_delete", map[string]any{"item": id, "file": file, "confirm": true}); !strings.Contains(msg, says) {
			t.Errorf("deleting %s: %s", file, msg)
		}
		if !slices.Contains(s.onDisk(t), book+"/"+file) {
			t.Errorf("%s went from disk though refused", file)
		}
	}

	out = call(t, "item_delete", map[string]any{"item": id, "file": "Zzyzx Stray Book.epub", "confirm": true})
	if !strings.Contains(text(out["note"]), "left with none") || slices.Contains(s.onDisk(t), book+"/Zzyzx Stray Book.epub") {
		t.Errorf("the main ebook's delete = %v, and the disk %v", out, s.onDisk(t))
	}
	if got := call(t, "item_get", map[string]any{"item": id}); got["ebook"] != nil || num(t, got["audio_tracks"], "audio_tracks") != 2 {
		t.Errorf("after: ebook %v, %v tracks", got["ebook"], got["audio_tracks"])
	}
}

// A book in three files merged into one m4b: previewed at its own quality,
// then merged, waited for and read back. The m4b is in the folder, the files
// it was made from are not, and it plays for as long as they did.
func TestJourneyMergedIntoOneM4B(t *testing.T) {
	const book = "Zzyzx Merge Author/Zzyzx Merge Book"

	s := newDiskShelf(t, "Zzyzx Merge Shelf", "zzyzx-merge")
	for i, secs := range []int{2, 3, 4} {
		diskSilence(t, filepath.Join(s.root, book, fmt.Sprintf("0%d.mp3", i+1)), secs)
	}
	s.open(t, 1)
	id := s.ids(t)[book]

	out := call(t, "item_embed_metadata", map[string]any{"item": id, "m4b": true})
	plan := object(out["would_merge"])
	if plan["into"] != "Zzyzx Merge Book.m4b" || number(plan["channels"]) != 1 || !slices.Equal(strs(t, plan["files"], "files"), []string{"01.mp3", "02.mp3", "03.mp3"}) {
		t.Fatalf("the preview = %v", out)
	}
	if got := s.onDisk(t); slices.Contains(got, book+"/Zzyzx Merge Book.m4b") {
		t.Fatalf("a preview merged: %v", got)
	}

	out = call(t, "item_embed_metadata", map[string]any{"item": id, "m4b": true, "confirm": true})
	if truth(out["running"]) {
		diskUntil(t, "the merge", func() (bool, string) {
			tasks := rows(t, call(t, "server_tasks", nil)["tasks"], "tasks")
			return len(tasks) == 0, fmt.Sprint(tasks)
		})
		call(t, "item_rescan", map[string]any{"item": id})
	} else if object(out["merged"])["into"] != "Zzyzx Merge Book.m4b" {
		t.Fatalf("the merge = %v", out)
	}
	if got := s.onDisk(t); !slices.Equal(got, []string{"Zzyzx Merge Author/", book + "/", book + "/Zzyzx Merge Book.m4b"}) {
		t.Errorf("on disk after the merge: %v", got)
	}
	tracks := rows(t, call(t, "item_get", map[string]any{"item": id, "files": true})["track_list"], "track_list")
	if len(tracks) != 1 || tracks[0]["filename"] != "Zzyzx Merge Book.m4b" || tracks[0]["codec"] != "aac" || number(tracks[0]["duration_s"]) < 8 || number(tracks[0]["duration_s"]) > 10 {
		t.Errorf("plays %v", tracks)
	}
	if msg := callErr(t, "item_embed_metadata", map[string]any{"item": id, "m4b": true}); !strings.Contains(msg, "already one m4b") {
		t.Errorf("merging it again: %s", msg)
	}
}

// A book kept in disc folders, two of whose files share a name: item_get
// says which folder each file is in, and item_edit takes the play order by
// those paths, refusing a name two files carry.
func TestJourneyDiscFolders(t *testing.T) {
	const book = "Zzyzx Disc Author/Zzyzx Disc Book"

	s := newDiskShelf(t, "Zzyzx Disc Shelf", "zzyzx-discs")
	for rel, secs := range map[string]int{"Disc 1/01.mp3": 2, "Disc 1/02.mp3": 3, "Disc 2/01.mp3": 4} {
		diskSilence(t, filepath.Join(s.root, book, rel), secs)
	}
	s.open(t, 1)
	id := s.ids(t)[book]

	paths := func() []string {
		return valuesIn(t, call(t, "item_get", map[string]any{"item": id, "files": true})["track_list"], "track_list", "path")
	}
	if got := paths(); !slices.Equal(got, []string{"Disc 1/01.mp3", "Disc 1/02.mp3", "Disc 2/01.mp3"}) {
		t.Fatalf("the scan plays %v, want each disc's files by their folder", got)
	}
	if got := chapterSpans(t, id); !slices.Equal(got, []string{"01 0-2", "02 2-5", "01 5-9"}) {
		t.Fatalf("the scan chaptered it %v, want one chapter to a file", got)
	}

	msg := callErr(t, "item_edit", map[string]any{"item": id, "tracks": []any{"01.mp3", "02.mp3", "01.mp3"}})
	if !strings.Contains(msg, `2 audio files are named "01.mp3"`) || !strings.Contains(msg, `"Disc 1/01.mp3"`) || !strings.Contains(msg, `"Disc 2/01.mp3"`) {
		t.Errorf("a name two files carry: %s", msg)
	}
	if got := paths(); !slices.Equal(got, []string{"Disc 1/01.mp3", "Disc 1/02.mp3", "Disc 2/01.mp3"}) {
		t.Fatalf("a refused list changed the order to %v", got)
	}

	// by path, and by name alone where only one file has it
	out := call(t, "item_edit", map[string]any{"item": id, "tracks": []any{"Disc 2/01.mp3", "Disc 1/01.mp3", "02.mp3"}})
	want := []string{"Disc 2/01.mp3", "Disc 1/01.mp3", "Disc 1/02.mp3"}
	if out["chapters"] != "moved" || !slices.Equal(strs(t, out["tracks"], "tracks"), want) {
		t.Errorf("item_edit tracks = %v", out)
	}
	if got := paths(); !slices.Equal(got, want) {
		t.Errorf("plays %v", got)
	}
	if got := chapterSpans(t, id); !slices.Equal(got, []string{"01 0-4", "01 4-6", "02 6-9"}) {
		t.Errorf("chapters %v, want each with its file", got)
	}
}

// Chapters that are not cut along the files, one running out of the first
// file into the second, cannot follow the files to a new order: the files
// are reordered, the chapters are left where they were, and the answer says
// so.
func TestJourneyChaptersAcrossFilesAreLeft(t *testing.T) {
	const book = "Zzyzx Span Author/Zzyzx Span Book"

	s := newDiskShelf(t, "Zzyzx Span Shelf", "zzyzx-span")
	for i, secs := range []int{2, 3, 4} {
		diskSilence(t, filepath.Join(s.root, book, fmt.Sprintf("0%d.mp3", i+1)), secs)
	}
	s.open(t, 1)
	id := s.ids(t)[book]

	call(t, "item_chapters_set", map[string]any{"item": id, "chapters": []any{
		map[string]any{"title": "Zzyzx One", "start_s": 0},
		map[string]any{"title": "Zzyzx Two", "start_s": 3},
	}})
	across := []string{"Zzyzx One 0-3", "Zzyzx Two 3-9"}
	if got := chapterSpans(t, id); !slices.Equal(got, across) {
		t.Fatalf("the chapters set = %v, want %v", got, across)
	}

	out := call(t, "item_edit", map[string]any{"item": id, "tracks": []any{"03.mp3", "01.mp3", "02.mp3"}})
	if out["chapters"] != "left" || !slices.Equal(strs(t, out["tracks"], "tracks"), []string{"03.mp3", "01.mp3", "02.mp3"}) {
		t.Errorf("item_edit tracks = %v, want the chapters left", out)
	}
	if got := trackOrder(t, id); !slices.Equal(got, []string{"03.mp3", "01.mp3", "02.mp3"}) {
		t.Errorf("plays %v", got)
	}
	if got := chapterSpans(t, id); !slices.Equal(got, across) {
		t.Errorf("chapters %v, want them as they were, %v", got, across)
	}

	// the order it already has moves nothing, chapters across files or not
	out = call(t, "item_edit", map[string]any{"item": id, "tracks": []any{"03.mp3", "01.mp3", "02.mp3"}})
	if out["chapters"] != "unchanged" || truth(out["updated"]) {
		t.Errorf("the same order again = %v", out)
	}
}

// A book merged at a quality chosen for it and not its own: the preview
// says what was asked, a quality no audiobook has is refused, and the m4b
// made is at what was asked.
func TestJourneyMergedAtAChosenQuality(t *testing.T) {
	const book = "Zzyzx Quality Author/Zzyzx Quality Book"

	s := newDiskShelf(t, "Zzyzx Quality Shelf", "zzyzx-quality")
	// a reading, not silence: silence encodes to next to nothing at any bitrate
	for i, seed := range []uint64{11, 12} {
		encode(t, voice(t, seed, 20), filepath.Join(s.root, book, fmt.Sprintf("0%d.mp3", i+1)), nil, []string{"-c:a", "libmp3lame", "-b:a", "64k"})
	}
	s.open(t, 1)
	id := s.ids(t)[book]

	own := object(call(t, "item_embed_metadata", map[string]any{"item": id, "m4b": true})["would_merge"])
	if number(own["bitrate_kbps"]) != 64 || number(own["channels"]) != 1 {
		t.Fatalf("the book's own quality = %v, want 64k mono", own)
	}
	chosen := object(call(t, "item_embed_metadata", map[string]any{"item": id, "m4b": true, "bitrate_kbps": 32, "channels": 2})["would_merge"])
	if number(chosen["bitrate_kbps"]) != 32 || number(chosen["channels"]) != 2 {
		t.Fatalf("the preview = %v, want 32k stereo", chosen)
	}
	for _, c := range []struct {
		args map[string]any
		says string
	}{
		{map[string]any{"bitrate_kbps": 8}, "outside 16 to 320"},
		{map[string]any{"bitrate_kbps": 400}, "outside 16 to 320"},
		{map[string]any{"channels": 6}, "1 or 2"},
	} {
		args := map[string]any{"item": id, "m4b": true, "confirm": true}
		maps.Copy(args, c.args)
		if msg := callErr(t, "item_embed_metadata", args); !strings.Contains(msg, c.says) {
			t.Errorf("merging with %v: %s", c.args, msg)
		}
	}
	if msg := callErr(t, "item_embed_metadata", map[string]any{"item": id, "bitrate_kbps": 32}); !strings.Contains(msg, "are for m4b") {
		t.Errorf("a bitrate without m4b: %s", msg)
	}
	if got := s.onDisk(t); !slices.Contains(got, book+"/01.mp3") || slices.Contains(got, book+"/Zzyzx Quality Book.m4b") {
		t.Fatalf("a preview or a refusal merged: %v", got)
	}

	out := call(t, "item_embed_metadata", map[string]any{"item": id, "m4b": true, "confirm": true, "bitrate_kbps": 32, "channels": 2})
	if truth(out["running"]) {
		diskUntil(t, "the merge", func() (bool, string) {
			tasks := rows(t, call(t, "server_tasks", nil)["tasks"], "tasks")
			return len(tasks) == 0, fmt.Sprint(tasks)
		})
		call(t, "item_rescan", map[string]any{"item": id})
	} else if merged := object(out["merged"]); number(merged["bitrate_kbps"]) != 32 || number(merged["channels"]) != 2 {
		t.Fatalf("the merge = %v", out)
	}
	tracks := rows(t, call(t, "item_get", map[string]any{"item": id, "files": true})["track_list"], "track_list")
	if len(tracks) != 1 || tracks[0]["codec"] != "aac" || number(tracks[0]["channels"]) != 2 {
		t.Fatalf("plays %v, want one stereo m4b", tracks)
	}
	// an encoder keeps near what it is asked, not to it
	if kbps := number(tracks[0]["bitrate_kbps"]); kbps < 24 || kbps > 40 {
		t.Errorf("the m4b is %vk, want about the 32k asked for, not the book's own 64k", kbps)
	}
}

// A merge stopped while it runs: the book is long enough that the server is
// still encoding when the cancel lands. The call that started the merge says
// it made no m4b, and the book and its folder are as they were.
func TestJourneyMergeCancelled(t *testing.T) {
	const book = "Zzyzx Halt Author/Zzyzx Halt Book"
	// an uncancelled merge of three files of eight hours took the server 52
	// seconds; the cancel lands inside the first
	const hours = 4

	s := newDiskShelf(t, "Zzyzx Halt Shelf", "zzyzx-halt")
	files := []string{"01.m4b", "02.m4b", "03.m4b"}
	for _, f := range files {
		diskSilence(t, filepath.Join(s.root, book, f), hours*3600)
	}
	s.open(t, 1)
	id := s.ids(t)[book]
	onDisk := s.onDisk(t)
	// silence is stored at next to no bitrate, under what an m4b is made at,
	// so the merge is told one
	merge64 := map[string]any{"item": id, "m4b": true, "confirm": true, "bitrate_kbps": 64}
	if msg := callErr(t, "item_embed_metadata", map[string]any{"item": id, "m4b": true}); !strings.Contains(msg, "pass bitrate_kbps") {
		t.Errorf("a book stored under 16k, merged at its own bitrate: %s", msg)
	}

	if msg := callErr(t, "item_embed_metadata", map[string]any{"item": id, "m4b": true, "cancel": true}); !strings.Contains(msg, "no merge of") {
		t.Errorf("cancelling with nothing running: %s", msg)
	}

	type result struct {
		out map[string]any
		err error
	}
	merged := make(chan result, 1)
	go func() {
		out, err := invoke("item_embed_metadata", merge64)
		merged <- result{out, err}
	}()
	// however the test ends, no merge is left running under the next one
	t.Cleanup(func() {
		_, _ = invoke("item_embed_metadata", map[string]any{"item": id, "m4b": true, "cancel": true})
		waitIdle(t)
	})

	var ended *result
	diskUntil(t, "the merge starting", func() (bool, string) {
		select {
		case r := <-merged:
			ended = &r
			return true, ""
		default:
		}
		tasks := rows(t, call(t, "server_tasks", nil)["tasks"], "tasks")
		return len(tasks) > 0, "no task is running"
	})
	if ended != nil {
		t.Fatalf("the merge ended before it could be cancelled: %v %v; the book has to be longer", ended.out, ended.err)
	}
	// asked about while it runs, it is running, and is not started twice
	if out := call(t, "item_embed_metadata", merge64); !truth(out["running"]) {
		t.Errorf("a second merge of a book being merged = %v", out)
	}

	out := call(t, "item_embed_metadata", map[string]any{"item": id, "m4b": true, "cancel": true})
	if !truth(out["cancelled"]) {
		t.Errorf("cancel = %v", out)
	}
	select {
	case r := <-merged:
		if r.err == nil || !strings.Contains(r.err.Error(), "without making the book one m4b") {
			t.Errorf("the merge that was cancelled answered %v %v, want that it made no m4b", r.out, r.err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the call that started the merge never came back after the cancel")
	}

	waitIdle(t)
	if got := s.onDisk(t); !slices.Equal(got, onDisk) {
		t.Errorf("on disk after the cancel: %v, want it as it was, %v", got, onDisk)
	}
	if got := trackOrder(t, id); !slices.Equal(got, files) {
		t.Errorf("plays %v after the cancel, want %v", got, files)
	}
	if msg := callErr(t, "item_embed_metadata", map[string]any{"item": id, "m4b": true, "cancel": true}); !strings.Contains(msg, "no merge of") {
		t.Errorf("cancelling it twice: %s", msg)
	}
}
