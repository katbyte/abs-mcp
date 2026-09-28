package tools

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// wholeWithFiles is a book fetched whole, holding the files named, each by
// its path in the book's folder.
func wholeWithFiles(t *testing.T, id, title string, files ...string) string {
	t.Helper()

	return wholeAt(t, id, "", title, files...)
}

// wholeAt is wholeWithFiles at a path in the library.
func wholeAt(t *testing.T, id, relPath, title string, files ...string) string {
	t.Helper()

	lf := make([]string, 0, len(files))
	for i, rel := range files {
		name, err := json.Marshal(path.Base(rel))
		if err != nil {
			t.Fatal(err)
		}
		fileRel, err := json.Marshal(rel)
		if err != nil {
			t.Fatal(err)
		}
		lf = append(lf, fmt.Sprintf(`{"ino":"%d","metadata":{"filename":%s,"relPath":%s}}`, i, name, fileRel))
	}
	return fmt.Sprintf(`{"id":%q,"libraryId":%q,"relPath":%q,"mediaType":"book","media":{"metadata":{"title":%q}},"libraryFiles":[%s]}`,
		id, libID, relPath, title, strings.Join(lf, ","))
}

// nameLists serves a library's own author, series and narrator lists.
func nameLists(f *fakeABS, authors, series, narrators string) {
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[`+authors+`],"total":`+strconv.Itoa(strings.Count(authors, `"id"`))+`}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[`+series+`],"total":`+strconv.Itoa(strings.Count(series, `"name"`))+`}`)
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[`+narrators+`]}`)
}

// whitespaceRows reads audit_whitespace's rows keyed where|problem|what, what
// being the value of a name row or the id of an item's.
func whitespaceRows(t *testing.T, out map[string]any) (rows map[string]map[string]any, order []string) {
	t.Helper()

	rows = map[string]map[string]any{}
	findings := list(t, out["findings"])
	order = make([]string, 0, len(findings))
	for _, row := range findings {
		what := str(t, row["id"])
		if row["value"] != nil {
			what = str(t, row["value"])
		}
		key := str(t, row["where"]) + "|" + str(t, row["problem"]) + "|" + what
		rows[key] = row
		order = append(order, key)
	}
	return rows, order
}

