package tools

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// A name stored with a space at an end is what audit_whitespace reports, and
// the tools its fix names must find it as written: not refuse it, and not
// take its tidy twin for it. A name given tidy still finds the spaced one
// when that is the only one, several matches say which records they are,
// and a rename that would change nothing says so rather than succeed.
func TestNamesStoredWithSpacesAreFoundAsWritten(t *testing.T) {
	t.Parallel()

	const (
		spaced   = "a1111111-1111-4111-8111-111111111111" // "Brandon Sanderson "
		tidy     = "a2222222-2222-4222-8222-222222222222" // "Brandon Sanderson"
		lonely   = "a3333333-3333-4333-8333-333333333333" // "Ursula Le Guin " alone
		mistSp   = "b1111111-1111-4111-8111-111111111111" // "Mistborn "
		mistTidy = "b2222222-2222-4222-8222-222222222222" // "Mistborn"
	)
	f := newFakeABS(t)
	oneLibrary(f)
	author := func(id, name string) string {
		return `{"id":"` + id + `","name":"` + name + `","libraryId":"` + libID + `","numBooks":1}`
	}
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[`+author(spaced, "Brandon Sanderson ")+`,`+author(tidy, "Brandon Sanderson")+`,`+author(lonely, "Ursula Le Guin ")+`],"total":3}`)
	for id, name := range map[string]string{spaced: "Brandon Sanderson ", tidy: "Brandon Sanderson", lonely: "Ursula Le Guin "} {
		f.json("GET /api/authors/"+id, author(id, name))
	}
	series := func(id, name string) string {
		return `{"id":"` + id + `","name":"` + name + `","libraryId":"` + libID + `","books":[{"id":"i1"}]}`
	}
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[`+series(mistSp, "Mistborn ")+`,`+series(mistTidy, "Mistborn")+`],"total":2}`)
	f.json("GET /api/series/"+mistSp, series(mistSp, "Mistborn "))
	f.json("GET /api/series/"+mistTidy, series(mistTidy, "Mistborn"))
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Michael Kramer ","numBooks":2},{"name":"Michael Kramer","numBooks":9},{"name":"Kate Reading","numBooks":4}]}`)
	f.json("PATCH /api/libraries/{lib}/narrators/{id}", `{"updated":2}`)
	f.json("PATCH /api/authors/{id}", `{"author":{"id":"`+spaced+`","name":"Brandon Sanderson"},"merged":true}`)
	call := toolCaller(t, f)

	// authors: as written, and a tidy name case aside, never a stored name
	// trimmed to answer it
	for given, want := range map[string]string{"Brandon Sanderson ": spaced, "Brandon Sanderson": tidy, "brandon sanderson": tidy, "Ursula Le Guin ": lonely} {
		out, err := call("author_get", map[string]any{"author": given})
		if err != nil || str(t, out["id"]) != want {
			t.Errorf("author_get %q = %v, %v; want %s", given, out["id"], err, want)
		}
	}
	// the only Ursula Le Guin is spelled with a space: the tidy name does not
	// reach it, as a delete by it must not, and the answer names it
	_, err := call("author_get", map[string]any{"author": "Ursula Le Guin"})
	if err == nil || !strings.Contains(err.Error(), "no author named") || !strings.Contains(err.Error(), lonely) {
		t.Errorf("the tidy name of a spaced author = %v, want not found, naming %s", err, lonely)
	}
	// a name given with a space that no record has exactly is none of them
	if _, err := call("author_get", map[string]any{"author": "Isaac Asimov "}); err == nil || !strings.Contains(err.Error(), "no author named") {
		t.Errorf("a spaced name no record has = %v, want not found", err)
	}

	// series_edit: the spaced series is the one renamed, so the merge it
	// points at goes from it into the tidy one, not the other way
	_, err = call("series_edit", map[string]any{"series": "Mistborn ", "name": "Mistborn"})
	if err == nil || !strings.Contains(err.Error(), "series_merge from="+mistSp+" into="+mistTidy) {
		t.Errorf("renaming the spaced series onto the tidy name = %v, want a merge from %s into %s", err, mistSp, mistTidy)
	}
	// a rename to the name it has writes nothing and says so: an
	// idempotent call, such as a cleanup renaming a series back, still works
	out, err := call("series_edit", map[string]any{"series": mistTidy, "name": "Mistborn"})
	if err != nil || !isTrue(out["unchanged"]) {
		t.Errorf("renaming a series to its own name = %v, %v; want unchanged", out, err)
	}

	// metadata_rename: the narrator as written is the one renamed
	out, err = call("metadata_rename", map[string]any{"field": "narrators", "from": "Michael Kramer ", "to": "Michael Kramer"})
	if err != nil || num(t, out["items_updated"]) != 2 {
		t.Fatalf("renaming the spaced narrator = %v, %v", out, err)
	}
	wantPath := "/api/libraries/" + libID + "/narrators/" + base64.StdEncoding.EncodeToString([]byte("Michael Kramer "))
	if sent := f.requests(wantPath); len(sent) != 1 || sent[0].Method != http.MethodPatch {
		t.Errorf("the rename went to %v, want the spaced name's %s", f.requests("/narrators/"), wantPath)
	}
	if _, err := call("metadata_rename", map[string]any{"field": "narrators", "from": "Nobody", "to": "Somebody"}); err == nil || !strings.Contains(err.Error(), `no narrator named "Nobody"`) {
		t.Errorf("renaming a narrator no library has = %v, want no narrator named", err)
	}
	for _, args := range []map[string]any{
		{"field": "narrators", "from": "Michael Kramer", "to": "Michael Kramer"},
		{"field": "authors", "from": "Brandon Sanderson", "to": "Brandon Sanderson"},
	} {
		before := len(f.seen)
		got, cerr := call("metadata_rename", args)
		if cerr != nil || !isTrue(got["unchanged"]) || num(t, got["items_updated"]) != 0 {
			t.Errorf("%v = %v, %v; want unchanged, nothing updated", args, got, cerr)
		}
		for _, r := range f.seen[before:] {
			if r.Method != http.MethodGet {
				t.Errorf("%v wrote %s %s, want nothing written", args, r.Method, r.Path)
			}
		}
	}
	// the authors rename found the spaced record as written
	if _, err := call("metadata_rename", map[string]any{"field": "authors", "from": "Brandon Sanderson ", "to": "Brandon Sanderson"}); err != nil {
		t.Errorf("renaming the spaced author onto the tidy one: %v", err)
	}
	if sent := f.requests("/api/authors/" + spaced); !slices.ContainsFunc(sent, func(r request) bool { return r.Method == http.MethodPatch }) {
		t.Errorf("the spaced author was not the one renamed: %v", f.requests("/api/authors/"))
	}
	// author_edit: a rename to the name it has writes nothing and says so
	if edited, eerr := call("author_edit", map[string]any{"author": tidy, "name": "Brandon Sanderson"}); eerr != nil || !isTrue(edited["unchanged"]) {
		t.Errorf("author_edit to its own name = %v, %v; want unchanged", edited, eerr)
	}
	// the remove preview counts the name as written
	f.json("GET /api/libraries/"+libID+"/items", `{"results":[],"total":0}`)
	out, err = call("metadata_rename", map[string]any{"field": "narrators", "from": "Michael Kramer ", "remove": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["preview"].(map[string]any); !ok {
		t.Fatalf("remove without confirm = %v, want a preview", out)
	}
	filter := "narrators." + base64.StdEncoding.EncodeToString([]byte("Michael Kramer "))
	if !slices.ContainsFunc(f.requests("/api/libraries/"+libID+"/items"), func(r request) bool { return parseQuery(t, r.Query).Get("filter") == filter }) {
		t.Errorf("the preview did not ask for the name as written, %s", filter)
	}
}

// A narrator named with a space at an end is the spaced one in every library
// that has it, and the libraries with only the tidy name are left alone; the
// rename audit_whitespace suggests, with no library, renames the spaced name
// and does not fail on the library that already has the tidy one.
func TestNarratorRenameTakesTheClosestSpellingEverywhere(t *testing.T) {
	t.Parallel()

	const libB = "33333333-3333-4333-8333-333333333333"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+libB+`","name":"B","mediaType":"book"}]}`)
	f.json("PATCH /api/libraries/{lib}/narrators/{id}", `{"updated":2}`)
	call := toolCaller(t, f)
	renamed := func(before int) []string {
		var out []string
		for _, r := range f.seen[before:] {
			if r.Method != http.MethodPatch {
				continue
			}
			lib, enc, _ := strings.Cut(strings.TrimPrefix(r.Path, "/api/libraries/"), "/narrators/")
			name, err := base64.StdEncoding.DecodeString(enc)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, lib[:1]+":"+string(name))
		}
		return out
	}

	// the spaced name in A, the tidy one in B: the fix renames A alone
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Michael Kramer ","numBooks":2}]}`)
	f.json("GET /api/libraries/"+libB+"/narrators", `{"narrators":[{"name":"Michael Kramer","numBooks":5}]}`)
	out, err := call("metadata_rename", map[string]any{"field": "narrators", "from": "Michael Kramer ", "to": "Michael Kramer"})
	if got := renamed(0); err != nil || isTrue(out["unchanged"]) || !slices.Equal(got, []string{libID[:1] + ":Michael Kramer "}) {
		t.Errorf("the suggested fix = %v, %v, renamed %v; want the spaced name in A renamed and B left alone", out, err, got)
	}
}

