package tools

import (
	"net/http"
	"strings"
	"testing"
)

// A tool that changes an item needs its whole title: a title that is only part
// of one book's would change that book, which is not the one named - asking to
// delete "Foundation" once Foundation is gone must not delete Foundation and
// Empire. A lookup still takes the part, and the refusal names the book so its
// id can be passed.
func TestAChangeNeedsTheWholeTitle(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/search", `{"book":[{"libraryItem":`+item(itemID, "Foundation and Empire", "", "")+`}]}`)
	f.json("GET /api/items/"+itemID, item(itemID, "Foundation and Empire", "", ""))
	f.json("DELETE /api/items/"+itemID, `{}`)
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"item_delete", map[string]any{"item": "Foundation", "delete_files": true}},
		{"item_edit", map[string]any{"item": "Foundation", "publisher": "Gnome Press"}},
		{"user_progress_set", map[string]any{"item": "Foundation", "finished": true}},
	} {
		_, err := call(tc.tool, tc.args)
		if err == nil {
			t.Fatalf("%s changed the book a part of its title named", tc.tool)
		}
		if !strings.Contains(err.Error(), "Foundation and Empire") || !strings.Contains(err.Error(), itemID) {
			t.Errorf("%s: the refusal does not name the nearest book and its id: %v", tc.tool, err)
		}
	}
	for _, r := range f.seen {
		if r.Method != http.MethodGet {
			t.Errorf("a refused change reached the server: %s %s", r.Method, r.Path)
		}
	}

	// reading is still forgiving
	out, err := call("item_get", map[string]any{"item": "Foundation"})
	if err != nil {
		t.Fatal(err)
	}
	if got := str(t, out["id"]); got != itemID {
		t.Errorf("item_get resolved %q, want %s", got, itemID)
	}

	// and the whole title changes it
	if _, err := call("item_edit", map[string]any{"item": "foundation AND empire", "publisher": "Gnome Press"}); err != nil {
		t.Errorf("the whole title, in another case, was refused: %v", err)
	}
}
