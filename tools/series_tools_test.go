package tools

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// The series tools after a day of merging spellings by hand: series_get has
// to show every series a book is in, because an edit built from a listing
// that showed one per book stripped four Stormlight books of their Cosmere
// link; series_merge does that move in one call; and the edit tools take
// add_series and remove_series so a link can be added without rewriting the
// list.

const (
	seriesA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" // Stormlight Archive
	seriesB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" // The Stormlight Archive
)

// seriesRoutes is a canned library where the item listing filtered by series
// collapses each book's series to the one matched, as the server does, and
// the batch fetch has the whole list.
func seriesRoutes(f *fakeABS) {
	oneLibrary(f)
	f.json("GET /api/series/"+seriesA, `{"id":"`+seriesA+`","name":"Stormlight Archive","libraryId":"`+libID+`"}`)
	f.json("GET /api/series/"+seriesB, `{"id":"`+seriesB+`","name":"The Stormlight Archive","libraryId":"`+libID+`"}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"`+seriesA+`","name":"Stormlight Archive","libraryId":"`+libID+`"},{"id":"`+seriesB+`","name":"The Stormlight Archive","libraryId":"`+libID+`"}],"total":2}`)
	// the server's filter data is cached and keeps names from before a
	// rename; nothing may resolve a series through it
	f.json("GET /api/libraries/"+libID+"/filterdata", `{"series":[{"id":"`+seriesA+`","name":"Stale Name"},{"id":"`+seriesB+`","name":"Older Name"}]}`)
	collapsed := func(id, title, seriesID, name, seq string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"A/` + title + `","media":{"metadata":{"title":"` + title + `","series":{"id":"` + seriesID + `","name":"` + name + `","sequence":"` + seq + `"}}}}`
	}
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/items", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("filter") {
		case abs.EncodeFilter("series", seriesA):
			_, _ = io.WriteString(w, page(collapsed("i3", "Oathbringer", seriesA, "Stormlight Archive", "3")))
		case abs.EncodeFilter("series", seriesB):
			_, _ = io.WriteString(w, page(
				collapsed("i2", "Words of Radiance", seriesB, "The Stormlight Archive", "2"),
				collapsed("i4", "Rhythm of War", seriesB, "The Stormlight Archive", "4"),
			))
		default:
			_, _ = io.WriteString(w, page())
		}
	})
	full := func(id, title, series string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"A/` + title + `","media":{"metadata":{"title":"` + title + `","series":[` + series + `]}}}`
	}
	ref := func(id, name, seq string) string {
		return `{"id":"` + id + `","name":"` + name + `","sequence":"` + seq + `"}`
	}
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"libraryItemIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		items := map[string]string{
			"i2": full("i2", "Words of Radiance", ref(seriesB, "The Stormlight Archive", "2")+","+ref("c", "Cosmere", "")),
			"i3": full("i3", "Oathbringer", ref(seriesA, "Stormlight Archive", "3")),
			"i4": full("i4", "Rhythm of War", ref(seriesB, "The Stormlight Archive", "4")+","+ref("c", "Cosmere", "")+","+ref(seriesA, "Stormlight Archive", "")),
		}
		out := make([]string, 0, len(body.IDs))
		for _, id := range body.IDs {
			out = append(out, items[id])
		}
		_, _ = io.WriteString(w, `{"libraryItems":[`+strings.Join(out, ",")+`]}`)
	})
	f.json("POST /api/items/batch/update", `{"updates":2}`)
}

func TestSeriesGetShowsEverySeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	call := toolCaller(t, f)

	out, err := call("series_get", map[string]any{"series": seriesB})
	if err != nil {
		t.Fatal(err)
	}
	books := list(t, out["books"])
	if len(books) != 2 {
		t.Fatalf("books = %v", out["books"])
	}
	if got := books[0]["series"]; !slices.Equal(anyStrings(got), []string{"The Stormlight Archive #2", "Cosmere"}) || str(t, books[0]["sequence"]) != "2" {
		t.Errorf("Words of Radiance = %v #%v, want both series and sequence 2", got, books[0]["sequence"])
	}
}

