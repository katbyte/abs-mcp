package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// shelfBook is a book as the listing returns it, at a folder of its own, with
// the fields the duplicate rules read.
func shelfBook(id, path, title, author, narrator string, seconds, tracks int, asin string) string {
	return fmt.Sprintf(`{"id":%q,"libraryId":%q,"mediaType":"book","relPath":%q,"media":{"metadata":{"title":%q,"authorName":%q,"narratorName":%q,"asin":%q},"duration":%d,"numTracks":%d,"numAudioFiles":%d}}`,
		id, libID, path, title, author, narrator, asin, seconds, tracks, tracks)
}

// The cases of the zbooks sort, as a library would hold them: the pairs
// no key joins that are one recording come back as candidates, each saying
// why; the other readings of one book, and two works with nearly one name,
// do not; and neither counts as a duplicate group.
func TestDuplicateCandidatesFromTheZbooksSort(t *testing.T) {
	t.Parallel()

	const osc, niven = "Orson Scott Card", "Larry Niven"
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		// an edition in the title, and two asins: never a group, but a candidate
		shelfBook("a1", osc+"/The Ender Saga - 01 - Ender's Game", "Ender's Game", osc, "", 37981, 2, "B000ENDER1"),
		shelfBook("a2", osc+"/The Ender Saga - 01 - Ender's Game (20th Anniversary full cast)", "Ender's Game (20th Anniversary full cast)", osc, "", 40307, 107, "B000ENDER2"),
		// filed under another author, 3% longer
		shelfBook("b1", "Peter F. Hamilton/Salvation Sequence - 03 - The Saints of Salvation", "The Saints of Salvation", "Peter F. Hamilton", "", 60887, 1, ""),
		shelfBook("b2", "Philip K. Dick/The Saints of Salvation", "The Saints of Salvation: Salvation Sequence Series, Book 3", "Philip K. Dick", "", 62879, 52, ""),
		// split and single file, the author spelled with a translator in it
		shelfBook("c1", "Liu Cixin/Remembrance of Earth's Past - 4 - The Redemption of Time (Baoshu)", "The Redemption of Time", "Baoshu", "", 36772, 16, ""),
		shelfBook("c2", "Liu Cixin/Remembrance of Earth's Past - 4 - The Redemption of Time (Baoshu) (single file)", "The Redemption of Time: The Three-Body Problem, Book 4", "Ken Liu - Translator Baoshu", "", 36771, 1, ""),
		// the later title, with the old one in the album tag
		shelfBook("d1", osc+"/A Planet Called Treason", "A Planet Called Treason", osc, "", 38783, 33, ""),
		shelfBook("d2", osc+"/Treason", "Treason", osc, "", 39054, 8, ""),
		// the reader in the field on one and in brackets on the other: one reader
		shelfBook("p1", niven+"/Known Space - 05 - Protector", "Protector", niven, "Connor O'Brien", 26611, 16, ""),
		shelfBook("p2", niven+"/Known Space - 05 - Protector (O'Brien)", "Protector (O'Brien)", niven, "", 27284, 10, ""),

		// another reading far shorter, and another 10% shorter
		shelfBook("e1", osc+"/Alvin Maker - 05 - Heartfire", "Heartfire", osc, "", 43726, 37, ""),
		shelfBook("e2", osc+"/Alvin Maker - 05 - Heartfire (Nana Visitor)", "Heartfire (Nana Visitor)", osc, "", 21429, 8, ""),
		shelfBook("f1", osc+"/Alvin Maker - 02 - Red Prophet", "Red Prophet", osc, "", 43997, 37, ""),
		shelfBook("f2", osc+"/Alvin Maker - 02 - Red Prophet (Polk)", "Red Prophet (Polk)", osc, "", 39551, 8, ""),
		// two readers named, half a percent apart
		shelfBook("g1", niven+"/Ringworld - 01 - Ringworld (DeLotel)", "Ringworld (DeLotel)", niven, "", 39924, 20, ""),
		shelfBook("g2", niven+"/Ringworld - 01 - Ringworld (O'Brien)", "Ringworld (O'Brien)", niven, "", 39708, 20, ""),
		// one story and the collection named after it
		shelfBook("h1", "Robert A. Heinlein/—All You Zombies—", "—All You Zombies—", "Robert A. Heinlein", "", 1692, 1, ""),
		shelfBook("h2", "Robert A. Heinlein/'All You Zombies'", "'All You Zombies'", "Robert A. Heinlein", "", 11556, 5, ""),
		// one author, lengths within 1%, and nothing in the tags joins them
		shelfBook("s1", osc+"/Speaker for the Dead", "Speaker for the Dead", osc, "", 50000, 12, ""),
		shelfBook("s2", osc+"/Xenocide", "Xenocide", osc, "", 50200, 14, ""),
		// joined by their asin already: a group, not a candidate
		shelfBook("q1", "Neal Stephenson/Anathem", "Anathem", "Neal Stephenson", "", 118000, 1, "B001ANATHM"),
		shelfBook("q2", "Neal Stephenson/Anathem (Unabridged)", "Anathem: A Novel", "Neal Stephenson", "", 118100, 30, "B001ANATHM"),
	))
	albums := map[string]string{"d1": "Treason", "s1": "Speaker for the Dead", "e1": "Heartfire", "f1": "Red Prophet"}
	type batch struct {
		IDs []string `json:"libraryItemIds"`
	}
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var body batch
		_ = json.NewDecoder(r.Body).Decode(&body)
		items := make([]string, 0, len(body.IDs))
		for _, id := range body.IDs {
			items = append(items, item(id, id, "", fmt.Sprintf(`"audioFiles":[{"index":1,"metaTags":{"tagAlbum":%q}},{"index":2,"metaTags":{"tagAlbum":"not the first"}}]`, albums[id])))
		}
		_, _ = w.Write([]byte(`{"libraryItems":[` + strings.Join(items, ",") + `]}`))
	})
	call := toolCaller(t, f)

	out, err := call("audit_duplicates", nil)
	if err != nil {
		t.Fatal(err)
	}
	why := map[string]string{}
	for _, c := range list(t, out["candidates"]) {
		items := list(t, c["items"])
		why[str(t, items[0]["id"])+"+"+str(t, items[1]["id"])] = str(t, c["why"])
	}
	want := map[string]string{
		"a1+a2": `the same title, "Ender's Game", and author, one marked "20th Anniversary full cast"; lengths 5.8% apart`,
		"b1+b2": `the same title, "The Saints of Salvation", under another author (Peter F. Hamilton, Philip K. Dick); lengths 3.2% apart; one is a single file, the other 52`,
		"c1+c2": `the same title, "The Redemption of Time", and author; lengths 0.0% apart; one is a single file, the other 16`,
		"d1+d2": `the album tag of the first, "Treason", is the second's title; same author, lengths 0.7% apart`,
		"p1+p2": `the same title, "Protector", and author; lengths 2.5% apart`,
	}
	if len(why) != len(want) {
		t.Errorf("candidates = %v, want %v", why, want)
	}
	for pair, w := range want {
		if got := why[pair]; !strings.HasPrefix(got, w) || !strings.HasSuffix(got, "; confirm with item_compare_audio") {
			t.Errorf("%s: why = %q, want %q and the check to confirm it", pair, got, w)
		}
	}
	if num(t, out["total_candidates"]) != 5 || num(t, out["total_findings"]) != 1 || len(list(t, out["groups"])) != 1 {
		t.Errorf("total_candidates = %v, total_findings = %v: want the 5 candidates counted apart from the one group", out["total_candidates"], out["total_findings"])
	}

	// the tags were read only for books one author wrote within 1% of each
	// other's length: Treason's pair, and two pairs that are not one book
	reqs := f.requests("/api/items/batch/get")
	fetched := make([]string, 0, len(reqs)*embedBatchSize)
	for _, req := range reqs {
		var body batch
		if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
			t.Fatal(err)
		}
		fetched = append(fetched, body.IDs...)
	}
	slices.Sort(fetched)
	if !slices.Equal(fetched, []string{"d1", "d2", "e1", "f1", "s1", "s2"}) {
		t.Errorf("fetched whole = %v, want only the pairs by one author within 1%%", fetched)
	}

	capped, err := call("audit_duplicates", map[string]any{"limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(list(t, capped["candidates"])) != 2 || num(t, capped["total_candidates"]) != 5 {
		t.Errorf("with limit 2: %d candidates of %v, want 2 of 5", len(list(t, capped["candidates"])), capped["total_candidates"])
	}
}

