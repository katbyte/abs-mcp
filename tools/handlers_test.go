package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools end to end against a canned Audiobookshelf: the request a handler
// builds and the answer it projects, without a container. The live suite
// proves the canned shapes match a real server; these pin behaviour the
// fixtures there cannot reach (a library with covers, inconsistent spellings,
// more findings than the limit).

// fakeABS is a canned Audiobookshelf: routes on a ServeMux plus a record of
// every request the tools made to it.
type fakeABS struct {
	mux *http.ServeMux
	srv *httptest.Server

	mu   sync.Mutex
	seen []request
}

type request struct {
	Method, Path, Query string
	Body                string
}

func newFakeABS(t *testing.T) *fakeABS {
	t.Helper()

	f := &fakeABS{mux: http.NewServeMux()}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, request{r.Method, r.URL.Path, r.URL.RawQuery, string(body)})
		f.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)

	return f
}

// json registers a route ("GET /api/libraries") that answers with a body.
func (f *fakeABS) json(route, body string) {
	f.mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
}

// requests returns the calls made to a path, in order.
func (f *fakeABS) requests(path string) []request {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []request
	for _, r := range f.seen {
		if r.Path == path {
			out = append(out, r)
		}
	}

	return out
}

// toolCaller registers every tool against the fake and returns a function
// that calls one over an in-memory MCP session, the way a client would.
func toolCaller(t *testing.T, f *fakeABS) func(name string, args map[string]any) (map[string]any, error) {
	t.Helper()

	client, err := abs.New(f.srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	if _, err := RegisterAll(srv, client, Options{EnableDelete: true}); err != nil {
		t.Fatal(err)
	}
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

const (
	libID  = "11111111-1111-4111-8111-111111111111"
	itemID = "22222222-2222-4222-8222-222222222222"
)

// item is a minified library item as the listing endpoints return it, with
// extra metadata fields and extra media fields spliced in as raw JSON.
func item(id, title, meta, media string) string {
	if meta != "" {
		meta = "," + meta
	}
	if media != "" {
		media = "," + media
	}
	return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"A/` + title + `","media":{"metadata":{"title":"` + title + `"` + meta + `}` + media + `}}`
}

func page(items ...string) string {
	return fmt.Sprintf(`{"results":[%s],"total":%d,"limit":500,"page":0}`, strings.Join(items, ","), len(items))
}

func oneLibrary(f *fakeABS) {
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"}]}`)
}

func num(t *testing.T, v any) int {
	t.Helper()

	n, ok := v.(float64)
	if !ok {
		t.Fatalf("%v is %T, want a number", v, v)
	}

	return int(n)
}

func boolOf(t *testing.T, v any) bool {
	t.Helper()

	b, ok := v.(bool)
	if !ok {
		t.Fatalf("%v is %T, want a bool", v, v)
	}

	return b
}

// str reads a string field, treating an absent one as empty.
func str(t *testing.T, v any) string {
	t.Helper()

	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%v is %T, want a string", v, v)
	}

	return s
}

func list(t *testing.T, v any) []map[string]any {
	t.Helper()

	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%v is %T, want a list", v, v)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		row, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("%v is %T, want an object", e, e)
		}
		out = append(out, row)
	}

	return out
}