func TestSeriesMergeKeepsNumbersAndOtherSeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	call := toolCaller(t, f)

	out, err := call("series_merge", map[string]any{"from": "The Stormlight Archive", "into": "Stormlight Archive"})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["moved"]) != 2 || str(t, out["into_id"]) != seriesA {
		t.Errorf("out = %v", out)
	}
	if got := anyStrings(out["books"]); !slices.Equal(got, []string{"Words of Radiance #2", "Rhythm of War #4"}) {
		t.Errorf("books = %v", got)
	}

	sent := f.requests("/api/items/batch/update")
	if len(sent) != 1 {
		t.Fatalf("batch update sent %d times", len(sent))
	}
	var updates []struct {
		ID      string `json:"id"`
		Payload struct {
			Metadata struct {
				Series []abs.SeriesRef `json:"series"`
			} `json:"metadata"`
		} `json:"mediaPayload"`
	}
	if err := json.Unmarshal([]byte(sent[0].Body), &updates); err != nil {
		t.Fatal(err, sent[0].Body)
	}
	got := map[string][]abs.SeriesRef{}
	for _, u := range updates {
		got[u.ID] = u.Payload.Metadata.Series
	}
	// Words of Radiance: the old entry becomes the target in its place with its number, Cosmere stays
	if want := []abs.SeriesRef{{Name: "Stormlight Archive", Sequence: "2"}, {ID: "c", Name: "Cosmere"}}; !sameRefs(got["i2"], want) {
		t.Errorf("i2 series = %v, want %v", got["i2"], want)
	}
	// Rhythm of War was already in the target without a number: it takes the one it had
	if want := []abs.SeriesRef{{ID: "c", Name: "Cosmere"}, {ID: seriesA, Name: "Stormlight Archive", Sequence: "4"}}; !sameRefs(got["i4"], want) {
		t.Errorf("i4 series = %v, want %v", got["i4"], want)
	}

	if _, err := call("series_merge", map[string]any{"from": seriesA, "into": seriesA}); err == nil || !strings.Contains(err.Error(), "one series") {
		t.Errorf("merging a series into itself: %v", err)
	}
}

// A rename onto a name that already exists would leave two series with one
// name, not one series; the tool says which one and points at series_merge.
func TestSeriesEditRefusesAnExistingName(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	f.json("PATCH /api/series/"+seriesB, `{"id":"`+seriesB+`","name":"Stormlight"}`)
	call := toolCaller(t, f)

	_, err := call("series_edit", map[string]any{"series": seriesB, "name": "stormlight archive"})
	if err == nil || !strings.Contains(err.Error(), "series_merge") {
		t.Fatalf("renaming onto an existing name: %v", err)
	}
	if got := f.requests("/api/series/" + seriesB); slices.ContainsFunc(got, func(r request) bool { return r.Method == http.MethodPatch }) {
		t.Error("the rename was sent anyway")
	}

	out, err := call("series_edit", map[string]any{"series": seriesB, "name": "Stormlight"})
	if err != nil || str(t, out["name"]) != "Stormlight" {
		t.Errorf("a fresh name: %v %v", out, err)
	}
}