// What the rules read of a title and a folder: the words that name the book,
// the readers named, and the editions named.
func TestCandidateFactsReadTitlesAndFolders(t *testing.T) {
	t.Parallel()

	for title, want := range map[string]string{
		"Ender's Game (20th Anniversary full cast)":              "enders game",
		"The Redemption of Time: The Three-Body Problem, Book 4": "redemption of time",
		"—All You Zombies—":                                      "all you zombies",
		"'All You Zombies'":                                      "all you zombies",
		"A Planet Called Treason":                                "planet called treason",
		"The Gods Themselves (Morgan, Abridged)":                 "gods themselves",
	} {
		if got := candidateCore(title); got != want {
			t.Errorf("candidateCore(%q) = %q, want %q", title, got, want)
		}
	}

	facts := func(title, path, author, narrator string) candidateFacts {
		return candidateFactsOf(&itemSummary{Title: title, Path: path, Author: author, Narrator: narrator})
	}
	for _, c := range []struct {
		name              string
		got               candidateFacts
		narrators, labels [][]string
	}{
		{"a reader and an edition in one bracket", facts("The Gods Themselves", "Isaac Asimov/The Gods Themselves (Morgan, Abridged)", "Isaac Asimov", ""), [][]string{{"morgan"}}, [][]string{{"Abridged"}}},
		{"the author in brackets is not a reader", facts("The Redemption of Time", "Liu Cixin/The Redemption of Time (Baoshu) (single file)", "Baoshu", ""), nil, [][]string{{"single file"}}},
		{"a year and a novel are neither", facts("Dune (1965) (A Novel)", "Frank Herbert/Dune", "Frank Herbert", ""), nil, nil},
		{"the field, full cast aside", facts("Red Prophet", "Orson Scott Card/Red Prophet", "Orson Scott Card", "Full Cast, Emily Janice Card"), [][]string{{"emily", "janice", "card"}}, nil},
	} {
		var labels [][]string
		for _, l := range c.got.labels {
			labels = append(labels, []string{l})
		}
		if !reflect.DeepEqual(c.got.narrators, c.narrators) || !reflect.DeepEqual(labels, c.labels) {
			t.Errorf("%s: readers %v, labels %v; want %v, %v", c.name, c.got.narrators, labels, c.narrators, c.labels)
		}
	}

	baoshu, translated := facts("X", "", "Baoshu", ""), facts("X", "", "Ken Liu - Translator Baoshu", "")
	if !baoshu.sameAuthor(&translated) {
		t.Error("Baoshu and Ken Liu - Translator Baoshu read as two authors")
	}
	obrien, connor, delotel := facts("X (O'Brien)", "", "Larry Niven", ""), facts("X", "", "Larry Niven", "Connor O'Brien"), facts("X (DeLotel)", "", "Larry Niven", "")
	if obrien.otherReader(&connor) || !obrien.otherReader(&delotel) {
		t.Errorf("O'Brien against Connor O'Brien and DeLotel = %v, %v; want one reader, then two", obrien.otherReader(&connor), obrien.otherReader(&delotel))
	}
	if unnamed := facts("X", "", "Larry Niven", ""); unnamed.otherReader(&delotel) {
		t.Error("a copy naming no reader read as another reading")
	}
}
