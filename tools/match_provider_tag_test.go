package tools

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A book whose store is already recorded is not written to again: a match
// found the provider tag in the middle of the collector's tags and sent the
// whole list back with the tag moved to the end, a write that changed nothing
// but the order, on every re-match of a book tagged after it was matched.
func TestAStoreAlreadyRecordedIsNotWrittenAgain(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	tagged := `"tags":["zz-provider:audible","mine"]`
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B0DUNE"`, tagged))
	f.json("POST /api/items/"+itemID+"/match", `{"updated":false,"libraryItem":`+item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B0DUNE"`, tagged)+`}`)
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	if _, err := call("item_match_apply", map[string]any{"confirm": true, "item": itemID, "asin": "B0DUNE", "providers": []any{"audible"}}); err != nil {
		t.Fatal(err)
	}
	out, err := call("item_match_apply_batch", map[string]any{"confirm": true, "matches": []any{map[string]any{"item": itemID, "asin": "B0DUNE", "provider": "audible"}}})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["unchanged"]) != 1 {
		t.Errorf("unchanged = %v, want the book counted as unchanged", out["unchanged"])
	}
	if writes := tagWrites(t, f, itemID); len(writes) != 0 {
		t.Errorf("tags sent %v to a book that already records the store", writes)
	}
}

// A store whose search fails says nothing about whether it has the book: the
// book is not counted as missing from the stores, not tagged with the next
// store, and the row says what failed.
func TestAStoreSearchThatFailsIsNotANotFound(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f)
	serveListing(f, item("m1", "One", `"asin":"B001"`, ""))
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("provider") == "audible.ca" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`[{"title":"One","asin":"B001"}]`))
	})
	call := toolCaller(t, f)

	out, err := call("item_match_tag", map[string]any{"library": "Books", "providers": []any{"audible.ca", "audible"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["rows"])
	if num(t, out["failed"]) != 1 || num(t, out["not_found"]) != 0 || num(t, out["tagged"]) != 0 || len(rows) != 1 || !strings.Contains(str(t, rows[0]["error"]), "audible.ca") {
		t.Errorf("tag = %v, want one failed row naming the store, nothing tagged or not found", out)
	}
	if got := f.requests("/api/search/books"); len(got) != 1 {
		t.Errorf("%d searches, want the failed store's only", len(got))
	}

	// and the provider list the names are checked against failing is an
	// error, not a check skipped
	f = newFakeABS(t)
	audibleLibrary(f)
	f.fails("GET /api/search/providers")
	_, err = toolCaller(t, f)("item_match_tag", map[string]any{"library": "Books", "providers": []any{"audible.ca"}})
	wantErr(t, "the provider list failing", err, "server's providers", "500")
}

// A library left on google (a new library's provider) with no providers
// named or configured had every asin looked up there, and every matched book
// came back not_found. The tools that look an asin up refuse it before any
// work, naming the library and the fix; audit_all deep leaves audit_matched
// out and says why.
func TestLookupRefusesALibraryOnGoogle(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"google"}]}`)
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"google"}`)
	book := item("li_1", "Dune", `"asin":"B0DUNE"`, "")
	f.json("GET /api/libraries/"+libID+"/items", page(book))
	f.json("GET /api/items/li_1", book)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	f.json("GET /api/search/books", `[]`)
	f.json("POST /api/items/batch/get", `{"libraryItems":[]}`) // audit_all deep reads every book's files
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"audit_matched", nil},
		{"audit_matched", map[string]any{"library": "Books", "filter": "genres:Fiction"}},
		{"audit_covers", map[string]any{"library": "Books", "store": true}},
		{"item_match_tag", map[string]any{"library": "Books"}},
		{"item_cover_upgrade", map[string]any{"confirm": true, "library": "Books", "items": []any{"li_1"}}},
		{"item_cover_upgrade", map[string]any{"confirm": true, "items": []any{"li_1"}}}, // the book's own library
	} {
		_, err := call(tc.tool, tc.args)
		if err == nil || !strings.Contains(err.Error(), `library "Books" is on the google provider, which cannot look up an asin`) || !strings.Contains(err.Error(), "--providers (ABS_PROVIDERS), e.g. audible.ca,audible") {
			t.Errorf("%s %v: %v, want the library, its provider and the fix", tc.tool, tc.args, err)
		}
	}
	// refused before the library was read or a store asked
	if got := f.requests("/api/libraries/" + libID + "/items"); len(got) != 0 {
		t.Errorf("a refused call listed the library: %v", got)
	}
	if got := f.requests("/api/search/books"); len(got) != 0 {
		t.Errorf("a refused call asked a store: %v", got)
	}

	all, err := call("audit_all", map[string]any{"deep": true})
	if err != nil {
		t.Fatal(err)
	}
	if skipped, ok := all["skipped"].([]any); !ok || !slices.Equal(skipped, []any{"audit_matched", "audit_abridged"}) {
		t.Errorf("audit_all deep skipped %v, want audit_matched and audit_abridged", all["skipped"])
	}
	var why string
	for _, row := range list(t, all["not_run"]) {
		if str(t, row["audit"]) == "audit_matched" {
			why = str(t, row["reason"])
		}
	}
	if !strings.Contains(why, `library "Books" is on the google provider`) {
		t.Errorf("audit_matched not run because %q, want the library and its provider", why)
	}
	if got := f.requests("/api/search/books"); len(got) != 0 {
		t.Errorf("audit_all deep asked google for an asin: %v", got)
	}

	// named providers, or the server's, are asked instead
	if out, err := call("audit_matched", map[string]any{"providers": []any{"audible"}}); err != nil || num(t, out["items_scanned"]) != 1 {
		t.Errorf("with providers: %v %v", out, err)
	}
	if out, err := callerWith(t, f, Options{Providers: []string{"audible.ca", "audible"}})("audit_matched", nil); err != nil || num(t, out["items_scanned"]) != 1 {
		t.Errorf("with --providers: %v %v", out, err)
	}
	// and a title search takes google as it is
	if _, err := call("item_match_batch", map[string]any{"filter": "genres:Fiction"}); err != nil {
		t.Errorf("item_match_batch on google: %v", err)
	}
}

