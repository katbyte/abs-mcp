package tools

import (
	"slices"
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