// clear has to reach the server as an empty list for each field, or the
// server sees nothing to change and the field keeps its value.
func TestItemEditClearSendsEmptyLists(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"narratorName":"Scott Brick"`, ""))
	f.json("PATCH /api/items/"+itemID+"/media", `{"updated":true}`)
	call := toolCaller(t, f)

	out, err := call("item_edit", map[string]any{"item": itemID, "clear": []any{"narrators", "series", "genres", "tags"}})
	if err != nil {
		t.Fatal(err)
	}
	if updated, ok := out["updated"].(bool); !ok || !updated {
		t.Errorf("updated = %v", out["updated"])
	}

	sent := f.requests("/api/items/" + itemID + "/media")
	if len(sent) != 1 {
		t.Fatalf("PATCH sent %d times", len(sent))
	}
	var body struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
		Tags     json.RawMessage            `json:"tags"`
	}
	if err := json.Unmarshal([]byte(sent[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"narrators", "series", "genres"} {
		if string(body.Metadata[field]) != "[]" {
			t.Errorf("%s sent as %s, want [] (body %s)", field, body.Metadata[field], sent[0].Body)
		}
	}
	if string(body.Tags) != "[]" {
		t.Errorf("tags sent as %s, want []", body.Tags)
	}
	if _, present := body.Metadata["title"]; present {
		t.Errorf("an untouched field was sent: %s", sent[0].Body)
	}
}

// Removing a cover is asked for with remove; a call that forgot its url is an
// error, not a deletion.
func TestItemCoverEditNeedsIntent(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	var covered atomic.Bool
	covered.Store(true)
	f.mux.HandleFunc("GET /api/items/"+itemID, func(w http.ResponseWriter, _ *http.Request) {
		if covered.Load() {
			_, _ = io.WriteString(w, item(itemID, "Dune", "", `"coverPath":"/c.jpg"`))
			return
		}
		_, _ = io.WriteString(w, item(itemID, "Dune", "", ""))
	})
	f.mux.HandleFunc("DELETE /api/items/"+itemID+"/cover", func(w http.ResponseWriter, _ *http.Request) {
		covered.Store(false)
		_, _ = io.WriteString(w, `{}`)
	})
	f.mux.HandleFunc("POST /api/items/"+itemID+"/cover", func(w http.ResponseWriter, _ *http.Request) {
		covered.Store(true)
		_, _ = io.WriteString(w, `{}`)
	})
	call := toolCaller(t, f)

	if _, err := call("item_cover_edit", map[string]any{"item": itemID}); err == nil {
		t.Error("a call with nothing to set was not refused")
	}
	if got := f.requests("/api/items/" + itemID + "/cover"); len(got) != 0 {
		t.Errorf("the cover was touched: %v", got)
	}

	out, err := call("item_cover_edit", map[string]any{"item": itemID, "remove": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/items/" + itemID + "/cover"); len(got) != 1 || got[0].Method != http.MethodDelete {
		t.Errorf("remove sent %v, want one DELETE", got)
	}
	if out["cover"] != "" {
		t.Errorf("after the removal cover = %v, want none, read back", out["cover"])
	}

	if out, err = call("item_cover_edit", map[string]any{"item": itemID, "url": "http://img/c.jpg"}); err != nil {
		t.Fatal(err)
	}
	if out["cover"] != "/c.jpg" {
		t.Errorf("after setting cover = %v, want the one read back", out["cover"])
	}
	if got := f.requests("/api/items/" + itemID + "/cover"); len(got) != 2 || got[1].Method != http.MethodPost || !strings.Contains(got[1].Body, "http://img/c.jpg") {
		t.Errorf("url sent %v, want a POST carrying the url", got)
	}
}

func pngOf(t *testing.T, w, h int) string {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

// The cover audit measures the file on disk (raw=1, not the server's 400-wide
// cache), and does not ask for a cover the listing already says is absent.
func TestAuditCoversMeasuresTheFile(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Square", "", `"coverPath":"/1.jpg"`),
		item("i2", "Tall", "", `"coverPath":"/2.jpg"`),
		item("i3", "Tiny", "", `"coverPath":"/3.jpg"`),
		item("i4", "Bare", "", ""),
	))
	covers := map[string]string{"i1": pngOf(t, 600, 600), "i2": pngOf(t, 300, 600), "i3": pngOf(t, 200, 200)}
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("raw") != "1" {
			http.Error(w, "the cache, not the file", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, covers[r.PathValue("id")])
	})
	call := toolCaller(t, f)

	out, err := call("audit_covers", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["covers_checked"]); got != 3 {
		t.Errorf("covers_checked = %d, want 3", got)
	}
	if _, skipped := out["skipped"]; skipped {
		t.Errorf("skipped = %v, want none: a missing cover is a finding, not a skip", out["skipped"])
	}
	if got := f.requests("/api/items/i4/cover"); len(got) != 0 {
		t.Errorf("a coverless item was fetched: %v", got)
	}

	why := map[string]string{}
	problem := map[string]string{}
	for _, row := range list(t, out["findings"]) {
		why[str(t, row["id"])] = str(t, row["why"])
		problem[str(t, row["id"])] = str(t, row["problem"])
	}
	if problem["i4"] != "missing" {
		t.Errorf("the coverless item: %q %q", problem["i4"], why["i4"])
	}
	if problem["i2"] != "ratio" || problem["i3"] != "small" {
		t.Errorf("problems = %v", problem)
	}
	if !strings.HasPrefix(why["i2"], "not square") {
		t.Errorf("the tall cover: %q", why["i2"])
	}
	if !strings.HasPrefix(why["i3"], "only 200px") {
		t.Errorf("the small cover: %q", why["i3"])
	}
	if _, flagged := why["i1"]; flagged {
		t.Errorf("the square cover was flagged: %q", why["i1"])
	}
}

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

// A worklist stays at its limit across libraries, and the request for a
// library whose findings are not wanted still asks for one row rather than
// no limit at all, which the server reads as everything.
func TestAuditMissingLimitHoldsAcrossLibraries(t *testing.T) {
	t.Parallel()

	const libB = "33333333-3333-4333-8333-333333333333"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+libB+`","name":"B","mediaType":"book"}]}`)
	f.json("GET /api/libraries/"+libID+"/items", `{"results":[`+item("a1", "One", "", "")+`],"total":3}`)
	f.json("GET /api/libraries/"+libB+"/items", `{"results":[`+item("b1", "Two", "", "")+`],"total":2}`)
	call := toolCaller(t, f)

	out, err := call("audit_missing", map[string]any{"field": "cover", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["total_findings"]); got != 5 {
		t.Errorf("total_findings = %d, want 3+2", got)
	}
	if got := list(t, out["findings"]); len(got) != 1 {
		t.Errorf("findings = %d, want the limit of 1", len(got))
	}
	// the second library is asked twice: once for its size, once filtered,
	// and the filtered request carries the limit
	var filtered []request
	for _, r := range f.requests("/api/libraries/" + libB + "/items") {
		if strings.Contains(r.Query, "filter=") {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) != 1 || !strings.Contains(filtered[0].Query, "limit=1") {
		t.Errorf("second library asked with %v, want one filtered request with limit=1", filtered)
	}
}

// audit_all agrees with the individual tools on the same library, covers the
// cross-item audits too, and names the two it leaves out unless asked to go
// deep.
func TestAuditAllMatchesTheAudits(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f) // a library whose own store can look its asins up
	f.json("GET /api/search/books", `[]`)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Dune", `"authorName":"Frank Herbert","asin":"B0","genres":["Sci-Fi"]`, `"coverPath":"/c.jpg","numTracks":1,"numAudioFiles":1`),
		item("i2", "Neuromancer", `"authorName":"Neuromancer"`, ""),
		item("i3", "Dune", `"authorName":"Frank Herbert","asin":"b0","genres":["sci fi"]`, `"coverPath":"/c.jpg","numTracks":1,"numAudioFiles":1`),
	))
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1","name":"Frank Herbert","numBooks":2}],"total":1}`)
	tiny := pngOf(t, 100, 100)
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, tiny)
	})
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+item("i1", "Dune", "", `"audioFiles":[{"index":1,"metaTags":{}}]`)+`]}`)
	call := toolCaller(t, f)

	counts := func(out map[string]any) map[string]int {
		found := map[string]int{}
		for _, row := range list(t, out["audits"]) {
			name := str(t, row["audit"])
			if field := str(t, row["field"]); field != "" {
				name += " " + field
			}
			found[name] = num(t, row["found"])
		}
		return found
	}

	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, all["items_scanned"]); got != 3 {
		t.Errorf("items_scanned = %d", got)
	}
	found := counts(all)
	for name, want := range map[string]int{
		"audit_unmatched": 1, "audit_missing cover": 1, "audit_no_audio": 1,
		"audit_duplicates": 1, "audit_spelling": 1, "audit_authors": 2,
	} {
		if found[name] != want {
			t.Errorf("%s = %d, want %d (all: %v)", name, found[name], want, found)
		}
	}
	clean, ok := all["clean"].([]any)
	if !ok {
		t.Fatalf("clean is %T, want a list", all["clean"])
	}
	for _, name := range []string{"audit_issues", "audit_series"} {
		if !slices.Contains(clean, any(name)) {
			t.Errorf("%s should be clean: %v", name, all["clean"])
		}
	}
	skipped, ok := all["skipped"].([]any)
	if !ok {
		t.Fatalf("skipped is %T, want a list", all["skipped"])
	}
	if !slices.Equal(skipped, []any{"audit_covers", "audit_unembedded", "audit_matched", "audit_abridged"}) {
		t.Errorf("skipped = %v, want the four per-item-request audits", all["skipped"])
	}
	if _, ran := found["audit_covers"]; ran || slices.Contains(clean, any("audit_covers")) {
		t.Errorf("audit_covers was reported without deep: %v", all)
	}
	if got := f.requests("/api/items/i1/cover"); len(got) != 0 {
		t.Errorf("a cover was fetched without deep: %v", got)
	}

	deep, err := call("audit_all", map[string]any{"deep": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := deep["skipped"]; present {
		t.Errorf("deep still skipped something: %v", deep["skipped"])
	}
	if deepFound := counts(deep); deepFound["audit_covers"] != 3 || deepFound["audit_unembedded"] != 1 {
		t.Errorf("deep found %v, want 2 tiny covers plus 1 missing, and 1 unembedded book", deepFound)
	}

	// and every count is what the audit itself says
	for _, name := range []string{"audit_duplicates", "audit_spelling", "audit_authors"} {
		one, err := call(name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := num(t, one["total_findings"]); got != found[name] {
			t.Errorf("%s says %d, audit_all said %d", name, got, found[name])
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

	const libB = "33333333-3333-4333-8333-333333333333"
	const authorID = "44444444-4444-4444-8444-444444444444"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+libB+`","name":"B","mediaType":"book"}]}`)
	f.json("POST /api/tags/rename", `{"numItemsUpdated":4}`)
	f.json("DELETE /api/genres/{id}", `{"numItemsUpdated":2}`)
	f.json("PATCH /api/libraries/{lib}/narrators/{id}", `{"updated":3}`)
	f.json("DELETE /api/libraries/{lib}/narrators/{id}", `{"updated":1}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"`+authorID+`","name":"jrr tolkien","libraryId":"`+libID+`","numBooks":2}],"total":1}`)
	f.json("GET /api/libraries/"+libB+"/authors", `{"results":[],"total":0}`)
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
	if sent := f.requests("/api/libraries/" + libB + "/narrators/amltIGRhbGU="); len(sent) != 1 || sent[0].Method != http.MethodPatch {
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

// audit_unembedded reads the tags off the expanded items, fetched in batches:
// a book with nothing in its files, one whose tags are behind an edit, and
// one that was embedded after its last edit.
func TestAuditUnembeddedReadsFileTags(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Dune", "", `"numAudioFiles":2`),
		item("i2", "Neuromancer", "", `"numAudioFiles":1`),
		item("i3", "Foundation", "", `"numAudioFiles":1`),
		item("i4", "Ebook Only", "", ""),
	))
	expanded := func(id, title, tags string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"A/` + title + `",` +
			`"media":{"metadata":{"title":"` + title + `","authorName":"Frank Herbert","narratorName":"Scott Brick","seriesName":"Dune #1","genres":["Science Fiction","Classic"],"publishedYear":"1965"},` +
			`"audioFiles":[{"index":1,"metaTags":{` + tags + `}},{"index":2,"metaTags":{` + tags + `}}]}}`
	}
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+
		expanded("i1", "Dune", `"tagTrack":"1","tagEncoder":"LAME"`)+","+
		expanded("i2", "Neuromancer", `"tagTitle":"Neuromancer","tagArtist":"William Gibson","tagGenre":"Science Fiction","tagDate":"1965"`)+","+
		expanded("i3", "Foundation", `"tagTitle":"Foundation","tagAlbumArtist":"frank herbert","tagComposer":"Scott Brick","tagGrouping":"Dune #1","tagGenre":"Classic; Science Fiction","tagDate":"1965-01-01"`)+
		`]}`)
	call := toolCaller(t, f)

	out, err := call("audit_unembedded", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_scanned"]); got != 3 {
		t.Errorf("items_scanned = %d, want the 3 books with audio", got)
	}
	if got := num(t, out["total_findings"]); got != 2 {
		t.Errorf("total_findings = %d, want 2", got)
	}
	detail := map[string]string{}
	for _, row := range list(t, out["findings"]) {
		detail[str(t, row["id"])] = str(t, row["detail"])
	}
	if !strings.HasPrefix(detail["i1"], "2 of 2 files carry no tags") {
		t.Errorf("untagged book: %q", detail["i1"])
	}
	for _, want := range []string{`author "William Gibson" vs "Frank Herbert"`, "no narrator tag", "no series tag", `genres "Science Fiction" vs "Science Fiction; Classic"`} {
		if !strings.Contains(detail["i2"], want) {
			t.Errorf("stale book lacks %q: %q", want, detail["i2"])
		}
	}
	if _, flagged := detail["i3"]; flagged {
		t.Errorf("an embedded book was flagged: %q", detail["i3"])
	}

	// the ebook-only item was never asked for
	batches := f.requests("/api/items/batch/get")
	if len(batches) != 1 || strings.Contains(batches[0].Body, `"i4"`) || !strings.Contains(batches[0].Body, `"i3"`) {
		t.Errorf("batch request %v", batches)
	}
}

// The per-file comparison, on the shapes an embed and a ripper leave behind.
func TestEmbedMismatches(t *testing.T) {
	t.Parallel()

	m := &abs.Metadata{Title: "Dune", AuthorName: "Frank Herbert", Genres: []string{"SF"}, PublishedYear: "1965"}
	af := func(tags map[string]string) *abs.AudioFile { return &abs.AudioFile{MetaTags: tags} }

	if got := embedMismatches(m, af(map[string]string{"tagTitle": "Chapter 1", "tagAlbum": "Dune: A Novel", "tagArtist": "FRANK HERBERT", "tagGenre": "sf", "tagDate": "1965"})); len(got) != 0 {
		t.Errorf("a chaptered track under the right album was flagged: %v", got)
	}
	got := embedMismatches(m, af(map[string]string{"tagTitle": "Dune", "tagArtist": "Unknown"}))
	if !slices.Equal(got, []string{`author "Unknown" vs "Frank Herbert"`, "no genres tag", "no year tag"}) {
		t.Errorf("mismatches = %v", got)
	}
	// fields the item lacks are not expected in the file
	if got := embedMismatches(&abs.Metadata{Title: "Dune"}, af(map[string]string{"tagTitle": "Dune"})); len(got) != 0 {
		t.Errorf("absent metadata demanded a tag: %v", got)
	}

	if !tagsEmpty(af(map[string]string{"tagTrack": "3", "tagEncoder": "LAME"})) || tagsEmpty(af(map[string]string{"tagAlbum": "x"})) {
		t.Error("tagsEmpty: ripper leftovers count as tags, or an album does not")
	}
	for tag, want := range map[string]bool{"SF; Classic": true, "classic/sf": true, "SF": false, "SF; Classic; Space Opera": false} {
		if got := sameList(tag, []string{"SF", "Classic"}); got != want {
			t.Errorf("sameList(%q) = %v", tag, got)
		}
	}
}

// The server's search also answers on subtitle, asin and isbn (and older
// servers on authors and narrators), without saying which field matched. A
// lone hit whose title does not contain the words asked for is not the item
// that was named: it is refused with the id on offer, never acted on.
func TestResolveItemRejectsNonTitleMatch(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/search", `{"book":[{"libraryItem":`+item(itemID, "Dune Messiah", `"asin":"B0DUNE"`, "")+`}]}`)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune Messiah", `"asin":"B0DUNE"`, ""))
	f.json("DELETE /api/items/"+itemID, `{}`)
	call := toolCaller(t, f)

	_, err := call("item_delete", map[string]any{"item": "B0DUNE"})
	if err == nil {
		t.Fatal("an item whose title does not contain the query was resolved, and deleted")
	}
	if !strings.Contains(err.Error(), "another field") || !strings.Contains(err.Error(), itemID) {
		t.Errorf("the refusal does not say what matched or offer the id: %v", err)
	}
	if got := f.requests("/api/items/" + itemID); len(got) != 0 {
		t.Errorf("the item was touched: %v", got)
	}

	// a title that contains the words asked for is the partial match a
	// lookup has always accepted when it is the only one
	out, err := call("item_get", map[string]any{"item": "Messiah"})
	if err != nil {
		t.Fatal(err)
	}
	if got := str(t, out["id"]); got != itemID {
		t.Errorf("resolved %q, want %s", got, itemID)
	}
}

// A match with nothing to name the book would take the provider's first hit
// unseen; the omitted argument is an error, not a quick match.
func TestItemMatchApplyNeedsACandidate(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"authorName":"Frank Herbert"`, ""))
	f.json("POST /api/items/"+itemID+"/match", `{"updated":true,"libraryItem":`+item(itemID, "Dune", `"authorName":"Frank Herbert","asin":"B9"`, "")+`}`)
	call := toolCaller(t, f)

	if _, err := call("item_match_apply", map[string]any{"item": itemID, "override_details": true}); err == nil {
		t.Error("a match naming no candidate, asin or isbn was not refused")
	}
	if got := f.requests("/api/items/" + itemID + "/match"); len(got) != 0 {
		t.Errorf("the match was sent anyway: %v", got)
	}

	out, err := call("item_match_apply", map[string]any{"item": itemID, "asin": "B0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/items/" + itemID + "/match"); len(got) != 1 || !strings.Contains(got[0].Body, `"asin":"B0"`) {
		t.Errorf("asin sent %v, want one POST carrying it", got)
	}
	// what was applied is echoed, and an id the item did not take is called out
	applied, ok := out["applied"].(map[string]any)
	if !ok || str(t, applied["asin"]) != "B0" {
		t.Errorf("applied = %v, want the asin sent", out["applied"])
	}
	if !strings.Contains(str(t, out["warning"]), "B0") {
		t.Errorf("warning = %q, want one saying the item kept its asin", out["warning"])
	}
}

