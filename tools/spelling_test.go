package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// A remove drops a value from every item that carries it, server-wide for a
// tag or genre, with nothing to put back: removing zz-provider:audible.ca
// would have erased the store record everywhere in one call. Without confirm
// it says how many items carry the value and which, and sends no change.
func TestMetadataRenameRemoveNeedsConfirm(t *testing.T) {
	t.Parallel()

	const podLib = "55555555-5555-4555-8555-555555555503"
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"},{"id":"`+podLib+`","name":"Pods","mediaType":"podcast"}]}`)
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/items", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("filter") {
		case "tags." + b64("zz-provider:audible.ca"):
			_, _ = io.WriteString(w, `{"results":[`+item("i1", "Dune", "", "")+`,`+item("i2", "Emma", "", "")+`],"total":2}`)
		case "narrators." + b64("Jim Dale"):
			_, _ = io.WriteString(w, `{"results":[`+item("i3", "Harry Potter", "", "")+`],"total":1}`)
		case "":
			_, _ = io.WriteString(w, page(item("i4", "Hobbit", `"language":"eng"`, ""), item("i5", "Ubik", `"language":"English"`, "")))
		default:
			_, _ = io.WriteString(w, page())
		}
	})
	f.mux.HandleFunc("GET /api/libraries/"+podLib+"/items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") == "tags."+b64("zz-provider:audible.ca") {
			_, _ = io.WriteString(w, `{"results":[`+item("p1", "A Podcast", "", "")+`],"total":1}`)
			return
		}
		_, _ = io.WriteString(w, page())
	})
	f.json("DELETE /api/tags/{id}", `{"numItemsUpdated":3}`)
	f.json("DELETE /api/libraries/{lib}/narrators/{id}", `{"updated":1}`)
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Jim Dale","numBooks":1}]}`)
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	call := toolCaller(t, f)

	out, err := call("metadata_rename", map[string]any{"field": "tags", "from": "zz-provider:audible.ca", "remove": true})
	if err != nil {
		t.Fatal(err)
	}
	preview, ok := out["would_remove"].(map[string]any)
	if !ok || num(t, preview["found"]) != 3 || !slices.Equal(strs(t, preview["items"]), []string{"Dune", "Emma", "A Podcast"}) || num(t, out["items_updated"]) != 0 {
		t.Errorf("tag preview = %v, want 3 items in both libraries named, nothing updated", out)
	}

	out, err = call("metadata_rename", map[string]any{"field": "narrators", "from": "Jim Dale", "remove": true})
	if err != nil {
		t.Fatal(err)
	}
	if preview, ok := out["would_remove"].(map[string]any); !ok || num(t, preview["found"]) != 1 {
		t.Errorf("narrator preview = %v", out)
	}
	// a podcast library has no narrators and answers an unknown filter with
	// everything: it is not asked
	for _, r := range f.requests("/api/libraries/" + podLib + "/items") {
		if strings.Contains(parseQuery(t, r.Query).Get("filter"), "narrators") {
			t.Errorf("the podcast library was asked for narrators: %s", r.Query)
		}
	}

	out, err = call("metadata_rename", map[string]any{"field": "languages", "from": "eng", "remove": true, "library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	if preview, ok := out["would_remove"].(map[string]any); !ok || num(t, preview["found"]) != 1 || !slices.Equal(strs(t, preview["items"]), []string{"Hobbit"}) {
		t.Errorf("language preview = %v, want the one book spelled eng", out)
	}

	for _, r := range f.seen {
		if r.Method != http.MethodGet {
			t.Fatalf("a preview changed something: %s %s", r.Method, r.Path)
		}
	}

	// confirm does it
	out, err = call("metadata_rename", map[string]any{"field": "tags", "from": "zz-provider:audible.ca", "remove": true, "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_updated"]) != 3 || out["would_remove"] != nil {
		t.Errorf("confirmed remove = %v", out)
	}
	if got := f.requests("/api/tags/" + b64("zz-provider:audible.ca")); len(got) != 1 || got[0].Method != http.MethodDelete {
		t.Errorf("confirm sent %v, want one DELETE", got)
	}
}