// Both spellings in A, the tidy one in B: asking for the spaced one renames
// it alone, never the tidy name of either library.
func TestNarratorRenameLeavesTheTidyNameAlone(t *testing.T) {
	t.Parallel()

	const libB = "33333333-3333-4333-8333-333333333333"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+libB+`","name":"B","mediaType":"book"}]}`)
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Michael Kramer ","numBooks":2},{"name":"Michael Kramer","numBooks":9}]}`)
	f.json("GET /api/libraries/"+libB+"/narrators", `{"narrators":[{"name":"Michael Kramer","numBooks":5}]}`)
	f.json("PATCH /api/libraries/{lib}/narrators/{id}", `{"updated":1}`)
	if _, err := toolCaller(t, f)("metadata_rename", map[string]any{"field": "narrators", "from": "Michael Kramer ", "to": "Mike Kramer"}); err != nil {
		t.Fatal(err)
	}
	want := "/api/libraries/" + libID + "/narrators/" + base64.StdEncoding.EncodeToString([]byte("Michael Kramer "))
	var patched []string
	for _, r := range f.seen {
		if r.Method == http.MethodPatch {
			patched = append(patched, r.Path)
		}
	}
	if !slices.Equal(patched, []string{want}) {
		t.Errorf("renamed %v, want only the spaced name in A (%s)", patched, want)
	}
}

