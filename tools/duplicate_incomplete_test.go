package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// A copy with files missing beside the whole one is not a duplicate but a
// bad copy: kept out of the groups for its length, it comes back as
// incomplete when every track it has is one of the whole copy's. Another
// reading as far apart, whose tracks match none, is neither; nor is a single
// file, whose one length could match by chance.
func TestDuplicatesFindAnIncompleteCopy(t *testing.T) {
	t.Parallel()

	// tracks is n audio files, the i-th running 6000+10i seconds, from
	// the first given
	tracks := func(name string, from, n int) (string, int) {
		files := make([]string, 0, n)
		total := 0
		for i := from; i < from+n; i++ {
			files = append(files, fmt.Sprintf(`{"index":%d,"duration":%d,"metadata":{"filename":"%s %02d.mp3"}}`, i, 6000+10*i, name, i))
			total += 6000 + 10*i
		}
		return `"audioFiles":[` + strings.Join(files, ",") + `]`, total
	}
	wholeFiles, whole := tracks("Dune", 1, 12)
	partFiles, part := tracks("Dune", 1, 8)
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		shelfBook("whole", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", whole, 12, ""),
		shelfBook("part", "Frank Herbert/Dune (old rip)", "Dune", "Frank Herbert", "", part, 8, ""),
		// another reading a third shorter: its tracks match none of these
		shelfBook("f1", "Isaac Asimov/Foundation", "Foundation", "Isaac Asimov", "", 30000, 3, ""),
		shelfBook("f2", "Isaac Asimov/Foundation (2)", "Foundation", "Isaac Asimov", "", 20000, 2, ""),
		// one file, the length of one of the whole copy's three and nothing
		// else in common
		shelfBook("m1", "Isaac Asimov/Nightfall", "Nightfall", "Isaac Asimov", "", 30000, 3, ""),
		shelfBook("m2", "Isaac Asimov/Nightfall (single)", "Nightfall", "Isaac Asimov", "", 10000, 1, ""),
		// one file, the whole copy's first to the name
		shelfBook("s1", "Frank Herbert/Children of Dune", "Children of Dune", "Frank Herbert", "", 20000, 2, ""),
		shelfBook("s2", "Frank Herbert/Children of Dune (old)", "Children of Dune", "Frank Herbert", "", 10000, 1, ""),
		// a record whose folder is gone keeps the length it had: it groups
		// with the copy its file went to, and is audit_issues' to report
		`{"id":"gone","libraryId":"`+libID+`","mediaType":"book","relPath":"Terry Pratchett/Mort","isMissing":true,"media":{"metadata":{"title":"Mort","authorName":"Terry Pratchett"},"duration":1,"numTracks":1}}`,
		shelfBook("kept", "Terry Pratchett/Discworld - 04 - Mort", "Mort", "Terry Pratchett", "", 2, 2, ""),
	))
	files := map[string]string{
		"whole": wholeFiles, "part": partFiles,
		"f1": `"audioFiles":[{"index":1,"duration":10000},{"index":2,"duration":10000},{"index":3,"duration":10000}]`,
		"f2": `"audioFiles":[{"index":1,"duration":9000},{"index":2,"duration":11000}]`,
		"m1": `"audioFiles":[{"index":1,"duration":10000},{"index":2,"duration":10000},{"index":3,"duration":10000}]`,
		"m2": `"audioFiles":[{"index":1,"duration":10000}]`,
		"s1": `"audioFiles":[{"index":1,"duration":10000,"metadata":{"filename":"Children of Dune 1.mp3"}},{"index":2,"duration":10000,"metadata":{"filename":"Children of Dune 2.mp3"}}]`,
		"s2": `"audioFiles":[{"index":1,"duration":10000,"metadata":{"filename":"Children of Dune 1.mp3"}}]`,
	}
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var asked struct {
			IDs []string `json:"libraryItemIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := make([]string, 0, len(asked.IDs))
		for _, id := range asked.IDs {
			out = append(out, item(id, id, "", files[id]))
		}
		if _, err := fmt.Fprintf(w, `{"libraryItems":[%s]}`, strings.Join(out, ",")); err != nil {
			t.Error(err)
		}
	})
	call := toolCaller(t, f)

	out, err := call("audit_duplicates", nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := list(t, out["groups"])
	if len(groups) != 1 || str(t, list(t, groups[0]["items"])[0]["id"]) != "gone" {
		t.Errorf("groups = %v, want the two Morts alone: every other pair is more than 15%% apart", groups)
	}
	rows := list(t, out["incomplete"])
	if len(rows) != 2 || num(t, out["total_incomplete"]) != 2 || num(t, out["total_findings"]) != 3 {
		t.Fatalf("incomplete = %v, total_findings %v; want the old rips of Dune and Children of Dune, counted with the group", rows, out["total_findings"])
	}
	if items := list(t, rows[0]["items"]); str(t, items[0]["id"]) != "s2" || num(t, rows[0]["tracks_held"]) != 1 || !slices.Equal(strs(t, rows[0]["missing"]), []string{"Children of Dune 2.mp3"}) {
		t.Errorf("Children of Dune = %v, want the one-file copy holding the first of two", rows[0])
	}
	rows = rows[1:]
	row := rows[0]
	items := list(t, row["items"])
	if str(t, items[0]["id"]) != "part" || str(t, items[1]["id"]) != "whole" || num(t, row["tracks_held"]) != 8 || num(t, row["tracks_whole"]) != 12 {
		t.Errorf("row = %v, want part holding 8 of whole's 12", row)
	}
	if missing := strs(t, row["missing"]); strings.Join(missing, "|") != "Dune 09.mp3|Dune 10.mp3|Dune 11.mp3|Dune 12.mp3" {
		t.Errorf("missing = %v, want tracks 9 to 12", missing)
	}
	if why := str(t, row["why"]); !strings.Contains(why, "not a second copy but an incomplete one") {
		t.Errorf("why = %q", why)
	}
}

// Track matching follows play order, as the same rip with files missing
// keeps it, and when few tracks match, or few of the long copy's, each must
// be the same file by name or size: two tracks can match two of a CD rip's
// hundred by length alone.
func TestHeldTracksFollowPlayOrder(t *testing.T) {
	t.Parallel()

	files := func(lengths ...float64) []abs.AudioFile {
		out := make([]abs.AudioFile, 0, len(lengths))
		for i, l := range lengths {
			out = append(out, abs.AudioFile{Index: i + 1, Duration: l, Metadata: abs.FileMetadata{Filename: fmt.Sprintf("%02d.mp3", i+1)}})
		}
		return out
	}
	if held, missing, ok := heldTracks(files(100, 200, 300), files(100, 200, 300, 400)); !ok || held != 3 || !slices.Equal(missing, []string{"04.mp3"}) {
		t.Errorf("the first three of four = %d, %v, %v; want held, the fourth missing", held, missing, ok)
	}
	if _, _, ok := heldTracks(files(300, 100, 200), files(100, 200, 300, 400)); ok {
		t.Error("tracks out of the other copy's order were held")
	}
	if _, _, ok := heldTracks(files(100, 300), files(100, 200, 400)); ok {
		t.Error("a track matching none of the other copy's was held")
	}
	// two unrelated tracks the length of two of a hundred: not the same rip
	cd := make([]float64, 100)
	for i := range cd {
		cd[i] = 180 + float64(i)
	}
	two := []abs.AudioFile{{Index: 1, Duration: 200.4, Metadata: abs.FileMetadata{Filename: "extra 1.mp3"}}, {Index: 2, Duration: 245.6, Metadata: abs.FileMetadata{Filename: "extra 2.mp3"}}}
	if _, _, ok := heldTracks(two, files(cd...)); ok {
		t.Error("two tracks matching two of a hundred by length alone were held")
	}
}

// One incomplete copy is one row, against the fullest copy it is part of,
// however many whole copies there are; and a record whose folder is gone is
// neither the whole copy nor the incomplete one.
func TestIncompleteIsOneRowPerCopy(t *testing.T) {
	t.Parallel()

	tracks := func(n int) string {
		files := make([]string, 0, n)
		for i := 1; i <= n; i++ {
			files = append(files, fmt.Sprintf(`{"index":%d,"duration":%d,"metadata":{"filename":"%02d.mp3"}}`, i, 6000+10*i, i))
		}
		return `"audioFiles":[` + strings.Join(files, ",") + `]`
	}
	length := func(n int) int {
		total := 0
		for i := 1; i <= n; i++ {
			total += 6000 + 10*i
		}
		return total
	}
	gone := func(id, path string, n int) string {
		return fmt.Sprintf(`{"id":%q,"libraryId":%q,"mediaType":"book","relPath":%q,"isMissing":true,"media":{"metadata":{"title":"Emma","authorName":"Jane Austen"},"duration":%d,"numTracks":%d}}`, id, libID, path, length(n), n)
	}
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		shelfBook("whole1", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", length(12), 12, ""),
		shelfBook("whole2", "Frank Herbert/Dune (copy)", "Dune", "Frank Herbert", "", length(12), 12, ""),
		shelfBook("part", "Frank Herbert/Dune (old rip)", "Dune", "Frank Herbert", "", length(8), 8, ""),
		// a record whose folder is gone, its stale files one more than the
		// whole copy's: it would make the whole copy look incomplete
		gone("gone", "Jane Austen/Emma (old)", 13),
		shelfBook("emma", "Jane Austen/Emma", "Emma", "Jane Austen", "", length(12), 12, ""),
		shelfBook("emmapart", "Jane Austen/Emma (part)", "Emma", "Jane Austen", "", length(4), 4, ""),
	))
	files := map[string]string{"whole1": tracks(12), "whole2": tracks(12), "part": tracks(8), "gone": tracks(13), "emma": tracks(12), "emmapart": tracks(4)}
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var asked struct {
			IDs []string `json:"libraryItemIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := make([]string, 0, len(asked.IDs))
		for _, id := range asked.IDs {
			out = append(out, item(id, id, "", files[id]))
		}
		if _, err := fmt.Fprintf(w, `{"libraryItems":[%s]}`, strings.Join(out, ",")); err != nil {
			t.Error(err)
		}
	})
	out, err := toolCaller(t, f)("audit_duplicates", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["incomplete"])
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		items := list(t, row["items"])
		got = append(got, str(t, items[0]["id"])+" of "+str(t, items[1]["id"]))
	}
	slices.Sort(got)
	if want := []string{"emmapart of emma", "part of whole1"}; !slices.Equal(got, want) || num(t, out["total_incomplete"]) != 2 {
		t.Errorf("incomplete = %v (%v), want %v: one row a copy, and never the record whose folder is gone", got, out["total_incomplete"], want)
	}
}