// The shapes the zbooks sort found on the real library: double spaces in
// folders where a renamer dropped something, in audio file names, a space
// before a look-alike colon and before the extension, a title with a double
// space, and names read from the library's own lists, a comma in a name
// kept whole. The ideographic space in a Japanese title is left alone.
func TestAuditWhitespace(t *testing.T) {
	t.Parallel()

	haruhi := "涼宮ハルヒの憂鬱\u3000第一巻"
	f := newFakeABS(t)
	oneLibrary(f)
	serveListing(f,
		shelved("siege", "Horus Heresy/Siege of Terra, Book 2 -  The Lost and the Damned", "The Lost and the Damned", "Guy Haley", 3600, "", ""),
		shelved("horus", "Horus Heresy/The Horus Heresy - 22  Shadows of Treachery", "Shadows of Treachery", "Various", 3600, "", ""),
		shelved("hole", "Jo Nesbo/Harry Hole - 06 -  The Redeemer [Sean Barrett]", "The Redeemer", "Jo Nesbo", 3600, `"narratorName":"Sean Barrett"`, ""),
		shelved("klein", "Naomi Klein/This Changes Everything", "This Changes Everything", "Naomi Klein", 3600, "", ""),
		shelved("carnegie", "Dale Carnegie/How to Win Friends & Influence People", "How to Win Friends & Influence People", "Dale Carnegie", 3600, "", ""),
		shelved("manager", "Camille Fournier/The Manager's Path", "The Manager's Path", "Camille Fournier", 3600, "", ""),
		shelved("rascal", "Hajime Kamoshida/Rascal Does Not Dream of a Dreaming Girl", "Rascal Does Not Dream of, Vol. 06:  a Dreaming Girl", "Hajime Kamoshida", 3600, "", ""),
		shelved("haruhi", "谷川流/"+haruhi, haruhi, "谷川流", 3600, "", ""),
		// the listing joins names with ", ": read from it, these would be
		// cut at the comma; the lists below carry them whole
		shelved("sand1", "Brandon Sanderson/Mistborn - 01 - The Final Empire", "The Final Empire", "Brandon  Sanderson", 3600, `"narratorName":"Jane  Doe, Ph.D.","seriesName":"Chronicles  of Amber, The #1"`, ""),
		`{"id":"eric","libraryId":"`+libID+`","mediaType":"book","relPath":"Discworld - 09 - Eric .m4b","isFile":true,"media":{"metadata":{"title":"Eric","authorName":"Terry Pratchett"},"duration":3600}}`,
	)
	nameLists(f,
		`{"id":"a1","name":"Brandon  Sanderson","numBooks":2},{"id":"a2","name":"Naomi Klein","numBooks":1}`,
		`{"id":"s1","name":"Chronicles  of Amber, The","books":[{"id":"sand1"},{"id":"x"}]},{"id":"s2","name":"Mistborn ","books":[{"id":"sand1"}]}`,
		`{"name":"Jane  Doe, Ph.D.","numBooks":2},{"name":"Michael\u00a0Kramer","numBooks":3},{"name":"Kate Reading","numBooks":4}`,
	)
	klein := "This Changes Everything -  Naomi Klein - Part %d.mp3"
	carnegie := "How to Win Friends & Influence People - Dale Carnegie - 07 - Part Two ꞉ Six Ways꞉ Chapter 3 ꞉  If You Don't Do This….m4b"
	manager := "The Manager's Path꞉ A Guide for Tech Leaders - Camille Fournier - 179 - End Credits .m4b"
	serveWhole(f, map[string]string{
		"siege":    wholeWithFiles(t, "siege", "The Lost and the Damned", "01 - The Lost and the Damned.mp3", "cover.jpg"),
		"horus":    wholeWithFiles(t, "horus", "Shadows of Treachery", "Shadows of Treachery.m4b"),
		"hole":     wholeWithFiles(t, "hole", "The Redeemer", "The Redeemer.m4b"),
		"klein":    wholeWithFiles(t, "klein", "This Changes Everything", fmt.Sprintf(klein, 1), fmt.Sprintf(klein, 2), fmt.Sprintf(klein, 3), fmt.Sprintf(klein, 4), "cover.jpg"),
		"carnegie": wholeWithFiles(t, "carnegie", "How to Win Friends & Influence People", carnegie),
		"manager":  wholeWithFiles(t, "manager", "The Manager's Path", manager, "notes .nfo", "cover.jpg"),
		"rascal":   wholeWithFiles(t, "rascal", "Rascal", "Rascal.m4b"),
		"haruhi":   wholeWithFiles(t, "haruhi", haruhi, haruhi+".m4b"),
		"sand1":    wholeWithFiles(t, "sand1", "The Final Empire", "The Final Empire.m4b"),
		"eric":     wholeWithFiles(t, "eric", "Eric", "Discworld - 09 - Eric .m4b"),
	})
	call := toolCaller(t, f)

	out, err := call("audit_whitespace", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, order := whitespaceRows(t, out)
	want := []string{
		"title|double_space|rascal",
		"author|double_space|Brandon  Sanderson",
		"narrator|odd_space|Michael\u00a0Kramer",
		"narrator|double_space|Jane  Doe, Ph.D.",
		"series|double_space|Chronicles  of Amber, The",
		"series|edge_space|Mistborn ",
		"folder|double_space|siege",
		"folder|double_space|horus",
		"folder|double_space|hole",
		"file|double_space|carnegie",
		"file|double_space|klein",
		"file|space_before_extension|eric",
		"file|space_before_extension|manager",
		"file|space_before_colon|carnegie",
	}
	if sorted, w := slices.Sorted(slices.Values(order)), slices.Sorted(slices.Values(want)); !slices.Equal(sorted, w) {
		t.Fatalf("rows = %v, want %v", order, want)
	}
	if num(t, out["total_findings"]) != len(want) || num(t, out["items_scanned"]) != 10 || num(t, out["files_read"]) != 10 {
		t.Errorf("total %v, scanned %v, read %v; want %d, 10, 10", out["total_findings"], out["items_scanned"], out["files_read"], len(want))
	}
	// by where, then by problem: the metadata first, the files last
	if order[0] != "title|double_space|rascal" || !strings.HasPrefix(order[len(order)-1], "file|space_before_colon|") {
		t.Errorf("order = %v, want the title first and the files by problem last", order)
	}
	wantNumbers(t, "audit_whitespace", out, map[string]float64{
		"counts.double_space": 9, "counts.odd_space": 1, "counts.edge_space": 1, "counts.space_before_extension": 2, "counts.space_before_colon": 1,
		"by_where.title": 1, "by_where.author": 1, "by_where.narrator": 2, "by_where.series": 2, "by_where.folder": 3, "by_where.file": 5,
	})

	for key, w := range map[string]struct{ text, suggest string }{
		"folder|double_space|siege":                     {"Siege of Terra, Book 2 -␣␣The Lost and the Damned", "Siege of Terra, Book 2 - The Lost and the Damned"},
		"folder|double_space|horus":                     {"The Horus Heresy - 22␣␣Shadows of Treachery", "The Horus Heresy - 22 Shadows of Treachery"},
		"title|double_space|rascal":                     {"Rascal Does Not Dream of, Vol. 06:␣␣a Dreaming Girl", "Rascal Does Not Dream of, Vol. 06: a Dreaming Girl"},
		"narrator|odd_space|Michael\u00a0Kramer":        {"Michael[U+00A0]Kramer", "Michael Kramer"},
		"narrator|double_space|Jane  Doe, Ph.D.":        {"Jane␣␣Doe, Ph.D.", "Jane Doe, Ph.D."},
		"series|double_space|Chronicles  of Amber, The": {"Chronicles␣␣of Amber, The", "Chronicles of Amber, The"},
		"series|edge_space|Mistborn ":                   {"Mistborn␣", "Mistborn"},
		"file|space_before_extension|eric":              {"Discworld - 09 - Eric␣.m4b", "Discworld - 09 - Eric.m4b"},
		"file|space_before_extension|manager":           {"The Manager's Path꞉ A Guide for Tech Leaders - Camille Fournier - 179 - End Credits␣.m4b", "The Manager's Path꞉ A Guide for Tech Leaders - Camille Fournier - 179 - End Credits.m4b"},
		"file|space_before_colon|carnegie": {
			"How to Win Friends & Influence People - Dale Carnegie - 07 - Part Two␣꞉ Six Ways꞉ Chapter 3␣꞉␣␣If You Don't Do This….m4b",
			"How to Win Friends & Influence People - Dale Carnegie - 07 - Part Two꞉ Six Ways꞉ Chapter 3꞉ If You Don't Do This….m4b",
		},
	} {
		row := rows[key]
		if row["text"] != w.text || row["suggest"] != w.suggest {
			t.Errorf("%s: text %q suggest %q, want %q and %q", key, row["text"], row["suggest"], w.text, w.suggest)
		}
	}

	// a name is its record: its id to fix it by, its book count, and no item
	for key, w := range map[string]struct {
		record string
		items  int
	}{
		"author|double_space|Brandon  Sanderson":        {"a1", 2},
		"series|edge_space|Mistborn ":                   {"s2", 1},
		"series|double_space|Chronicles  of Amber, The": {"s1", 2},
		"narrator|odd_space|Michael\u00a0Kramer":        {"", 3},
	} {
		row := rows[key]
		got := ""
		if row["record_id"] != nil {
			got = str(t, row["record_id"])
		}
		if got != w.record || num(t, row["items"]) != w.items || row["id"] != nil || row["path"] != nil {
			t.Errorf("%s = %v, want record %q, %d books and no item", key, row, w.record, w.items)
		}
	}
	if fix := str(t, rows["author|double_space|Brandon  Sanderson"]["fix"]); !strings.HasPrefix(fix, "author_edit author= the record_id") {
		t.Errorf("author fix = %q, want the id passed", fix)
	}
	// a book's files are one row per problem, with a count and examples
	klein4 := rows["file|double_space|klein"]
	if examples, ok := klein4["examples"].([]any); !ok || num(t, klein4["files"]) != 4 || len(examples) != 3 {
		t.Errorf("Klein's files = %v, want 4 files and 3 examples", klein4)
	}
	if row := rows["file|space_before_extension|manager"]; num(t, row["files"]) != 2 {
		t.Errorf("The Manager's Path files = %v, want the m4b and the .nfo", row)
	}
	if row := rows["file|double_space|carnegie"]; row["examples"] != nil {
		t.Errorf("one file carries examples: %v", row)
	}
	// the folder rows say where to look and what the gap may have been
	if row := rows["folder|double_space|siege"]; row["title"] != "The Lost and the Damned" || !strings.Contains(str(t, row["fix"]), "renamer dropped") {
		t.Errorf("folder row = %v, want the item's title beside it and the dropped-character warning", row)
	}
	if row := rows["title|double_space|rascal"]; !strings.HasPrefix(str(t, row["fix"]), "item_edit title=") {
		t.Errorf("title row fix = %v", row["fix"])
	}

	// audit_all counts the titles, names and folders, and the files only
	// with deep
	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	counted := func(all map[string]any) int {
		for _, row := range list(t, all["audits"]) {
			if row["audit"] == "audit_whitespace" {
				return num(t, row["found"])
			}
		}
		return 0
	}
	if n := counted(all); n != 9 {
		t.Errorf("audit_all counts audit_whitespace %d without deep, want the 9 titles, names and folders", n)
	}
	partial := list(t, all["partial"])
	if !slices.ContainsFunc(partial, func(row map[string]any) bool {
		return row["audit"] == "audit_whitespace" && strings.Contains(str(t, row["reason"]), "deep")
	}) {
		t.Errorf("partial = %v, want audit_whitespace and why", partial)
	}
	deep, err := call("audit_all", map[string]any{"deep": true})
	if err != nil {
		t.Fatal(err)
	}
	if n := counted(deep); n != len(want) {
		t.Errorf("audit_all deep counts audit_whitespace %d, want %d", n, len(want))
	}
}

// What the review found: a suggestion that is already taken, files in a
// disc folder, a record whose folder is gone, one folder name under two
// library folders, a podcast's author, and a name with nothing left.
func TestAuditWhitespaceTakenDiscsAndGone(t *testing.T) {
	t.Parallel()

	const rootA, rootB = "fa", "fb"
	book := func(id, folder, relPath, title string, extra string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","folderId":"` + folder + `","mediaType":"book","relPath":"` + relPath + `","media":{"metadata":{"title":"` + title + `"}}` + extra + `}`
	}
	f := newFakeABS(t)
	oneLibrary(f)
	serveListing(f,
		// the folder put right is taken: its author already has one
		book("mist", rootA, "Brandon  Sanderson/Mistborn", "Mistborn", ""),
		book("elan", rootA, "Brandon Sanderson/Elantris", "Elantris", ""),
		// two folders here put right to one name
		book("w1", rootA, "Wheel  of Time/Book", "The Eye of the World", ""),
		book("w2", rootA, "Wheel of Time /Book", "The Great Hunt", ""),
		// the same folder name under two library folders is two folders
		book("r1", rootA, "Robin  Hobb/Assassin's Apprentice", "Assassin's Apprentice", ""),
		book("r2", rootB, "Robin  Hobb/Royal Assassin", "Royal Assassin", ""),
		// a record whose folder is gone names nothing on disk
		book("gone", rootA, "Old  Name/Book", "Gone", `,"isMissing":true`),
		// files in disc folders, one taken, one with nothing left
		book("discs", rootA, "Ken Follett/The Pillars of the Earth", "The Pillars of the Earth", ""),
		book("discs2", rootA, "Ken Follett/World Without End", "World Without End", ""),
	)
	nameLists(f, "", "", "")
	serveWhole(f, map[string]string{
		"mist": wholeWithFiles(t, "mist", "Mistborn", "Mistborn.m4b"),
		"elan": wholeWithFiles(t, "elan", "Elantris", "Elantris.m4b"),
		"w1":   wholeWithFiles(t, "w1", "The Eye of the World", "a.m4b"),
		"w2":   wholeWithFiles(t, "w2", "The Great Hunt", "b.m4b"),
		"r1":   wholeWithFiles(t, "r1", "Assassin's Apprentice", "a.m4b"),
		"r2":   wholeWithFiles(t, "r2", "Royal Assassin", "b.m4b"),
		"discs": wholeAt(t, "discs", "Ken Follett/The Pillars of the Earth", "The Pillars of the Earth",
			"Disc 1 /Track 01 .mp3", "Disc 1 /Track 02 .mp3", "Disc 2 /Track 01 .mp3", "Disc 2 /Track 02 .mp3",
			"01 - Intro.mp3", "01 - Intro .mp3", " .m4b", "Chapter 1 .mp3 "),
		"discs2": wholeAt(t, "discs2", "Ken Follett/World Without End", "World Without End",
			"Disc 1 /Track 01 .mp3", "Disc 1 /Track 02 .mp3", "Disc 2 /Track 01 .mp3", "Disc 2 /Track 02 .mp3", "Disc 3 /Track 01 .mp3", "Disc 3 /Track 02 .mp3"),
	})
	call := toolCaller(t, f)

	out, err := call("audit_whitespace", map[string]any{"limit": 1000})
	if err != nil {
		t.Fatal(err)
	}
	folders := map[string]map[string]any{}
	files := map[string]map[string]any{}
	for _, row := range list(t, out["findings"]) {
		switch row["where"] {
		case "folder":
			folders[str(t, row["path"])+"|"+str(t, row["problem"])] = row
		case "file":
			files[str(t, row["id"])+"|"+str(t, row["problem"])] = row
		}
	}
	taken := func(row map[string]any) []string { return strs(t, row["taken"]) }

	if row := folders["Brandon  Sanderson|double_space"]; row == nil || len(taken(row)) != 1 || !strings.Contains(taken(row)[0], "already there") || !strings.Contains(str(t, row["fix"]), "merge the two by hand") {
		t.Errorf("Brandon  Sanderson beside Brandon Sanderson = %v, want its suggestion taken", row)
	}
	w1, w2 := folders["Wheel  of Time|double_space"], folders["Wheel of Time |edge_space"]
	if w1 == nil || w2 == nil || len(taken(w1)) != 1 || len(taken(w2)) != 1 || !strings.Contains(taken(w1)[0], "put right to that name too") {
		t.Errorf("two folders put right to one name = %v and %v, want both taken", w1, w2)
	}
	// one row per library folder, each with its own item, nothing taken
	robins := 0
	for _, row := range list(t, out["findings"]) {
		if row["where"] == "folder" && row["path"] == "Robin  Hobb" {
			robins++
			if num(t, row["items"]) != 1 || len(taken(row)) != 0 {
				t.Errorf("Robin  Hobb = %v, want one item and nothing taken", row)
			}
		}
	}
	if robins != 2 {
		t.Errorf("Robin  Hobb under two library folders gave %d rows, want 2", robins)
	}
	for k := range folders {
		if strings.HasPrefix(k, "Old  Name") {
			t.Errorf("a record whose folder is gone gave a folder row: %v", folders[k])
		}
	}
	// the disc folders are folders, each once, and their files are read
	// by their path in the book
	for _, disc := range []string{"Disc 1 ", "Disc 2 "} {
		if row := folders["Ken Follett/The Pillars of the Earth/"+disc+"|edge_space"]; row == nil || str(t, row["suggest"]) != strings.TrimSpace(disc) {
			t.Errorf("disc folder %q = %v", disc, row)
		}
	}
	ext := files["discs|space_before_extension"]
	if ext == nil || num(t, ext["files"]) != 7 {
		t.Fatalf("space before the extension = %v, want all 7 files, the four in disc folders among them", ext)
	}
	if examples := strs(t, ext["examples"]); !slices.Equal(examples, []string{"␣.m4b", "01 - Intro␣.mp3", "Chapter 1␣.mp3␣"}) {
		t.Errorf("examples = %v, want the first three by their path in the book", examples)
	}
	// six files in three disc folders, two names each: six files, shown
	// by their path, not two by their bare name
	if row := files["discs2|space_before_extension"]; row == nil || num(t, row["files"]) != 6 || str(t, row["text"]) != "Disc 1␣/Track 01␣.mp3" {
		t.Errorf("files in disc folders = %v, want 6 of them, by their path", row)
	}
	if got := taken(ext); len(got) == 0 || !slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "01 - Intro␣.mp3 → 01 - Intro.mp3") }) {
		t.Errorf("taken = %v, want 01 - Intro .mp3 onto the 01 - Intro.mp3 beside it", got)
	}
	edge := files["discs|edge_space"]
	if edge == nil || !strings.Contains(strings.Join(strs(t, edge["examples"]), "|")+str(t, edge["text"]), "Chapter 1␣.mp3␣") {
		t.Errorf("a space after the extension = %v, want the one before it marked too", edge)
	}
	for _, row := range list(t, out["findings"]) {
		if row["where"] == "file" && str(t, row["text"]) == "␣.m4b" && (str(t, row["suggest"]) != "" || !strings.Contains(str(t, row["fix"]), "name it by hand")) {
			t.Errorf("a name with only its extension left = %v, want no suggestion and a name by hand", row)
		}
	}
}