func TestProviderTag(t *testing.T) {
	t.Parallel()

	var prov providerConfig // the default prefix
	it := &abs.Item{Media: abs.Media{Tags: []string{"Fantasy", defaultProviderTag + "audible.ca"}}}
	if prov.providerTag(it) != "audible.ca" {
		t.Errorf("providerTag = %q", prov.providerTag(it))
	}
	if got := prov.providerOrder(it, []string{"audible", "audible.ca"}); !slices.Equal(got, []string{"audible.ca", "audible"}) {
		t.Errorf("providerOrder = %v", got)
	}
	if got := prov.withProviderTag(it.Media.Tags, "audible"); !slices.Equal(got, []string{"Fantasy", defaultProviderTag + "audible"}) {
		t.Errorf("withProviderTag = %v", got)
	}
	none := &abs.Item{MediaType: "book", Media: abs.Media{Tags: []string{defaultProviderTag + "none"}}}
	if !prov.markedUnmatchable(none) {
		t.Error("provider:none not recognised")
	}
	if _, bad := prov.auditCheck("unmatched")(none); bad {
		t.Error("audit_unmatched reported a book marked provider:none")
	}
	if got := prov.providerOrder(none, []string{"audible"}); !slices.Equal(got, []string{"audible"}) {
		t.Errorf("providerOrder with none = %v", got)
	}
}

func TestProviderTagPrefix(t *testing.T) {
	t.Parallel()

	prov := providerConfig{tag: "provider:"}
	it := &abs.Item{Media: abs.Media{Tags: []string{"zz-provider:audible", "provider:audible.ca"}}}
	if got := prov.providerTag(it); got != "audible.ca" {
		t.Errorf("providerTag with a custom prefix = %q", got)
	}
	if got := prov.withProviderTag([]string{"Fantasy"}, "audible"); !slices.Equal(got, []string{"Fantasy", "provider:audible"}) {
		t.Errorf("withProviderTag = %v", got)
	}
	off := providerConfig{tag: "off"}
	if off.providerTag(it) != "" || !slices.Equal(off.withProviderTag([]string{"Fantasy"}, "audible"), []string{"Fantasy"}) {
		t.Error("off still tags")
	}
}

