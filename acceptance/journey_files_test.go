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
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
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

	lib, _ := call(t, "library_create", map[string]any{"name": s.name, "folders": []any{s.folder}})["library"].(map[string]any)
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
	call(t, "user_bookmark_edit", map[string]any{"item": ids[one], "action": "add", "time_s": 0.5, "title": "Zzyzx Delete Mark"})

	t.Run("previewed, nothing goes", func(t *testing.T) {
		out := call(t, "item_delete", map[string]any{"item": ids[one], "delete_files": true})
		if out["would_delete"] != "Zzyzx Book One" || out["deleted"] != nil || out["files_removed"] != false {
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
		if out["deleted"] != "Zzyzx Book One" || out["files_removed"] != true || num(t, out["bookmarks_removed"], "bookmarks_removed") != 1 {
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
		t.Cleanup(func() { call(t, "user_progress_remove", map[string]any{"item": id}) })
		call(t, "user_bookmark_edit", map[string]any{"item": id, "action": "add", "time_s": 0.5, "title": "Zzyzx Moved Mark"})
		t.Cleanup(func() {
			call(t, "user_bookmark_edit", map[string]any{"item": id, "action": "remove", "time_s": 0.5})
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
		progress, _ := call(t, "user_progress_get", map[string]any{"item": id})["progress"].(map[string]any)
		if progress == nil || num(t, progress["percent"], "percent") != 50 {
			t.Errorf("progress = %v, want the 50%% set before the move", progress)
		}
		if marks := titlesIn(t, call(t, "user_bookmarks", map[string]any{"item": id})["bookmarks"], "bookmarks"); !slices.Equal(marks, []string{"Zzyzx Moved Mark"}) {
			t.Errorf("bookmarks = %v", marks)
		}
		if held := valuesIn(t, call(t, "collection_get", map[string]any{"collection": "Zzyzx Moved Shelf"})["items"], "items", "id"); !slices.Equal(held, []string{id}) {
			t.Errorf("the collection holds %v, want the moved book", held)
		}
		var queued []string
		for _, e := range rows(t, call(t, "playlist_get", map[string]any{"playlist": "Zzyzx Moved Queue"})["entries"], "entries") {
			it, _ := e["item"].(map[string]any)
			queued = append(queued, text(it["id"]))
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
			return missing == true && tracks(inside) == 2, fmt.Sprintf("the outside copy missing %v, the series copy has %d tracks", missing, tracks(inside))
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
		call(t, "item_batch_edit", map[string]any{
			"library": s.name, "items": both, "tags": []any{"zzyzx-keep", "zzyzx-drop"},
			"year": "1999", "publisher": "Zzyzx Press", "language": "English", "add_series": []any{"Zzyzx Rescan Omnibus"},
		})
		for _, id := range []string{book, other} {
			if series := strs(t, call(t, "item_get", map[string]any{"item": id})["series"], "series"); !slices.Contains(series, "Zzyzx Rescan Omnibus") || len(series) != 2 {
				t.Errorf("series after add_series = %v, want the omnibus beside the book's own", series)
			}
		}
		out := call(t, "item_batch_edit", map[string]any{"library": s.name, "items": both, "remove_tags": []any{"zzyzx-drop"}, "remove_series": []any{"Zzyzx Rescan Omnibus"}})
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
			if it["missing"] == true {
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
		progress, _ := call(t, "user_progress_get", map[string]any{"item": kept})["progress"].(map[string]any)
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

	s := newDiskShelf(t, "Zzyzx Folder Shelf", "zzyzx-folders")
	s.write(t, first+"/01.mp3", second+"/01.mp3", third+"/01.mp3", institute+"/01.mp3", sisters+"/01.mp3", vanya+"/01.mp3")
	s.open(t, 6)
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
}