// A podcast's author is text on the item, fixed there, not a record.
func TestAuditWhitespacePodcastAuthor(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Pods","mediaType":"podcast"}]}`)
	serveListing(f,
		`{"id":"p1","libraryId":"`+libID+`","mediaType":"podcast","relPath":"Behind the Bastards","media":{"metadata":{"title":"Behind the Bastards","author":"Cool  Zone Media"}}}`,
		`{"id":"p2","libraryId":"`+libID+`","mediaType":"podcast","relPath":"It Could Happen Here","media":{"metadata":{"title":"It Could Happen Here","author":"Cool  Zone Media"}}}`,
	)
	call := toolCaller(t, f)

	out, err := call("audit_whitespace", nil)
	if err != nil {
		t.Fatal(err)
	}
	findings := list(t, out["findings"])
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want the one author", findings)
	}
	if row := findings[0]; row["where"] != "author" || num(t, row["items"]) != 2 || !strings.HasPrefix(str(t, row["fix"]), "item_edit podcast_author=") {
		t.Errorf("podcast author = %v, want one row for both, fixed with item_edit", row)
	}
}

// One name at a time: which problems, the spaces made visible, the name put
// right.
func TestWhitespaceProblems(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name          string
		file          bool
		problems      []string
		text, suggest string
	}{
		{"End Credits .m4b", true, []string{"space_before_extension"}, "End Credits␣.m4b", "End Credits.m4b"},
		{" 01 - Prologue.mp3", true, []string{"edge_space"}, "␣01 - Prologue.mp3", "01 - Prologue.mp3"},
		{"Prologue.mp3 ", true, []string{"edge_space"}, "Prologue.mp3␣", "Prologue.mp3"},
		{"Mr. Smith Goes", true, nil, "Mr. Smith Goes", "Mr. Smith Goes"},
		{"Dune ", false, []string{"edge_space"}, "Dune␣", "Dune"},
		{"Dune\tMessiah", false, []string{"odd_space"}, "Dune[U+0009]Messiah", "Dune Messiah"},
		{"Title : Subtitle", false, []string{"space_before_colon"}, "Title␣: Subtitle", "Title: Subtitle"},
		{"涼宮ハルヒの憂鬱\u3000第一巻", false, nil, "涼宮ハルヒの憂鬱\u3000第一巻", "涼宮ハルヒの憂鬱\u3000第一巻"},
		{"A  B .mp3", true, []string{"double_space", "space_before_extension"}, "A␣␣B␣.mp3", "A B.mp3"},
		{"Chapter Three\u00a0.m4b", true, []string{"odd_space", "space_before_extension"}, "Chapter Three[U+00A0].m4b", "Chapter Three.m4b"},
		{"\u00a0Dune", false, []string{"odd_space", "edge_space"}, "[U+00A0]Dune", "Dune"},
		// a space after the extension hides none before it
		{"Chapter 1 .mp3 ", true, []string{"edge_space", "space_before_extension"}, "Chapter 1␣.mp3␣", "Chapter 1.mp3"},
		// an odd space doubles and stands before a colon as a space does
		{"A\u00a0\u00a0B", false, []string{"odd_space", "double_space"}, "A[U+00A0][U+00A0]B", "A B"},
		{"A\u00a0 B", false, []string{"odd_space", "double_space"}, "A[U+00A0]␣B", "A B"},
		{"Title\u00a0: Sub", false, []string{"odd_space", "space_before_colon"}, "Title[U+00A0]: Sub", "Title: Sub"},
		// nothing left but spaces, or an extension
		{"   ", false, []string{"double_space", "edge_space"}, "␣␣␣", ""},
		{" .m4b", true, []string{"edge_space", "space_before_extension"}, "␣.m4b", ""},
	} {
		if got := whitespaceProblems(c.name, c.file); !slices.Equal(got, c.problems) {
			t.Errorf("%q: problems %v, want %v", c.name, got, c.problems)
		}
		if got := whitespaceVisible(c.name, c.file); got != c.text {
			t.Errorf("%q: visible %q, want %q", c.name, got, c.text)
		}
		if got := whitespaceFixed(c.name, c.file); got != c.suggest {
			t.Errorf("%q: fixed %q, want %q", c.name, got, c.suggest)
		}
	}
}

