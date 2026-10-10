//go:build integration

package integration

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// The delete paths, each against something the test creates for the purpose,
// so nothing the other tests rely on is touched. They run last by file name.

// DeleteItem and RemoveIssues both need an item that can be thrown away, so
// they share one: a folder copied in, scanned, then taken away again.
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
	scratch := must(client.CreateLibrary(ctx, abs.LibraryCreate{
		Name: "SDK Scratch", MediaType: "book", Folders: []abs.Folder{{FullPath: "/nonfiction"}},
	}))
	id := scratch.ID
	t.Cleanup(func() {
		_ = client.DeleteLibrary(t.Context(), id)
		_ = os.RemoveAll(filepath.Join(data, "nonfiction", "SDK Author"))
	})

	if err := client.ScanLibrary(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) && len(ids) < len(dirs) {
		res, err := client.Items(ctx, id, abs.ItemsOptions{Limit: 200, Minified: true})
		if err == nil {
			for i := range res.Results {
				if _, want := dirs[res.Results[i].Title()]; want {
					ids[res.Results[i].Title()] = res.Results[i].ID
				}
			}
		}
		if len(ids) < len(dirs) {
			time.Sleep(2 * time.Second)
		}
	}
	if len(ids) < len(dirs) {
		t.Fatalf("the scan only found %d of the %d throwaway books", len(ids), len(dirs))
	}

	// DeleteItem removes the record; deleteFiles false leaves the folder
	if err := client.DeleteItem(ctx, ids["SDK Disposable"], false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Item(ctx, ids["SDK Disposable"]); !abs.IsNotFound(err) {
		t.Errorf("the deleted item is still there: %v", err)
	}

	// RemoveIssues sweeps up records whose folder has gone
	if err := os.RemoveAll(dirs["SDK Abandonable"]); err != nil {
		t.Fatal(err)
	}
	if err := client.ScanLibrary(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		it, err := client.Item(ctx, ids["SDK Abandonable"])
		if err == nil && it.IsMissing {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err := client.RemoveIssues(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Item(ctx, ids["SDK Abandonable"]); !abs.IsNotFound(err) {
		t.Errorf("the broken record survived RemoveIssues: %v", err)
	}
}

// DeleteAuthor unlinks the books rather than removing them, so this makes an
// author of its own to delete.
func TestDeleteAuthor(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	original := must(client.Item(ctx, item.ID)).Media.Metadata.AuthorDisplay()

	// giving the book a new author creates that author record
	if _, err := client.UpdateMedia(ctx, item.ID, abs.MediaUpdate{
		Metadata: &abs.MetadataUpdate{Authors: []abs.NameRef{{Name: "SDK Doomed Author"}}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.UpdateMedia(t.Context(), item.ID, abs.MediaUpdate{
			Metadata: &abs.MetadataUpdate{Authors: []abs.NameRef{{Name: original}}},
		})
	})

	authors, _, err := client.Authors(ctx, id, abs.ListOptions{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var doomed string
	for i := range authors {
		if authors[i].Name == "SDK Doomed Author" {
			doomed = authors[i].ID
		}
	}
	if doomed == "" {
		t.Fatal("the new author was not created")
	}

	if err := client.DeleteAuthor(ctx, doomed); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Author(ctx, doomed, false); !abs.IsNotFound(err) {
		t.Errorf("the deleted author is still there: %v", err)
	}
	// the book survives
	if _, err := client.Item(ctx, item.ID); err != nil {
		t.Errorf("the book went with the author: %v", err)
	}
}

// RemoveNarrator drops a narrator from every book carrying them.
func TestRemoveNarrator(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	if _, err := client.UpdateMedia(ctx, item.ID, abs.MediaUpdate{
		Metadata: &abs.MetadataUpdate{Narrators: []string{"SDK Doomed Narrator"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.UpdateMedia(t.Context(), item.ID, abs.MediaUpdate{
			Metadata: &abs.MetadataUpdate{Narrators: []string{"SDK Reader"}},
		})
	})

	n, err := client.RemoveNarrator(ctx, id, "SDK Doomed Narrator")
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("RemoveNarrator reported no items changed")
	}

	for _, row := range must(client.Narrators(ctx, id)) {
		if row.Name == "SDK Doomed Narrator" {
			t.Error("the narrator survived removal")
		}
	}
}

// EmbedMetadata rewrites the audio tags in the background; that it starts is
// all this layer can assert without waiting on a task.
func TestEmbedMetadata(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	if err := client.EmbedMetadata(ctx, item.ID, true, false); err != nil {
		t.Errorf("EmbedMetadata: %v", err)
	}
}

// SetCoverFromFile picks an image already inside the item's folder, so the
// test puts one there first.
func TestSetCoverFromFile(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	data := os.Getenv("ABS_TEST_DATA")
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	full := must(client.Item(ctx, item.ID))
	folder := filepath.Join(data, "fiction", filepath.FromSlash(full.RelPath))
	cover := filepath.Join(folder, "cover.jpg")

	if err := os.WriteFile(cover, tinyJPEG(), 0o666); err != nil {
		t.Skipf("cannot write a cover into %s: %v", folder, err)
	}
	t.Cleanup(func() {
		_ = os.Remove(cover)
		_ = client.RemoveCover(t.Context(), item.ID)
	})

	if _, err := client.ScanItem(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.SetCoverFromFile(ctx, item.ID, filepath.ToSlash(filepath.Join(full.Path, "cover.jpg"))); err != nil {
		t.Fatalf("SetCoverFromFile: %v", err)
	}

	w, h, err := client.CoverSize(ctx, item.ID)
	if err != nil {
		t.Fatalf("CoverSize after setting one: %v", err)
	}
	if w == 0 || h == 0 {
		t.Errorf("cover is %dx%d", w, h)
	}
}

// UploadCover sends the image itself, and CoverFile hands back the file the
// server kept: the bytes that were sent, where Cover answers a resized copy.
func TestUploadCoverAndReadItBack(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	t.Cleanup(func() { _ = client.RemoveCover(t.Context(), item.ID) })
	if err := client.UploadCover(ctx, item.ID, "cover.jpg", bytes.NewReader(tinyJPEG())); err != nil {
		t.Fatalf("UploadCover: %v", err)
	}
	body := must(client.CoverFile(ctx, item.ID))
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || !bytes.Equal(got, tinyJPEG()) {
		t.Errorf("CoverFile = %d bytes, %v; want the %d that were sent", len(got), err, len(tinyJPEG()))
	}

	if err := client.RemoveCover(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoverFile(ctx, item.ID); !errors.Is(err, abs.ErrNoCover) {
		t.Errorf("CoverFile with no cover = %v, want ErrNoCover", err)
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

// BatchDelete removes several records at once; it runs against throwaway
// copies in a library of its own.
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

	scratch := must(client.CreateLibrary(ctx, abs.LibraryCreate{
		Name: "SDK Batch Scratch", MediaType: "book",
		Folders: []abs.Folder{{FullPath: "/nonfiction"}},
	}))
	t.Cleanup(func() {
		_ = client.DeleteLibrary(t.Context(), scratch.ID)
		_ = os.RemoveAll(filepath.Join(data, "nonfiction", "SDK Batch"))
	})

	if err := client.ScanLibrary(ctx, scratch.ID, false); err != nil {
		t.Fatal(err)
	}
	var ids []string
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) && len(ids) < 2 {
		res, err := client.Items(ctx, scratch.ID, abs.ItemsOptions{Limit: 100, Minified: true})
		if err == nil {
			ids = nil
			for i := range res.Results {
				if strings.HasPrefix(res.Results[i].Title(), "SDK Batch") {
					ids = append(ids, res.Results[i].ID)
				}
			}
		}
		if len(ids) < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	if len(ids) < 2 {
		t.Fatalf("the scan found %d of the 2 throwaway books", len(ids))
	}

	if err := client.BatchDelete(ctx, ids); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := client.Item(ctx, id); !abs.IsNotFound(err) {
			t.Errorf("item %s survived BatchDelete: %v", id, err)
		}
	}
}

// DeleteItemFile takes one file off a book and off the disk, and answers a
// plain OK: the client once decoded that as the item, so every delete that
// worked came back as an error. PathExists is what tells the file is gone,
// and a gone file inside an item's folder still answers exists, with the
// item's title.
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
	scratch := must(client.CreateLibrary(ctx, abs.LibraryCreate{
		Name: "SDK File Delete", MediaType: "book", Folders: []abs.Folder{{FullPath: "/scratch/sdk-file-delete"}},
	}))
	t.Cleanup(func() {
		_ = client.DeleteLibrary(t.Context(), scratch.ID)
		_ = os.RemoveAll(root)
	})
	if err := client.ScanLibrary(ctx, scratch.ID, false); err != nil {
		t.Fatal(err)
	}
	var item *abs.Item
	for deadline := time.Now().Add(2 * time.Minute); item == nil && time.Now().Before(deadline); {
		if res, err := client.Items(ctx, scratch.ID, abs.ItemsOptions{Limit: 10}); err == nil && len(res.Results) == 1 {
			item = must(client.Item(ctx, res.Results[0].ID))
		} else {
			time.Sleep(2 * time.Second)
		}
	}
	if item == nil {
		t.Fatal("the scan never found the throwaway book")
	}
	var notes string
	for _, f := range item.LibraryFiles {
		if f.Metadata.Filename == "notes.txt" {
			notes = f.Ino
		}
	}
	if notes == "" {
		t.Fatalf("the scan did not list notes.txt: %+v", item.LibraryFiles)
	}

	if err := client.DeleteItemFile(ctx, item.ID, "no-such-file"); err == nil {
		t.Error("DeleteItemFile accepted a nonexistent file id")
	}
	if err := client.DeleteItemFile(ctx, item.ID, notes); err != nil {
		t.Fatalf("DeleteItemFile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); !os.IsNotExist(err) {
		t.Errorf("notes.txt is still on disk: %v", err)
	}
	for _, f := range must(client.Item(ctx, item.ID)).LibraryFiles {
		if f.Ino == notes {
			t.Error("the item still lists notes.txt")
		}
	}

	for file, want := range map[string]string{"01.mp3": "", "notes.txt": "SDK Trimmable"} {
		exists, holder, err := client.PathExists(ctx, "/scratch/sdk-file-delete", "SDK Author/SDK Trimmable/"+file)
		if err != nil {
			t.Fatalf("PathExists: %v", err)
		}
		if !exists || holder != want {
			t.Errorf("PathExists(%s) = %v naming %q; want exists, naming %q", file, exists, holder, want)
		}
	}
}