// tagWrites are the tag lists sent to an item, in order.
func tagWrites(t *testing.T, f *fakeABS, id string) [][]string {
	t.Helper()

	var out [][]string
	for _, req := range f.requests("/api/items/" + id + "/media") {
		var body struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
			t.Fatal(err)
		}
		if body.Tags != nil {
			out = append(out, body.Tags)
		}
	}

	return out
}

// A book with no tags gets the provider's from the match itself; the provider
// tag is added to those, not to the empty list the book had before. A smart
// match wrote the tag from the list as it was, and took the match's away.
func TestAMatchKeepsTheTagsItFilled(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert"`, ""))
	f.json("GET /api/search/books", `[{"title":"Dune","author":"Frank Herbert","asin":"B0DUNE"}]`)
	f.json("POST /api/items/"+itemID+"/match", `{"updated":true,"libraryItem":`+item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B0DUNE"`, `"tags":["Epic","Space Opera"]`)+`}`)
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	want := []string{"Epic", "Space Opera", "zz-provider:audible"}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"item_match_apply", map[string]any{"confirm": true, "item": itemID, "asin": "B0DUNE", "providers": []any{"audible"}, "smart": true}},
		{"item_match_apply_batch", map[string]any{"confirm": true, "matches": []any{map[string]any{"item": itemID, "asin": "B0DUNE", "provider": "audible"}}, "smart": true}},
		{"item_match_apply", map[string]any{"confirm": true, "item": itemID, "asin": "B0DUNE", "providers": []any{"audible"}}},
	} {
		before := len(tagWrites(t, f, itemID))
		if _, err := call(tc.tool, tc.args); err != nil {
			t.Fatal(err)
		}
		writes := tagWrites(t, f, itemID)
		if len(writes) != before+1 || !slices.Equal(writes[len(writes)-1], want) {
			t.Errorf("%s %v: tags sent %v, want the match's kept and the store added: %v", tc.tool, tc.args, writes[before:], want)
		}
	}
}

