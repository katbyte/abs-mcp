//go:build integration

package acceptance

import (
	"slices"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
)

func TestAuthorList(t *testing.T) {
	out := suite.Call(t, "author_list", map[string]any{"library": "Fiction"})

	if total := acc.Num(t, out["total"], "total"); total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	names := map[string]int{}
	for _, row := range acc.Rows(t, out["authors"], "authors") {
		name := acc.Str(row["name"])
		names[name] = acc.Num(t, row["books"], "books")
	}
	if names["Isaac Asimov"] != 3 {
		t.Errorf("Isaac Asimov has %d books, want 3 (have %v)", names["Isaac Asimov"], names)
	}
	if names["Tad Williams"] != 2 {
		t.Errorf("Tad Williams has %d books, want 2", names["Tad Williams"])
	}
}

func TestAuthorGet(t *testing.T) {
	out := suite.Call(t, "author_get", map[string]any{"library": "Fiction", "author": "Isaac Asimov"})

	books := acc.Rows(t, out["books"], "books")
	if len(books) != 3 {
		t.Fatalf("books = %d, want 3", len(books))
	}
	titles := make([]string, 0, len(books))
	for _, b := range books {
		title := acc.Str(b["title"])
		titles = append(titles, title)
	}
	slices.Sort(titles)
	if !slices.Equal(titles, []string{"Foundation", "Foundation and Empire", "Second Foundation"}) {
		t.Errorf("titles = %v", titles)
	}
}

// renaming to a name that already exists is how duplicate authors merge.
func TestAuthorEditAndMerge(t *testing.T) {
	suite.Call(t, "author_edit", map[string]any{
		"library": "Fiction", "author": "Tad Williams", "description": "Author of Otherland.",
	})
	got := suite.Call(t, "author_get", map[string]any{"library": "Fiction", "author": "Tad Williams"})
	if desc := acc.Str(got["description"]); desc != "Author of Otherland." {
		t.Errorf("description = %q", desc)
	}

	// and clear blanks it, which a plain edit cannot say
	suite.Call(t, "author_edit", map[string]any{
		"library": "Fiction", "author": "Tad Williams", "clear": []any{"description"},
	})
	got = suite.Call(t, "author_get", map[string]any{"library": "Fiction", "author": "Tad Williams"})
	if desc := acc.Str(got["description"]); desc != "" {
		t.Errorf("description after clear = %q", desc)
	}

	// rename away and back, checking merged is reported honestly
	out := suite.Call(t, "author_edit", map[string]any{
		"library": "Fiction", "author": "Tad Williams", "name": "T. Williams",
	})
	if merged := acc.BoolOf(out["merged"]); merged {
		t.Error("renaming to an unused name should not report a merge")
	}
	suite.Call(t, "author_edit", map[string]any{
		"library": "Fiction", "author": "T. Williams", "name": "Tad Williams",
	})

	if authors := acc.Rows(t, suite.Call(t, "author_list", map[string]any{"library": "Fiction"})["authors"], "authors"); len(authors) != 3 {
		t.Errorf("author count drifted to %d after the rename round trip", len(authors))
	}
}

func TestAuthorEditNothingToDo(t *testing.T) {
	if msg := suite.CallErr(t, "author_edit", map[string]any{"library": "Fiction", "author": "Isaac Asimov"}); msg == "" {
		t.Error("an edit with no fields should be refused")
	}
}

func TestSeriesList(t *testing.T) {
	out := suite.Call(t, "series_list", map[string]any{"library": "Fiction"})

	byName := map[string]int{}
	for _, row := range acc.Rows(t, out["series"], "series") {
		name := acc.Str(row["name"])
		byName[name] = acc.Num(t, row["books"], "books")
		// the numbers present are how the gap stands out
		if name == "The Expanse" && (row["sequence"] == nil || !slices.Equal(acc.Strs(t, row["sequence"], "sequence"), []string{"1", "3"})) {
			t.Errorf("The Expanse sequence = %v, want [1 3]", row["sequence"])
		}
	}
	for name, want := range map[string]int{"Foundation": 3, "Otherland": 2, "The Expanse": 2} {
		if byName[name] != want {
			t.Errorf("series %q has %d books, want %d (have %v)", name, byName[name], want, byName)
		}
	}
}