func TestSeriesListDefaultsToEveryBookLibrary(t *testing.T) {
	t.Parallel()

	const other = "33333333-3333-4333-8333-333333333333"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"},{"id":"`+other+`","name":"More","mediaType":"book"},{"id":"p","name":"Pods","mediaType":"podcast"}]}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"Foundation","books":[]}],"total":1}`)
	f.json("GET /api/libraries/"+other+"/series", `{"results":[{"id":"s2","name":"Culture","books":[]}],"total":1}`)
	call := toolCaller(t, f)

	out, err := call("series_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["series"])
	if num(t, out["total"]) != 2 || len(rows) != 2 || str(t, rows[0]["library"]) != "Books" || str(t, rows[1]["library"]) != "More" {
		t.Errorf("series_list = %v, want both book libraries, each row saying which", out)
	}
	if got := f.requests("/api/libraries/p/series"); len(got) != 0 {
		t.Error("the podcast library was asked for series")
	}

	one, err := call("series_list", map[string]any{"library": "More"})
	if err != nil || num(t, one["total"]) != 1 {
		t.Errorf("one library: %v %v", one, err)
	}
	if rows := list(t, one["series"]); len(rows) != 1 || rows[0]["library"] != nil {
		t.Errorf("one library names it on the rows: %v", rows)
	}
}

func TestItemEditAddsAndRemovesSeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Words of Radiance", `"series":[{"id":"s","name":"The Stormlight Archive","sequence":"2"},{"id":"c","name":"Cosmere"}]`, ""))
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	sentSeries := func() []abs.SeriesRef {
		t.Helper()
		sent := f.requests("/api/items/" + itemID + "/media")
		var body struct {
			Metadata struct {
				Series []abs.SeriesRef `json:"series"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(sent[len(sent)-1].Body), &body); err != nil {
			t.Fatal(err)
		}
		return body.Metadata.Series
	}

	if _, err := call("item_edit", map[string]any{"item": itemID, "add_series": []any{"Stormlight Archive #2"}}); err != nil {
		t.Fatal(err)
	}
	if want := []abs.SeriesRef{{ID: "s", Name: "The Stormlight Archive", Sequence: "2"}, {ID: "c", Name: "Cosmere"}, {Name: "Stormlight Archive", Sequence: "2"}}; !sameRefs(sentSeries(), want) {
		t.Errorf("add_series sent %v, want the two it had and the new one", sentSeries())
	}

	if _, err := call("item_edit", map[string]any{"item": itemID, "add_series": []any{"cosmere #1"}, "remove_series": []any{"The Stormlight Archive"}}); err != nil {
		t.Fatal(err)
	}
	if want := []abs.SeriesRef{{ID: "c", Name: "Cosmere", Sequence: "1"}}; !sameRefs(sentSeries(), want) {
		t.Errorf("add a number to one it is in and remove another: sent %v, want %v", sentSeries(), want)
	}

	for name, args := range map[string]map[string]any{
		"with series":         {"item": itemID, "series": []any{"X"}, "add_series": []any{"Y"}},
		"with clear":          {"item": itemID, "clear": []any{"series"}, "remove_series": []any{"Cosmere"}},
		"remove one not in":   {"item": itemID, "remove_series": []any{"Mistborn"}},
		"add an empty name":   {"item": itemID, "add_series": []any{" #3"}},
		"remove an empty one": {"item": itemID, "remove_series": []any{""}},
	} {
		if _, err := call("item_edit", args); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestItemBatchEditAddsSeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/items/"+itemID, item(itemID, "Elantris", `"series":[{"id":"e","name":"Elantris","sequence":"1"}]`, ""))
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	call := toolCaller(t, f)

	out, err := call("item_batch_edit", map[string]any{"items": []any{itemID}, "add_series": []any{"Cosmere"}})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_updated"]) != 1 {
		t.Errorf("out = %v", out)
	}
	sent := f.requests("/api/items/batch/update")
	if len(sent) != 1 || !strings.Contains(sent[0].Body, `"name":"Elantris","sequence":"1"`) || !strings.Contains(sent[0].Body, `"name":"Cosmere"`) {
		t.Errorf("batch sent %v, want Elantris kept and Cosmere added", sent)
	}
	if !strings.Contains(sent[0].Body, `"metadata":{`) || strings.Contains(sent[0].Body, `"genres"`) {
		t.Errorf("batch sent %v, want only the series in the metadata", sent[0].Body)
	}
}

func TestEditSeriesList(t *testing.T) {
	t.Parallel()

	have := []abs.SeriesRef{{Name: "Discworld", Sequence: "36"}, {Name: "Discworld: Moist von Lipwig", Sequence: "2"}}
	got, err := editSeriesList(have, []string{"Discworld: Industrial Revolution #5", "discworld #36"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := append(slices.Clone(have), abs.SeriesRef{Name: "Discworld: Industrial Revolution", Sequence: "5"}); !sameRefs(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(have) != 2 {
		t.Error("the caller's list was changed")
	}
	got, err = editSeriesList(have, nil, []string{"Discworld: Moist von Lipwig #2", "Discworld"})
	if err != nil || len(got) != 0 || got == nil {
		t.Errorf("removing everything = %v, %v; want an empty list that clears the field", got, err)
	}
	if _, err := editSeriesList(nil, nil, []string{"Discworld"}); err == nil || !strings.Contains(err.Error(), "no series") {
		t.Errorf("removing from nothing: %v", err)
	}
}

func TestMergeSeriesRefs(t *testing.T) {
	t.Parallel()

	from := &abs.Series{ID: "f", Name: "The Wheel of Time"}
	into := &abs.Series{ID: "i", Name: "Wheel of Time"}
	for _, tc := range []struct {
		name string
		have []abs.SeriesRef
		want []abs.SeriesRef
	}{
		{"moves the number", []abs.SeriesRef{{ID: "f", Name: "The Wheel of Time", Sequence: "5"}}, []abs.SeriesRef{{Name: "Wheel of Time", Sequence: "5"}}},
		{"keeps the rest", []abs.SeriesRef{{Name: "Cosmere"}, {Name: "the wheel of time", Sequence: "5"}}, []abs.SeriesRef{{Name: "Cosmere"}, {Name: "Wheel of Time", Sequence: "5"}}},
		{"already in both, target numbered", []abs.SeriesRef{{ID: "f", Name: "The Wheel of Time", Sequence: "5"}, {ID: "i", Name: "Wheel of Time", Sequence: "6"}}, []abs.SeriesRef{{ID: "i", Name: "Wheel of Time", Sequence: "6"}}},
		{"already in both, target unnumbered", []abs.SeriesRef{{ID: "i", Name: "Wheel of Time"}, {ID: "f", Name: "The Wheel of Time", Sequence: "5"}}, []abs.SeriesRef{{ID: "i", Name: "Wheel of Time", Sequence: "5"}}},
	} {
		if got := mergeSeriesRefs(tc.have, from, into); !sameRefs(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A gap whose book is on the shelf unlinked, a gap that closes once two
// spellings are one series, a near miss between two authors that is not a
// misspelling, and the articles list when asked for.
func TestAuditSeriesCrossReferences(t *testing.T) {
	t.Parallel()

	const (
		belgariad = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		wheel     = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		theWheel  = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
		witchery  = "ffffffff-ffff-4fff-8fff-ffffffffffff"
		witcher   = "99999999-9999-4999-8999-999999999999"
		expanse   = "88888888-8888-4888-8888-888888888888"
		shining   = "77777777-7777-4777-8777-777777777777"
		exec      = "66666666-6666-4666-8666-666666666666"
	)
	f := newFakeABS(t)
	oneLibrary(f)
	book := func(id, title, author, relPath, seriesName string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"` + relPath + `","media":{"metadata":{"title":"` + title + `","authorName":"` + author + `","seriesName":"` + seriesName + `"}}}`
	}
	series := func(id, name string, books ...string) string {
		return `{"id":"` + id + `","name":"` + name + `","books":[` + strings.Join(books, ",") + `]}`
	}
	shelf := []string{
		book("b1", "Pawn of Prophecy", "David Eddings", "David Eddings/The Belgariad Series - 01 - Pawn of Prophecy", "The Belgariad #1"),
		book("b2", "Queen of Sorcery", "David Eddings", "David Eddings/The Belgariad Series - 02 - Queen of Sorcery", "The Belgariad #2"),
		book("b3", "Magician's Gambit", "David Eddings", "David Eddings/The Belgariad Series - 03 - Magician's Gambit", "The Belgariad #3"),
		book("b4", "Castle of Wizardry", "David Eddings", "David Eddings/The Belgariad Series - 04 - Castle of Wizardry", ""),
		book("b5", "Enchanter's End Game", "David Eddings", "David Eddings/The Belgariad Series - 05 - Enchanter's End Game", "The Belgariad #5"),
		book("w1", "The Eye of the World", "Robert Jordan", "Robert Jordan/The Wheel of Time - 01 - The Eye of the World", "Wheel of Time #1"),
		book("w2", "The Great Hunt", "Robert Jordan", "Robert Jordan/The Wheel of Time - 02 - The Great Hunt", "The Wheel of Time #2"),
		book("w3", "The Dragon Reborn", "Robert Jordan", "Robert Jordan/The Wheel of Time - 03 - The Dragon Reborn", "Wheel of Time #3"),
		book("w4", "The Shadow Rising", "Robert Jordan", "Robert Jordan/The Wheel of Time - 04 - The Shadow Rising", "The Wheel of Time #4"),
		book("w5", "The Fires of Heaven", "Robert Jordan", "Robert Jordan/The Wheel of Time - 05 - The Fires of Heaven", "The Wheel of Time #5"),
		book("w6", "Lord of Chaos", "Robert Jordan", "Robert Jordan/The Wheel of Time - 06 - Lord of Chaos", "Wheel of Time #6"),
		book("w7", "A Crown of Swords", "Robert Jordan", "Robert Jordan/The Wheel of Time - 07 - A Crown of Swords", "Wheel of Time #7"),
		book("v1", "The Witchery", "S. Isabelle", "S. Isabelle/The Witchery - 01 - The Witchery", "Witchery #1"),
		book("v2", "Shadow Coven", "S. Isabelle", "S. Isabelle/The Witchery - 02 - Shadow Coven", "Witchery #2"),
		book("x1", "The Last Wish", "Andrzej Sapkowski", "Andrzej Sapkowski/The Last Wish", "The Witcher #0"),
		book("e1", "Leviathan Wakes", "James S. A. Corey", "James S. A. Corey/The Expanse - 01 - Leviathan Wakes", "The Expanse #1"),
		book("e2", "Caliban's War", "James S. A. Corey", "James S. A. Corey/The Expanse - 02 - Caliban's War", "The Expanse #2"),
		book("k1", "The Shining", "Stephen King", "Stephen King/The Shining", "The Shining #1"),
		book("m1", "The Executioner and Her Way of Life, Vol. 01", "Mato Sato", "Mato Sato/The Executioner and Her Way of Life, Vol. 01", "The Executioner and Her Way of Life #1"),
	}
	byID := map[string]string{}
	for _, b := range shelf {
		var it struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(b), &it)
		byID[it.ID] = b
	}
	pick := func(ids ...string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, byID[id])
		}
		return out
	}
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[`+strings.Join([]string{
		series(belgariad, "The Belgariad", pick("b1", "b2", "b3", "b5")...),
		series(wheel, "Wheel of Time", pick("w1", "w3", "w6", "w7")...),
		series(theWheel, "The Wheel of Time", pick("w2", "w4", "w5")...),
		series(witchery, "Witchery", pick("v1", "v2")...),
		series(witcher, "The Witcher", pick("x1")...),
		series(expanse, "The Expanse", pick("e1", "e2")...),
		series(shining, "The Shining", pick("k1")...),
		series(exec, "The Executioner and Her Way of Life", pick("m1")...),
	}, ",")+`],"total":8}`)
	// the gap sweep asks for each series' items with their numbers; the
	// sweep for numbering reads the whole shelf
	sequenced := func(id, seriesID, seq string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","media":{"metadata":{"series":[{"id":"` + seriesID + `","name":"x","sequence":"` + seq + `"}]}}}`
	}
	filtered := map[string]string{
		abs.EncodeFilter("series", belgariad): page(sequenced("b1", belgariad, "1"), sequenced("b2", belgariad, "2"), sequenced("b3", belgariad, "3"), sequenced("b5", belgariad, "5")),
		abs.EncodeFilter("series", wheel):     page(sequenced("w1", wheel, "1"), sequenced("w3", wheel, "3"), sequenced("w6", wheel, "6"), sequenced("w7", wheel, "7")),
		abs.EncodeFilter("series", theWheel):  page(sequenced("w2", theWheel, "2"), sequenced("w4", theWheel, "4"), sequenced("w5", theWheel, "5")),
		abs.EncodeFilter("series", witchery):  page(sequenced("v1", witchery, "1"), sequenced("v2", witchery, "2")),
		abs.EncodeFilter("series", expanse):   page(sequenced("e1", expanse, "1"), sequenced("e2", expanse, "2")),
	}
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/items", func(w http.ResponseWriter, r *http.Request) {
		if body, ok := filtered[r.URL.Query().Get("filter")]; ok {
			_, _ = io.WriteString(w, body)
			return
		}
		_, _ = io.WriteString(w, page(shelf...))
	})
	call := toolCaller(t, f)

	out, err := call("audit_series", nil)
	if err != nil {
		t.Fatal(err)
	}
	gaps := map[string]map[string]any{}
	for _, row := range list(t, out["gaps"]) {
		gaps[str(t, row["name"])] = row
	}
	if len(gaps) != 3 {
		t.Fatalf("gaps = %v, want the Belgariad and both Wheel spellings", out["gaps"])
	}

	// the Belgariad's missing #4 is Castle of Wizardry, on the shelf unlinked
	// under a folder that says "The Belgariad Series"
	unlinked := list(t, gaps["The Belgariad"]["unlinked"])
	if len(unlinked) != 1 || str(t, unlinked[0]["missing"]) != "4" || str(t, unlinked[0]["id"]) != "b4" || str(t, unlinked[0]["suggest"]) != "The Belgariad #4" {
		t.Errorf("Belgariad unlinked = %v, want Castle of Wizardry at #4", unlinked)
	}
	if _, merged := gaps["The Belgariad"]["merged"]; merged {
		t.Errorf("the Belgariad has one spelling and was reported merged: %v", gaps["The Belgariad"])
	}

	// the two Wheel spellings together run 1-7: nothing is missing
	for _, name := range []string{"Wheel of Time", "The Wheel of Time"} {
		merged, ok := gaps[name]["merged"].(map[string]any)
		if !ok {
			t.Fatalf("%s merged = %v", name, gaps[name]["merged"])
		}
		if missing, present := merged["missing"]; !present || len(anyStrings(missing)) != 0 {
			t.Errorf("%s missing once merged = %v, want an empty list", name, missing)
		}
		if with := anyStrings(merged["with"]); len(with) != 1 || with[0] == name {
			t.Errorf("%s merged with %v", name, with)
		}
	}

	// names: the Wheel spellings, with their author on each, and not the
	// Witchery / The Witcher near miss, which is two authors
	names := list(t, out["names"])
	if len(names) != 1 || str(t, names[0]["keep"]) != "Wheel of Time" {
		t.Fatalf("names = %v, want the Wheel spellings only", names)
	}
	for _, sp := range list(t, names[0]["spellings"]) {
		if str(t, sp["author"]) != "Robert Jordan" {
			t.Errorf("spelling %v carries no author", sp)
		}
	}
	if _, present := out["articles"]; present {
		t.Errorf("articles were listed without being asked for: %v", out["articles"])
	}

	// asked for, the articles list has the labels and not the book title
	out, err = call("audit_series", map[string]any{"articles": true})
	if err != nil {
		t.Fatal(err)
	}
	articles := map[string]string{}
	for _, row := range list(t, out["articles"]) {
		articles[str(t, row["name"])] = str(t, row["suggest"])
	}
	want := map[string]string{"The Belgariad": "Belgariad", "The Wheel of Time": "Wheel of Time", "The Witcher": "Witcher", "The Expanse": "Expanse"}
	if len(articles) != len(want) {
		t.Errorf("articles = %v, want %v (The Shining is a book's title, and so is The Executioner with a volume number)", articles, want)
	}
	for name, suggest := range want {
		if articles[name] != suggest {
			t.Errorf("articles[%q] = %q, want %q", name, articles[name], suggest)
		}
	}
	counts, ok := out["counts"].(map[string]any)
	if !ok || num(t, counts["articles"]) != 4 {
		t.Errorf("counts = %v, want 4 articles", counts)
	}
}

