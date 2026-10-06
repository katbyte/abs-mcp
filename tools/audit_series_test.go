package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// A library set to hide single-book series lists none of them, and a
// one-book series is where a stray name sits: the books still carry it.
func TestAuditSeriesReadsTheSeriesTheListingHides(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	book := func(id, title, series string) string {
		return item(id, title, `"authorName":"Robert Jordan","seriesName":"`+series+`"`, "")
	}
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s-wot","name":"The Wheel of Time","books":[`+
		book("w1", "The Eye of the World", "The Wheel of Time #1")+","+book("w2", "The Great Hunt", "The Wheel of Time #2")+`]}],"total":1}`)
	f.json("GET /api/libraries/"+libID+"/items", page(
		book("w1", "The Eye of the World", "The Wheel of Time #1"),
		book("w2", "The Great Hunt", "The Wheel of Time #2"),
		book("w3", "The Dragon Reborn", "Wheel of Time #3"),
		book("h1", "New Spring", "Wheel of Time Prequels Series"),
	))
	f.json("POST /api/items/batch/get", `{"libraryItems":[{"id":"h1","libraryId":"`+libID+`","mediaType":"book","media":{"metadata":{"title":"New Spring","series":[{"id":"s-prequels","name":"Wheel of Time Prequels Series"}]}}}]}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	call := toolCaller(t, f)

	out, err := call("audit_series", nil)
	if err != nil {
		t.Fatal(err)
	}
	var spellings []string
	for _, g := range list(t, out["names"]) {
		for _, sp := range list(t, g["spellings"]) {
			spellings = append(spellings, str(t, sp["value"]))
		}
	}
	if !slices.Contains(spellings, "Wheel of Time") || !slices.Contains(spellings, "The Wheel of Time") {
		t.Errorf("names = %v, want the hidden one-book spelling beside the listed one", out["names"])
	}
	odd := list(t, out["odd"])
	if len(odd) != 1 || str(t, odd[0]["name"]) != "Wheel of Time Prequels Series" || str(t, odd[0]["id"]) != "s-prequels" || str(t, odd[0]["suggest"]) != "Wheel of Time Prequels" {
		t.Errorf("odd = %v, want the hidden series, with the id its book carries", odd)
	}
	if got := num(t, out["items_scanned"]); got != 4 {
		t.Errorf("items_scanned = %d, want 4", got)
	}
	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list(t, all["audits"]) {
		if str(t, row["audit"]) == "audit_series" && num(t, row["found"]) != num(t, out["total_findings"]) {
			t.Errorf("audit_all counts %v, audit_series %v", row["found"], out["total_findings"])
		}
	}
}

// A year typed as the sequence read as two thousand missing books, a date
// as twenty million, and made the series pad to four digits.
func TestSeriesNumbersFarFromTheRest(t *testing.T) {
	t.Parallel()

	if got := missingBetween([]float64{1, 2, 2019}); got != nil {
		t.Errorf("missing = %d numbers, want none", len(got))
	}
	if kept, outliers := splitOutliers([]float64{1, 2, 3, 20190315}); !slices.Equal(kept, []float64{1, 2, 3}) || !slices.Equal(outliers, []float64{20190315}) {
		t.Errorf("kept %v outliers %v", kept, outliers)
	}
	// five books of a forty-book series still have their gaps read
	if got := missingBetween([]float64{1, 7, 20, 35, 41}); len(got) != 36 {
		t.Errorf("a sparse series: %d missing, want 36", len(got))
	}
	for _, s := range []string{"inf", "-Inf", "NaN"} {
		if _, ok := sequenceNumber(s); ok {
			t.Errorf("%q read as a number", s)
		}
	}

	c := newNumberingCollector()
	for n := 1; n <= 9; n++ {
		c.add(numbered(strconv.Itoa(n), "Series/"+strconv.Itoa(n), "Series", strconv.Itoa(n)))
	}
	c.add(numbered("y", "Series/Year", "Series", "2019"))
	if w := c.padWidth(seriesKey("Series")); w != 1 {
		t.Errorf("width %d, want 1: the year does not set it", w)
	}

	f := newFakeABS(t)
	oneLibrary(f)
	ref := func(id, seq string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","media":{"metadata":{"title":"` + id + `","series":[{"id":"s1","name":"Series","sequence":"` + seq + `"}]}}}`
	}
	books := ref("b1", "1") + "," + ref("b2", "2") + "," + ref("b3", "2019")
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"Series","books":[`+books+`]}],"total":1}`)
	f.json("GET /api/libraries/"+libID+"/items", `{"results":[`+books+`],"total":3}`)
	out, err := toolCaller(t, f)("audit_series", nil)
	if err != nil {
		t.Fatal(err)
	}
	gaps := list(t, out["gaps"])
	if len(gaps) != 1 || fmt.Sprint(gaps[0]["missing"]) != "[]" || fmt.Sprint(gaps[0]["outliers"]) != "[2019]" {
		t.Errorf("gaps = %v, want no missing and 2019 as the outlier", gaps)
	}
}

// The listing joins an item's series with ", ", which a series name may hold.
func TestSeriesWithACommaInItsName(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/items", page(
		`{"id":"l1","libraryId":"`+libID+`","mediaType":"book","relPath":"Love, Death & Robots - 03 - Zima Blue","media":{"metadata":{"title":"Zima Blue","seriesName":"Love, Death & Robots #2"}}}`,
		`{"id":"f1","libraryId":"`+libID+`","mediaType":"book","relPath":"Foundation - 2 - Foundation and Empire","media":{"metadata":{"title":"Foundation and Empire","seriesName":"Foundation #2, Robot #9"}}}`,
	))
	f.json("POST /api/items/batch/get", `{"libraryItems":[{"id":"l1","libraryId":"`+libID+`","mediaType":"book","relPath":"Love, Death & Robots - 03 - Zima Blue","media":{"metadata":{"title":"Zima Blue","series":[{"id":"s-ldr","name":"Love, Death & Robots","sequence":"2"}]}}}]}`)
	out, err := toolCaller(t, f)("audit_series", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["numbering"])
	if len(rows) != 1 || str(t, rows[0]["problem"]) != "folder_disagrees" || str(t, rows[0]["suggest"]) != "Love, Death & Robots #3" {
		t.Errorf("numbering = %v, want the folder's #3 against #2 in the one series", rows)
	}
	// two numbered series read plainly; only the ambiguous item is fetched
	if batches := f.requests("/api/items/batch/get"); len(batches) != 1 || strings.Contains(batches[0].Body, "f1") {
		t.Errorf("fetched %v, want l1 alone", batches)
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
		if missing, present := merged["missing"]; !present || len(strs(t, missing)) != 0 {
			t.Errorf("%s missing once merged = %v, want an empty list", name, missing)
		}
		if with := strs(t, merged["with"]); len(with) != 1 || with[0] == name {
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

// Every series problem in one place: the gap, the article dropped from one
// book's series name, and the names that are not series names.
func TestAuditSeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	book := func(id, title, author string) string {
		return item(id, title, `"authorName":"`+author+`"`, "")
	}
	series := func(id, name string, books ...string) string {
		return `{"id":"` + id + `","name":"` + name + `","books":[` + strings.Join(books, ",") + `]}`
	}
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[`+strings.Join([]string{
		series("s1", "The Chronicles of Amber", book("i1", "Nine Princes", "Roger Zelazny"), book("i2", "Guns of Avalon", "Roger Zelazny")),
		series("s2", "Chronicles of Amber", book("i3", "Sign of the Unicorn", "Roger Zelazny")),
		series("s3", "Roger Zelazny", book("i4", "Lord of Light", "Roger Zelazny")),
		series("s4", "Harry Hole Series", book("i5", "The Bat", "Jo Nesbø")),
		series("s5", "Alien™: The Novelizations", book("i6", "Alien", "Alan Dean Foster")),
		series("s6", "Star Trek : The Next Generation", book("i7", "Q-Space", "Greg Cox")),
		series("s7", "Siege of Terra: The Horus Heresy", book("i8", "The Solar War", "John French"), book("i9", "The Lost and the Damned", "Guy Haley")),
		series("s8", "Foundation", book("i10", "Foundation", "Isaac Asimov")),
		series("s9", "Discworld", book("i11", "Mort", "Terry Pratchett"), book("i12", "Men at Arms", "Terry Pratchett")),
		series("s10", "Discworld: Death", book("i11", "Mort", "Terry Pratchett")),
	}, ",")+`],"total":10}`)
	f.json("GET /api/libraries/"+libID+"/items", page()) // no sequences, so no gaps
	call := toolCaller(t, f)

	out, err := call("audit_series", nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, ok := out["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts = %v", out["counts"])
	}
	if num(t, counts["gaps"]) != 0 || num(t, counts["names"]) != 1 || num(t, counts["odd"]) != 4 {
		t.Errorf("counts = %v, want no gaps, one name group, four odd names", counts)
	}
	names := list(t, out["names"])
	if len(names) != 1 || str(t, names[0]["kind"]) != "spelling" || str(t, names[0]["keep"]) != "The Chronicles of Amber" {
		t.Errorf("names = %v, want the two Amber spellings behind the one with more books, and Discworld: Death not read as Discworld cut short", names)
	}
	odd := map[string]map[string]any{}
	for _, row := range list(t, out["odd"]) {
		odd[str(t, row["name"])] = row
	}
	for name, want := range map[string]string{
		"Harry Hole Series":               "Harry Hole",
		"Alien™: The Novelizations":       "Alien: The Novelizations",
		"Star Trek : The Next Generation": "Star Trek: The Next Generation",
		"Roger Zelazny":                   "",
	} {
		row, ok := odd[name]
		if !ok {
			t.Errorf("%q not reported as odd: %v", name, odd)
			continue
		}
		got, present := row["suggest"]
		if want == "" {
			if present {
				t.Errorf("%q suggest = %v, want none", name, got)
			}
		} else if str(t, got) != want {
			t.Errorf("%q suggest = %v, want %q", name, got, want)
		}
	}
	if _, reported := odd["Siege of Terra: The Horus Heresy"]; reported {
		t.Error("a subtitle is taste, not an oddity, and was reported")
	}
}