// A sweep writes in batches, and one that failed part way threw away how many
// had already changed: the caller read the whole change as not made.
func TestMetadataRenameSaysHowFarItGotBeforeAFailure(t *testing.T) {
	t.Parallel()

	const libB = "55555555-5555-4555-8555-555555555504"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+libB+`","name":"B","mediaType":"book"}]}`)
	books := make([]string, 0, 150)
	for i := range 150 {
		books = append(books, item(fmt.Sprintf("i%03d", i), fmt.Sprintf("Book %03d", i), `"publisher":"Bantom","genres":["SF & Fantasy"]`, ""))
	}
	f.json("GET /api/libraries/"+libID+"/items", page(books...))
	f.json("GET /api/libraries/"+libB+"/items", page())
	var mu sync.Mutex
	calls := 0
	f.mux.HandleFunc("POST /api/items/batch/update", func(w http.ResponseWriter, r *http.Request) {
		var body []json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls%2 == 0 { // every second batch times out
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		_, _ = fmt.Fprintf(w, `{"updates":%d}`, len(body))
	})
	for _, lib := range []string{libID, libB} {
		f.json("GET /api/libraries/"+lib+"/narrators", `{"narrators":[{"name":"jim dale","numBooks":3}]}`)
	}
	f.json("PATCH /api/libraries/"+libID+"/narrators/{id}", `{"updated":3}`)
	f.mux.HandleFunc("PATCH /api/libraries/"+libB+"/narrators/{id}", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	call := toolCaller(t, f)

	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		// publishers and genre splits both go in batches of 100, and say
		// which batch failed and how many items went before it
		{map[string]any{"field": "publishers", "from": "Bantom", "to": "Bantam", "library": "A"}, "items 101 to 150 of 150 failed and may have landed in part; 100 items before it were updated"},
		{map[string]any{"field": "genres", "from": "SF & Fantasy", "into": []any{"Science Fiction", "Fantasy"}}, "items 101 to 150 of 150 failed and may have landed in part; 100 items before it were updated"},
		// the first library renamed, the second failed
		{map[string]any{"field": "narrators", "from": "jim dale", "to": "Jim Dale"}, "3 items were changed"},
	} {
		mu.Lock()
		calls = 0
		mu.Unlock()
		_, err := call("metadata_rename", tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
}

// For authors items_updated preferred the count on the server's reply, which
// after a merge is the surviving author's books, both authors' together; the
// books that changed are the ones the renamed author had.
func TestMetadataRenameAuthorCountsTheBooksThatChanged(t *testing.T) {
	t.Parallel()

	const authorID = "44444444-4444-4444-8444-444444444445"
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"`+authorID+`","name":"jrr tolkien","libraryId":"`+libID+`"}],"total":1}`)
	f.json("GET /api/authors/"+authorID, `{"id":"`+authorID+`","name":"jrr tolkien","libraryId":"`+libID+`","libraryItems":[`+item("i1", "The Hobbit", "", "")+`,`+item("i2", "Smith of Wootton Major", "", "")+`]}`)
	f.json("PATCH /api/authors/"+authorID, `{"author":{"id":"a-keep","name":"J.R.R. Tolkien","numBooks":9},"merged":true}`)
	call := toolCaller(t, f)

	out, err := call("metadata_rename", map[string]any{"field": "authors", "from": "jrr tolkien", "to": "J.R.R. Tolkien"})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["merged"]) || num(t, out["items_updated"]) != 2 {
		t.Errorf("out = %v, want merged with the 2 books that changed", out)
	}
}

// The provider tags of two stores are a letter apart by design.
func TestAuditSpellingLeavesMarkerTagsOut(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "One", "", `"tags":["zz-provider:audible.ca"]`),
		item("i2", "Two", "", `"tags":["zz-provider:audible.uk"]`),
		item("i3", "Three", "", `"tags":["Romance"]`),
		item("i4", "Four", "", `"tags":["Romances"]`),
	))
	call := toolCaller(t, f)

	out, err := call("audit_spelling", map[string]any{"field": "tags"})
	if err != nil {
		t.Fatal(err)
	}
	groups := list(t, out["groups"])
	if len(groups) != 1 || str(t, groups[0]["keep"]) != "Romance" && str(t, groups[0]["keep"]) != "Romances" {
		t.Errorf("groups = %v, want Romance alone", groups)
	}

	// a prefix the server was given, whatever its shape
	c := newSpellingCounts([]string{"tags"})
	c.addValue("tags", "Store/audible.ca", 3)
	c.addValue("tags", "Store/audible.uk", 1)
	c.dropMarkers((&registry{opts: Options{ProviderTag: "Store/"}}).providerConfig())
	if got := c.report("tags"); len(got) != 0 {
		t.Errorf("configured provider tags grouped: %+v", got)
	}
}

// en-US was reported as an unknown language, and no unknown language was
// counted as a finding anywhere.
func TestAuditSpellingCountsUnrecognisedLanguages(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "One", `"language":"en-US"`, ""),
		item("i2", "Two", `"language":"English"`, ""),
		item("i3", "Three", `"language":"pt_BR"`, ""),
		item("i4", "Four", `"language":"XXX"`, ""),
	))
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	call := toolCaller(t, f)

	out, err := call("audit_spelling", nil)
	if err != nil {
		t.Fatal(err)
	}
	odd := list(t, out["unrecognized_languages"])
	if len(odd) != 1 || str(t, odd[0]["value"]) != "XXX" {
		t.Errorf("unrecognized = %v, want XXX alone", odd)
	}
	groups := list(t, out["groups"])
	if len(groups) != 1 || num(t, out["total_findings"]) != 2 {
		t.Errorf("groups %v, total_findings %v: want en-US beside English, and XXX counted", groups, out["total_findings"])
	}
	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list(t, all["audits"]) {
		if str(t, row["audit"]) == "audit_spelling" && num(t, row["found"]) != 2 {
			t.Errorf("audit_all counts %v, audit_spelling 2", row["found"])
		}
	}
}

// valuesOf is what the audit_spelling sweep reads; both item shapes have to
// yield the same values.
func TestValuesOfHandlesBothShapes(t *testing.T) {
	t.Parallel()

	expanded := &abs.Item{Media: abs.Media{Metadata: abs.Metadata{
		Narrators: []string{"Jim Dale", "Kate Reading"},
		Authors:   []abs.NameRef{{Name: "A"}, {Name: "B"}},
		Language:  "English", Publisher: "Bantam", Genres: []string{"SF"},
	}, Tags: []string{"x"}}}
	minified := &abs.Item{Media: abs.Media{Metadata: abs.Metadata{
		NarratorName: "Jim Dale, Kate Reading", AuthorName: "A, B",
		Language: "English", Publisher: "Bantam", Genres: []string{"SF"},
	}, Tags: []string{"x"}}}

	for _, field := range vocabFields {
		a, b := valuesOf(field, expanded), valuesOf(field, minified)
		for i := range a {
			a[i] = strings.TrimSpace(a[i])
		}
		for i := range b {
			b[i] = strings.TrimSpace(b[i])
		}
		if !slices.Equal(a, b) {
			t.Errorf("%s: expanded %v, minified %v", field, a, b)
		}
		if len(a) == 0 {
			t.Errorf("%s: nothing read", field)
		}
	}
	if got := valuesOf("nope", expanded); got != nil {
		t.Errorf("unknown field gave %v", got)
	}
}

// metadata_rename routes each field to the endpoint that can change it:
// tags and genres server-wide, narrators per library, authors through the
// record, languages and publishers by sweep. Each branch has to build the
// request that endpoint reads.
func TestMetadataRenameRoutesByField(t *testing.T) {
	t.Parallel()

	const authorID = "44444444-4444-4444-8444-444444444444"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+otherLibID+`","name":"B","mediaType":"book"}]}`)
	f.json("POST /api/tags/rename", `{"numItemsUpdated":4}`)
	f.json("DELETE /api/genres/{id}", `{"numItemsUpdated":2}`)
	f.json("PATCH /api/libraries/{lib}/narrators/{id}", `{"updated":3}`)
	f.json("DELETE /api/libraries/{lib}/narrators/{id}", `{"updated":1}`)
	for _, lib := range []string{libID, otherLibID} {
		f.json("GET /api/libraries/"+lib+"/narrators", `{"narrators":[{"name":"jim dale","numBooks":3},{"name":"Nobody","numBooks":1}]}`)
	}
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"`+authorID+`","name":"jrr tolkien","libraryId":"`+libID+`","numBooks":2}],"total":1}`)
	f.json("GET /api/libraries/"+otherLibID+"/authors", `{"results":[],"total":0}`)
	f.json("GET /api/authors/"+authorID, `{"id":"`+authorID+`","name":"jrr tolkien","libraryId":"`+libID+`","numBooks":2}`)
	f.json("PATCH /api/authors/"+authorID, `{"author":{"id":"`+authorID+`","name":"J.R.R. Tolkien","numBooks":5},"merged":true}`)
	f.json("GET /api/libraries/{lib}/items", page(
		item("i1", "One", `"language":"eng"`, ""),
		item("i2", "Two", `"language":"English"`, ""),
	))
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	call := toolCaller(t, f)

	// tags: one server-wide call, and library is refused rather than ignored
	out, err := call("metadata_rename", map[string]any{"field": "tag", "from": "Sci-Fi", "to": "Science Fiction"})
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_updated"]); got != 4 || str(t, out["field"]) != "tags" {
		t.Errorf("tags: %v", out)
	}
	if sent := f.requests("/api/tags/rename"); len(sent) != 1 || !strings.Contains(sent[0].Body, `"newTag":"Science Fiction"`) {
		t.Errorf("tags rename sent %v", sent)
	}
	if _, err := call("metadata_rename", map[string]any{"field": "tags", "from": "a", "to": "b", "library": "A"}); err == nil {
		t.Error("a library-scoped tag rename was not refused")
	}

	// genres with remove: a DELETE addressed by base64 of the value
	out, err = call("metadata_rename", map[string]any{"field": "genres", "from": "Temp", "remove": true, "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_updated"]); got != 2 {
		t.Errorf("genres remove: %v", out)
	}
	if sent := f.requests("/api/genres/VGVtcA=="); len(sent) != 1 || sent[0].Method != http.MethodDelete {
		t.Errorf("genre removal sent %v", sent)
	}

	// narrators: every library when none is named, counts summed
	out, err = call("metadata_rename", map[string]any{"field": "narrators", "from": "jim dale", "to": "Jim Dale"})
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_updated"]); got != 6 {
		t.Errorf("narrators across two libraries: %v", out)
	}
	if sent := f.requests("/api/libraries/" + otherLibID + "/narrators/amltIGRhbGU="); len(sent) != 1 || sent[0].Method != http.MethodPatch {
		t.Errorf("narrator rename in the second library sent %v", sent)
	}
	out, err = call("metadata_rename", map[string]any{"field": "narrators", "from": "Nobody", "remove": true, "confirm": true, "library": "B"})
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_updated"]); got != 1 {
		t.Errorf("narrator remove: %v", out)
	}
	if sent := f.requests("/api/libraries/" + libID + "/narrators/Tm9ib2R5"); len(sent) != 0 {
		t.Errorf("a rename scoped to B touched A: %v", sent)
	}

	// authors: resolved by name, renamed on the record, merge reported, and
	// the count is the renamed author's 2 books, not the merged total
	out, err = call("metadata_rename", map[string]any{"field": "authors", "from": "jrr tolkien", "to": "J.R.R. Tolkien"})
	if err != nil {
		t.Fatal(err)
	}
	if merged, ok := out["merged"].(bool); !ok || !merged || num(t, out["items_updated"]) != 2 {
		t.Errorf("authors: %v", out)
	}
	var patched []request
	for _, r := range f.requests("/api/authors/" + authorID) {
		if r.Method == http.MethodPatch {
			patched = append(patched, r)
		}
	}
	if len(patched) != 1 || !strings.Contains(patched[0].Body, `"name":"J.R.R. Tolkien"`) {
		t.Errorf("author rename sent %v", patched)
	}
	if _, err := call("metadata_rename", map[string]any{"field": "authors", "from": "jrr tolkien", "remove": true}); err == nil {
		t.Error("removing an author was not refused")
	}

	// languages: the sweep finds the one item that carries the spelling
	out, err = call("metadata_rename", map[string]any{"field": "languages", "from": "eng", "to": "English", "library": "A"})
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_updated"]); got != 1 {
		t.Errorf("languages: %v", out)
	}
	if sent := f.requests("/api/items/batch/update"); len(sent) != 1 || !strings.Contains(sent[0].Body, `"id":"i1"`) || strings.Contains(sent[0].Body, `"id":"i2"`) {
		t.Errorf("language sweep sent %v", sent)
	}

	// and the refusals that need no server
	for _, args := range []map[string]any{
		{"field": "nope", "from": "a", "to": "b"},
		{"field": "tags", "from": "", "to": "b"},
		{"field": "tags", "from": "a"},
		{"field": "tags", "from": "a", "to": "b", "remove": true},
	} {
		if _, err := call("metadata_rename", args); err == nil {
			t.Errorf("%v was not refused", args)
		}
	}
}

// vocabField takes the field however a caller is likely to spell it.
func TestVocabField(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"tags": "tags", "tag": "tags", "Genre": "genres", "NARRATORS": "narrators",
		"author": "authors", "language": "languages", "PUBLISHERS": "publishers",
		"": "", "series": "", "nope": "",
	} {
		if got := vocabField(in); got != want {
			t.Errorf("vocabField(%q) = %q, want %q", in, got, want)
		}
	}
	if got := vocabField("  narrator  "); got != "narrators" {
		t.Errorf("vocabField with spacing = %q", got)
	}
}

// A compound genre is split in one call with split, and only the books that
// carry it change: a part that is a genre on other books stays theirs. Doing
// it in two calls (into, then to_field) moved the part off every book.
func TestMetadataRenameSplitChangesOnlyTheCompound(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item(bookB1, "Equal Rites", `"genres":["Science Fiction & Fantasy, Fantasy"]`, `"tags":["witches"]`),
		item(bookB2, "Mort", `"genres":["Fantasy"]`, ""),
	))
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	call := toolCaller(t, f)

	out, err := call("metadata_rename", map[string]any{
		"field": "genres", "from": "Science Fiction & Fantasy, Fantasy",
		"split": map[string]any{"genres": []any{"Science Fiction & Fantasy"}, "tags": []any{"Fantasy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_updated"]) != 1 {
		t.Errorf("items_updated = %v", out["items_updated"])
	}
	sent := f.requests("/api/items/batch/update")
	if len(sent) != 1 {
		t.Fatalf("batch updates = %v", sent)
	}
	var body []struct {
		ID           string `json:"id"`
		MediaPayload struct {
			Tags     []string `json:"tags"`
			Metadata struct {
				Genres []string `json:"genres"`
			} `json:"metadata"`
		} `json:"mediaPayload"`
	}
	if err := json.Unmarshal([]byte(sent[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body[0].ID != bookB1 {
		t.Fatalf("updated %v, want only the book carrying the compound", body)
	}
	if got := body[0].MediaPayload; !slices.Equal(got.Metadata.Genres, []string{"Science Fiction & Fantasy"}) || !slices.Equal(got.Tags, []string{"witches", "Fantasy"}) {
		t.Errorf("sent genres %v tags %v", got.Metadata.Genres, got.Tags)
	}
	if _, err := call("metadata_rename", map[string]any{"field": "genres", "from": "x", "to": "y", "split": map[string]any{"tags": []any{"x"}}}); err == nil {
		t.Error("split with to was not refused")
	}
}

// A narrator named with a space at an end is the spaced one in every library
// that has it, and the libraries with only the tidy name are left alone; the
// rename audit_whitespace suggests, with no library, renames the spaced name
// and does not fail on the library that already has the tidy one.
func TestNarratorRenameTakesTheClosestSpellingEverywhere(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+otherLibID+`","name":"B","mediaType":"book"}]}`)
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
	f.json("GET /api/libraries/"+otherLibID+"/narrators", `{"narrators":[{"name":"Michael Kramer","numBooks":5}]}`)
	out, err := call("metadata_rename", map[string]any{"field": "narrators", "from": "Michael Kramer ", "to": "Michael Kramer"})
	if got := renamed(0); err != nil || isTrue(out["unchanged"]) || !slices.Equal(got, []string{libID[:1] + ":Michael Kramer "}) {
		t.Errorf("the suggested fix = %v, %v, renamed %v; want the spaced name in A renamed and B left alone", out, err, got)
	}
}

