//go:build acceptance

package abs_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// The delete paths, each against something the test creates for the purpose,
// so nothing the other tests rely on is touched. They run last by file name.

// Deleting an item and sweeping away the items with issues both need an item that can be thrown away, so they share one: a folder copied in,
// scanned, then taken away again.
func TestDeleteItemAndRemoveIssues(t *testing.T) {
	ctx := skipUnlessLive(t)

	data := os.Getenv("ABS_TEST_DATA")
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	source := filepath.Join(data, "fiction", "Isaac Asimov", "Foundation", "01.mp3")
	audio, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("reading a fixture to copy: %v", err)
	}

	// a library of its own over the otherwise unused /nonfiction mount, so
	// nothing here can disturb the fixture the rest of the suite asserts on
	dirs := map[string]string{
		"SDK Disposable":  filepath.Join(data, "nonfiction", "SDK Author", "SDK Disposable"),
		"SDK Abandonable": filepath.Join(data, "nonfiction", "SDK Author", "SDK Abandonable"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "01.mp3"), audio, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	id := must(client.LibraryCreate(ctx, abs.LibraryCreateRequest{Name: "SDK Scratch", MediaType: "book", Folders: []abs.LibraryFolder{{FullPath: "/nonfiction"}}})).Id
	t.Cleanup(func() {
		_ = errorOf(client.LibraryDelete(afterwards(t), id))
		_ = os.RemoveAll(filepath.Join(data, "nonfiction", "SDK Author"))
	})

	if err := errorOf(client.LibraryScan(ctx, id)); err != nil {
		t.Fatal(err)
	}
	ids := scannedItems(t, id, len(dirs), func(title string) bool { _, want := dirs[title]; return want })

	// deleting the record without hard leaves the folder
	if err := errorOf(client.LibraryItemDelete(ctx, ids["SDK Disposable"])); err != nil {
		t.Fatal(err)
	}
	if _, err := client.LibraryItemFindOne(ctx, ids["SDK Disposable"]); !abs.IsNotFound(err) {
		t.Errorf("the deleted item is still there: %v", err)
	}

	// the sweep for issues takes away records whose folder has gone
	if err := os.RemoveAll(dirs["SDK Abandonable"]); err != nil {
		t.Fatal(err)
	}
	if err := errorOf(client.LibraryScan(ctx, id)); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		it, err := record(client.LibraryItemFindOne(ctx, ids["SDK Abandonable"]))
		if err == nil && it.IsMissing {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err := errorOf(client.LibraryRemoveLibraryItemsWithIssues(ctx, id)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.LibraryItemFindOne(ctx, ids["SDK Abandonable"]); !abs.IsNotFound(err) {
		t.Errorf("the broken record survived the sweep for issues: %v", err)
	}
}

// Deleting an author unlinks the books rather than removing them, so this makes an author of its own to delete.
func TestDeleteAuthor(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]
	original := whole(ctx, item.Id).Media.Metadata.AuthorDisplay()

	// giving the book a new author creates that author record
	if err := errorOf(client.LibraryItemUpdateMedia(ctx, item.Id, abs.MediaUpdate{Metadata: &abs.MediaUpdateMetadata{Authors: []abs.AuthorMinimal{{Name: "SDK Doomed Author"}}}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = errorOf(client.LibraryItemUpdateMedia(afterwards(t), item.Id, abs.MediaUpdate{Metadata: &abs.MediaUpdateMetadata{Authors: []abs.AuthorMinimal{{Name: original}}}}))
	})

	var doomed abs.AuthorID
	for _, a := range firstAuthors(ctx, id, 200) {
		if a.Name == "SDK Doomed Author" {
			doomed = a.Id
		}
	}
	if doomed == "" {
		t.Fatal("the new author was not created")
	}

	if err := errorOf(client.AuthorDelete(ctx, doomed)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AuthorFindOne(ctx, doomed); !abs.IsNotFound(err) {
		t.Errorf("the deleted author is still there: %v", err)
	}
	// the book survives
	if _, err := client.LibraryItemFindOne(ctx, item.Id); err != nil {
		t.Errorf("the book went with the author: %v", err)
	}
}

// Removing a narrator drops them from every book carrying them.
func TestRemoveNarrator(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]
	if err := errorOf(client.LibraryItemUpdateMedia(ctx, item.Id, abs.MediaUpdate{Metadata: &abs.MediaUpdateMetadata{Narrators: []string{"SDK Doomed Narrator"}}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = errorOf(client.LibraryItemUpdateMedia(afterwards(t), item.Id, abs.MediaUpdate{Metadata: &abs.MediaUpdateMetadata{Narrators: []string{"SDK Reader"}}}))
	})

	removed, err := record(client.LibraryRemoveNarrator(ctx, id, abs.PathName("SDK Doomed Narrator")))
	if err != nil {
		t.Fatal(err)
	}
	if removed.Updated == 0 {
		t.Error("removing the narrator reported no items changed")
	}

	doomed := func(n abs.LibraryGetNarratorsAnswerNarrators) bool { return n.Name == "SDK Doomed Narrator" }
	if slices.ContainsFunc(must(client.LibraryGetNarrators(ctx, id)).Narrators, doomed) {
		t.Error("the narrator survived removal")
	}
}

// Embedding rewrites the audio tags in the background; that it starts is all this layer can assert without waiting on a task.
func TestEmbedMetadata(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]
	if err := errorOf(client.ToolsEmbedAudioFileMetadata(ctx, item.Id, abs.ToolsEmbedAudioFileMetadataOptions{ForceEmbedChapters: "1"})); err != nil {
		t.Errorf("embedding the metadata: %v", err)
	}
}

// Setting the cover from a file picks an image already inside the item's folder, so the test puts one there first.
func TestSetCoverFromFile(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	data := os.Getenv("ABS_TEST_DATA")
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}

	full := whole(ctx, firstItems(ctx, id, 1)[0].Id)
	folder := filepath.Join(data, "fiction", filepath.FromSlash(full.RelPath))
	cover := filepath.Join(folder, "cover.jpg")

	if err := os.WriteFile(cover, tinyJPEG(), 0o666); err != nil {
		t.Skipf("cannot write a cover into %s: %v", folder, err)
	}
	t.Cleanup(func() {
		_ = os.Remove(cover)
		_ = errorOf(client.LibraryItemRemoveCover(afterwards(t), full.Id))
	})

	if err := errorOf(client.LibraryItemScan(ctx, full.Id)); err != nil {
		t.Fatal(err)
	}
	if err := errorOf(client.LibraryItemUpdateCover(ctx, full.Id, abs.LibraryItemUpdateCoverRequest{Cover: filepath.ToSlash(filepath.Join(full.Path, "cover.jpg"))})); err != nil {
		t.Fatalf("setting the cover from a file: %v", err)
	}

	w, h, err := coverSize(ctx, full.Id)
	if err != nil {
		t.Fatalf("measuring the cover after setting one: %v", err)
	}
	if w == 0 || h == 0 {
		t.Errorf("cover is %dx%d", w, h)
	}
}

// Uploading a cover sends the image itself, and the cover asked for raw is the file the server kept: the bytes that were sent, where the
// cover otherwise is a resized copy.
func TestUploadCoverAndReadItBack(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := firstItems(ctx, id, 1)[0]
	t.Cleanup(func() { _ = errorOf(client.LibraryItemRemoveCover(afterwards(t), item.Id)) })
	body, kind, err := form("cover", "cover.jpg", tinyJPEG(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := errorOf(client.LibraryItemUploadCover(ctx, item.Id, body, kind)); err != nil {
		t.Fatalf("uploading a cover: %v", err)
	}

	got, err := client.LibraryItemGetCover(ctx, item.Id, abs.LibraryItemGetCoverOptions{Raw: "1"})
	if err != nil {
		t.Fatalf("reading the cover back: %v", err)
	}
	if kept, err := got.Bytes(1 << 20); err != nil || !bytes.Equal(kept, tinyJPEG()) {
		t.Errorf("the cover read back = %d bytes, %v; want the %d that were sent", len(kept), err, len(tinyJPEG()))
	}

	if err := errorOf(client.LibraryItemRemoveCover(ctx, item.Id)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.LibraryItemGetCover(ctx, item.Id, abs.LibraryItemGetCoverOptions{Raw: "1"}); !abs.IsNotFound(err) {
		t.Errorf("the cover of an item with none = %v, want a 404", err)
	}
}

// tinyJPEG is a 1x1 baseline JPEG, enough for the server to accept and for
// image.DecodeConfig to read a size out of.
func tinyJPEG() []byte {
	return []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0x01, 0x00, 0x00, 0x01,
		0x00, 0x01, 0x00, 0x00, 0xFF, 0xDB, 0x00, 0x43, 0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x01, 0x00, 0x01, 0x01, 0x01, 0x11, 0x00,
		0xFF, 0xC4, 0x00, 0x14, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x03,
		0xFF, 0xC4, 0x00, 0x14, 0x10, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00, 0x37, 0xFF, 0xD9,
	}
}

// Deleting several records at once; it runs against throwaway copies in a library of its own.
func TestBatchDelete(t *testing.T) {
	ctx := skipUnlessLive(t)

	data := os.Getenv("ABS_TEST_DATA")
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	audio, err := os.ReadFile(filepath.Join(data, "fiction", "Isaac Asimov", "Foundation", "01.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"SDK Batch One", "SDK Batch Two"} {
		dir := filepath.Join(data, "nonfiction", "SDK Batch", title)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "01.mp3"), audio, 0o666); err != nil {
			t.Fatal(err)
		}
	}

	scratch := must(client.LibraryCreate(ctx, abs.LibraryCreateRequest{Name: "SDK Batch Scratch", MediaType: "book", Folders: []abs.LibraryFolder{{FullPath: "/nonfiction"}}}))
	t.Cleanup(func() {
		_ = errorOf(client.LibraryDelete(afterwards(t), scratch.Id))
		_ = os.RemoveAll(filepath.Join(data, "nonfiction", "SDK Batch"))
	})

	if err := errorOf(client.LibraryScan(ctx, scratch.Id)); err != nil {
		t.Fatal(err)
	}
	ids := make([]abs.LibraryItemID, 0, 2)
	for _, id := range scannedItems(t, scratch.Id, 2, func(title string) bool { return strings.HasPrefix(title, "SDK Batch") }) {
		ids = append(ids, id)
	}

	if err := errorOf(client.LibraryItemBatchDelete(ctx, abs.LibraryItemBatchDeleteRequest{LibraryItemIds: ids})); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := client.LibraryItemFindOne(ctx, id); !abs.IsNotFound(err) {
			t.Errorf("item %s survived the batch delete: %v", id, err)
		}
	}
}

// Deleting one file takes it off a book and off the disk, and answers a plain OK: the client once decoded that as the item, so every delete
// that worked came back as an error. The check for a path is what tells the file is gone, and a gone file inside an item's folder still
// answers exists, with the item's title.
func TestDeleteItemFile(t *testing.T) {
	ctx := skipUnlessLive(t)

	data := os.Getenv("ABS_TEST_DATA")
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	audio, err := os.ReadFile(filepath.Join(data, "fiction", "Isaac Asimov", "Foundation", "01.mp3"))
	if err != nil {
		t.Fatalf("reading a fixture to copy: %v", err)
	}
	root := filepath.Join(data, "scratch", "sdk-file-delete")
	dir := filepath.Join(root, "SDK Author", "SDK Trimmable")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"01.mp3": audio, "notes.txt": []byte("notes\n")} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	scratch := must(client.LibraryCreate(ctx, abs.LibraryCreateRequest{Name: "SDK File Delete", MediaType: "book", Folders: []abs.LibraryFolder{{FullPath: "/scratch/sdk-file-delete"}}}))
	t.Cleanup(func() {
		_ = errorOf(client.LibraryDelete(afterwards(t), scratch.Id))
		_ = os.RemoveAll(root)
	})
	if err := errorOf(client.LibraryScan(ctx, scratch.Id)); err != nil {
		t.Fatal(err)
	}
	item := whole(ctx, scannedItems(t, scratch.Id, 1, func(title string) bool { return title == "SDK Trimmable" })["SDK Trimmable"])

	var notes string
	for _, f := range item.LibraryFiles {
		if f.Metadata != nil && f.Metadata.Filename == "notes.txt" {
			notes = f.Ino
		}
	}
	if notes == "" {
		t.Fatalf("the scan did not list notes.txt: %+v", item.LibraryFiles)
	}

	if err := errorOf(client.LibraryItemDeleteLibraryFile(ctx, item.Id, "no-such-file")); err == nil {
		t.Error("deleting a file the item does not have went through")
	}
	if err := errorOf(client.LibraryItemDeleteLibraryFile(ctx, item.Id, notes)); err != nil {
		t.Fatalf("deleting the file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); !os.IsNotExist(err) {
		t.Errorf("notes.txt is still on disk: %v", err)
	}
	for _, f := range whole(ctx, item.Id).LibraryFiles {
		if f.Ino == notes {
			t.Error("the item still lists notes.txt")
		}
	}

	for file, want := range map[string]string{"01.mp3": "", "notes.txt": "SDK Trimmable"} {
		got, err := record(client.FileSystemCheckPathExists(ctx, abs.FileSystemCheckPathExistsRequest{FolderPath: "/scratch/sdk-file-delete", Directory: "SDK Author/SDK Trimmable/" + file}))
		if err != nil {
			t.Fatalf("checking a path: %v", err)
		}
		if !got.Exists || got.LibraryItemTitle != want {
			t.Errorf("the path %s = %v naming %q; want exists, naming %q", file, got.Exists, got.LibraryItemTitle, want)
		}
	}
}

// Merging a book's audio into one m4b is a task the server runs in the background, and a merge that finishes moves the files it merged out of
// the book's folder. A one-second file can merge before the cancel arrives, so it runs on a throwaway book in a library of its own, and the
// cancel is taken either way: the merge stopped, or already done.
func TestEncodeM4B(t *testing.T) {
	ctx := skipUnlessLive(t)

	data := os.Getenv("ABS_TEST_DATA")
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	audio, err := os.ReadFile(filepath.Join(data, "fiction", "Isaac Asimov", "Foundation", "01.mp3"))
	if err != nil {
		t.Fatalf("reading a fixture to copy: %v", err)
	}
	root := filepath.Join(data, "scratch", "sdk-encode")
	dir := filepath.Join(root, "SDK Author", "SDK Mergeable")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01.mp3"), audio, 0o666); err != nil {
		t.Fatal(err)
	}
	scratch := must(client.LibraryCreate(ctx, abs.LibraryCreateRequest{Name: "SDK Encode", MediaType: "book", Folders: []abs.LibraryFolder{{FullPath: "/scratch/sdk-encode"}}}))
	t.Cleanup(func() {
		_ = errorOf(client.LibraryDelete(afterwards(t), scratch.Id))
		_ = os.RemoveAll(root)
	})
	if err := errorOf(client.LibraryScan(ctx, scratch.Id)); err != nil {
		t.Fatal(err)
	}
	id := scannedItems(t, scratch.Id, 1, func(title string) bool { return title == "SDK Mergeable" })["SDK Mergeable"]

	if err := errorOf(client.ToolsEncodeM4b(ctx, id)); err != nil {
		t.Fatalf("starting the merge: %v", err)
	}
	if err := errorOf(client.ToolsCancelM4bEncode(ctx, id)); err != nil && !abs.IsNotFound(err) {
		t.Errorf("cancelling the merge = %v, want it stopped, or a 404 for one already done", err)
	}

	// the server ends the task either way, which the clean-up waits for rather than take the folder from under it
	merging := func(task abs.MiscGetTasksAnswerTasks) bool {
		return task.Action == "encode-m4b" && !task.IsFinished && task.Data != nil && task.Data.LibraryItemId == id
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if !slices.ContainsFunc(must(client.MiscGetTasks(ctx)).Tasks, merging) {
			return
		}
	}
	t.Error("the merge was still running 30s after it was cancelled")
}

// scannedItems waits for a library's scan to find n items whose titles keep takes, and answers their ids by title.
func scannedItems(t *testing.T, lib abs.LibraryID, n int, keep func(title string) bool) map[string]abs.LibraryItemID {
	t.Helper()

	found := map[string]abs.LibraryItemID{}
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		res, err := record(client.LibraryGetLibraryItems(t.Context(), lib, abs.LibraryGetLibraryItemsOptions{Limit: new(200), Minified: "1"}))
		if err != nil {
			continue
		}
		found = map[string]abs.LibraryItemID{}
		for _, it := range res.Results {
			if keep(it.Title()) {
				found[it.Title()] = it.Id
			}
		}
		if len(found) >= n {
			return found
		}
	}
	t.Fatalf("the scan found %d of the %d books it was given; %s", len(found), n, scanned(t, lib))

	return nil
}
