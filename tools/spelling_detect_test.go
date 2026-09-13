package tools

import (
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
		"田中芳樹":               "",
	} {
		if got := norm(in); got != want {
			t.Errorf("norm(%q) = %q, want %q", in, got, want)
		}
	}
}