// An author on the second page of the listing is still an author: a name
// lookup pages until the server has no more.
func TestResolveAuthorPagesPastTheFirst(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/authors", func(w http.ResponseWriter, r *http.Request) {
		var rows []string
		if r.URL.Query().Get("page") == "0" {
			for i := range authorPageSize {
				rows = append(rows, fmt.Sprintf(`{"id":"a%d","name":"Author %d"}`, i, i))
			}
		} else {
			rows = append(rows, `{"id":"a-zed","name":"Zed Last"}`)
		}
		_, _ = fmt.Fprintf(w, `{"results":[%s],"total":%d}`, strings.Join(rows, ","), authorPageSize+1)
	})
	f.json("GET /api/authors/a-zed", `{"id":"a-zed","name":"Zed Last","libraryItems":[]}`)
	call := toolCaller(t, f)

	out, err := call("author_get", map[string]any{"author": "Zed Last"})
	if err != nil {
		t.Fatal(err)
	}
	if got := str(t, out["id"]); got != "a-zed" {
		t.Errorf("resolved %q, want a-zed", got)
	}
	if got := f.requests("/api/libraries/" + libID + "/authors"); len(got) != 2 {
		t.Errorf("fetched %d author pages, want 2", len(got))
	}
}

// A negative offset used to index past the end of the episode list, and the
// MCP transport has no recover: one bad argument took the whole server down.
func TestPodcastEpisodesOffsetBelowZero(t *testing.T) {
	t.Parallel()

	const podID = "33333333-3333-4333-8333-333333333333"
	f := newFakeABS(t)
	f.json("GET /api/items/"+podID, `{"id":"`+podID+`","libraryId":"`+libID+`","mediaType":"podcast","media":{"metadata":{"title":"Pod"},"episodes":[{"id":"e1","title":"One"},{"id":"e2","title":"Two"}]}}`)
	call := toolCaller(t, f)

	out, err := call("podcast_episodes", map[string]any{"item": podID, "offset": -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := list(t, out["episodes"]); len(got) != 2 {
		t.Errorf("episodes = %d, want both from the start", len(got))
	}
	if past, err := call("podcast_episodes", map[string]any{"item": podID, "offset": 5}); err != nil || len(list(t, past["episodes"])) != 0 {
		t.Errorf("offset past the end = %v, %v; want none", past, err)
	}

	// a page says where the next starts, and the last says nothing
	out, err = call("podcast_episodes", map[string]any{"item": podID, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["offset"]) != 0 || num(t, out["next_offset"]) != 1 {
		t.Errorf("first page = %v, want offset 0 and next_offset 1", out)
	}
	if out, err = call("podcast_episodes", map[string]any{"item": podID, "limit": 1, "offset": 1}); err != nil || out["next_offset"] != nil {
		t.Errorf("last page = %v, %v; want no next_offset", out, err)
	}
}

// An audit the server can filter for reports the library's size as scanned,
// not the number of hits, and a check that never fires for a podcast does not
// ask a podcast library with a book filter it does not know.
func TestNativeAuditCountsTheLibrary(t *testing.T) {
	t.Parallel()

	const podLib = "44444444-4444-4444-8444-444444444444"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"},{"id":"`+podLib+`","name":"Pods","mediaType":"podcast"}]}`)
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "" {
			_, _ = io.WriteString(w, `{"results":[`+item("i2", "Quiet", "", "")+`],"total":1}`)
			return
		}
		_, _ = io.WriteString(w, `{"results":[`+item("i1", "Dune", "", "")+`],"total":3}`)
	})
	f.mux.HandleFunc("GET /api/libraries/"+podLib+"/items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "" {
			_, _ = io.WriteString(w, `{"results":[],"total":0}`)
			return
		}
		pods := make([]string, 0, 5)
		for i := range 5 {
			pods = append(pods, fmt.Sprintf(`{"id":"p%d","mediaType":"podcast","media":{"coverPath":"/c.jpg","metadata":{"title":"Show %d"}}}`, i, i))
		}
		_, _ = io.WriteString(w, `{"results":[`+strings.Join(pods, ",")+`],"total":5}`)
	})
	call := toolCaller(t, f)

	out, err := call("audit_missing", map[string]any{"field": "narrator"})
	if err != nil {
		t.Fatal(err)
	}
	if scanned, found := num(t, out["items_scanned"]), num(t, out["total_findings"]); scanned != 3 || found != 1 {
		t.Errorf("narrator: scanned %d found %d, want 3 and 1", scanned, found)
	}
	if got := f.requests("/api/libraries/" + podLib + "/items"); len(got) != 0 {
		t.Errorf("the podcast library was asked about narrators: %v", got)
	}

	// a field podcasts have too still covers the podcast library
	out, err = call("audit_missing", map[string]any{"field": "cover"})
	if err != nil {
		t.Fatal(err)
	}
	if scanned, found := num(t, out["items_scanned"]), num(t, out["total_findings"]); scanned != 8 || found != 1 {
		t.Errorf("cover: scanned %d found %d, want 3+5 and 1", scanned, found)
	}
}

