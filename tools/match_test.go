package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connected calls srv's tools over an in-memory session, the way toolCaller
// does, for a test that registers the tools itself.
func connected(t *testing.T, srv *mcp.Server) func(name string, args map[string]any) (map[string]any, error) {
	t.Helper()

	st, ct := mcp.NewInMemoryTransports()
	ctx := t.Context()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return func(name string, args map[string]any) (map[string]any, error) {
		res, err := session.CallTool(context.WithoutCancel(ctx), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return nil, err
		}
		if res.IsError {
			var msgs []string
			for _, c := range res.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					msgs = append(msgs, tc.Text)
				}
			}
			return nil, fmt.Errorf("%s: %s", name, strings.Join(msgs, "; "))
		}
		out, ok := res.StructuredContent.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: structured content is %T", name, res.StructuredContent)
		}

		return out, nil
	}
}

// callerWith is toolCaller for a server registered with opts.
func callerWith(t *testing.T, f *fakeABS, opts Options) func(name string, args map[string]any) (map[string]any, error) {
	t.Helper()

	client, err := abs.New(f.srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	if _, err := RegisterAll(srv, client, opts); err != nil {
		t.Fatal(err)
	}

	return connected(t, srv)
}

// registryCaller is toolCaller with the registry behind it, for a test that
// holds its locks.
func registryCaller(t *testing.T, f *fakeABS) (r *registry, call func(name string, args map[string]any) (map[string]any, error)) {
	t.Helper()

	client, err := abs.New(f.srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	r = &registry{server: srv, client: client, opts: Options{EnableDelete: true}}
	queueTools(r)
	for _, p := range r.pending {
		p.register()
	}

	return r, connected(t, srv)
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
		{"item_match_apply", map[string]any{"item": itemID, "asin": "B0DUNE", "provider": "audible", "smart": true}},
		{"item_match_apply_batch", map[string]any{"matches": []any{map[string]any{"item": itemID, "asin": "B0DUNE", "provider": "audible"}}, "smart": true}},
		{"item_match_apply", map[string]any{"item": itemID, "asin": "B0DUNE", "provider": "audible"}},
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

	out, err := call("item_match_apply", map[string]any{"item": itemID, "asin": "B0NONE", "provider": "audible", "override_details": true, "keep": []any{"title"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := str(t, out["warning"]); !strings.Contains(w, "No audible match found") || strings.Contains(w, "override_details") {
		t.Errorf("warning = %q, want the server's and no advice to override", w)
	}
	if boolOf(t, out["updated"]) || out["kept"] != nil {
		t.Errorf("answer = %v, want nothing updated or kept", out)
	}

	out, err = call("item_match_apply_batch", map[string]any{"matches": []any{map[string]any{"item": itemID, "asin": "B0NONE", "provider": "audible"}}})
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

// applied counts the books a batch changed, not the rows it sent: a preview
// changes nothing, and neither does a match with nothing new or one that
// found nothing.
func TestItemMatchApplyBatchCountsWhatChanged(t *testing.T) {
	t.Parallel()

	const (
		changed = "33333333-3333-4333-8333-000000000001"
		same    = "33333333-3333-4333-8333-000000000002"
		missing = "33333333-3333-4333-8333-000000000003"
	)
	f := newFakeABS(t)
	for _, id := range []string{changed, same, missing} {
		f.json("GET /api/items/"+id, item(id, "Dune", `"authorName":"Frank Herbert"`, ""))
		f.json("PATCH /api/items/"+id+"/media", `{"updated":true}`)
	}
	f.json("POST /api/items/"+changed+"/match", `{"updated":true,"libraryItem":`+item(changed, "Dune", `"asin":"B0DUNE"`, "")+`}`)
	f.json("POST /api/items/"+same+"/match", `{"updated":false,"libraryItem":`+item(same, "Dune", `"asin":"B0DUNE"`, "")+`}`)
	f.json("POST /api/items/"+missing+"/match", `{"warning":"No audible match found"}`)
	f.json("GET /api/search/books", `[{"title":"Dune","author":"Frank Herbert","asin":"B0DUNE"}]`)
	call := toolCaller(t, f)

	rows := []any{
		map[string]any{"item": changed, "asin": "B0DUNE", "provider": "audible"},
		map[string]any{"item": same, "asin": "B0DUNE", "provider": "audible"},
		map[string]any{"item": missing, "asin": "B0DUNE", "provider": "audible"},
		map[string]any{"item": changed},
	}
	out, err := call("item_match_apply_batch", map[string]any{"matches": rows})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["applied"]) != 1 || num(t, out["unchanged"]) != 2 || num(t, out["failed"]) != 1 {
		t.Errorf("applied/unchanged/failed = %v/%v/%v, want 1/2/1", out["applied"], out["unchanged"], out["failed"])
	}

	out, err = call("item_match_apply_batch", map[string]any{"matches": rows[:1], "smart": true, "preview": true})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["applied"]) != 0 || num(t, out["previewed"]) != 1 {
		t.Errorf("preview: applied/previewed = %v/%v, want 0/1", out["applied"], out["previewed"])
	}
}

// Applying a window's rows takes those books out of missing:asin and the
// listing closes up behind them, so next_offset would skip as many books as
// were applied: the answer says to ask for the same offset again.
func TestItemMatchBatchSaysToAskForTheOffsetAgain(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", `{"results":[`+item("i1", "Dune", "", "")+`],"total":3,"limit":1,"page":0}`)
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	out, err := call("item_match_batch", map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["next_offset"]) != 1 || !strings.Contains(str(t, out["paging"]), "offset 0 again") {
		t.Errorf("next_offset/paging = %v/%q, want offset 0 asked for again once rows are applied", out["next_offset"], out["paging"])
	}

	out, err = call("item_match_batch", map[string]any{"limit": 1, "filter": "genres:Fiction"})
	if err != nil {
		t.Fatal(err)
	}
	if out["paging"] != nil {
		t.Errorf("paging = %v under a filter a match does not change", out["paging"])
	}
}

// A tolerance wide enough takes in the shorter editions it is there to tell
// apart, so every row comes back exact.
func TestItemMatchBatchBoundsTheTolerance(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	call := toolCaller(t, f)

	for _, tolerance := range []float64{0.5, 5, -0.1} {
		if _, err := call("item_match_batch", map[string]any{"tolerance": tolerance}); err == nil || !strings.Contains(err.Error(), "tolerance") {
			t.Errorf("tolerance %v: %v, want it refused", tolerance, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) != 0 {
		t.Errorf("a refused call reached the server: %v", f.seen)
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
			if _, err := callA("item_match_apply", map[string]any{"item": bookA, "asin": "B0DUNE"}); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if _, err := callB("item_match_apply", map[string]any{"item": bookB, "asin": "B0DUNE"}); err != nil {
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

// A row's hold on its book is let go however the row ends: one left behind
// by a panic, which the server now survives, stalled every later edit of the
// book for good.
func TestARowThatPanicsLetsGoOfItsBook(t *testing.T) {
	t.Parallel()

	for name, row := range map[string]func(r *registry){
		"item_match_apply_batch": func(r *registry) {
			r.applyRow(t.Context(), itemID, "audible", rowMatch{}, &applyResult{})
		},
		"item_match_tag": func(r *registry) {
			_ = r.tagOne(t.Context(), itemID, "audible")
		},
	} {
		r := &registry{} // no client: the read after the hold panics
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: the row did not panic", name)
				}
			}()
			row(r)
		}()
		released(t, &r.locks)
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
		{"item_cover_upgrade", map[string]any{"items": []any{"li_1"}, "providers": []any{"google"}}, "cannot look up an asin"},
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
	client, err := abs.New(f.srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
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
		if !slices.Contains([]string{"item_match_tag", "item_match_batch", "item_batch_edit"}, tool.Name) {
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
		args := map[string]any{"item": itemID, "provider": "audibel.ca"}
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

	if _, err := call("item_match", map[string]any{"item": itemID, "provider": "audible.ca"}); err != nil {
		t.Errorf("a store the server has was refused: %v", err)
	}
}