// Both spellings in A, the tidy one in B: asking for the spaced one renames
// it alone, never the tidy name of either library.
func TestNarratorRenameLeavesTheTidyNameAlone(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+otherLibID+`","name":"B","mediaType":"book"}]}`)
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Michael Kramer ","numBooks":2},{"name":"Michael Kramer","numBooks":9}]}`)
	f.json("GET /api/libraries/"+otherLibID+"/narrators", `{"narrators":[{"name":"Michael Kramer","numBooks":5}]}`)
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

// A rename whose from and to are the same, spaces and all, asks for nothing:
// it must not rename the spaced record onto its tidy twin and merge them.
// A rename run again, the name in two libraries already the one asked for,
// is done, not ambiguous.
func TestRenameThatAsksForNothingWritesNothing(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+otherLibID+`","name":"B","mediaType":"book"}]}`)
	author := func(id, lib, name string) string {
		return `{"id":"` + id + `","name":"` + name + `","libraryId":"` + lib + `","numBooks":1}`
	}
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[`+author("a1111111-1111-4111-8111-111111111111", libID, "Brandon Sanderson ")+`,`+author("a2222222-2222-4222-8222-222222222222", libID, "Brandon Sanderson")+`],"total":2}`)
	f.json("GET /api/libraries/"+otherLibID+"/authors", `{"results":[`+author("a3333333-3333-4333-8333-333333333333", otherLibID, "Brandon Sanderson")+`],"total":1}`)
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

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+otherLibID+`","name":"B","mediaType":"book"}]}`)
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Michael Kramer","numBooks":3}]}`)
	f.json("GET /api/libraries/"+otherLibID+"/narrators", `{"narrators":[{"name":"Michael Kramer ","numBooks":1},{"name":" Michael Kramer","numBooks":1}]}`)
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