func TestSeriesKeySetsAsideArticleAndWord(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"The Belgariad", "Belgariad", "The Belgariad Series", "belgariad series", "Belgariad  Series"} {
		if got := seriesKey(name); got != "belgariad" {
			t.Errorf("seriesKey(%q) = %q", name, got)
		}
	}
	if seriesKey("Series") == "" || seriesKey("The Series") != seriesKey("Series") {
		t.Errorf("a series called Series is still something: %q %q", seriesKey("Series"), seriesKey("The Series"))
	}
}

func anyStrings(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func sameRefs(a, b []abs.SeriesRef) bool {
	return slices.EqualFunc(a, b, func(x, y abs.SeriesRef) bool {
		return x.ID == y.ID && x.Name == y.Name && x.Sequence == y.Sequence
	})
}

// A title that is the series name, or carries it with a number, where the
// folder says what the title is. Light-novel titles that really are
// "Series, Vol. 4" sit in folders without a title segment and are left alone,
// and so is a first book named after its series.
func TestSeriesShapedTitle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		title   string
		series  []string
		problem string
	}{
		{"Harry Hole 1 (Sean Barrett)", []string{"Harry Hole"}, "series_as_title"},
		{"Beebo Brinker", []string{"Beebo Brinker"}, "series_as_title"},
		{"Jumper Series Bk 1", []string{"Jumper"}, "series_as_title"},
		{"Spice and Wolf, Vol. 1 (Light Novel)", []string{"Spice and Wolf"}, "series_as_title"},
		{"The Bat - Harry Hole Series, Book 1", []string{"Harry Hole"}, "series_in_title"},
		{"Bright Falls 03 - Iris Kelly Doesn't Date", []string{"Bright Falls"}, "series_in_title"},
		{"game changers03: tough guy", []string{"Game Changers"}, "series_in_title"},
		{"Doctor Proctor 1 Doctor Proctor's Fart Powder (Miriam Margolyes)", []string{"Doctor Proctor"}, "series_in_title"},
		{"Steven Gould - [Jumper, #2.5] - Shade", []string{"Jumper"}, "series_in_title"},
		{"Rogue Protocol: The Murderbot Diaries, Book 3", []string{"Murderbot Diaries"}, "series_in_title"},
		{"Consider Phlebas: Culture Series, Book 1", []string{"Culture"}, "series_in_title"},
		{"The Bat", []string{"Harry Hole"}, ""},
		{"Foundation and Empire", []string{"Foundation"}, ""},
		{"2001: A Space Odyssey", []string{"Space Odyssey"}, ""},
		{"The Fall of Hyperion", []string{"Hyperion"}, ""},
		{"Making Money", []string{"Discworld", "Discworld: Moist von Lipwig"}, ""},
		{"Odd Girl Out", nil, ""},
	} {
		if got := seriesShapedTitle(tc.title, tc.series); got != tc.problem {
			t.Errorf("seriesShapedTitle(%q, %v) = %q, want %q", tc.title, tc.series, got, tc.problem)
		}
	}
}

