//go:build integration

// Journey 7: a library's whole life, with its contents read back at each
// step, over a scratch folder no fixture library covers and titles no
// provider knows.
package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
)

func TestJourneyLibraryLife(t *testing.T) {
	data := dataDir()
	if data == "" {
		t.Skip("ABS_TEST_DATA is not set")
	}
	admin := adminClient(t)

	const name, renamed = "Zzyzx Life", "Zzyzx Life Renamed"
	// /scratch is the server's view of ABS_TEST_DATA/scratch
	root := filepath.Join(data, "scratch", "zzyzx-life")
	audio, err := os.ReadFile(filepath.Join(data, "fiction", "Isaac Asimov", "Foundation", "01.mp3"))
	if err != nil {
		t.Fatalf("reading a fixture to copy: %v", err)
	}
	addBook := func(title string) {
		dir := filepath.Join(root, "Zzyzx Author", title)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "01.mp3"), audio, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	titles := func(library string) (all, missing []string) {
		for _, it := range rows(t, call(t, "library_items", map[string]any{"library": library, "limit": 50})["items"], "items") {
			title, _ := it["title"].(string)
			all = append(all, title)
			if m, _ := it["missing"].(bool); m {
				missing = append(missing, title)
			}
		}
		slices.Sort(all)
		return all, missing
	}

	addBook("Zzyzx First Book")
	var libID string
	t.Cleanup(func() {
		if libID != "" {
			eventually(t, "deleting the scratch library", func() error {
				if err := admin.DeleteLibrary(ctx, libID); err != nil && !isNotFound(err) {
					return err
				}
				return nil
			})
		}
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("removing %s: %v", root, err)
		}
	})

	t.Run("create", func(t *testing.T) {
		out := call(t, "library_create", map[string]any{"name": name, "folders": []any{"/scratch/zzyzx-life"}, "media_type": "book"})
		lib, _ := out["library"].(map[string]any)
		libID, _ = lib["id"].(string)
		if libID == "" {
			t.Fatalf("no id: %v", out)
		}
		if got := call(t, "library_get", map[string]any{"library": name}); got["id"] != libID || num(t, got["items"], "items") != 0 {
			t.Errorf("library_get after create = %v, want it empty", got)
		}
	})
	if libID == "" {
		t.FailNow()
	}

	t.Run("scan", func(t *testing.T) {
		call(t, "library_scan", map[string]any{"library": name})
		if err := waitForItems(name, 1); err != nil {
			t.Fatal(err)
		}
		if all, _ := titles(name); !slices.Equal(all, []string{"Zzyzx First Book"}) {
			t.Errorf("contents = %v", all)
		}
	})

	t.Run("a folder added, scanned", func(t *testing.T) {
		addBook("Zzyzx Second Book")
		call(t, "library_scan", map[string]any{"library": name})
		if err := waitForItems(name, 2); err != nil {
			t.Fatal(err)
		}
		if all, _ := titles(name); !slices.Equal(all, []string{"Zzyzx First Book", "Zzyzx Second Book"}) {
			t.Errorf("contents = %v", all)
		}
	})

	t.Run("a folder removed, scanned", func(t *testing.T) {
		if err := os.RemoveAll(filepath.Join(root, "Zzyzx Author", "Zzyzx First Book")); err != nil {
			t.Fatal(err)
		}
		call(t, "library_scan", map[string]any{"library": name})
		// a scan keeps the record of a vanished folder and marks it missing
		var missing []string
		for range 30 {
			if _, missing = titles(name); len(missing) > 0 {
				break
			}
			time.Sleep(time.Second)
		}
		if !slices.Equal(missing, []string{"Zzyzx First Book"}) {
			t.Fatalf("missing = %v, want the removed book", missing)
		}
		issues := titlesIn(t, call(t, "audit_issues", map[string]any{"library": name})["findings"], "findings")
		if !slices.Equal(issues, []string{"Zzyzx First Book"}) {
			t.Errorf("audit_issues = %v", issues)
		}
		if removed := num(t, call(t, "library_issues_remove", map[string]any{"library": name})["removed"], "removed"); removed != 1 {
			t.Errorf("removed = %d, want 1", removed)
		}
		if err := waitForItems(name, 1); err != nil {
			t.Fatal(err)
		}
		if all, _ := titles(name); !slices.Equal(all, []string{"Zzyzx Second Book"}) {
			t.Errorf("contents = %v", all)
		}
	})

	t.Run("renamed", func(t *testing.T) {
		call(t, "library_edit", map[string]any{"library": name, "name": renamed})
		if msg := callErr(t, "library_get", map[string]any{"library": name}); msg == "" {
			t.Error("the old name still resolves")
		}
		if got := call(t, "library_get", map[string]any{"library": renamed}); got["id"] != libID || num(t, got["items"], "items") != 1 {
			t.Errorf("by the new name = %v", got)
		}
		if msg := callErr(t, "library_edit", map[string]any{"library": renamed, "name": "Fiction"}); msg == "" {
			t.Error("renaming onto Fiction was not refused")
		}
		if msg := callErr(t, "library_create", map[string]any{"name": renamed, "folders": []any{"/scratch/zzyzx-life"}}); msg == "" {
			t.Error("a second library by the same name was not refused")
		}
	})

	t.Run("a folder that is not there", func(t *testing.T) {
		missingDir := filepath.Join(data, "scratch", "zzyzx-nowhere")
		if msg := callErr(t, "library_create", map[string]any{"name": "Zzyzx Nowhere", "folders": []any{"/scratch/zzyzx-nowhere"}}); msg == "" {
			t.Error("a library over a folder that is not there was created")
		}
		// the server would have made the folder; nothing was sent, so it did not
		if _, err := os.Stat(missingDir); !os.IsNotExist(err) {
			t.Errorf("%s exists after the refusal: %v", missingDir, err)
			_ = os.RemoveAll(missingDir)
		}
	})

	t.Run("deleted", func(t *testing.T) {
		// there is no tool for this: deleting a library is the one step a
		// session is not trusted with
		if err := admin.DeleteLibrary(ctx, libID); err != nil {
			t.Fatal(err)
		}
		for _, l := range rows(t, call(t, "library_list", nil)["libraries"], "libraries") {
			if l["id"] == libID {
				t.Errorf("the deleted library is still listed: %v", l)
			}
		}
		if msg := callErr(t, "library_get", map[string]any{"library": libID}); msg == "" {
			t.Error("the deleted library still resolves by id")
		}
		libID = ""
	})
}

// isNotFound reports whether an SDK error is the server's 404.
func isNotFound(err error) bool { return abs.IsNotFound(err) }