// A name put right has nothing left to put right: every suggestion checked
// again comes back clean.
func TestWhitespaceFixedIsClean(t *testing.T) {
	t.Parallel()

	names := []string{
		"Chapter 1 .mp3 ", "Chapter Three\u00a0.m4b", " .m4b", "A  B", "A  B .mp3", "Part Two ꞉ Six", "Part Two ꞉  Six ꞉ Seven .m4b",
		"Dune\tMessiah", "\tDune\t", "A\u00a0 B", "Title\u00a0: Sub", "涼宮\u3000 ハルヒ", "涼宮 \u3000ハルヒ .m4b", "\u3000 \u3000",
		" :Lead", "x.mp3 .m4b", "file.tar .gz", "Part 1. Intro .m4b", "Mr. Smith Goes ", ". hidden ", "A ꞉ ꞉ B", "  ",
		"Disc\u20021 ", "End\u202f.mp3", "Name\r\n.mp3", "The  Horus Heresy - 22  Shadows of Treachery",
	}
	for _, name := range names {
		for _, file := range []bool{false, true} {
			fixed := whitespaceFixed(name, file)
			if fixed == "" {
				continue
			}
			if got := whitespaceProblems(fixed, file); len(got) > 0 {
				t.Errorf("%q (file %v) put right as %q still has %v", name, file, fixed, got)
			}
		}
	}
}

