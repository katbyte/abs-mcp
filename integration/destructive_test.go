//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
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
