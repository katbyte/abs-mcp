package tools

import (
	"slices"
	"testing"
)

// library_search is how a title someone typed becomes an item id, and the
// first row is the one taken. The server lists its hits in an order of its
// own - a live server answers "Foundation" with Second Foundation, then
// Foundation and Empire, then Foundation - so the book whose title is the
// query goes first, and the rest keep the server's order.
func TestLibrarySearchPutsTheTitleAskedForFirst(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}}`)
	f.json("GET /api/libraries/"+libID+"/search", `{"book":[`+
		`{"libraryItem":`+item("i1", "Second Foundation", "", "")+`},`+
		`{"libraryItem":`+item("i2", "Foundation and Empire", "", "")+`},`+
		`{"libraryItem":`+item("i3", "Foundation", "", "")+`}]}`)
	call := toolCaller(t, f)

	out, err := call("library_search", map[string]any{"query": " foundation "})
	if err != nil {
		t.Fatal(err)
	}
	items := list(t, out["items"])
	titles := make([]string, 0, len(items))
	for _, row := range items {
		titles = append(titles, str(t, row["title"]))
	}
	if want := []string{"Foundation", "Second Foundation", "Foundation and Empire"}; !slices.Equal(titles, want) {
		t.Errorf("items = %v, want %v", titles, want)
	}
}

// series_list's sequence column is how a gap stands out, and the server's
// series listing carries each book minified: no series list, only the joined
// "The Expanse #1" string. Read from the series list alone, every row came
// back with no numbers at all.
func TestSeriesListReadsTheNumbersFromTheMinifiedBooks(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	minified := func(id, title, series string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","media":{"metadata":{"title":"` + title + `","seriesName":"` + series + `"}}}`
	}
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"The Expanse","books":[`+
		minified("b1", "Leviathan Wakes", "The Expanse #1")+`,`+
		minified("b3", "Abaddon's Gate", "The Expanse #3, Other Saga #9")+`]}],"total":1}`)
	call := toolCaller(t, f)

	out, err := call("series_list", map[string]any{"library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["series"])
	if len(rows) != 1 {
		t.Fatalf("series = %v, want The Expanse", rows)
	}
	var seq []string
	if rows[0]["sequence"] != nil {
		seq = anyStrings(rows[0]["sequence"])
	}
	if !slices.Equal(seq, []string{"1", "3"}) {
		t.Errorf("sequence = %v, want [1 3]: this series' numbers, not the other one's", seq)
	}
}

// A book whose store is already recorded is not written to again: a match
// found the provider tag in the middle of the collector's tags and sent the
// whole list back with the tag moved to the end, a write that changed nothing
// but the order, on every re-match of a book tagged after it was matched.
func TestAStoreAlreadyRecordedIsNotWrittenAgain(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	tagged := `"tags":["zz-provider:audible","mine"]`
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B0DUNE"`, tagged))
	f.json("POST /api/items/"+itemID+"/match", `{"updated":false,"libraryItem":`+item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B0DUNE"`, tagged)+`}`)
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	if _, err := call("item_match_apply", map[string]any{"item": itemID, "asin": "B0DUNE", "provider": "audible"}); err != nil {
		t.Fatal(err)
	}
	out, err := call("item_match_apply_batch", map[string]any{"matches": []any{map[string]any{"item": itemID, "asin": "B0DUNE", "provider": "audible"}}})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["unchanged"]) != 1 {
		t.Errorf("unchanged = %v, want the book counted as unchanged", out["unchanged"])
	}
	if writes := tagWrites(t, f, itemID); len(writes) != 0 {
		t.Errorf("tags sent %v to a book that already records the store", writes)
	}
}