// A library that hides one-book series leaves them out of its series list:
// the audit reads them from the books, fetched whole for the series record,
// id and all.
func TestAuditWhitespaceReadsSeriesTheListHides(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","settings":{"hideSingleBookSeries":true}}]}`)
	serveListing(f,
		shelved("m1", "Brandon Sanderson/Mistborn", "The Final Empire", "Brandon Sanderson", 3600, `"seriesName":"Mistborn  Era One #1"`, ""),
		shelved("w1", "Brandon Sanderson/Warbreaker", "Warbreaker", "Brandon Sanderson", 3600, `"seriesName":"Cosmere #3"`, ""))
	nameLists(f, `{"id":"a1","name":"Brandon Sanderson","numBooks":2}`, `{"id":"s-cos","name":"Cosmere","books":[{"id":"w1"}]}`, ``)
	serveWhole(f, map[string]string{
		"m1": `{"id":"m1","libraryId":"` + libID + `","relPath":"Brandon Sanderson/Mistborn","mediaType":"book","media":{"metadata":{"title":"The Final Empire","series":[{"id":"s-mist","name":"Mistborn  Era One","sequence":"1"}]}}}`,
		"w1": wholeWithFiles(t, "w1", "Warbreaker"),
	})
	out, err := toolCaller(t, f)("audit_whitespace", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := whitespaceRows(t, out)
	row := rows["series|double_space|Mistborn  Era One"]
	if row == nil || row["record_id"] != "s-mist" || num(t, row["items"]) != 1 || row["suggest"] != "Mistborn Era One" {
		t.Errorf("the hidden one-book series = %v, want its row with its record id (rows %v)", row, rows)
	}
}

// Two books that are one file each, beside each other, put right to one
// name: a plain rename of one then the other would overwrite the first.
func TestAuditWhitespaceSingleFilesPutRightToOneName(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	single := func(id, rel string, whole bool) string {
		files := ""
		if whole {
			files = `,"libraryFiles":[{"ino":"1","metadata":{"filename":` + strconv.Quote(rel) + `,"relPath":` + strconv.Quote(rel) + `}}]`
		}
		return `{"id":"` + id + `","libraryId":"` + libID + `","folderId":"fa","mediaType":"book","relPath":` + strconv.Quote(rel) + `,"isFile":true,"media":{"metadata":{"title":"Eric"}}` + files + `}`
	}
	a, b := "Discworld - 09 - Eric .m4b", "Discworld - 09 -  Eric.m4b"
	serveListing(f, single("e1", a, false), single("e2", b, false))
	nameLists(f, "", "", "")
	serveWhole(f, map[string]string{"e1": single("e1", a, true), "e2": single("e2", b, true)})
	out, err := toolCaller(t, f)("audit_whitespace", nil)
	if err != nil {
		t.Fatal(err)
	}
	taken := map[string]bool{}
	for _, row := range list(t, out["findings"]) {
		if row["where"] == "file" && row["taken"] != nil {
			taken[str(t, row["id"])] = true
		}
	}
	if !taken["e1"] || !taken["e2"] {
		t.Errorf("taken on %v, want both single-file books: each is put right to %q", taken, "Discworld - 09 - Eric.m4b")
	}
}

// A folder or file name that starts with a dot once its spaces go is given
// no suggestion: the server skips a name starting with a dot, and the book
// would vanish. A title may start with one, ".hack//Sign".
func TestWhitespaceFixedNeverHides(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		file bool
	}{{" .hack", false}, {" .m4b", true}, {"  .hidden folder", false}, {".hack  Sign", false}} {
		if got := diskFixed(c.name, c.file); got != "" {
			t.Errorf("diskFixed(%q) = %q, want no suggestion", c.name, got)
		}
	}
	if got := whitespaceFixed(" .hack//Sign", false); got != ".hack//Sign" {
		t.Errorf("a title starting with a dot = %q, want .hack//Sign", got)
	}
}