// A name that only an empty series has exactly is not taken for the series a
// trim or a fold would reach: after "Mistborn " is merged into "Mistborn",
// a rename of "Mistborn " must not land on the tidy series, nor a lookup of
// "Mistborn" on the spaced one when the tidy one is emptied. The lookup is
// refused, naming both, and either id resolves.
func TestSeriesLookupRefusesAnEmptyExactName(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"a1111111-1111-4111-8111-111111111111","name":"Mistborn","libraryId":"`+libID+`","books":[]},{"id":"b2222222-2222-4222-8222-222222222222","name":"Mistborn ","libraryId":"`+libID+`","books":[{"id":"i1"}]}],"total":2}`)
	f.json("GET /api/series/b2222222-2222-4222-8222-222222222222", `{"id":"b2222222-2222-4222-8222-222222222222","name":"Mistborn ","libraryId":"`+libID+`"}`)
	client, err := abs.New(f.srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	s, err := resolveSeries(t.Context(), client, "", "Mistborn")
	if err == nil || !strings.Contains(err.Error(), "b2222222-2222-4222-8222-222222222222") {
		t.Errorf("resolving Mistborn = %+v, %v; want refused, naming the spaced series that has the books", s, err)
	}
	if s, err := resolveSeries(t.Context(), client, "", "Mistborn "); err != nil || s.ID != "b2222222-2222-4222-8222-222222222222" {
		t.Errorf("resolving the spaced name = %+v, %v; want the spaced series", s, err)
	}
	if s, err := resolveSeries(t.Context(), client, "", "b2222222-2222-4222-8222-222222222222"); err != nil || s.ID != "b2222222-2222-4222-8222-222222222222" {
		t.Errorf("resolving by id = %+v, %v; want the spaced series", s, err)
	}
}

// isTrue reports whether an answer's field is the boolean true.
func isTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// A rename whose from and to are the same, spaces and all, asks for nothing:
// it must not rename the spaced record onto its tidy twin and merge them.
// A rename run again, the name in two libraries already the one asked for,
// is done, not ambiguous.
func TestRenameThatAsksForNothingWritesNothing(t *testing.T) {
	t.Parallel()

	const libB = "33333333-3333-4333-8333-333333333333"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+libB+`","name":"B","mediaType":"book"}]}`)
	author := func(id, lib, name string) string {
		return `{"id":"` + id + `","name":"` + name + `","libraryId":"` + lib + `","numBooks":1}`
	}
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[`+author("a1111111-1111-4111-8111-111111111111", libID, "Brandon Sanderson ")+`,`+author("a2222222-2222-4222-8222-222222222222", libID, "Brandon Sanderson")+`],"total":2}`)
	f.json("GET /api/libraries/"+libB+"/authors", `{"results":[`+author("a3333333-3333-4333-8333-333333333333", libB, "Brandon Sanderson")+`],"total":1}`)
	call := toolCaller(t, f)
	for _, args := range []map[string]any{
		{"field": "authors", "from": "Brandon Sanderson ", "to": "Brandon Sanderson "},
		{"field": "narrators", "from": "Michael Kramer ", "to": "Michael Kramer "},
		{"field": "authors", "library": "B", "from": "brandon sanderson", "to": "Brandon Sanderson"},
	} {
		before := len(f.seen)
		out, err := call("metadata_rename", args)
		if err != nil || !isTrue(out["unchanged"]) {
			t.Errorf("%v = %v, %v; want unchanged", args, out, err)
		}
		for _, r := range f.seen[before:] {
			if r.Method != http.MethodGet {
				t.Errorf("%v wrote %s %s, want nothing written", args, r.Method, r.Path)
			}
		}
	}
	// the tidy name in both libraries, already the name asked for: done
	out, err := call("metadata_rename", map[string]any{"field": "authors", "library": "B", "from": "Brandon Sanderson", "to": "Brandon Sanderson"})
	if err != nil || !isTrue(out["unchanged"]) {
		t.Errorf("a rename already done = %v, %v; want unchanged", out, err)
	}
}

// Several spellings of a narrator in another library, none of them the one
// asked for exactly, do not fail a rename of the one that is.
func TestNarratorSpellingsElsewhereDoNotBlockTheExactOne(t *testing.T) {
	t.Parallel()

	const libB = "33333333-3333-4333-8333-333333333333"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+libB+`","name":"B","mediaType":"book"}]}`)
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Michael Kramer","numBooks":3}]}`)
	f.json("GET /api/libraries/"+libB+"/narrators", `{"narrators":[{"name":"Michael Kramer ","numBooks":1},{"name":" Michael Kramer","numBooks":1}]}`)
	f.json("PATCH /api/libraries/{lib}/narrators/{id}", `{"updated":3}`)
	call := toolCaller(t, f)
	out, err := call("metadata_rename", map[string]any{"field": "narrators", "from": "Michael Kramer", "to": "Mike Kramer"})
	if err != nil || num(t, out["items_updated"]) != 3 {
		t.Fatalf("renaming the exact name = %v, %v; want library A renamed", out, err)
	}
	for _, r := range f.seen {
		if r.Method == http.MethodPatch && !strings.HasPrefix(r.Path, "/api/libraries/"+libID+"/") {
			t.Errorf("renamed in %s, want library A alone", r.Path)
		}
	}
	// the name asked for is trimmed: A's narrator already has it
	if out, err := call("metadata_rename", map[string]any{"field": "narrators", "from": "Michael Kramer", "to": "Michael Kramer "}); err != nil || !isTrue(out["unchanged"]) {
		t.Errorf("renaming A's name to itself with a space = %v, %v; want unchanged", out, err)
	}
}

