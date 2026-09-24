package tools

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The narrator list that motivated these: every shape in it has to come back
// with the right kind and the right spelling to keep.
func TestAuditSpellingFindsNameShapes(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "One", `"narratorName":"Narrator..........Sean Barrett"`, ""),
		item("i2", "Two", `"narratorName":"Narrator..........Sean Barrett"`, ""),
		item("i3", "Three", `"narratorName":"Sean Barrett"`, ""),
		item("i4", "Four", `"narratorName":"Read by Gabrielle Baker"`, ""),
		item("i5", "Five", `"narratorName":"Fajer Al"`, ""),
		item("i6", "Six", `"narratorName":"Fajer Al-Kaisi"`, ""),
		item("i7", "Seven", `"narratorName":"Peter Whickam"`, ""),
		item("i8", "Eight", `"narratorName":"Peter Wickham"`, ""),
		item("i9", "Nine", `"narratorName":"Etienne Mailloux/Paul Ablaze"`, ""),
		item("i10", "Ten", `"narratorName":"Jane Doe, Ph.D."`, ""),
		item("i11", "Eleven", `"narratorName":"Jack Evans"`, ""),
		item("i12", "Twelve", `"narratorName":"Jack R. B. Evans"`, ""),
		item("i13", "Thirteen", `"narratorName":"Wil Wheaton"`, ""),
	))
	// the library holds "Ph.D." as a narrator of its own: a real fragment
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Jane Doe","numBooks":1},{"name":"Ph.D.","numBooks":1}]}`)
	call := toolCaller(t, f)

	if _, err := call("audit_spelling", map[string]any{"field": "narrators"}); err == nil {
		t.Error("audit_spelling took narrators, which audit_narrators owns")
	}
	out, err := call("audit_narrators", nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := list(t, out["names"])
	if got := num(t, out["total_findings"]); got != 7 || len(groups) != 7 || len(list(t, out["roles"])) != 0 {
		t.Fatalf("total_findings = %d, names = %d, want 7 and no roles: %v", got, len(groups), out)
	}

	type want struct{ kind, keep string }
	wants := map[string]want{ // keyed by the first spelling's value
		"Sean Barrett":                 {"affix", "Sean Barrett"},
		"Read by Gabrielle Baker":      {"affix", "Gabrielle Baker"},
		"Fajer Al-Kaisi":               {"contains", "Fajer Al-Kaisi"},
		"Peter Whickam":                {"near", "Peter Whickam"},
		"Etienne Mailloux/Paul Ablaze": {"split", ""},
		"Ph.D.":                        {"fragment", ""},
		"Jack R. B. Evans":             {"contains", "Jack R. B. Evans"},
	}
	seen := map[string]bool{}
	for _, g := range groups {
		sp := list(t, g["spellings"])
		first := str(t, sp[0]["value"])
		w, ok := wants[first]
		if !ok {
			t.Errorf("unexpected group led by %q: %v", first, g)
			continue
		}
		seen[first] = true
		if str(t, g["kind"]) != w.kind {
			t.Errorf("%s: kind = %v, want %s", first, g["kind"], w.kind)
		}
		if w.keep != "" {
			if keep := str(t, g["keep"]); keep != w.keep {
				t.Errorf("%s: keep = %q, want %q", first, keep, w.keep)
			}
		} else if keep, present := g["keep"]; present { // split and fragment have nothing to merge into
			t.Errorf("%s: keep = %v, want none", first, keep)
		}
		if w.kind == "split" {
			parts, ok := g["parts"].([]any)
			if !ok || len(parts) != 2 || parts[0] != "Etienne Mailloux" || parts[1] != "Paul Ablaze" {
				t.Errorf("split parts = %v", g["parts"])
			}
		}
	}
	for first := range wants {
		if !seen[first] {
			t.Errorf("no group led by %q", first)
		}
	}

	// the affix group keeps the clean spelling even though the wrapped one is
	// on more books
	for _, g := range groups {
		sp := list(t, g["spellings"])
		if str(t, sp[0]["value"]) == "Sean Barrett" {
			if len(sp) != 2 || num(t, sp[0]["items"]) != 1 || num(t, sp[1]["items"]) != 2 {
				t.Errorf("Sean Barrett spellings = %v, want the clean one first with 1 item, the wrapped one with 2", sp)
			}
		}
	}
}