func TestSeriesGet(t *testing.T) {
	out := suite.Call(t, "series_get", map[string]any{"library": "Fiction", "series": "The Expanse"})

	books := acc.Rows(t, out["books"], "books")
	if len(books) != 2 {
		t.Fatalf("books = %d, want 2", len(books))
	}
	// in sequence order, and the sequence must survive the projection
	if seq := acc.Str(books[0]["sequence"]); seq != "1" {
		t.Errorf("first book sequence = %v, want 1", books[0]["sequence"])
	}
	if seq := acc.Str(books[1]["sequence"]); seq != "3" {
		t.Errorf("second book sequence = %v, want 3", books[1]["sequence"])
	}
}

func TestSeriesEdit(t *testing.T) {
	suite.Call(t, "series_edit", map[string]any{
		"library": "Fiction", "series": "Otherland", "description": "Four volumes, two of them here.",
	})
	t.Cleanup(func() {
		suite.Call(t, "series_edit", map[string]any{"library": "Fiction", "series": "Otherland", "description": " "})
	})

	out := suite.Call(t, "series_get", map[string]any{"library": "Fiction", "series": "Otherland"})
	if desc := acc.Str(out["description"]); desc != "Four volumes, two of them here." {
		t.Errorf("description = %q", desc)
	}
}

// author_delete unlinks the books rather than removing them, so this runs last
// and puts the author back via item_edit.
func TestAuthorDelete(t *testing.T) {
	suite.Call(t, "author_edit", map[string]any{
		"library": "Fiction", "author": "Isaac Asimov", "name": "Doomed Author",
	})
	out := suite.Call(t, "author_delete", map[string]any{"library": "Fiction", "author": "Doomed Author"})
	if deleted := acc.Str(out["deleted"]); deleted != "Doomed Author" {
		t.Errorf("deleted = %v", out["deleted"])
	}
	t.Cleanup(func() {
		for _, title := range []string{"Foundation", "Foundation and Empire", "Second Foundation"} {
			suite.Call(t, "item_edit", map[string]any{"item": title, "authors": []any{"Isaac Asimov"}})
		}
	})

	// the books survive, they just lost the link
	if item := suite.Call(t, "item_get", map[string]any{"item": "Foundation"}); item["author"] != nil {
		t.Errorf("Foundation still has author %v after the author was deleted", item["author"])
	}
}

// narrator_list and the narrator branch of metadata_rename use a different
// endpoint from every other catalogue tool: the narrator id is base64 of the
// name, percent-encoded.
func TestNarratorListAndRename(t *testing.T) {
	out := suite.Call(t, "narrator_list", map[string]any{"library": "Fiction"})

	byName := map[string]int{}
	for _, row := range acc.Rows(t, out["narrators"], "narrators") {
		name := acc.Str(row["name"])
		byName[name] = acc.Num(t, row["books"], "books")
	}
	if byName["Jefferson Mays"] != 2 {
		t.Errorf("Jefferson Mays narrates %d, want 2 (have %v)", byName["Jefferson Mays"], byName)
	}

	// rename, confirm, and rename back
	edited := suite.Call(t, "metadata_rename", map[string]any{
		"library": "Fiction", "field": "narrators", "from": "Jefferson Mays", "to": "J. Mays",
	})
	if n := acc.Num(t, edited["items_updated"], "items_updated"); n != 2 {
		t.Errorf("items_updated = %d, want 2", n)
	}
	t.Cleanup(func() {
		suite.Call(t, "metadata_rename", map[string]any{
			"library": "Fiction", "field": "narrators", "from": "J. Mays", "to": "Jefferson Mays",
		})
	})

	after := suite.Call(t, "narrator_list", map[string]any{"library": "Fiction"})
	var renamed bool
	for _, row := range acc.Rows(t, after["narrators"], "narrators") {
		if row["name"] == "J. Mays" {
			renamed = true
		}
	}
	if !renamed {
		t.Error("the renamed narrator is not in narrator_list")
	}
}

func TestNarratorRenameValidation(t *testing.T) {
	if msg := suite.CallErr(t, "metadata_rename", map[string]any{"library": "Fiction", "field": "narrators", "from": "Scott Brick"}); msg == "" {
		t.Error("neither to nor remove should be refused")
	}
	if msg := suite.CallErr(t, "metadata_rename", map[string]any{"library": "Fiction", "field": "narrators", "from": "", "to": "x"}); msg == "" {
		t.Error("an empty from should be refused")
	}
}
