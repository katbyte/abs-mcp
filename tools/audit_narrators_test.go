package tools

import (
	"encoding/json"
	"slices"
	"testing"
)

// The sweep sees the minified shape, where narrators are one joined string;
// the spellings still have to be found.
func TestAuditSpellingReadsMinifiedNarrators(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "One", `"narratorName":"Jim Dale","authorName":"A. Writer"`, ""),
		item("i2", "Two", `"narratorName":"jim dale, Someone Else","authorName":"a. writer"`, ""),
		item("i3", "Three", `"narratorName":"Jim Dale","authorName":"A. Writer"`, ""),
	))
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Jim Dale","numBooks":2},{"name":"jim dale","numBooks":1},{"name":"Someone Else","numBooks":1}]}`)
	call := toolCaller(t, f)

	out, err := call("audit_spelling", nil)
	if err != nil {
		t.Fatal(err)
	}
	if groups := list(t, out["groups"]); len(groups) != 0 {
		t.Fatalf("groups = %v, want none: authors and narrators belong to their own audits", groups)
	}
	out, err = call("audit_narrators", nil)
	if err != nil {
		t.Fatal(err)
	}
	names := list(t, out["names"])
	if len(names) != 1 || names[0]["keep"] != "Jim Dale" {
		t.Errorf("narrator names = %v, want Jim Dale kept over jim dale", names)
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