// vocabKey decides what counts as "the same value spelled differently", which
// is the whole basis of audit_spelling. Languages are special: en, eng and
// English are the same language but share no normalized spelling.
func TestVocabKey(t *testing.T) {
	t.Parallel()

	same := func(field string, values ...string) {
		t.Helper()
		first := vocabKey(field, values[0])
		for _, v := range values[1:] {
			if got := vocabKey(field, v); got != first {
				t.Errorf("%s: %q (%q) and %q (%q) should group together", field, values[0], first, v, got)
			}
		}
	}
	differ := func(field, a, b string) {
		t.Helper()
		if vocabKey(field, a) == vocabKey(field, b) {
			t.Errorf("%s: %q and %q should NOT group together", field, a, b)
		}
	}

	// the real mess found in a live library
	same("languages", "en", "eng", "English", "english", "ENGLISH")
	same("languages", "de", "ger", "deu", "German", "Deutsch")
	differ("languages", "en", "de")
	differ("languages", "en", "XXX")

	// everything else groups on spelling alone
	same("narrators", "Jim Dale", "jim dale", "JIM DALE")
	same("genres", "Sci-Fi", "sci fi", "Sci Fi")
	same("tags", "space-opera", "Space Opera", "space_opera")
	same("authors", "J.R.R. Tolkien", "J R R Tolkien", "j.r.r. tolkien") //nolint:dupword // initials, not a repeated word
	differ("narrators", "Jim Dale", "Jim Dales")
	differ("genres", "Science Fiction", "Science")

	// a language code is not folded when it is not one we know
	if knownLanguage("XXX") {
		t.Error("XXX should not be a recognized language")
	}
	if !knownLanguage("eng") || !knownLanguage("English") {
		t.Error("eng/English should be recognized")
	}
}

