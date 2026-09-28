// The tools end to end against a canned Audiobookshelf: the request a handler
// builds and the answer it projects, without a container. The live suite
// proves the canned shapes match a real server; the tests on this one pin
// behaviour the fixtures there cannot reach (a library with covers,
// inconsistent spellings, more findings than the limit). The fake server, the
// ways to call the tools on it and the records it serves are all in this
// file; what reads the answers is in helpers_test.go.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type request struct {
	Method, Path, Query string
	Body                string
}

// reply is what a registered route answers.
type reply struct {
	status int
	body   string
}

// fakeABS is a canned Audiobookshelf: routes on a ServeMux plus a record of
// every request the tools made to it.
type fakeABS struct {
	mux *http.ServeMux
	srv *httptest.Server

	mu     sync.Mutex
	seen   []request
	bodies map[string]reply // what each json route answers, the latest registration's
}

// failsAfter serves a route that answers body n times, then 500.
func (f *fakeABS) failsAfter(route string, n int32, body string) {
	var served atomic.Int32
	f.mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) {
		if served.Add(1) > n {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// json registers a route ("GET /api/libraries") that answers with a body; a
// route registered again answers with the new body.
func (f *fakeABS) json(route, body string) { f.answer(route, reply{http.StatusOK, body}) }

// fails registers a route that answers 500, in place of any it had.
func (f *fakeABS) fails(route string) { f.answer(route, reply{http.StatusInternalServerError, "boom"}) }

func (f *fakeABS) answer(route string, r reply) {
	f.mu.Lock()
	_, known := f.bodies[route]
	f.bodies[route] = r
	f.mu.Unlock()
	if known {
		return
	}
	f.mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		r := f.bodies[route]
		f.mu.Unlock()
		if r.status != http.StatusOK {
			http.Error(w, r.body, r.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, r.body)
	})
}

// client is a client of the fake.
func (f *fakeABS) client(t *testing.T) *abs.Client {
	t.Helper()

	c, err := abs.New(f.srv.URL, "test")
	if err != nil {
		t.Fatal(err)
	}

	return c
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

// changes are the requests made so far that were not reads.
func (f *fakeABS) changes() []request {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []request
	for _, r := range f.seen {
		if r.Method != http.MethodGet {
			out = append(out, r)
		}
	}

	return out
}

func newFakeABS(t *testing.T) *fakeABS {
	t.Helper()

	f := &fakeABS{mux: http.NewServeMux(), bodies: map[string]reply{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, request{r.Method, r.URL.Path, r.URL.RawQuery, string(body)})
		f.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	// every library has a narrator list, empty unless a test serves its
	// own: audit_all and audit_whitespace read it for every book library
	f.json("GET /api/libraries/{lib}/narrators", `{"narrators":[]}`)
	// and the providers a server offers, as a real one lists them: the
	// match tools check a named store against it
	f.json("GET /api/search/providers", `{"providers":{"books":[{"value":"google"},{"value":"openlibrary"},{"value":"audible"},{"value":"audible.ca"},{"value":"audible.uk"},{"value":"audible.au"}],"podcasts":[{"value":"itunes"}]}}`)

	return f
}

// toolCaller registers every tool against the fake, the delete tools too,
// and returns a function that calls one over an in-memory MCP session, the
// way a client would.
func toolCaller(t *testing.T, f *fakeABS) func(name string, args map[string]any) (map[string]any, error) {
	t.Helper()

	return callerWith(t, f, Options{EnableDelete: true})
}

// callerWith registers the tools against the fake with opts and returns a
// function that calls one.
func callerWith(t *testing.T, f *fakeABS, opts Options) func(name string, args map[string]any) (map[string]any, error) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	if _, err := RegisterAll(srv, f.client(t), opts); err != nil {
		t.Fatal(err)
	}

	return connected(t, srv)
}

// registryCaller is toolCaller with the registry behind it, for a test that
// holds its locks.
func registryCaller(t *testing.T, f *fakeABS) (r *registry, call func(name string, args map[string]any) (map[string]any, error)) {
	t.Helper()

	client := f.client(t)
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	r = &registry{server: srv, client: client, opts: Options{EnableDelete: true}}
	queueTools(r)
	for _, p := range r.pending {
		p.register()
	}

	return r, connected(t, srv)
}

// connected calls srv's tools over an in-memory session and hands back the
// answer's structured content, or its error text as an error.
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

const (
	libID  = "11111111-1111-4111-8111-111111111111"
	itemID = "22222222-2222-4222-8222-222222222222"
)

const (
	otherLibID = "33333333-3333-4333-8333-333333333333"
	podLibID   = "44444444-4444-4444-8444-444444444444"
	bookB1     = "b1b1b1b1-0000-4000-8000-000000000001"
	bookB2     = "b1b1b1b1-0000-4000-8000-000000000002"
	bookB3     = "b1b1b1b1-0000-4000-8000-000000000003"
	otherBook  = "b1b1b1b1-0000-4000-8000-000000000009"
	podcastID  = "c1c1c1c1-0000-4000-8000-000000000001"
	playlistP  = "d1d1d1d1-0000-4000-8000-000000000001"
	playlistQ  = "d1d1d1d1-0000-4000-8000-000000000002"
	colC       = "e1e1e1e1-0000-4000-8000-000000000001"
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

// shelved is a minified book as the listing returns it, at a folder of its
// own, with extra metadata and media fields spliced in as raw JSON.
func shelved(id, path, title, author string, seconds float64, meta, media string) string {
	if meta != "" {
		meta = "," + meta
	}
	if media != "" {
		media = "," + media
	}
	return fmt.Sprintf(`{"id":%q,"libraryId":%q,"mediaType":"book","relPath":%q,"media":{"metadata":{"title":%q,"authorName":%q%s},"duration":%g%s}}`,
		id, libID, path, title, author, meta, seconds, media)
}

func page(items ...string) string {
	return fmt.Sprintf(`{"results":[%s],"total":%d,"limit":500,"page":0}`, strings.Join(items, ","), len(items))
}

// podcastWith is a podcast item holding episodes.
func podcastWith(episodes ...string) string {
	return `{"id":"` + podcastID + `","libraryId":"` + podLibID + `","mediaType":"podcast","media":{"metadata":{"title":"Show","feedUrl":"http://feed.test/show.xml"},"episodes":[` + strings.Join(episodes, ",") + `]}}`
}

func oneLibrary(f *fakeABS) {
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"}]}`)
}

// audit_matched looks each asin up and reports the books whose recording the
// asin does not describe.
// audibleLibrary is oneLibrary on an Audible store, where the library's own
// provider can look an asin up.
func audibleLibrary(f *fakeABS) {
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"}]}`)
}

// shelfRoutes is a book library of three books, a second book library with a
// book of its own, and a podcast library with one show of one episode.
func shelfRoutes(f *fakeABS) {
	f.json("GET /api/libraries", `{"libraries":[`+
		`{"id":"`+libID+`","name":"Books","mediaType":"book"},`+
		`{"id":"`+otherLibID+`","name":"Other","mediaType":"book"},`+
		`{"id":"`+podLibID+`","name":"Shows","mediaType":"podcast"}]}`)
	f.json("GET /api/items/"+bookB1, item(bookB1, "First", "", ""))
	f.json("GET /api/items/"+bookB2, item(bookB2, "Second", "", ""))
	f.json("GET /api/items/"+bookB3, item(bookB3, "Third", "", ""))
	f.json("GET /api/items/"+otherBook, `{"id":"`+otherBook+`","libraryId":"`+otherLibID+`","mediaType":"book","media":{"metadata":{"title":"Elsewhere"}}}`)
	f.json("GET /api/items/"+podcastID, `{"id":"`+podcastID+`","libraryId":"`+podLibID+`","mediaType":"podcast","media":{"metadata":{"title":"The Show"},"episodes":[{"id":"ep-1","title":"Pilot"}]}}`)
}

// servePages answers a library's item listing from n books titled "Book 000"
// on, paged by the limit and page asked for, the way the server pages.
func servePages(f *fakeABS, library string, n int) {
	f.mux.HandleFunc("GET /api/libraries/"+library+"/items", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if limit <= 0 {
			limit = n
		}
		var rows []string
		for i := pg * limit; i < min((pg+1)*limit, n); i++ {
			rows = append(rows, item(fmt.Sprintf("i%03d", i), fmt.Sprintf("Book %03d", i), "", ""))
		}
		_, _ = fmt.Fprintf(w, `{"results":[%s],"total":%d}`, strings.Join(rows, ","), n)
	})
}

// serveListing answers the library's item listing from books, paged by the
// limit and page asked for, the way the server pages.
func serveListing(f *fakeABS, books ...string) {
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/items", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if limit <= 0 {
			limit = max(len(books), 1)
		}
		lo, hi := min(pg*limit, len(books)), min((pg+1)*limit, len(books))
		_, _ = fmt.Fprintf(w, `{"results":[%s],"total":%d}`, strings.Join(books[lo:hi], ","), len(books))
	})
}

// serveWhole answers the batch fetch with the books asked for, from whole.
func serveWhole(f *fakeABS, whole map[string]string) {
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var asked struct {
			IDs []string `json:"libraryItemIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&asked)
		var out []string
		for _, id := range asked.IDs {
			if b, ok := whole[id]; ok {
				out = append(out, b)
			}
		}
		_, _ = fmt.Fprintf(w, `{"libraryItems":[%s]}`, strings.Join(out, ","))
	})
}

// fakeStore is a store's image host beside the fake, which the tools fetch
// covers from themselves: it serves /img/<name>.jpg at 1500 pixels and the
// "_SL500_" rendition of it at 500, drawn by draw, and answers 404 for a
// name draw has no picture for.
func fakeStore(t *testing.T, draw func(name string, size int) image.Image) *httptest.Server {
	t.Helper()

	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/img/")
		base, _, _ := strings.Cut(name, ".")
		size := 1500
		if strings.Contains(name, "_SL500_") {
			size = 500
		}
		img := draw(base, size)
		if img == nil {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, encodeJPEG(t, img, 85))
	}))
	t.Cleanup(store.Close)

	return store
}