// A match that found nothing changed nothing: no store is recorded, which
// would have item_match_tag pass the book over, nothing kept is written back,
// and the warning does not send the caller to override_details.
func TestAMatchThatFoundNothingRecordsNothing(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert"`, `"tags":["Fiction"]`))
	f.json("POST /api/items/"+itemID+"/match", `{"warning":"No audible match found"}`)
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	out, err := call("item_match_apply", map[string]any{"confirm": true, "item": itemID, "asin": "B0NONE", "providers": []any{"audible"}, "override_details": true, "keep": []any{"title"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := str(t, out["warning"]); !strings.Contains(w, "No audible match found") || strings.Contains(w, "override_details") {
		t.Errorf("warning = %q, want the server's and no advice to override", w)
	}
	if boolOf(t, out["updated"]) || out["kept"] != nil {
		t.Errorf("answer = %v, want nothing updated or kept", out)
	}

	out, err = call("item_match_apply_batch", map[string]any{"confirm": true, "matches": []any{map[string]any{"item": itemID, "asin": "B0NONE", "provider": "audible"}}})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["applied"]) != 0 || num(t, out["unchanged"]) != 1 {
		t.Errorf("applied/unchanged = %v/%v, want the book counted as unchanged", out["applied"], out["unchanged"])
	}
	if got := f.requests("/api/items/" + itemID + "/media"); len(got) != 0 {
		t.Errorf("a match that found nothing wrote %v", got)
	}
}

// Two servers in one process each keep their own provider tag and default
// stores: registering the second one changed the first one's, and a server
// asking for the default tag kept whatever the one before had set.
func TestServersKeepTheirOwnProviderSettings(t *testing.T) {
	t.Parallel()

	const (
		bookA = "44444444-4444-4444-8444-00000000000a"
		bookB = "44444444-4444-4444-8444-00000000000b"
	)
	f := newFakeABS(t)
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"}`)
	for _, id := range []string{bookA, bookB} {
		f.json("GET /api/items/"+id, item(id, "Dune", "", ""))
		f.json("POST /api/items/"+id+"/match", `{"updated":true,"libraryItem":`+item(id, "Dune", `"asin":"B0DUNE"`, "")+`}`)
		f.json("PATCH /api/items/"+id+"/media", `{"updated":true}`)
	}
	callA := callerWith(t, f, Options{ProviderTag: "provider:", Providers: []string{"audible.ca"}})
	callB := callerWith(t, f, Options{})

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := callA("item_match_apply", map[string]any{"confirm": true, "item": bookA, "asin": "B0DUNE"}); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if _, err := callB("item_match_apply", map[string]any{"confirm": true, "item": bookB, "asin": "B0DUNE"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	for id, want := range map[string][]string{bookA: {"provider:audible.ca"}, bookB: {"zz-provider:audible"}} {
		for _, got := range tagWrites(t, f, id) {
			if !slices.Equal(got, want) {
				t.Errorf("%s tagged %v, want %v", id, got, want)
			}
		}
	}
}

// A store the server does not have is refused, naming the ones it does: the
// server answers an unknown name from Google, and every book came back not
// found with the region blamed. An asin lookup takes only an Audible store.
func TestProvidersAreCheckedAgainstTheServer(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/search/providers", `{"providers":{"books":[{"value":"google"},{"value":"audible"},{"value":"audible.ca"}],"podcasts":[{"value":"itunes"}]}}`)
	f.json("GET /api/libraries/"+libID+"/items", page(item("i1", "Dune", `"asin":"B0DUNE"`, "")))
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"item_match_batch", map[string]any{"providers": []any{"audible", "audibel.ca"}}, `no provider "audibel.ca"; it has google, audible, audible.ca`},
		{"audit_matched", map[string]any{"providers": []any{"google"}}, "google cannot look up an asin; only an Audible store can: audible, audible.ca"},
		{"item_match_tag", map[string]any{"library": "Books", "providers": []any{"Audible.ca"}}, `no provider "Audible.ca"`},
		{"audit_covers", map[string]any{"library": "Books", "store": true, "providers": []any{"google"}}, "cannot look up an asin"},
		{"item_cover_upgrade", map[string]any{"confirm": true, "items": []any{"li_1"}, "providers": []any{"google"}}, "cannot look up an asin"},
	} {
		_, err := call(tc.tool, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %v: %v, want %q", tc.tool, tc.args, err, tc.want)
		}
	}
	if got := f.requests("/api/search/books"); len(got) != 0 {
		t.Errorf("a refused provider was searched: %v", got)
	}

	// a title search takes any store the server has
	if _, err := call("item_match_batch", map[string]any{"providers": []any{"google"}}); err != nil {
		t.Errorf("google for a title search: %v", err)
	}

	// the server's own default is checked where it is used
	callBad := callerWith(t, f, Options{Providers: []string{"audible.cq"}})
	if _, err := callBad("audit_matched", nil); err == nil || !strings.Contains(err.Error(), `"audible.cq"`) {
		t.Errorf("a misspelt --providers: %v", err)
	}
}

// The provider tag's prefix is --provider-tag's to set, so text that names
// the tag says zz-provider: is only the default.
func TestProviderTagTextSaysItIsTheDefault(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	client := f.client(t)
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	if _, err := RegisterAll(srv, client, Options{}); err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if !slices.Contains([]string{"item_match_tag", "item_match_batch", "item_edit"}, tool.Name) {
			continue
		}
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for i := strings.Index(text, "zz-provider:"); i >= 0; {
			if near := text[i:min(len(text), i+60)]; !strings.Contains(near, "by default") {
				t.Errorf("%s names the tag without saying it is the default: %q", tool.Name, near)
			}
			next := strings.Index(text[i+1:], "zz-provider:")
			if next < 0 {
				break
			}
			i += next + 1
		}
	}
}

// The single-book match tools check a named provider too: the server answers
// a name it does not know by searching Google, so a misspelt store came back
// as Google's guesses with nothing to say so.
func TestSingleBookMatchesCheckTheProviderNamed(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/search/providers", `{"providers":{"books":[{"value":"google"},{"value":"audible"},{"value":"audible.ca"}],"podcasts":[{"value":"itunes"}]}}`)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", "", ""))
	f.json("GET /api/search/books", `[]`)
	f.json("GET /api/search/covers", `{"results":[]}`)
	call := toolCaller(t, f)

	for _, tool := range []string{"item_match", "item_match_apply", "item_cover_search"} {
		args := map[string]any{"item": itemID, "providers": []any{"audibel.ca"}}
		if tool == "item_match_apply" {
			args["asin"] = "B0DUNE"
		}
		if _, err := call(tool, args); err == nil || !strings.Contains(err.Error(), `no provider "audibel.ca"`) {
			t.Errorf("%s with a misspelt provider: %v", tool, err)
		}
	}
	for _, r := range f.seen {
		if strings.HasPrefix(r.Path, "/api/search/books") || strings.HasPrefix(r.Path, "/api/search/covers") || strings.Contains(r.Path, "/match") {
			t.Errorf("a refused provider was asked: %s %s", r.Method, r.Path)
		}
	}

	if _, err := call("item_match", map[string]any{"item": itemID, "providers": []any{"audible.ca"}}); err != nil {
		t.Errorf("a store the server has was refused: %v", err)
	}
}