func TestAuditSeriesTitles(t *testing.T) {
	t.Parallel()

	book := func(id, title, relPath, seriesName string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"` + relPath + `","media":{"metadata":{"title":"` + title + `","seriesName":"` + seriesName + `"}}}`
	}
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/items", page(
		book("h1", "Harry Hole 1 (Sean Barrett)", "Jo Nesbø/Harry Hole - 01 - The Bat [Sean Barrett]", "Harry Hole #01"),
		book("h2", "The Bat - Harry Hole Series, Book 1", "Jo Nesbø/Harry Hole - 01 - The Bat [John Lee]", "Harry Hole #01"),
		book("b1", "Beebo Brinker", "Ann Bannon/Beebo Brinker - 01 - Odd Girl Out", "Beebo Brinker #1"),
		book("b0", "Beebo Brinker", "Ann Bannon/Beebo Brinker - 00 - Beebo Brinker", "Beebo Brinker #0"),
		book("s1", "Skyward", "Brandon Sanderson/Skyward - 01 - Skyward", "Skyward #1"),
		book("w1", "Spice and Wolf, Vol. 1 (Light Novel)", "Spice and Wolf/Spice and Wolf, Vol. 01", "Spice and Wolf #01"),
		book("u1", "Jericho", "Ann McMan/Jericho - 02 - Aftermath", ""), // not linked: the folder names the series
		book("m1", "Making Money", "Terry Pratchett/Discworld - 36 - INDUSTRY 4 - Making Money [50th]", "Discworld #36, Discworld: Moist von Lipwig #2"),
	))
	call := toolCaller(t, f)

	out, err := call("audit_series", nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, ok := out["counts"].(map[string]any)
	if !ok || num(t, counts["titles"]) != 4 {
		t.Fatalf("counts = %v, want 4 title findings: %v", counts, out["titles"])
	}
	got := map[string][2]string{}
	for _, row := range list(t, out["titles"]) {
		got[str(t, row["id"])] = [2]string{str(t, row["problem"]), str(t, row["suggest"])}
	}
	want := map[string][2]string{
		"h1": {"series_as_title", "The Bat"},
		"h2": {"series_in_title", "The Bat"},
		"b1": {"series_as_title", "Odd Girl Out"},
		"u1": {"series_as_title", "Aftermath"},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s = %v, want %v", id, got[id], w)
		}
	}
	for _, id := range []string{"b0", "s1", "w1", "m1"} {
		if _, reported := got[id]; reported {
			t.Errorf("%s reported: a book named after its series, a light novel, or a plain title", id)
		}
	}
}

// An author or narrator written "Last, First" is the same person as "First
// Last"; a suffix after the comma is not a first name.
func TestVocabKeyTurnsLastFirstRound(t *testing.T) {
	t.Parallel()

	if vocabKey("authors", "Sanderson, Brandon") != vocabKey("authors", "Brandon Sanderson") {
		t.Error("Sanderson, Brandon is Brandon Sanderson")
	}
	if vocabKey("narrators", "Kramer, Michael") != vocabKey("narrators", "Michael Kramer") {
		t.Error("Kramer, Michael is Michael Kramer")
	}
	for _, same := range []string{"Martin Luther King, Jr.", "Bray, R. C., Scott Brick"} {
		if firstLast(same) != same {
			t.Errorf("firstLast(%q) = %q, want it left alone", same, firstLast(same))
		}
	}
	if vocabKey("series", "Wheel, The") == vocabKey("series", "The Wheel") {
		t.Error("a series name is not turned round")
	}
}

// A series renamed a moment ago is still listed under its old name in the
// server's filter data for up to half an hour. Lookups by name read the live
// series route, so the new name is found and the old one is not.
func TestSeriesResolvesByTheNameItHasNow(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	call := toolCaller(t, f)

	out, err := call("series_get", map[string]any{"series": "The Stormlight Archive"})
	if err != nil {
		t.Fatalf("by the current name: %v", err)
	}
	if str(t, out["id"]) != seriesB {
		t.Errorf("id = %v, want %s", out["id"], seriesB)
	}
	if _, err := call("series_get", map[string]any{"series": "Older Name"}); err == nil {
		t.Error("a name only the stale filter data has was resolved")
	}
	if _, err := call("library_items", map[string]any{"filter": "series:The Stormlight Archive"}); err != nil {
		t.Errorf("a filter by the current name: %v", err)
	}
	if got := f.requests("/api/libraries/" + libID + "/filterdata"); len(got) != 0 {
		t.Errorf("filter data was read %d times, want never", len(got))
	}
}