// The listing joins a book's narrators and authors with ", ", so a name with a
// comma in it read as two: "Jane Doe, Ph.D." gave a "Ph.D." fragment that no
// narrator carries, and metadata_rename could not remove. The parts are put
// back together wherever the library holds them as one name, and a string
// that reads both ways is settled by the book's own record.
func TestAuditNarratorsReadsANameWithAComma(t *testing.T) {
	t.Parallel()

	books := func(extra ...string) *fakeABS {
		f := newFakeABS(t)
		oneLibrary(f)
		f.json("GET /api/libraries/"+libID+"/items", page(append([]string{
			item("i1", "One", `"narratorName":"Jane Doe, Ph.D.","authorName":"Some Writer"`, ""),
			item("i2", "Two", `"narratorName":"Jim Dale, Kate Reading","authorName":"Some Writer"`, ""),
			item("i3", "Three", `"narratorName":"Jim Dale","authorName":"Martin Luther King, Jr., Coretta Scott King"`, ""),
			item("i4", "Four", `"narratorName":"Martin Luther King, Jr.","authorName":"Some Writer"`, ""),
		}, extra...)...))
		f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1","name":"Some Writer"},{"id":"a2","name":"Martin Luther King, Jr."},{"id":"a3","name":"Coretta Scott King"}],"total":3}`)
		f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
		return f
	}
	groupsLedBy := func(t *testing.T, out map[string]any) map[string]string {
		t.Helper()
		led := map[string]string{}
		for _, g := range list(t, out["names"]) {
			led[str(t, list(t, g["spellings"])[0]["value"])] = str(t, g["kind"])
		}
		return led
	}

	// the library holds the name whole: no fragment, and the author and
	// narrator with a comma in their name is one person in two roles
	f := books()
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Jane Doe, Ph.D."},{"name":"Jim Dale"},{"name":"Kate Reading"},{"name":"Martin Luther King, Jr."}]}`)
	call := toolCaller(t, f)
	out, err := call("audit_narrators", nil)
	if err != nil {
		t.Fatal(err)
	}
	if led := groupsLedBy(t, out); len(led) != 0 {
		t.Errorf("names = %v, want none: every narrator is spelled one way", led)
	}
	roles := list(t, out["roles"])
	if len(roles) != 1 || roles[0]["name"] != "Martin Luther King, Jr." {
		t.Errorf("roles = %v, want Martin Luther King, Jr. alone, not his name's two halves", roles)
	}
	if got := f.requests("/api/items/batch/get"); len(got) != 0 {
		t.Errorf("a name read one way was fetched: %v", got)
	}
	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list(t, all["audits"]) {
		if row["audit"] == "audit_narrators" && num(t, row["found"]) != num(t, out["total_findings"]) {
			t.Errorf("audit_all counts %v narrator findings, audit_narrators %v", row["found"], out["total_findings"])
		}
	}

	// the library holds the name whole and in halves: each book's record says
	// which it carries, and the one that carries a real "Ph.D." has it found
	f = books(item("i5", "Five", `"narratorName":"Jane Doe, Ph.D.","authorName":"Some Writer"`, ""))
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Jane Doe, Ph.D."},{"name":"Jane Doe"},{"name":"Ph.D."},{"name":"Jim Dale"},{"name":"Kate Reading"},{"name":"Martin Luther King, Jr."}]}`)
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+
		item("i1", "One", `"narrators":["Jane Doe, Ph.D."],"authors":[{"id":"a1","name":"Some Writer"}]`, "")+`,`+
		item("i5", "Five", `"narrators":["Jane Doe","Ph.D."],"authors":[{"id":"a1","name":"Some Writer"}]`, "")+`]}`)
	out, err = toolCaller(t, f)("audit_narrators", nil)
	if err != nil {
		t.Fatal(err)
	}
	var asked struct {
		IDs []string `json:"libraryItemIds"`
	}
	if got := f.requests("/api/items/batch/get"); len(got) != 1 || json.Unmarshal([]byte(got[0].Body), &asked) != nil || !slices.Equal(asked.IDs, []string{"i1", "i5"}) {
		t.Errorf("fetched %v, want the two books whose names read both ways", got)
	}
	if led := groupsLedBy(t, out); led["Ph.D."] != "fragment" {
		t.Errorf("names = %v, want the real Ph.D. fragment", led)
	}
}