// storesHolding serves the provider search so that only the stores named have
// candidates for a title and a record for an asin.
func storesHolding(f *fakeABS, holding ...string) {
	f.json("GET /api/search/providers", `{"providers":{"books":[{"value":"google"},{"value":"audible"},{"value":"audible.ca"}],"podcasts":[]}}`)
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(holding, r.URL.Query().Get("provider")) {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"title":"Dune","author":"Frank Herbert","asin":"B0DUNE"}]`))
	})
}

// asked is the stores the provider search was asked, in order.
func asked(t *testing.T, f *fakeABS) []string {
	t.Helper()

	reqs := f.requests("/api/search/books")
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, parseQuery(t, r.Query).Get("provider"))
	}
	return out
}

// The single-book tools take the stores in order, as the batch tools do: the
// first with any candidates answers, and the answer says which it was.
func TestItemMatchAsksEachProviderInOrder(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert"`, ""))
	f.json("GET /api/search/covers", `{"results":[]}`)
	storesHolding(f, "audible")
	call := toolCaller(t, f)

	out, err := call("item_match", map[string]any{"item": itemID, "providers": []any{"audible.ca", "audible", "google"}})
	if err != nil {
		t.Fatal(err)
	}
	if str(t, out["provider"]) != "audible" || len(list(t, out["candidates"])) != 1 {
		t.Errorf("answer = %v, want the candidate, from audible", out)
	}
	if got := asked(t, f); !slices.Equal(got, []string{"audible.ca", "audible"}) {
		t.Errorf("asked %v, want audible.ca then audible, and no further", got)
	}

	// no store has any: the last one asked is named, and that is an answer
	out, err = call("item_match", map[string]any{"item": itemID, "providers": []any{"audible.ca", "google"}})
	if err != nil || str(t, out["provider"]) != "google" || len(list(t, out["candidates"])) != 0 {
		t.Errorf("no store holding it = %v, %v", out, err)
	}

	// covers are asked for the same way
	out, err = call("item_cover_search", map[string]any{"item": itemID, "providers": []any{"audible.ca", "audible"}})
	if err != nil || str(t, out["provider"]) != "audible" {
		t.Errorf("item_cover_search = %v, %v; want both stores asked and the last named", out, err)
	}
	if got := f.requests("/api/search/covers"); len(got) != 2 {
		t.Errorf("covers asked %d times, want once a store", len(got))
	}
}

// With no store named the server's --providers are asked in order, the
// store the book's tag records first.
func TestItemMatchDefaultsToTheConfiguredProviders(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert"`, `"tags":["zz-provider:audible"]`))
	storesHolding(f)
	call := callerWith(t, f, Options{Providers: []string{"audible.ca", "audible"}})

	if _, err := call("item_match", map[string]any{"item": itemID}); err != nil {
		t.Fatal(err)
	}
	if got := asked(t, f); !slices.Equal(got, []string{"audible", "audible.ca"}) {
		t.Errorf("asked %v, want the recorded store and then the other", got)
	}
}