// A delete by the tidy name does not reach the only record, which is spelled
// with a space: the tool answers that none is named so, naming the one that
// differs, and deletes nothing.
func TestAuthorDeleteByTheTidyNameDeletesNothing(t *testing.T) {
	t.Parallel()

	const spacedID = "a1111111-1111-4111-8111-111111111111"
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"`+spacedID+`","name":"Brandon Sanderson ","libraryId":"`+libID+`","numBooks":5}],"total":1}`)
	_, err := toolCaller(t, f)("author_delete", map[string]any{"author": "Brandon Sanderson"})
	if err == nil || !strings.Contains(err.Error(), spacedID) {
		t.Errorf("author_delete by the tidy name = %v, want refused, naming the spaced record", err)
	}
	for _, r := range f.seen {
		if r.Method == http.MethodDelete {
			t.Errorf("deleted %s", r.Path)
		}
	}
}

// A library that hides one-book series lists none of them, so a lookup by
// name says so, and to pass the id.
func TestSeriesLookupSaysTheLibraryHidesOneBookSeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","settings":{"hideSingleBookSeries":true}}]}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	_, err := toolCaller(t, f)("series_get", map[string]any{"series": "Mistborn "})
	if err == nil || !strings.Contains(err.Error(), "hides one-book series") {
		t.Errorf("series_get of a hidden series = %v, want the hint to pass its id", err)
	}
}

// A rename of a spaced series to its own spaced name asks for nothing.
func TestSeriesEditToItsOwnSpacedName(t *testing.T) {
	t.Parallel()

	const id = "b1111111-1111-4111-8111-111111111111"
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/series/"+id, `{"id":"`+id+`","name":"Dune ","libraryId":"`+libID+`"}`)
	out, err := toolCaller(t, f)("series_edit", map[string]any{"series": id, "name": "Dune "})
	if err != nil || !isTrue(out["unchanged"]) {
		t.Errorf("series_edit to its own spaced name = %v, %v; want unchanged", out, err)
	}
	for _, r := range f.seen {
		if r.Method != http.MethodGet {
			t.Errorf("wrote %s %s", r.Method, r.Path)
		}
	}
}

// A tidy name that two records answer, one exactly and one case aside, is
// refused, naming both, as before: neither is picked for a write.
func TestExactCaseDoesNotWinOverACaseVariant(t *testing.T) {
	t.Parallel()

	const libB = "33333333-3333-4333-8333-333333333333"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+libB+`","name":"B","mediaType":"book"}]}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1111111-1111-4111-8111-111111111111","name":"Brandon Sanderson","libraryId":"`+libID+`","numBooks":3}],"total":1}`)
	f.json("GET /api/libraries/"+libB+"/authors", `{"results":[{"id":"b1111111-1111-4111-8111-111111111111","name":"brandon sanderson","libraryId":"`+libB+`","numBooks":1}],"total":1}`)
	_, err := toolCaller(t, f)("author_delete", map[string]any{"author": "Brandon Sanderson"})
	if err == nil || !strings.Contains(err.Error(), "a1111111") || !strings.Contains(err.Error(), "b1111111") {
		t.Errorf("author_delete of a name two records answer = %v, want refused, naming both", err)
	}
	for _, r := range f.seen {
		if r.Method == http.MethodDelete {
			t.Errorf("deleted %s", r.Path)
		}
	}
}

// A description set beside the name a series already has, spaces and all,
// leaves the name as it is.
func TestSeriesEditDescriptionKeepsASpacedName(t *testing.T) {
	t.Parallel()

	const id = "b1111111-1111-4111-8111-111111111111"
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/series/"+id, `{"id":"`+id+`","name":"Mistborn ","libraryId":"`+libID+`"}`)
	f.json("PATCH /api/series/"+id, `{"id":"`+id+`","name":"Mistborn ","libraryId":"`+libID+`"}`)
	if _, err := toolCaller(t, f)("series_edit", map[string]any{"series": id, "name": "Mistborn ", "description": "d"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.seen {
		if r.Method == http.MethodPatch && strings.Contains(r.Body, `"name"`) {
			t.Errorf("sent %s, want the description alone", r.Body)
		}
	}
}