func TestOddSeriesName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, author string
		problems     []string
		suggest      string
	}{
		{"Discworld", "Terry Pratchett", nil, ""},
		{"Discworld: Death", "Terry Pratchett", nil, ""},
		{"Siege of Terra: The Horus Heresy", "John French", nil, ""}, // a subtitle is taste, not an error
		{"Mars Trilogy", "Kim Stanley Robinson", nil, ""},
		{"Dragonlance Tales II", "Margaret Weis", nil, ""},
		{"Memory, Sorrow & Thorn", "Tad Williams", nil, ""},
		{"The Bridge Kingdom Series", "Danielle L. Jensen", []string{"redundant_word"}, "The Bridge Kingdom"},
		{"Roger Zelazny", "Roger Zelazny", []string{"named_after_author"}, ""},
		{"Dune Book 1", "Frank Herbert", []string{"sequence_in_name"}, ""},
		{"Alien™: The Novelizations", "Alan Dean Foster", []string{"stray_characters"}, "Alien: The Novelizations"},
		{"Star Trek : The Next Generation", "David Mack", []string{"stray_characters"}, "Star Trek: The Next Generation"},
		{"Xanth  ", "Piers Anthony", []string{"stray_characters"}, "Xanth"},
	} {
		problems, suggest := oddSeriesName(tc.name, tc.author)
		if !slices.Equal(problems, tc.problems) || suggest != tc.suggest {
			t.Errorf("oddSeriesName(%q) = %v, %q; want %v, %q", tc.name, problems, suggest, tc.problems, tc.suggest)
		}
	}
}