// The whole copy anchors the group: of three rips with 12, 11 and 10 tracks,
// the 11 is a duplicate of the 12, 8% shorter, and only the 10, 17% shorter
// than the 12, is incomplete; neither is reported against the other rip.
func TestIncompleteAgainstTheWholeCopyOnly(t *testing.T) {
	t.Parallel()

	tracks := func(n int) string {
		files := make([]string, 0, n)
		for i := 1; i <= n; i++ {
			files = append(files, fmt.Sprintf(`{"index":%d,"duration":1000,"metadata":{"filename":"%02d.mp3"}}`, i, i))
		}
		return `"audioFiles":[` + strings.Join(files, ",") + `]`
	}
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		shelfBook("p10", "Frank Herbert/Dune (10)", "Dune", "Frank Herbert", "", 10000, 10, ""),
		shelfBook("p11", "Frank Herbert/Dune (11)", "Dune", "Frank Herbert", "", 11000, 11, ""),
		shelfBook("x", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 12000, 12, ""),
	))
	files := map[string]string{"p10": tracks(10), "p11": tracks(11), "x": tracks(12)}
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var asked struct {
			IDs []string `json:"libraryItemIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := make([]string, 0, len(asked.IDs))
		for _, id := range asked.IDs {
			out = append(out, item(id, id, "", files[id]))
		}
		if _, err := fmt.Fprintf(w, `{"libraryItems":[%s]}`, strings.Join(out, ",")); err != nil {
			t.Error(err)
		}
	})
	out, err := toolCaller(t, f)("audit_duplicates", nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := list(t, out["groups"])
	var grouped []string
	for _, g := range groups {
		for _, it := range list(t, g["items"]) {
			grouped = append(grouped, str(t, it["id"]))
		}
	}
	slices.Sort(grouped)
	rows := list(t, out["incomplete"])
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		items := list(t, row["items"])
		got = append(got, str(t, items[0]["id"])+" of "+str(t, items[1]["id"]))
	}
	if !slices.Equal(grouped, []string{"p11", "x"}) || !slices.Equal(got, []string{"p10 of x"}) {
		t.Errorf("grouped %v, incomplete %v; want the 11 grouped with the whole copy and the 10 incomplete against it", grouped, got)
	}
}

// A copy is judged incomplete only against a copy more than 15% longer than
// itself, not one whose group spreads that far: a rip 10% shorter than the
// middle copy is no incomplete copy of it, though the whole group is 21%
// apart.
func TestIncompleteOnlyAgainstAFarLongerCopy(t *testing.T) {
	t.Parallel()

	tracks := func(n, each int) string {
		files := make([]string, 0, n)
		for i := 1; i <= n; i++ {
			files = append(files, fmt.Sprintf(`{"index":%d,"duration":%d,"metadata":{"filename":"%02d.mp3"}}`, i, each, i))
		}
		return `"audioFiles":[` + strings.Join(files, ",") + `]`
	}
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		shelfBook("x", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 12000, 12, ""),
		shelfBook("y", "Frank Herbert/Dune (other rip)", "Dune", "Frank Herbert", "", 10500, 10, ""),
		shelfBook("z", "Frank Herbert/Dune (part)", "Dune", "Frank Herbert", "", 9450, 9, ""),
	))
	files := map[string]string{"x": tracks(12, 1000), "y": tracks(10, 1050), "z": tracks(9, 1050)}
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var asked struct {
			IDs []string `json:"libraryItemIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := make([]string, 0, len(asked.IDs))
		for _, id := range asked.IDs {
			out = append(out, item(id, id, "", files[id]))
		}
		if _, err := fmt.Fprintf(w, `{"libraryItems":[%s]}`, strings.Join(out, ",")); err != nil {
			t.Error(err)
		}
	})
	out, err := toolCaller(t, f)("audit_duplicates", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows := list(t, out["incomplete"]); len(rows) != 0 {
		t.Errorf("incomplete = %v, want none: the rip is 10%% shorter than the copy it matches", rows)
	}
}