func TestVocabKeyEmpty(t *testing.T) {
	t.Parallel()

	for _, field := range vocabFields {
		if got := vocabKey(field, "   "); got != "" {
			t.Errorf("%s: blank value gave key %q", field, got)
		}
	}
}

// A language renamed in a book library and a podcast library at once: the
// book goes in the batch and the show by the route item_edit uses, which the
// server took on a library where its batch route answered 502 for podcasts.
func TestMetadataRenameSendsPodcastsOneAtATime(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	shelfRoutes(f)
	show := `{"id":"` + podcastID + `","libraryId":"` + podLibID + `","mediaType":"podcast","media":{"metadata":{"title":"The Show","language":"eng"}}}`
	f.json("GET /api/libraries/"+libID+"/items", page(item(bookB1, "First", `"language":"eng"`, "")))
	f.json("GET /api/libraries/"+otherLibID+"/items", page())
	f.json("GET /api/libraries/"+podLibID+"/items", page(show))
	f.json("POST /api/items/batch/update", `{"updates":1}`)
	f.json("PATCH /api/items/"+podcastID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	out, err := call("metadata_rename", map[string]any{"field": "languages", "from": "eng", "to": "English"})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_updated"]) != 2 {
		t.Errorf("answer = %v, want the book and the show", out)
	}
	batch, single := f.requests("/api/items/batch/update"), f.requests("/api/items/"+podcastID+"/media")
	if len(batch) != 1 || !strings.Contains(batch[0].Body, bookB1) || strings.Contains(batch[0].Body, podcastID) {
		t.Errorf("the batch = %v, want the book alone", batch)
	}
	if len(single) != 1 || !strings.Contains(single[0].Body, `"language":"English"`) {
		t.Errorf("the show was sent %v, want one edit of its own", single)
	}
}