// The folder is the collector's record: a book numbered against it, a book
// its folder puts in a series it is not in, and two titles at one number.
func TestAuditSeriesNumbering(t *testing.T) {
	t.Parallel()

	book := func(id, title, relPath, series string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"` + relPath + `","media":{"metadata":{"title":"` + title + `","series":[` + series + `]}}}`
	}
	ref := func(name, seq string) string { return `{"id":"x","name":"` + name + `","sequence":"` + seq + `"}` }
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/items", page(
		book("i1", "The Hitchhiker's Guide To The Galaxy", "Douglas Adams/The Hitchhiker's Guide to the Galaxy - 1 - The Hitchhiker's Guide to the Galaxy", ref("Hitchhiker's Guide to the Galaxy", "4")),
		book("i2", "So Long, And Thanks For All The Fish", "Douglas Adams/The Hitchhiker's Guide to the Galaxy - 4 - So Long And Thanks For All The Fish", ref("Hitchhiker's Guide to the Galaxy", "4")),
		book("i3", "The Executioner and Her Way of Life, Vol. 02", "The Executioner and Her Way of Life/The Executioner and Her Way of Life, Vol. 02 - Whiteout", ""),
		book("i4", "The Executioner and Her Way of Life, Vol. 01", "The Executioner and Her Way of Life/The Executioner and Her Way of Life, Vol. 01 - Thus", ref("The Executioner and Her Way of Life", "1")),
		book("i5", "All Systems Red", "Martha Wells/The Murderbot Diaries - 1 - All Systems Red", ref("The Murderbot Diaries", "1")),
		book("i6", "All Systems Red (Dramatized)", "Martha Wells/The Murderbot Diaries - 1 - All Systems Red (Dramatization)", ref("The Murderbot Diaries", "1")),
		book("i7", "Standalone", "Someone/Standalone", ""),
		book("i8", "Padded", "Roger Zelazny/The Chronicles of Amber - 02 - The Guns of Avalon", ref("The Chronicles of Amber", "02")),
		book("i10", "Nine Princes", "Roger Zelazny/The Chronicles of Amber - 1 - Nine Princes in Amber", ref("The Chronicles of Amber", "")),
		book("i11", "Rogue Protocol: The Murderbot Diaries, Book 3", "Martha Wells/The Murderbot Diaries - 3 - Rogue Protocol", ref("The Murderbot Diaries", "3")),
		book("i12", "Rogue Protocol (Dramatized)", "Martha Wells/The Murderbot Diaries - 3 - Rogue Protocol (Dramatization)", ref("The Murderbot Diaries", "3")),
		// the minified listing joins series into one string
		`{"id":"i9","libraryId":"`+libID+`","mediaType":"book","relPath":"Isaac Asimov/Foundation - 3 - Second Foundation","media":{"metadata":{"title":"Second Foundation","seriesName":"Foundation #2, Robot #9"}}}`,
	))
	call := toolCaller(t, f)

	out, err := call("audit_series", nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, ok := out["counts"].(map[string]any)
	if !ok || num(t, counts["numbering"]) != 6 {
		t.Fatalf("counts = %v, want 6 numbering findings: %v", counts, out["numbering"])
	}
	byProblem := map[string]map[string]any{}
	for _, row := range list(t, out["numbering"]) {
		byProblem[str(t, row["problem"])] = row
	}
	disagree := map[string]string{}
	for _, row := range list(t, out["numbering"]) {
		if str(t, row["problem"]) == "folder_disagrees" {
			disagree[str(t, row["title"])] = str(t, row["suggest"])
		}
	}
	if disagree["The Hitchhiker's Guide To The Galaxy"] != "Hitchhiker's Guide to the Galaxy #1" {
		t.Errorf("folder_disagrees = %v, want the first book suggested at #1 under the series' own spelling", disagree)
	}
	if disagree["Second Foundation"] != "Foundation #3" {
		t.Errorf("folder_disagrees = %v, want the minified series string read and Foundation #3 suggested", disagree)
	}
	// the folder says 02, the series writes its numbers unpadded, so #2
	if row := byProblem["unlinked"]; row == nil || str(t, row["title"]) != "The Executioner and Her Way of Life, Vol. 02" || str(t, row["suggest"]) != "The Executioner and Her Way of Life #2" {
		t.Errorf("unlinked = %v, want volume 2 suggested into its folder's series in the series' own style", row)
	}
	if row := byProblem["duplicate_sequence"]; row == nil || !strings.Contains(str(t, row["suggest"]), "Hitchhiker's Guide") {
		t.Errorf("duplicate_sequence = %v, want the two different titles at #4, not the two editions at #1 or the subtitled and dramatized Rogue Protocol at #3", row)
	}
	if n := 0; true {
		for _, row := range list(t, out["numbering"]) {
			if str(t, row["problem"]) == "duplicate_sequence" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%d duplicate_sequence findings, want 1", n)
		}
	}
	// a book in its folder's series with no number, suggested in the style
	// a two-book series uses: one digit, whatever its neighbour wrote
	if row := byProblem["unnumbered"]; row == nil || str(t, row["title"]) != "Nine Princes" || str(t, row["suggest"]) != "The Chronicles of Amber #1" {
		t.Errorf("unnumbered = %v, want Nine Princes suggested as #1", row)
	}
	// and the neighbour's "02" is the odd one out in a series of two
	if row := byProblem["padding"]; row == nil || str(t, row["title"]) != "Padded" || str(t, row["suggest"]) != "The Chronicles of Amber #2" {
		t.Errorf("padding = %v, want Padded (#02) suggested as #2 in a two-book series", row)
	}

	for _, tc := range []struct{ path, series, number string }{
		{"Douglas Adams/The Hitchhiker's Guide to the Galaxy - 1 - Title", "The Hitchhiker's Guide to the Galaxy", "1"},
		{"Series/Series, Vol. 02 - Title", "", "02"},
		{"Series/03 - Title", "", "03"},
		{"Series/Book 12: Title", "", "12"},
		{"Author/Plain Title", "", ""},
		{"Warhammer 40k - The Horus Heresy/The Horus Heresy - 25 - Mark of Calth", "The Horus Heresy", "25"},
	} {
		series, number := folderNumber(tc.path)
		if series != tc.series || number != tc.number {
			t.Errorf("folderNumber(%q) = %q, %q; want %q, %q", tc.path, series, number, tc.series, tc.number)
		}
	}
}