// A hidden one-book series spelled like a listed one but for a space is found
// by the book, not missed because the listing trims its name; and a book read
// whole for its files is not read again for its series.
func TestAuditWhitespaceHiddenSeriesBesideItsTwin(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","settings":{"hideSingleBookSeries":true}}]}`)
	serveListing(f,
		shelved("m1", "Brandon Sanderson/Mistborn 1", "The Final Empire", "Brandon Sanderson", 3600, `"seriesName":"Mistborn #1"`, ""),
		shelved("m2", "Brandon Sanderson/Mistborn 2", "The Well of Ascension", "Brandon Sanderson", 3600, `"seriesName":"Mistborn #2"`, ""),
		shelved("m3", "Brandon Sanderson/Mistborn 3", "The Hero of Ages", "Brandon Sanderson", 3600, `"seriesName":"Mistborn  #3"`, ""))
	nameLists(f, `{"id":"a1","name":"Brandon Sanderson","numBooks":3}`, `{"id":"s-tidy","name":"Mistborn","books":[{"id":"m1"},{"id":"m2"}]}`, ``)
	whole := func(id, series string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","relPath":"Brandon Sanderson/` + id + `","mediaType":"book","media":{"metadata":{"title":"` + id + `","series":[` + series + `]}}}`
	}
	serveWhole(f, map[string]string{
		"m1": whole("m1", `{"id":"s-tidy","name":"Mistborn","sequence":"1"}`),
		"m2": whole("m2", `{"id":"s-tidy","name":"Mistborn","sequence":"2"}`),
		"m3": whole("m3", `{"id":"s-sp","name":"Mistborn ","sequence":"3"}`),
	})
	out, err := toolCaller(t, f)("audit_whitespace", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := whitespaceRows(t, out)
	if row := rows["series|edge_space|Mistborn "]; row == nil || row["record_id"] != "s-sp" {
		t.Errorf("the hidden spaced series = %v, want its row (rows %v)", row, rows)
	}
	if n := len(f.requests("/api/items/batch/get")); n != 1 {
		t.Errorf("%d batch reads, want one: the books read for their files carry their series", n)
	}
}

// Only a book whose series, as the listing joins them, show a space out of
// place is read whole for a hidden series: without deep the books are not
// read for their files, and a tidy hidden series needs no read at all.
func TestAuditWhitespaceReadsOnlySpacedHiddenSeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","settings":{"hideSingleBookSeries":true}}]}`)
	serveListing(f,
		shelved("t1", "Brandon Sanderson/Elantris", "Elantris", "Brandon Sanderson", 3600, `"seriesName":"Elantris #1"`, ""),
		shelved("s1", "Brandon Sanderson/Warbreaker", "Warbreaker", "Brandon Sanderson", 3600, `"seriesName":"Warbreaker  #1"`, ""))
	nameLists(f, `{"id":"a1","name":"Brandon Sanderson","numBooks":2}`, ``, ``)
	serveWhole(f, map[string]string{
		"s1": `{"id":"s1","libraryId":"` + libID + `","relPath":"Brandon Sanderson/Warbreaker","mediaType":"book","media":{"metadata":{"title":"Warbreaker","series":[{"id":"s-w","name":"Warbreaker ","sequence":"1"}]}}}`,
	})
	client := f.client(t)
	libs, err := client.Libraries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sweep := whitespaceSweep{namesOnly: true}
	if err := client.ItemsAll(t.Context(), libs[0].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
		for i := range items {
			sweep.add(&items[i])
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if err := sweep.resolve(t.Context(), client); err != nil {
		t.Fatal(err)
	}
	reads := f.requests("/api/items/batch/get")
	asked := make([]string, 0, len(reads))
	for _, r := range reads {
		asked = append(asked, r.Body)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], `"s1"`) || strings.Contains(asked[0], `"t1"`) {
		t.Errorf("batch reads %v, want one for the spaced series' book alone", asked)
	}
	found := false
	for _, row := range sweep.rows {
		found = found || (row.Where == "series" && row.Value == "Warbreaker ")
	}
	if !found {
		t.Errorf("rows %v, want the hidden spaced series", sweep.rows)
	}
}
