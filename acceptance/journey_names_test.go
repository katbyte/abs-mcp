//go:build integration

package acceptance

import (
	"testing"
)

// A session clearing a stray "Ph.D." off the narrators, on a shelf where one
// narrator's name holds a comma. The listing joins a book's narrators with
// ", ", so "Zzyzx Jane Doe, Ph.D." read as a narrator and a "Ph.D." fragment
// on every book she reads, and the metadata_rename remove the audit suggested
// matched nothing there. Now the fragment is found on the books that really
// carry it, both where the joined string reads one way and where the shelf
// also holds "Zzyzx Jane Doe" alone so that only the book's record can say,
// and removing it leaves her name whole. It would catch the name split
// again, and a fix that takes her name with the fragment.
func TestJourneyANarratorWithACommaInTheName(t *testing.T) {
	const author = "Zzyzx Comma Author"
	s := newDiskShelf(t, "Zzyzx Comma Shelf", "zzyzx-comma")
	books := map[string][]any{
		author + "/Zzyzx Comma One":   {"Zzyzx Jane Doe, Ph.D."},
		author + "/Zzyzx Comma Two":   {"Zzyzx Jane Doe, Ph.D.", "Zzyzx Jim Reader"},
		author + "/Zzyzx Comma Three": {"Zzyzx Kim Reader", "Ph.D."},
		author + "/Zzyzx Comma Four":  {"Zzyzx Jane Doe", "Ph.D."}, // reads the same joined as One
	}
	for path := range books {
		s.write(t, path+"/01.mp3")
	}
	s.open(t, len(books))
	ids := s.ids(t)
	for path, narrators := range books {
		call(t, "item_edit", map[string]any{"item": ids[path], "narrators": narrators})
	}

	fragment := func(t *testing.T) (items int, found bool) {
		t.Helper()
		for _, g := range rows(t, call(t, "audit_narrators", map[string]any{"library": s.name})["names"], "names") {
			for _, sp := range rows(t, g["spellings"], "spellings") {
				if sp["value"] == "Ph.D." && g["kind"] == "fragment" {
					return num(t, sp["items"], "items"), true
				}
			}
		}
		return 0, false
	}

	if n, found := fragment(t); !found || n != 2 {
		t.Fatalf("audit_narrators finds the Ph.D. fragment on %d books (found %v), want the two that carry it, not the four whose joined names hold it", n, found)
	}

	out := call(t, "metadata_rename", map[string]any{"library": s.name, "field": "narrators", "from": "Ph.D.", "remove": true, "confirm": true})
	if n := num(t, out["items_updated"], "items_updated"); n != 2 {
		t.Errorf("metadata_rename remove = %v, want the two books changed", out)
	}
	if _, found := fragment(t); found {
		t.Error("audit_narrators still finds the fragment once it is removed")
	}
	for path, want := range map[string]string{
		author + "/Zzyzx Comma One":   "Zzyzx Jane Doe, Ph.D.",
		author + "/Zzyzx Comma Two":   "Zzyzx Jane Doe, Ph.D., Zzyzx Jim Reader",
		author + "/Zzyzx Comma Three": "Zzyzx Kim Reader",
		author + "/Zzyzx Comma Four":  "Zzyzx Jane Doe",
	} {
		if got := call(t, "item_get", map[string]any{"item": ids[path]})["narrator"]; got != want {
			t.Errorf("%s reads %v, want %q", path, got, want)
		}
	}
}