// An asin is applied from the first store that holds it, and without confirm
// nothing is applied: the answer says which book, from which store.
func TestItemMatchApplyFindsTheStoreAndWaitsForConfirm(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert"`, ""))
	f.json("POST /api/items/"+itemID+"/match", `{"updated":true,"libraryItem":`+item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B0DUNE"`, "")+`}`)
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	storesHolding(f, "audible")
	call := toolCaller(t, f)

	args := map[string]any{"item": itemID, "asin": "B0DUNE", "providers": []any{"audible.ca", "audible"}}
	out, err := call("item_match_apply", args)
	if err != nil {
		t.Fatal(err)
	}
	would, ok := out["would_apply"].(map[string]any)
	if !ok || str(t, would["provider"]) != "audible" || str(t, would["asin"]) != "B0DUNE" || out["applied"] != nil {
		t.Errorf("without confirm = %v, want would_apply naming audible", out)
	}
	if got := f.changes(); len(got) != 0 {
		t.Fatalf("without confirm sent %v", got)
	}

	args["confirm"] = true
	if out, err = call("item_match_apply", args); err != nil {
		t.Fatal(err)
	}
	applied, ok := out["applied"].(map[string]any)
	if !ok || str(t, applied["provider"]) != "audible" || !boolOf(t, out["updated"]) || out["would_apply"] != nil {
		t.Errorf("with confirm = %v, want it applied from audible", out)
	}
	matches := f.requests("/api/items/" + itemID + "/match")
	if len(matches) != 1 || !strings.Contains(matches[0].Body, `"provider":"audible"`) {
		t.Errorf("matched with %v, want one match at audible", matches)
	}
	if writes := tagWrites(t, f, itemID); len(writes) != 1 || !slices.Contains(writes[0], "zz-provider:audible") {
		t.Errorf("tags written %v, want the store that held it recorded", writes)
	}

	// no store holds it: an error naming them, and nothing sent
	sent := len(f.changes())
	_, err = call("item_match_apply", map[string]any{"item": itemID, "asin": "B0DUNE", "providers": []any{"audible.ca", "google"}, "confirm": true})
	wantErr(t, "an asin no store holds", err, "none of audible.ca, google", "B0DUNE")
	if len(f.changes()) != sent {
		t.Error("a match was sent for an asin no store holds")
	}
}

// A batch row that names no store is applied from the first of the call's
// that holds its asin; without confirm no row is applied and each says which
// store it would be.
func TestItemMatchApplyBatchFindsTheStoreAndWaitsForConfirm(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert"`, ""))
	f.json("POST /api/items/"+itemID+"/match", `{"updated":true,"libraryItem":`+item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B0DUNE"`, "")+`}`)
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	storesHolding(f, "audible")
	call := toolCaller(t, f)

	args := map[string]any{"matches": []any{map[string]any{"item": itemID, "asin": "B0DUNE"}}, "providers": []any{"audible.ca", "audible"}}
	out, err := call("item_match_apply_batch", args)
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["results"])
	if num(t, out["would_apply"]) != 1 || num(t, out["applied"]) != 0 || len(rows) != 1 || str(t, rows[0]["provider"]) != "audible" {
		t.Errorf("without confirm = %v, want one row that would be applied from audible", out)
	}
	if got := f.changes(); len(got) != 0 {
		t.Fatalf("without confirm sent %v", got)
	}

	args["confirm"] = true
	if out, err = call("item_match_apply_batch", args); err != nil {
		t.Fatal(err)
	}
	if num(t, out["applied"]) != 1 || out["would_apply"] != nil {
		t.Errorf("with confirm = %v, want the row applied", out)
	}
	if matches := f.requests("/api/items/" + itemID + "/match"); len(matches) != 1 || !strings.Contains(matches[0].Body, `"provider":"audible"`) {
		t.Errorf("matched with %v, want one match at audible", matches)
	}
}