// clear is how a wrong author_match is undone: an empty description and asin
// have to reach the server as empty strings, and the photo has its own route.
func TestAuthorEditClear(t *testing.T) {
	t.Parallel()

	const authorID = "55555555-5555-4555-8555-555555555555"
	author := `{"id":"` + authorID + `","name":"Emily Andras","asin":"B00OKO2VB8","description":"someone else","imagePath":"/a.jpg","libraryItems":[]}`
	f := newFakeABS(t)
	f.json("GET /api/authors/"+authorID, author)
	f.json("PATCH /api/authors/"+authorID, `{"author":`+author+`,"merged":false}`)
	f.json("DELETE /api/authors/"+authorID+"/image", `{"author":`+author+`}`)
	call := toolCaller(t, f)

	if _, err := call("author_edit", map[string]any{"author": authorID, "clear": []any{"name"}}); err == nil {
		t.Error("clearing a field that cannot be blank was not refused")
	}

	if _, err := call("author_edit", map[string]any{"author": authorID, "clear": []any{"asin", "description", "image"}}); err != nil {
		t.Fatal(err)
	}
	// the reply to a PATCH carries no books, so the author is read back
	patched := f.requests("/api/authors/" + authorID)
	if len(patched) != 3 || patched[1].Method != http.MethodPatch || patched[2].Method != http.MethodGet {
		t.Fatalf("author requests = %v, want a GET, a PATCH, and a GET to read it back", patched)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patched[1].Body), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["asin"]) != `""` || string(body["description"]) != `""` {
		t.Errorf("PATCH sent %s, want asin and description as empty strings", patched[1].Body)
	}
	if _, present := body["name"]; present {
		t.Errorf("the name was sent: %s", patched[1].Body)
	}
	if got := f.requests("/api/authors/" + authorID + "/image"); len(got) != 1 || got[0].Method != http.MethodDelete {
		t.Errorf("image requests = %v, want one DELETE", got)
	}

	// clearing only the image sends no PATCH at all
	if _, err := call("author_edit", map[string]any{"author": authorID, "clear": []any{"image"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/authors/" + authorID); len(got) != 5 || got[3].Method != http.MethodGet || got[4].Method != http.MethodGet {
		t.Errorf("author requests after an image-only clear = %v, want only a GET and the read back", got)
	}
}

// an author with no photo can still have their image cleared: the server
// answers 400 to a DELETE with nothing to remove, so that route is not called.
func TestAuthorEditClearImageWithoutOne(t *testing.T) {
	t.Parallel()

	const authorID = "55555555-5555-4555-8555-555555555556"
	author := `{"id":"` + authorID + `","name":"Steve Wolf","asin":"B002BMG2T8","description":"","imagePath":"","libraryItems":[]}`
	f := newFakeABS(t)
	f.json("GET /api/authors/"+authorID, author)
	f.json("PATCH /api/authors/"+authorID, `{"author":`+author+`,"merged":false}`)
	call := toolCaller(t, f)

	if _, err := call("author_edit", map[string]any{"author": authorID, "clear": []any{"asin", "description", "image"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/authors/" + authorID); len(got) != 3 || got[1].Method != http.MethodPatch {
		t.Errorf("author requests = %v, want a GET, a PATCH and the read back", got)
	}
	if got := f.requests("/api/authors/" + authorID + "/image"); len(got) != 0 {
		t.Errorf("image requests = %v, want none for an author with no photo", got)
	}
}

// author_match only looks: the provider's name lookup is tolerant, so it can
// hand back someone else, and what it found is returned for a decision, never
// applied. author_match_apply is the write, by asin.
func TestAuthorMatchLooksAndApplyWrites(t *testing.T) {
	t.Parallel()

	const authorID = "66666666-6666-4666-8666-666666666666"
	author := `{"id":"` + authorID + `","name":"Sarah Diemer","libraryItems":[]}`
	f := newFakeABS(t)
	f.json("GET /api/authors/"+authorID, author)
	f.mux.HandleFunc("GET /api/search/authors", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("q") {
		case "Sarah Diemer":
			_, _ = io.WriteString(w, `{"asin":"B002LTD1MC","name":"Sarah Miller","description":"Wrote The Borden Murders.","image":"https://img/miller.jpg"}`)
		case "S. Diemer":
			_, _ = io.WriteString(w, `{"asin":"B0DIEMER","name":"Sarah Diemer","description":"Wrote The Dark Wife.","image":""}`)
		default:
			_, _ = io.WriteString(w, `null`)
		}
	})
	f.json("POST /api/authors/"+authorID+"/match", `{"updated":true,"author":`+author+`}`)
	call := toolCaller(t, f)

	// someone else: offered, flagged, not applied
	out, err := call("author_match", map[string]any{"author": authorID})
	if err != nil {
		t.Fatal(err)
	}
	cand, ok := out["candidate"].(map[string]any)
	if !ok {
		t.Fatalf("candidate = %T, want the author Audible found", out["candidate"])
	}
	if str(t, cand["name"]) != "Sarah Miller" || str(t, cand["asin"]) != "B002LTD1MC" || !boolOf(t, cand["has_image"]) || boolOf(t, cand["name_matches"]) {
		t.Errorf("candidate = %v", cand)
	}

	// the author's own name: still only offered, with name_matches set
	out, err = call("author_match", map[string]any{"author": authorID, "query": "S. Diemer"})
	if err != nil {
		t.Fatal(err)
	}
	if cand, ok := out["candidate"].(map[string]any); !ok || !boolOf(t, cand["name_matches"]) {
		t.Errorf("own name: candidate = %v, want name_matches", out["candidate"])
	}

	// nobody close enough: no candidate, no error
	out, err = call("author_match", map[string]any{"author": authorID, "query": "Nobody Atall"})
	if err != nil {
		t.Fatal(err)
	}
	if _, offered := out["candidate"]; offered {
		t.Errorf("a lookup that found nobody offered %v", out["candidate"])
	}
	if got := f.requests("/api/authors/" + authorID + "/match"); len(got) != 0 {
		t.Fatalf("author_match wrote something: %v", got)
	}

	// applying is a separate call, by asin, and nothing else
	if _, err := call("author_match_apply", map[string]any{"author": authorID}); err == nil {
		t.Error("author_match_apply with no asin was not refused")
	}
	out, err = call("author_match_apply", map[string]any{"author": authorID, "asin": "B0DIEMER", "region": "uk"})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["updated"]) {
		t.Errorf("apply reported no update: %v", out)
	}
	sent := f.requests("/api/authors/" + authorID + "/match")
	if len(sent) != 1 || !strings.Contains(sent[0].Body, `"asin":"B0DIEMER"`) || !strings.Contains(sent[0].Body, `"region":"uk"`) || strings.Contains(sent[0].Body, `"q"`) {
		t.Errorf("apply sent %v, want one POST by asin and region, no name query", sent)
	}
	if got := f.requests("/api/search/authors"); len(got) != 3 {
		t.Errorf("lookups = %d, want the 3 from author_match: apply needs none", len(got))
	}
}

func TestSameName(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"Ursula K. Le Guin", "ursula k le guin", true},
		{"N. K. Jemisin", "N.K. Jemisin", true},
		{"isaac asimov", "Isaac Asimov", true},
		{"Emily Andras", "Emily Adrian", false},
		{"Sarah Diemer", "Sarah Miller", false},
		{"Smedley D. Butler", "Smedley Butler", false},
	} {
		if got := sameName(c.a, c.b); got != c.same {
			t.Errorf("sameName(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// A description that says nothing is as missing as none.
func TestStubDescription(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]string{
		"":                             "no description",
		"  <p></p> ":                   "no description",
		"Read by Paul Heck":            "credit line",
		"<b>Narrated by</b> Jim Dale.": "credit line",
		"https://example.com/book":     "only a url",
		"Unabridged.":                  "stub, 11 characters",
		strings.Repeat("A real description of the book. ", 5): "",
	} {
		detail, flagged := stubDescription(text)
		if (want == "") == flagged || !strings.Contains(detail, want) {
			t.Errorf("stubDescription(%q) = %q, %v; want %q", text, detail, flagged, want)
		}
	}
}