// dupFixture serves a listing and, for the batch reads, each book's audio
// files; files maps an id to its tracks' lengths.
func dupFixture(t *testing.T, listing []string, files map[string][]float64) map[string]any {
	t.Helper()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(listing...))
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var asked struct {
			IDs []string `json:"libraryItemIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := make([]string, 0, len(asked.IDs))
		for _, id := range asked.IDs {
			tracks := make([]string, 0, len(files[id]))
			for i, l := range files[id] {
				tracks = append(tracks, fmt.Sprintf(`{"index":%d,"duration":%g,"metadata":{"filename":"%02d.mp3"}}`, i+1, l, i+1))
			}
			out = append(out, item(id, id, "", `"audioFiles":[`+strings.Join(tracks, ",")+`]`))
		}
		if _, err := fmt.Fprintf(w, `{"libraryItems":[%s]}`, strings.Join(out, ",")); err != nil {
			t.Error(err)
		}
	})
	out, err := toolCaller(t, f)("audit_duplicates", nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// dupShape is the groups, incomplete rows and candidate pairs of an answer,
// each as sorted ids.
func dupShape(t *testing.T, out map[string]any) (groups, incomplete, candidates []string) {
	t.Helper()

	ids := func(items []map[string]any, sep string) string {
		got := make([]string, 0, len(items))
		for _, it := range items {
			got = append(got, str(t, it["id"]))
		}
		if sep == "+" {
			slices.Sort(got)
		}
		return strings.Join(got, sep)
	}
	for _, g := range list(t, out["groups"]) {
		groups = append(groups, ids(list(t, g["items"]), "+"))
	}
	for _, r := range list(t, out["incomplete"]) {
		incomplete = append(incomplete, ids(list(t, r["items"]), " of "))
	}
	for _, c := range list(t, out["candidates"]) {
		candidates = append(candidates, ids(list(t, c["items"]), "+"))
	}
	slices.Sort(groups)
	slices.Sort(incomplete)
	slices.Sort(candidates)
	return groups, incomplete, candidates
}

// An incomplete copy is a bad copy, not a duplicate: it leaves the groups,
// so the whole copies it stretched join again, and the other reading it
// was grouped with stands alone.
func TestIncompleteCopiesLeaveTheGroups(t *testing.T) {
	t.Parallel()

	twelve := make([]float64, 12)
	for i := range twelve {
		twelve[i] = 6000 + float64(i)
	}
	// one asin on the whole copy and the rip of it, none on a second whole copy
	groups, incomplete, candidates := dupShape(t, dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 72066, 12, "B002V1OF70"),
		shelfBook("p", "Frank Herbert/Dune (old)", "Dune", "Frank Herbert", "", 48028, 8, "B002V1OF70"),
		shelfBook("c", "Frank Herbert/Dune (copy)", "Dune", "Frank Herbert", "", 72066, 12, ""),
	}, map[string][]float64{"a": twelve, "p": twelve[:8], "c": twelve}))
	if !slices.Equal(groups, []string{"a+c"}) || len(incomplete) != 1 || !strings.HasPrefix(incomplete[0], "p of ") || len(candidates) != 0 {
		t.Errorf("groups %v, incomplete %v, candidates %v; want the two whole copies one group and the rip incomplete", groups, incomplete, candidates)
	}
	// the rip beside another reading its length: not grouped with it
	groups, incomplete, _ = dupShape(t, dupFixture(t, []string{
		shelfBook("w", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 72066, 12, ""),
		shelfBook("r", "Frank Herbert/Dune (2)", "Dune", "Frank Herbert", "", 49015, 5, ""),
		shelfBook("p", "Frank Herbert/Dune (old)", "Dune", "Frank Herbert", "", 48028, 8, ""),
	}, map[string][]float64{"w": twelve, "r": {9803, 9803, 9803, 9803, 9803}, "p": twelve[:8]}))
	if len(groups) != 0 || !slices.Equal(incomplete, []string{"p of w"}) {
		t.Errorf("groups %v, incomplete %v; want no group and the rip incomplete against the whole copy", groups, incomplete)
	}
}

// Of two editions in reach, a copy with no asin joins the one it matches,
// not the longest.
func TestUnmatchedCopyJoinsTheEditionItMatches(t *testing.T) {
	t.Parallel()

	groups, _, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("e1", "Frank Herbert/Dune (Full Cast)", "Dune", "Frank Herbert", "Full Cast", 79200, 1, "B0FULLCAST"),
		shelfBook("e2", "Frank Herbert/Dune", "Dune", "Frank Herbert", "Scott Brick", 72000, 1, "B002V1OF70"),
		shelfBook("u", "Frank Herbert/Dune (2)", "Dune", "Frank Herbert", "", 72000, 1, ""),
	}, nil))
	if !slices.Equal(groups, []string{"e2+u"}) {
		t.Errorf("groups %v, want the unmatched copy with the edition its length matches", groups)
	}
}

// Every copy a title joins is somewhere: grouped, or in a split row saying
// what kept the parts apart. Two readers the library knows are two readings;
// a note on the copy, "(Audible)", names no reader; one bad title on two
// books splits on its folders.
func TestEveryCopyATitleJoinsIsAccountedFor(t *testing.T) {
	t.Parallel()

	out := dupFixture(t, []string{
		// two readers the library knows: two readings
		shelfBook("k1", "Frank Herbert/Children of Dune (Scott Brick)", "Children of Dune", "Frank Herbert", "", 72000, 1, ""),
		shelfBook("k2", "Frank Herbert/Children of Dune (Simon Vance)", "Children of Dune", "Frank Herbert", "", 72010, 1, ""),
		shelfBook("n1", "Frank Herbert/God Emperor of Dune", "God Emperor of Dune", "Frank Herbert", "Scott Brick", 50000, 1, ""),
		shelfBook("n2", "Frank Herbert/Heretics of Dune", "Heretics of Dune", "Frank Herbert", "Simon Vance", 60000, 1, ""),
		// a reader beside a note on the copy: one recording
		shelfBook("a1", "Frank Herbert/Dune (Scott Brick)", "Dune", "Frank Herbert", "", 72000, 1, ""),
		shelfBook("a2", "Frank Herbert/Dune (Audible)", "Dune", "Frank Herbert", "", 72010, 1, ""),
		// one bad title on two books
		shelfBook("f1", "Isaac Asimov/Foundation", "Foundation", "Isaac Asimov", "", 31200, 1, ""),
		shelfBook("f2", "Isaac Asimov/Foundation and Empire", "Foundation", "Isaac Asimov", "", 34000, 1, ""),
	}, nil)
	groups, _, candidates := dupShape(t, out)
	if !slices.Equal(groups, []string{"a1+a2"}) || len(candidates) != 0 {
		t.Errorf("groups %v, candidates %v; want the reader and the note one group, and no candidate for a title's copies", groups, candidates)
	}
	why := map[string]string{}
	for _, row := range list(t, out["split"]) {
		why[str(t, row["key"])] = fmt.Sprint(row["why"])
		if parts, ok := row["parts"].([]any); !ok || len(parts) != 2 {
			t.Errorf("%v split into %v, want 2 parts", row["key"], row["parts"])
		}
	}
	if !strings.Contains(why["title:children of dune|frank herbert"], "different readers") || !strings.Contains(why["title:foundation|isaac asimov"], "folders name different books") || len(why) != 2 || num(t, out["total_split"]) != 2 {
		t.Errorf("split why = %v, want the two readers and the bad title, and nothing else", why)
	}
}

// One book is one group however its folders are spelled - an article, "&",
// the server's narrator braces, a note on the copy. Numbered in two orders,
// it is kept apart, and shown in split saying so.
func TestOneBookUnderFoldersSpelledTwoWays(t *testing.T) {
	t.Parallel()

	groups, _, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("h1", "J.R.R. Tolkien/The Hobbit {Andy Serkis}", "The Hobbit", "J.R.R. Tolkien", "", 36000, 1, ""),
		shelfBook("h2", "J.R.R. Tolkien/Hobbit - Copy", "The Hobbit", "J.R.R. Tolkien", "", 36010, 1, ""),
		shelfBook("l1", "Radclyffe/Love & Honor", "Love & Honor", "Radclyffe", "", 30000, 1, ""),
		shelfBook("l2", "Radclyffe/Love and Honor", "Love & Honor", "Radclyffe", "", 30010, 1, ""),
		`{"id":"n6","libraryId":"` + libID + `","mediaType":"book","relPath":"C. S. Lewis/The Magician's Nephew","media":{"metadata":{"title":"The Magician's Nephew","authorName":"C. S. Lewis","seriesName":"The Chronicles of Narnia #6"},"duration":20000,"numTracks":1}}`,
		`{"id":"n1","libraryId":"` + libID + `","mediaType":"book","relPath":"C. S. Lewis/Magician's Nephew","media":{"metadata":{"title":"The Magician's Nephew","authorName":"C. S. Lewis","seriesName":"The Chronicles of Narnia #1"},"duration":20000,"numTracks":1}}`,
	}, nil))
	if want := []string{"h1+h2", "l1+l2"}; !slices.Equal(groups, want) {
		t.Errorf("groups %v, want %v", groups, want)
	}
}

// An incomplete copy is judged against the copy it was cut from even when
// its group's longest copy is another rip, one file; and against the copy
// that shares its asin.
func TestIncompleteAgainstTheCopyItWasCutFrom(t *testing.T) {
	t.Parallel()

	twelve := make([]float64, 12)
	for i := range twelve {
		twelve[i] = 6000 + float64(i)
	}
	_, incomplete, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 72066, 12, ""),
		shelfBook("e", "Frank Herbert/Dune (one file)", "Dune", "Frank Herbert", "", 72070, 1, ""),
		shelfBook("p", "Frank Herbert/Dune (old)", "Dune", "Frank Herbert", "", 48028, 8, ""),
	}, map[string][]float64{"a": twelve, "e": {72070}, "p": twelve[:8]}))
	if !slices.Equal(incomplete, []string{"p of a"}) {
		t.Errorf("incomplete %v, want the rip against the copy it was cut from", incomplete)
	}
	_, incomplete, _ = dupShape(t, dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 72066, 12, "B002V1OF70"),
		shelfBook("p", "Frank Herbert/Dune (old)", "Dune", "Frank Herbert", "", 48028, 8, "B002V1OF70"),
	}, map[string][]float64{"a": twelve, "p": twelve[:8]}))
	if !slices.Equal(incomplete, []string{"p of a"}) {
		t.Errorf("incomplete %v, want the rip that shares the whole copy's asin", incomplete)
	}
}

// Readers are judged group against group: a copy read by two joins copies
// read by either. A placeholder in the narrator field names no reader.
func TestReadersPooledAndPlaceholdersNone(t *testing.T) {
	t.Parallel()

	groups, _, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("p", "Frank Herbert/Dune", "Dune", "Frank Herbert", "Scott Brick", 72000, 1, ""),
		shelfBook("q", "Frank Herbert/Dune - Copy", "Dune", "Frank Herbert", "Scott Brick, Simon Vance", 72000, 1, ""),
		shelfBook("r", "Frank Herbert/Dune - Copy 2", "Dune", "Frank Herbert", "Simon Vance", 72000, 1, ""),
		shelfBook("u", "Frank Herbert/Children of Dune", "Children of Dune", "Frank Herbert", "Scott Brick", 70000, 1, ""),
		shelfBook("v", "Frank Herbert/Children of Dune - Copy", "Children of Dune", "Frank Herbert", "Unknown", 76000, 1, ""),
		shelfBook("w", "Frank Herbert/Children of Dune - Copy 2", "Children of Dune", "Frank Herbert", "Full Cast", 73000, 1, ""),
	}, nil))
	if want := []string{"p+q+r", "u+v+w"}; !slices.Equal(groups, want) {
		t.Errorf("groups %v, want %v", groups, want)
	}
}

// A narrator field's "Last, First" pair of single words is one reader: two
// copies read by "Barrett, Sean" and "Connery, Sean" share no reader.
func TestReaderNamesReadLastFirstWhole(t *testing.T) {
	t.Parallel()

	for field, want := range map[string][]string{
		"Barrett, Sean":                {"Sean Barrett"},
		"Brick, Scott, Vance, Simon":   {"Scott Brick", "Simon Vance"},
		"Kate Reading, Michael Kramer": {"Kate Reading", "Michael Kramer"},
		"Scott Brick":                  {"Scott Brick"},
	} {
		if got := readerNames(field); !slices.Equal(got, want) {
			t.Errorf("readerNames(%q) = %v, want %v", field, got, want)
		}
	}
	groups, _, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "Barrett, Sean", 30000, 1, ""),
		shelfBook("b", "Frank Herbert/Dune - Copy", "Dune", "Frank Herbert", "Connery, Sean", 30000, 1, ""),
	}, nil))
	if len(groups) != 0 {
		t.Errorf("groups %v, want none: Sean Barrett and Sean Connery are two readers", groups)
	}
}

// Two copies of one title kept apart only on evidence a listen settles, and
// near enough in length for one recording, are a candidate as well as a
// split row: A Planet Called Treason, retitled Treason, 0.7% apart.
func TestCloseCopiesKeptApartAreACandidate(t *testing.T) {
	t.Parallel()

	out := dupFixture(t, []string{
		shelfBook("d1", "Orson Scott Card/A Planet Called Treason", "Treason", "Orson Scott Card", "", 38783, 1, ""),
		shelfBook("d2", "Orson Scott Card/Treason", "Treason", "Orson Scott Card", "", 39054, 1, ""),
		// kept apart as surely, and far apart: split alone
		shelfBook("f1", "Isaac Asimov/Foundation", "Foundation", "Isaac Asimov", "", 31200, 1, ""),
		shelfBook("f2", "Isaac Asimov/Foundation and Empire", "Foundation", "Isaac Asimov", "", 34000, 1, ""),
	}, nil)
	_, _, candidates := dupShape(t, out)
	if !slices.Equal(candidates, []string{"d1+d2"}) {
		t.Errorf("candidates %v, want the two Treasons", candidates)
	}
	if why := str(t, list(t, out["candidates"])[0]["why"]); !strings.Contains(why, "0.7% apart, though their folders name different books") {
		t.Errorf("why = %q", why)
	}
	if num(t, out["total_split"]) != 2 {
		t.Errorf("total_split = %v, want both titles", out["total_split"])
	}
}

// dupFixtureOut is dupFixture with each item's own title and asin free:
// an item line is id, path, title, seconds, tracks, asin.
func tracksOf(n int, each float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = each + float64(i)
	}
	return out
}

// An abridged copy an asin joins to the whole one does not stretch the
// title's length rule: a copy of the whole one's length still joins it.
func TestAsinGroupDoesNotStretchTheTitleRule(t *testing.T) {
	t.Parallel()

	groups, _, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 80000, 1, "B0DUNE0001"),
		shelfBook("x", "Frank Herbert/Dune (Abridged)", "Dune (Abridged)", "Frank Herbert", "", 30000, 1, "B0DUNE0001"),
		shelfBook("b", "Frank Herbert/Dune - Copy", "Dune", "Frank Herbert", "", 80000, 1, ""),
	}, nil))
	if !slices.Equal(groups, []string{"a+b+x"}) {
		t.Errorf("groups %v, want the copy of the whole one's length with it", groups)
	}
}

// A copy whose only title mate incomplete reports is still in the answer:
// a split row holds it, beside the bad copy.
func TestSplitKeepsTheMateOfAnIncompleteCopy(t *testing.T) {
	t.Parallel()

	twenty := tracksOf(20, 3000)
	out := dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 60190, 20, "B0DUNE0001"),
		shelfBook("p", "Frank Herbert/Dune Part One", "Dune Part One", "Frank Herbert", "", 30045, 10, "B0DUNE0001"),
		shelfBook("r", "Frank Herbert/Dune Part One (one file)", "Dune Part One", "Frank Herbert", "", 30445, 1, ""),
	}, map[string][]float64{"a": twenty, "p": twenty[:10], "r": {30445}})
	_, incomplete, _ := dupShape(t, out)
	if !slices.Equal(incomplete, []string{"p of a"}) {
		t.Errorf("incomplete %v, want the rip of the whole copy", incomplete)
	}
	placed := false
	for _, row := range list(t, out["split"]) {
		parts, ok := row["parts"].([]any)
		if !ok {
			t.Fatalf("parts = %v", row["parts"])
		}
		placed = placed || (row["key"] == "title:dune part one|frank herbert" && len(parts) == 1 && row["incomplete"] != nil)
	}
	if !placed {
		t.Errorf("split = %v, want the one-file copy's row beside the incomplete one", out["split"])
	}
}

// An asin's copies are read against its fullest copy, and a rip the first
// regroup leaves in a group is found by the next: none is left grouped as a
// duplicate.
func TestIncompleteCopiesInAnAsinGroup(t *testing.T) {
	t.Parallel()

	twenty := tracksOf(20, 3000)
	groups, incomplete, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("c1", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 60190, 20, "B0DUNE0001"),
		shelfBook("c2", "Frank Herbert/Dune (half)", "Dune (half)", "Frank Herbert", "", 30045, 10, "B0DUNE0001"),
		shelfBook("m", "Frank Herbert/Dune (less)", "Dune (less)", "Frank Herbert", "", 27036, 9, "B0DUNE0001"),
		shelfBook("one", "Frank Herbert/Dune (one file)", "Dune (one file)", "Frank Herbert", "", 58700, 1, "B0DUNE0001"),
	}, map[string][]float64{"c1": twenty, "c2": twenty[:10], "m": twenty[:9], "one": {58700}}))
	if !slices.Equal(incomplete, []string{"c2 of c1", "m of c1"}) || !slices.Equal(groups, []string{"c1+one"}) {
		t.Errorf("groups %v, incomplete %v; want both rips incomplete and the whole copies one group", groups, incomplete)
	}
}

// A folder that plays the book twice, its tracks and a file of the whole,
// holds nothing the copy of just the tracks lacks.
func TestDoubledFolderMakesNoIncompleteCopy(t *testing.T) {
	t.Parallel()

	ten := tracksOf(10, 3000)
	_, incomplete, _ := dupShape(t, dupFixture(t, []string{
		shelfBook("good", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 30045, 10, ""),
		shelfBook("twice", "Frank Herbert/Dune (m4b)", "Dune", "Frank Herbert", "", 60090, 11, ""),
	}, map[string][]float64{"good": ten, "twice": append(slices.Clone(ten), 30045)}))
	if len(incomplete) != 0 {
		t.Errorf("incomplete %v, want none: the longer folder plays the book twice", incomplete)
	}
}

// The incomplete row says what joined the pair: an asin, where the titles
// differ.
func TestIncompleteSaysWhatJoinedThePair(t *testing.T) {
	t.Parallel()

	twelve := tracksOf(12, 6000)
	out := dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "", 72066, 12, "B0DUNE0001"),
		shelfBook("p", "Unknown/Track 01", "Track 01", "Unknown", "", 48028, 8, "B0DUNE0001"),
	}, map[string][]float64{"a": twelve, "p": twelve[:8]})
	rows := list(t, out["incomplete"])
	if len(rows) != 1 || !strings.HasPrefix(str(t, rows[0]["why"]), "the same asin;") {
		t.Errorf("incomplete = %v, want one row saying the same asin joined them", rows)
	}
}

// A split row's reasons are what keeps its final parts apart: three
// readings the pooled readers join say nothing about the fourth copy's
// folder.
func TestSplitReasonsAreTheFinalParts(t *testing.T) {
	t.Parallel()

	out := dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "Scott Brick", 80000, 1, ""),
		shelfBook("b", "Frank Herbert/Dune - Copy", "Dune", "Frank Herbert", "Simon Vance", 80000, 1, ""),
		shelfBook("c", "Frank Herbert/Dune - Copy 2", "Dune", "Frank Herbert", "Scott Brick, Simon Vance", 80000, 1, ""),
		shelfBook("d", "Frank Herbert/Dune 1965 Edition", "Dune", "Frank Herbert", "", 75000, 1, ""),
	}, nil)
	rows := list(t, out["split"])
	if len(rows) != 1 {
		t.Fatalf("split = %v, want one row", rows)
	}
	why := fmt.Sprint(rows[0]["why"])
	if strings.Contains(why, "readers") || !strings.Contains(why, "folders name different books") {
		t.Errorf("why = %s, want the folder reason alone", why)
	}
}

// The false groups a sixth look found, each kept apart now: a narrator
// field's wrapper around a name, a format or source beside a reader in
// brackets, a reader beside a year, numbers a folder gives in words, a disc
// or a leading number, and an ebook beside the recording.
func TestRoundSixFalseGroupsKeptApart(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name       string
		a, b       string // folder and narrator field, "folder|narrator"
		secA, secB int
		tracksB    int
	}{
		{"a narrator field's wrapper", "Dune|Narrator..........Jim Dale", "Dune - Copy|Stephen Fry", 33900, 32580, 1},
		{"a format beside the reader", "Dune [Scott Brick] [FLAC]|", "Dune [George Guidall] [FLAC]|", 72000, 72010, 1},
		{"a source beside the reader", "Dune [Scott Brick] (Chirp)|", "Dune [George Guidall] (Chirp)|", 72000, 72010, 1},
		{"a language beside the reader", "Dune [Scott Brick] [English]|", "Dune [George Guidall] [English]|", 72000, 72010, 1},
		{"a reader beside a year", "Dune [Scott Brick 2007]|", "Dune [George Guidall 1993]|", 72000, 72010, 1},
		{"a leading number", "Honor/01 Honor|", "Honor/02 Honor|", 30000, 30100, 1},
		{"a number alone", "Honor/01|", "Honor/02|", 30000, 30100, 1},
		{"discs", "The Stand CD1|", "The Stand CD2|", 30000, 30100, 1},
		{"discs spelled out", "The Stand - Disc 1|", "The Stand - Disc 2|", 30000, 30100, 1},
		{"books in words", "Honor - Book One|", "Honor - Book Two|", 30000, 30100, 1},
		{"an ebook beside the recording", "Dune|", "Dune (ebook)|", 75000, 0, 0},
	} {
		fa, na, _ := strings.Cut(c.a, "|")
		fb, nb, _ := strings.Cut(c.b, "|")
		groups, _, _ := dupShape(t, dupFixture(t, []string{
			shelfBook("a", "Frank Herbert/"+fa, "Dune", "Frank Herbert", na, c.secA, 1, ""),
			shelfBook("b", "Frank Herbert/"+fb, "Dune", "Frank Herbert", nb, c.secB, c.tracksB, ""),
		}, nil))
		if len(groups) != 0 {
			t.Errorf("%s: groups %v, want none", c.name, groups)
		}
	}
	// and the ones that stay one book
	for _, c := range []struct{ name, a, b string }{
		{"one reader, one wrapped", "Dune|Narrator..........Jim Dale", "Dune - Copy|Jim Dale"},
		{"one reader beside a format", "Dune [Scott Brick] [FLAC]|", "Dune [Scott Brick]|"},
		{"one reader beside a year", "Dune [Scott Brick 2007]|", "Dune - Copy|Scott Brick"},
		{"one number, in words and in digits", "Dune - Book One|", "Dune - Book 1|"},
	} {
		fa, na, _ := strings.Cut(c.a, "|")
		fb, nb, _ := strings.Cut(c.b, "|")
		groups, _, _ := dupShape(t, dupFixture(t, []string{
			shelfBook("a", "Frank Herbert/"+fa, "Dune", "Frank Herbert", na, 72000, 1, ""),
			shelfBook("b", "Frank Herbert/"+fb, "Dune", "Frank Herbert", nb, 72010, 1, ""),
		}, nil))
		if !slices.Equal(groups, []string{"a+b"}) {
			t.Errorf("%s: groups %v, want one", c.name, groups)
		}
	}
}

// A close pair kept apart by its folders is no candidate when its own two
// copies name different readers, whatever a third copy bridging them says.
func TestClosePairNamingTwoReadersIsNoCandidate(t *testing.T) {
	t.Parallel()

	_, _, candidates := dupShape(t, dupFixture(t, []string{
		shelfBook("a", "Frank Herbert/Dune", "Dune", "Frank Herbert", "Scott Brick", 80000, 1, ""),
		shelfBook("b", "Frank Herbert/Dune - Copy", "Dune", "Frank Herbert", "Scott Brick, Simon Vance", 80000, 1, ""),
		shelfBook("m", "Frank Herbert/Dune 1965 Edition", "Dune", "Frank Herbert", "Simon Vance", 79000, 1, ""),
	}, nil))
	for _, c := range candidates {
		if c == "a+m" {
			t.Errorf("candidates %v, want no pair naming Scott Brick beside Simon Vance", candidates)
		}
	}
}