func TestReadJoined(t *testing.T) {
	t.Parallel()

	known := map[string]bool{"Jane Doe, Ph.D.": true, "Jim Dale": true, "A, B, C": true, "A": true, "B": true}
	for _, tc := range []struct {
		in   string
		want []string
		ok   bool
	}{
		{"Jane Doe, Ph.D., Jim Dale", []string{"Jane Doe, Ph.D.", "Jim Dale"}, true},
		{"Jim Dale, Jane Doe, Ph.D.", []string{"Jim Dale", "Jane Doe, Ph.D."}, true},
		{"A, B, C", []string{"A, B, C"}, true}, // A and B are names, C is not
		{"A, B", []string{"A", "B"}, true},
		{"Jim Dale, Someone New", nil, true}, // a name the lists do not hold yet: left to valuesOf
	} {
		if got, ok := readJoined(tc.in, known); ok != tc.ok || !slices.Equal(got, tc.want) {
			t.Errorf("readJoined(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	known["C"] = true
	if got, ok := readJoined("A, B, C", known); ok {
		t.Errorf("readJoined of a string that reads two ways = %q, want it fetched", got)
	}
}

// The detectors on their own, so a change to one threshold is caught here
// rather than in a fixture.
func TestNameShapeDetectors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		field, in, core string
		affixed         bool
	}{
		{"narrators", "narrator sean barrett", "sean barrett", true},
		{"narrators", "read by jim dale", "jim dale", true},
		{"narrators", "narrator read by jim dale", "jim dale", true},
		{"authors", "introduction by patrick lencioni", "patrick lencioni", true},
		{"narrators", "with", "with", false},
		{"narrators", "jim dale", "jim dale", false},
		{"publishers", "read by press", "read by press", false},
	} {
		if core, affixed := nameCore(tc.field, tc.in); core != tc.core || affixed != tc.affixed {
			t.Errorf("nameCore(%s, %q) = %q, %v; want %q, %v", tc.field, tc.in, core, affixed, tc.core, tc.affixed)
		}
	}

	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"Narrator..........Sean Barrett", 1, "Sean Barrett"},
		{"Read by Gabrielle Baker", 2, "Gabrielle Baker"},
		{"introduction by Conan O'Brien", 2, "Conan O'Brien"},
		{"Jim Dale", 0, "Jim Dale"},
	} {
		if got := cleanName(tc.in, tc.n); got != tc.want {
			t.Errorf("cleanName(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}

	for _, tc := range []struct {
		short, long string
		want        bool
	}{
		{"fajer al", "fajer al kaisi", true},
		{"full cast", "full cast recording", true},
		{"graphicaudio", "graphic audio llc", true},
		{"jack evans", "jack r b evans", true},
		{"john lee", "john lee hooker", true},
		{"john", "john lee", false},         // too short to mean anything
		{"jim dale", "kim dale", false},     // a typo, not a truncation
		{"jack evans", "evans jack", false}, // reordered is not contained
	} {
		if got := truncationOf(tc.short, tc.long); got != tc.want {
			t.Errorf("truncationOf(%q, %q) = %v, want %v", tc.short, tc.long, got, tc.want)
		}
	}

	for _, tc := range []struct {
		short, long string
		want        bool
	}{
		{"c z dunn", "christian dunn", true},
		{"j k rowling", "joanne rowling", true},
		{"j smith", "john smith", true},
		{"jim dale", "jim dole", false},         // different surname
		{"chris dunn", "christian dunn", false}, // a shortened first name is not an initial
		{"joe hill", "joey w hill", false},      // two people
		{"dunn", "christian dunn", false},       // one word is not a name with initials
		{"a lee", "b lee", false},               // two different initials
	} {
		if got := initialsOf(tc.short, tc.long); got != tc.want {
			t.Errorf("initialsOf(%q, %q) = %v, want %v", tc.short, tc.long, got, tc.want)
		}
	}

	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"peter whickam", "peter wickham", true},  // two edits, long, same first word
		{"tony robinsson", "tony robinson", true}, // one edit
		{"jim dale", "jim dole", true},            // one edit: reported, and the description says to read both
		{"jim dale", "kim dole", false},           // two edits on a short name
		{"anne holt", "tim holt", false},          // two edits, different first word
		{"sci fi", "scifi", false},                // too short for the typo check (norm groups these anyway)
		{"romance", "romances", true},
	} {
		if got := typoApart(tc.a, tc.b); got != tc.want {
			t.Errorf("typoApart(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}

	if d := typoDistance("wickham", "whickam", 5); d != 2 {
		t.Errorf("distance wickham/whickam = %d, want 2", d)
	}
	if d := typoDistance("abcd", "abdc", 5); d != 1 {
		t.Errorf("a transposition should be one edit, got %d", d)
	}
	if d := typoDistance("short", "a much longer string", 2); d != 3 {
		t.Errorf("capped distance = %d, want limit+1", d)
	}

	if got := splitParts("Etienne Mailloux/Paul Ablaze"); !slices.Equal(got, []string{"Etienne Mailloux", "Paul Ablaze"}) {
		t.Errorf("splitParts = %v", got)
	}
	if got := splitParts("A. Reader and B. Reader & C. Reader"); !slices.Equal(got, []string{"A. Reader", "B. Reader", "C. Reader"}) {
		t.Errorf("splitParts = %v", got)
	}
	if splitValue("narrators", "Anderson Cooper", "anderson cooper") {
		t.Error("a name containing 'and' is not a split value")
	}
}

// A name on both sides of a book is normal; a name that wrote one volume and
// read the rest, or that reads for a living and is credited as an author once,
// is a role in the wrong field.
func TestAuditNarratorAsAuthor(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Executioner 1", `"authorName":"Annie Wild","narratorName":"Mato Sato"`, ""),
		item("i2", "Executioner 2", `"authorName":"Mato Sato","narratorName":"Annie Wild"`, ""),
		item("i3", "Executioner 3", `"authorName":"Annie Wild","narratorName":"Mato Sato"`, ""),
		item("i4", "Own Voice", `"authorName":"Brené Brown","narratorName":"Brené Brown"`, ""),
		item("i5", "Confessions", `"authorName":"Kenna White, Abby Craden","narratorName":"Abby Craden"`, ""),
		item("i6", "Some Romance", `"authorName":"Someone Else","narratorName":"Abby Craden"`, ""),
		item("i7", "Plain", `"authorName":"Just Author","narratorName":"Just Reader"`, ""),
	))
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1","name":"Kenna White"},{"id":"a2","name":"Abby Craden"}],"total":2}`)
	call := toolCaller(t, f)

	out, err := call("audit_narrators", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_scanned"]); got != 7 {
		t.Errorf("items_scanned = %d", got)
	}
	findings := list(t, out["roles"])
	if got := num(t, out["total_findings"]); got != 3 || len(findings) != 3 {
		t.Fatalf("total_findings = %d, findings = %v; want Mato Sato, Annie Wild and Abby Craden", got, findings)
	}
	// fewest written first, then most read: Abby Craden and Mato Sato each
	// wrote one, Mato Sato read two; Annie Wild wrote two
	if str(t, findings[0]["name"]) != "Mato Sato" || num(t, findings[0]["books_written"]) != 1 || num(t, findings[0]["books_read_only"]) != 2 {
		t.Errorf("first finding = %v, want Mato Sato, wrote 1, read 2", findings[0])
	}
	if str(t, findings[1]["name"]) != "Abby Craden" || num(t, findings[1]["books_read_only"]) != 1 {
		t.Errorf("second finding = %v, want Abby Craden with one book read that she did not co-write", findings[1])
	}
	if str(t, findings[2]["name"]) != "Annie Wild" || num(t, findings[2]["books_written"]) != 2 || num(t, findings[2]["books_read_only"]) != 1 {
		t.Errorf("third finding = %v, want Annie Wild, wrote 2, read 1", findings[2])
	}
	read := list(t, findings[0]["read"])
	if len(read) != 2 || str(t, read[0]["title"]) != "Executioner 1" || str(t, read[1]["title"]) != "Executioner 3" {
		t.Errorf("Mato Sato read = %v", read)
	}
	for _, fd := range findings {
		if str(t, fd["name"]) == "Brené Brown" {
			t.Error("an author reading their own book was reported")
		}
	}

	limited, err := call("audit_narrators", map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(list(t, limited["roles"])) != 1 || num(t, limited["total_findings"]) != 3 {
		t.Errorf("limit 1 = %v, want one role and the full count", limited)
	}
}

// Genres that are one another a typo apart come back as one cluster, not as a
// pair for every edge, and the most used spelling is the one to keep.
func TestAuditSpellingClustersNearMisses(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "One", `"genres":["Audiobook"],"publisher":"Penguin Audio"`, ""),
		item("i2", "Two", `"genres":["Audiobook"],"publisher":"Penguin Audio"`, ""),
		item("i3", "Three", `"genres":["Audio Book"],"publisher":"Penguin Random House Audio"`, ""),
		item("i4", "Four", `"genres":["Audiobooks"]`, ""),
		item("i5", "Five", `"genres":["audiobook"]`, ""),
	))
	call := toolCaller(t, f)

	out, err := call("audit_spelling", nil)
	if err != nil {
		t.Fatal(err)
	}
	groups := list(t, out["groups"])
	if len(groups) != 3 {
		t.Fatalf("groups = %v, want the Audiobook case pair, one Audiobook cluster and one publisher pair", groups)
	}
	for _, g := range groups {
		sp := list(t, g["spellings"])
		switch str(t, g["field"]) + "/" + str(t, g["kind"]) {
		case "genres/spelling":
			if len(sp) != 2 || str(t, g["keep"]) != "Audiobook" {
				t.Errorf("case group = %v", g)
			}
		case "genres/near":
			if len(sp) != 4 || str(t, g["keep"]) != "Audiobook" {
				t.Errorf("cluster = %v, want all four spellings behind Audiobook", g)
			}
		case "publishers/contains":
			if str(t, g["keep"]) != "Penguin Audio" {
				t.Errorf("publisher keep = %v, want the one on more books, not the longer one", g["keep"])
			}
		default:
			t.Errorf("unexpected group %v", g)
		}
	}
}

