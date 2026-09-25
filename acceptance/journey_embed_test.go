//go:build integration

// Journey 11: what item_embed_metadata writes is what a scan reads. A book is
// edited and given chapters, embedded, and then its record is thrown away and
// scanned back from the file alone: every field and chapter the journey set
// has to come back, because the tags are all that is left to read.
package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// unembedded reports whether audit_unembedded lists a book, with its detail.
func unembedded(t *testing.T, library, id string) (string, bool) {
	t.Helper()

	for _, f := range rows(t, call(t, "audit_unembedded", map[string]any{"library": library})["findings"], "findings") {
		if f["id"] == id {
			return text(f["detail"]), true
		}
	}
	return "", false
}

// embed runs item_embed_metadata, which waits for the embed and reads the
// tags back, and checks the audit agrees with what it said.
func embed(t *testing.T, library, id string, args map[string]any) {
	t.Helper()

	out := call(t, "item_embed_metadata", args)
	if embedded, _ := out["embedded"].(bool); !embedded || out["running"] != nil || out["rescan"] == nil {
		t.Fatalf("item_embed_metadata = %v, want it embedded and read back", out)
	}
	if detail, listed := unembedded(t, library, id); listed {
		t.Errorf("item_embed_metadata says embedded, audit_unembedded says: %s", detail)
	}
}

func TestJourneyEmbeddedIsWhatAScanReads(t *testing.T) {
	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	admin := adminClient(t)

	const library = "Zzyzx Embed"
	root := filepath.Join(data, "scratch", "zzyzx-embed")
	book := filepath.Join(root, "Zzyzx Folder Author", "Zzyzx Folder Title")
	audio, err := os.ReadFile(filepath.Join(data, "fiction", "Isaac Asimov", "Foundation", "01.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(book, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(book, "01.mp3"), audio, 0o666); err != nil {
		t.Fatal(err)
	}

	lib := call(t, "library_create", map[string]any{"name": library, "folders": []any{"/scratch/zzyzx-embed"}})
	libID := text(lib["library"].(map[string]any)["id"])
	t.Cleanup(func() {
		eventually(t, "deleting the embed library", func() error {
			if err := admin.DeleteLibrary(ctx, libID); err != nil && !isNotFound(err) {
				return err
			}
			return nil
		})
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("removing %s: %v", root, err)
		}
	})
	call(t, "library_scan", map[string]any{"library": library})
	if err := waitForItems(library, 1); err != nil {
		t.Fatal(err)
	}
	waitIdle(t)
	id := text(rows(t, call(t, "library_items", map[string]any{"library": library})["items"], "items")[0]["id"])

	want := map[string]any{
		"title": "Zzyzx Embedded Title", "author": "Zzyzx Embedded Author", "narrator": "Zzyzx Embedded Reader",
		"year": "1999", "publisher": "Zzyzx Press",
	}
	wantSeries, wantGenres := []string{"Zzyzx Embedded Saga #2"}, []string{"Zzyzx Genre"}
	wantChapters := []string{"Zzyzx Opening", "Zzyzx Closing"}
	check := func(t *testing.T, id string) {
		t.Helper()

		got := call(t, "item_get", map[string]any{"item": id, "chapters": true})
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %v, want %v", k, got[k], v)
			}
		}
		if series := strs(t, got["series"], "series"); !slices.Equal(series, wantSeries) {
			t.Errorf("series = %v, want %v", series, wantSeries)
		}
		if genres := strs(t, got["genres"], "genres"); !slices.Equal(genres, wantGenres) {
			t.Errorf("genres = %v, want %v", genres, wantGenres)
		}
		if chapters := titlesIn(t, got["chapter_list"], "chapter_list"); !slices.Equal(chapters, wantChapters) {
			t.Errorf("chapters = %v, want %v", chapters, wantChapters)
		}
	}

	t.Run("edited, and due an embed", func(t *testing.T) {
		call(t, "item_edit", map[string]any{
			"item": id, "title": want["title"], "authors": []any{want["author"]}, "narrators": []any{want["narrator"]},
			"series": toAny(wantSeries), "genres": toAny(wantGenres), "year": want["year"], "publisher": want["publisher"],
		})
		call(t, "item_chapters_set", map[string]any{"item": id, "chapters": []any{
			map[string]any{"title": wantChapters[0], "start_s": 0}, map[string]any{"title": wantChapters[1], "start_s": 0.5},
		}})
		check(t, id)
		if detail, listed := unembedded(t, library, id); !listed || !strings.Contains(detail, "no tags") {
			t.Errorf("audit_unembedded = %q, %v; want the book, never embedded", detail, listed)
		}
	})

	t.Run("embedded, with a backup", func(t *testing.T) {
		embed(t, library, id, map[string]any{"item": id, "backup": true})
		if _, err := os.Stat(filepath.Join(data, "metadata", "cache", "items", id, "01.mp3")); err != nil {
			t.Errorf("the backup of the original file: %v", err)
		}
		// another rescan of the embedded file changes nothing the journey set
		if result := text(call(t, "item_rescan", map[string]any{"item": id})["result"]); result != "UPTODATE" {
			t.Errorf("a rescan straight after the embed's own = %s, want UPTODATE", result)
		}
		check(t, id)
	})

	t.Run("edited again, stale, embedded again", func(t *testing.T) {
		want["publisher"] = "Zzyzx Second Press"
		call(t, "item_edit", map[string]any{"item": id, "publisher": want["publisher"]})
		if detail, listed := unembedded(t, library, id); !listed || !strings.Contains(detail, "publisher") {
			t.Errorf("audit_unembedded = %q, %v; want the book, its publisher stale", detail, listed)
		}
		embed(t, library, id, map[string]any{"item": id})
		check(t, id)
	})

	t.Run("the record thrown away, scanned back from the file", func(t *testing.T) {
		call(t, "item_delete", map[string]any{"confirm": true, "item": id})
		if err := waitForItems(library, 0); err != nil {
			t.Fatal(err)
		}
		call(t, "library_scan", map[string]any{"library": library})
		if err := waitForItems(library, 1); err != nil {
			t.Fatal(err)
		}
		waitIdle(t)
		again := text(rows(t, call(t, "library_items", map[string]any{"library": library})["items"], "items")[0]["id"])
		if again == id {
			t.Fatalf("the scan kept the deleted record's id %s", id)
		}
		check(t, again)
		if _, listed := unembedded(t, library, again); listed {
			t.Error("the book scanned from its embedded file is due an embed")
		}
	})
}