// A rename sweep touches only items spelled exactly as asked: "english" must
// leave "English" alone, and a large sweep goes to the server in pages.
func TestMetadataRenameSweepIsExactAndPaged(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	items := []string{item("i0", "Zero", `"language":"english"`, "")}
	for i := 1; i <= sweepBatchSize+5; i++ {
		items = append(items, item(fmt.Sprintf("i%d", i), "Book", `"language":"English"`, ""))
	}
	f.json("GET /api/libraries/"+libID+"/items", page(items...))
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	call := toolCaller(t, f)

	out, err := call("metadata_rename", map[string]any{"field": "languages", "from": "english", "to": "English"})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := out["items"].([]any); !ok || len(got) != 1 || got[0] != "Zero" {
		t.Errorf("items = %v, want only the lowercase one", out["items"])
	}
	batches := f.requests("/api/items/batch/update")
	if len(batches) != 1 || !strings.Contains(batches[0].Body, `"i0"`) || strings.Contains(batches[0].Body, `"i1"`) {
		t.Errorf("batch = %v, want one request carrying only i0", batches)
	}

	// and a sweep over more than a page's worth goes out in pages
	f2 := newFakeABS(t)
	oneLibrary(f2)
	items = nil
	for i := 1; i <= sweepBatchSize+5; i++ {
		items = append(items, item(fmt.Sprintf("i%d", i), "Book", `"publisher":"Harper Audio"`, ""))
	}
	f2.json("GET /api/libraries/"+libID+"/items", page(items...))
	f2.json("POST /api/items/batch/update", `{"updates":50}`)
	out, err = toolCaller(t, f2)("metadata_rename", map[string]any{"field": "publishers", "from": "Harper Audio", "to": "HarperAudio"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f2.requests("/api/items/batch/update"); len(got) != 2 {
		t.Errorf("%d batch requests, want 2 pages", len(got))
	}
	if got := num(t, out["items_updated"]); got != 100 {
		t.Errorf("items_updated = %d, want the pages' counts summed", got)
	}
}

// Every author problem in one place: the item whose author is its title, the
// records with no asin, no photo or no books, the biography that belongs to
// someone else, and two records that are one name spelled two ways.
func TestAuditAuthors(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Dune", `"authorName":"Frank Herbert"`, ""),
		item("i2", "Neuromancer", `"authorName":"Neuromancer"`, ""),
		item("i3", "The Silent War", `"authorName":"C Z Dunn"`, ""),
	))
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[
		{"id":"a1","name":"Frank Herbert","asin":"B1","imagePath":"/a.jpg","numBooks":1,"description":"Frank Herbert was an American science fiction author."},
		{"id":"a2","name":"Sarah Diemer","asin":"B2","imagePath":"/b.jpg","numBooks":1,"description":"Sarah Miller began writing her first novel at the age of ten."},
		{"id":"a3","name":"Cat Hellisen","asin":"B3","imagePath":"/c.jpg","numBooks":1,"description":"I like mucking about with words. I live near a sea."},
		{"id":"a4","name":"Shayna Small","asin":"B4","imagePath":"/d.jpg","numBooks":0,"description":"Shayna Small is a narrator."},
		{"id":"a5","name":"Christian Dunn","asin":"B5","imagePath":"/e.jpg","numBooks":1},
		{"id":"a6","name":"C Z Dunn","asin":"B6","numBooks":1},
		{"id":"a7","name":"Neuromancer","numBooks":1},
		{"id":"a8","name":"Chris Wraight","asin":"B8","numBooks":8}
	],"total":8}`)
	call := toolCaller(t, f)

	out, err := call("audit_authors", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_scanned"]); got != 3 {
		t.Errorf("items_scanned = %d", got)
	}
	if got := num(t, out["authors_scanned"]); got != 8 {
		t.Errorf("authors_scanned = %d", got)
	}
	counts, ok := out["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts = %v, want an object", out["counts"])
	}
	for k, want := range map[string]int{"author_as_title": 1, "unmatched": 1, "no_photo": 3, "no_books": 1, "bio_mismatch": 1, "names": 1} {
		if got := num(t, counts[k]); got != want {
			t.Errorf("counts.%s = %d, want %d", k, got, want)
		}
	}
	items := list(t, out["items"])
	if len(items) != 1 || str(t, items[0]["title"]) != "Neuromancer" {
		t.Errorf("items = %v, want the book whose author is its title", items)
	}

	records := list(t, out["records"])
	// worst first: the wrong biography, the empty record, the unmatched one,
	// then photos only, most books first
	wantOrder := []string{"Sarah Diemer", "Shayna Small", "Neuromancer", "Chris Wraight", "C Z Dunn"}
	gotOrder := make([]string, 0, len(records))
	for _, r := range records {
		gotOrder = append(gotOrder, str(t, r["name"]))
	}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Errorf("records = %v, want %v", gotOrder, wantOrder)
	}
	probs := func(name string) []string {
		for _, r := range records {
			if str(t, r["name"]) != name {
				continue
			}
			raw, ok := r["problems"].([]any)
			if !ok {
				t.Fatalf("%s problems = %v, want a list", name, r["problems"])
			}
			ps := make([]string, 0, len(raw))
			for _, p := range raw {
				ps = append(ps, str(t, p))
			}
			return ps
		}
		return nil
	}
	if got := probs("Sarah Diemer"); !slices.Equal(got, []string{"bio_mismatch"}) {
		t.Errorf("Sarah Diemer problems = %v", got)
	}
	if got := probs("Neuromancer"); !slices.Equal(got, []string{"unmatched", "no_photo"}) {
		t.Errorf("Neuromancer problems = %v", got)
	}
	if got := probs("Cat Hellisen"); got != nil {
		t.Errorf("a first-person biography that names nobody was flagged: %v", got)
	}
	if got := probs("Frank Herbert"); got != nil {
		t.Errorf("a biography that names its author was flagged: %v", got)
	}

	names := list(t, out["names"])
	if len(names) != 1 || str(t, names[0]["kind"]) != "contains" || str(t, names[0]["keep"]) != "Christian Dunn" {
		t.Errorf("names = %v, want C Z Dunn contained in Christian Dunn", names)
	}

	if _, err := call("audit_spelling", map[string]any{"field": "authors"}); err == nil {
		t.Error("audit_spelling took authors, which audit_authors owns")
	}
}

func TestBioNamesSomeoneElse(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, bio string
		want      bool
	}{
		{"Sarah Diemer", "Sarah Miller began writing her first novel at the age of ten.", true},
		{"Reba Buhr", "Reba Bale writes contemporary lesbian romance.", true},
		{"Frank Herbert", "Frank Herbert was an American author.", false},
		{"Anne Holt", "Anne Holt is Norway's bestselling crime writer.", false},
		{"L.L. Raand", "Radclyffe has written many novels and, writing as L.L. Raand, a paranormal series.", false},
		{"Cat Hellisen", "I like mucking about with words.", false},
		{"Patrick Lencioni", "New York Times bestselling author Patrick Lencioni founded The Table Group.", false},
		{"Jane Doe", "New York Times bestselling author John Smith founded a firm.", true},
		{"Fuse", "", false},
		{"Martin Luther King Jr.", "Martin Luther King was a minister.", false},
		{"Alan Dean Foster", "Alan Dean Foster's work to date includes hard science fiction.", false},
		{"Carsen Taite", "Carsen Taite’s goal as an author is to spin tales.", false},
		{"Jo Nesbø", "Jo Nesbo is one of the world's bestselling crime writers.", false},
		{"Amanda Kay", "SHATTERING HEARTS IN THE NAME OF HAPPILY EVER AFTER I am a romance author.", false},
		{"Rachel Botchan", "Rachel Bateman grew up with an abundance of sisters.", true},
		{"Tyler Art", "Tyler Grant is the author of dozens of comedy stories.", true},
	} {
		if got := bioNamesSomeoneElse(tc.name, tc.bio); got != tc.want {
			t.Errorf("bioNamesSomeoneElse(%q, %q) = %v, want %v", tc.name, tc.bio, got, tc.want)
		}
	}
}

// Accented letters fold to their plain spelling rather than vanishing, so a
// record and a biography that disagree only on an ø are the same name.
func TestNormFoldsAccents(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"Jo Nesbø":           "jo nesbo",
		"Étienne Mailloux":   "etienne mailloux",
		"Jussi Adler-Olsen":  "jussi adler olsen",
		"Caitlín R. Kiernan": "caitlin r kiernan",
		"Straße":             "strasse",
		// a letter with nothing to fold to is kept, not dropped
		"田中芳樹":               "田中芳樹",
		"Мастер и Маргарита": "мастер и маргарита",
	} {
		if got := norm(in); got != want {
			t.Errorf("norm(%q) = %q, want %q", in, got, want)
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
