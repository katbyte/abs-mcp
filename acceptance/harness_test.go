//go:build integration

// The harness: scripts/abs-testenv.sh brings the container up and exports
// ABS_SERVER, ABS_TOKEN and ABS_TEST_DATA, and everything here drives it
// through the MCP tools rather than the HTTP API, so building the fixtures is
// itself a test of library_create, library_scan, item_edit and the rest.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/katbyte/abs-mcp/lib/providerproxy"
	"github.com/katbyte/abs-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// libraryFixture is one of the seeded libraries. The catalogue is shaped so every
// audit has both something to find and something it must leave alone.
type libraryFixture struct {
	Name, MediaType, Folder string
	Items                   int
}

// libraries are created by library_create and filled by library_scan.
var libraries = []libraryFixture{
	{"Fiction", "book", "/fiction", 7},
	{"Non-Fiction", "book", "/nonfiction", 3},
	{"Podcasts", "podcast", "/podcasts", 2},
}

// bookFixture is a seeded item. Foundation is complete, The Expanse skips one book
// and Otherland skips two, so a clean series, a single gap and a run of gaps
// are all represented. Non-fiction carries no series but its own tags, which
// is what makes server_tag_get (server-wide) differ from library_filters.
type bookFixture struct {
	Title, Author, Narrator, Publisher, Year, Language string
	Series                                             []string
	Tags, Genres                                       []string
}

var books = []bookFixture{
	{"Foundation", "Isaac Asimov", "Scott Brick", "Bantam", "1951", "English", []string{"Foundation #1"}, []string{"sf", "classic"}, []string{"Science Fiction"}},
	{"Foundation and Empire", "Isaac Asimov", "Scott Brick", "Bantam", "1952", "English", []string{"Foundation #2"}, []string{"sf", "classic"}, []string{"Science Fiction"}},
	{"Second Foundation", "Isaac Asimov", "Scott Brick", "Bantam", "1953", "English", []string{"Foundation #3"}, []string{"sf", "classic"}, []string{"Science Fiction"}},
	{"City of Golden Shadow", "Tad Williams", "George Newbern", "DAW", "1996", "English", []string{"Otherland #1"}, []string{"sf", "cyberpunk"}, []string{"Science Fiction"}},
	{"Sea of Silver Light", "Tad Williams", "George Newbern", "DAW", "2001", "English", []string{"Otherland #4"}, []string{"sf", "cyberpunk"}, []string{"Science Fiction"}},
	{"Leviathan Wakes", "James S. A. Corey", "Jefferson Mays", "Orbit", "2011", "English", []string{"The Expanse #1"}, []string{"sf", "space-opera"}, []string{"Science Fiction"}},
	{"Abaddon's Gate", "James S. A. Corey", "Jefferson Mays", "Orbit", "2013", "English", []string{"The Expanse #3"}, []string{"sf", "space-opera"}, []string{"Science Fiction"}},
	{"The Arms of Krupp", "William Manchester", "Grover Gardner", "Little, Brown", "1968", "English", nil, []string{"history", "industry"}, []string{"History"}},
	{"A Brief History of Vice", "Robert Evans", "Robert Evans", "Plume", "2016", "English", nil, []string{"history", "humour"}, []string{"History"}},
	{"War Is a Racket", "Smedley D. Butler", "Grover Gardner", "Round Table", "1935", "English", nil, []string{"war", "politics"}, []string{"History"}},
}

// podcasts are the shows laid out on disk, each with two episodes.
var podcasts = []string{"Well There's Your Problem", "Behind the Bastards"}

var (
	ctx     context.Context
	session *mcp.ClientSession
	ready   bool
	proxy   *providerproxy.Proxy
)

// recording reports whether this run should call the real providers and
// refresh the cassettes, rather than replay them.
func recording() bool { return os.Getenv("ABS_TEST_RECORD") != "" }

// verifying reports whether to check the cassettes against the live providers
// without rewriting them.
func verifying() bool { return os.Getenv("ABS_TEST_VERIFY") != "" }

// providersReady reports whether the provider proxy came up, so the tests that
// need it can skip rather than fail confusingly when it did not.
func providersReady() bool { return proxy != nil }

// configured reports whether the container environment is present.
func configured() bool {
	return os.Getenv("ABS_SERVER") != "" && os.Getenv("ABS_TOKEN") != ""
}

// dataDir is the host path the container's library folders are bind-mounted
// from, so a test can add or remove files and rescan.
func dataDir() string { return os.Getenv("ABS_TEST_DATA") }

// testMain connects, starts the provider proxy, seeds the fixtures, and runs.
func testMain(m *testing.M) {
	if !configured() {
		os.Exit(m.Run()) // every test skips
	}
	if err := startProxy(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		os.Exit(1)
	}
	if err := start(); err != nil {
		stopProxy()
		fmt.Fprintln(os.Stderr, "abstest setup:", err)
		os.Exit(1)
	}

	code := m.Run()
	stopProxy()

	// a replay miss means a test ran against a 502 rather than a recording, so
	// say so loudly even when the assertions happened to survive it
	if misses := proxyMisses; len(misses) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d request(s) had no recording:\n", len(misses))
		for _, m := range misses {
			fmt.Fprintln(os.Stderr, "  "+m)
		}
		fmt.Fprintln(os.Stderr, "run `make record` to capture them")
		if code == 0 {
			code = 1
		}
	}

	// drift is only collected under ABS_TEST_VERIFY: the providers still
	// answer, but no longer in the shape the client decodes
	if drifts := proxyDrifts; len(drifts) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d response(s) changed shape since recording:\n", len(drifts))
		for _, d := range drifts {
			fmt.Fprintln(os.Stderr, "  "+d.String())
		}
		fmt.Fprintln(os.Stderr, "\nreview the changes, then run `make record` to accept them")
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

var (
	proxyMisses []string
	proxyDrifts []providerproxy.Drift
)

// startProxy brings up the record/replay proxy the container's HTTP_PROXY
// already points at. Audiobookshelf makes the provider calls, not us, so this
// is the only layer that can intercept them.
func startProxy() error {
	port := 18080
	if v := os.Getenv("ABS_TEST_PROXY_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("ABS_TEST_PROXY_PORT=%q: %w", v, err)
		}
		port = n
	}

	mode := providerproxy.Replay
	switch {
	case recording():
		mode = providerproxy.Record
	case verifying():
		mode = providerproxy.Verify
	}

	p, err := providerproxy.New(providerproxy.Options{
		Mode:        mode,
		CassetteDir: filepath.Join("testdata", "cassettes"),
		// all interfaces: the container reaches this through host.docker.internal
		Addr: "0.0.0.0:" + strconv.Itoa(port),
	})
	if err != nil {
		return err
	}
	proxy = p

	return nil
}

func stopProxy() {
	if proxy == nil {
		return
	}
	proxyMisses = proxy.Misses()
	proxyDrifts = proxy.Drifts()
	if err := proxy.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy close:", err)
	}
	proxy = nil
}

func start() error {
	client, err := abs.New(os.Getenv("ABS_SERVER"), os.Getenv("ABS_TOKEN"))
	if err != nil {
		return err
	}

	ctx = context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "abs-mcp", Version: "test"}, nil)
	if _, err := tools.RegisterAll(srv, client, tools.Options{EnableDelete: true}); err != nil {
		return err
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		return err
	}
	if session, err = mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil); err != nil {
		return err
	}
	ready = true

	return seed()
}

// seed builds the fixtures through the tools. It is idempotent: a library that
// already exists is left alone, so either suite can run first.
func seed() error {
	existing, err := invoke("library_list", nil)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	if rows, ok := existing["libraries"].([]any); ok {
		for _, r := range rows {
			if row, ok := r.(map[string]any); ok {
				if name, ok := row["name"].(string); ok {
					have[name] = true
				}
			}
		}
	}

	var created bool
	for _, l := range libraries {
		if have[l.Name] {
			continue
		}
		if _, err := invoke("library_create", map[string]any{
			"name": l.Name, "media_type": l.MediaType, "folders": []any{l.Folder},
		}); err != nil {
			return err
		}
		created = true
	}
	if !created {
		return nil // already seeded by the other suite
	}

	for _, l := range libraries {
		if _, err := invoke("library_scan", map[string]any{"library": l.Name}); err != nil {
			return err
		}
	}
	for _, l := range libraries {
		if err := waitForItems(l.Name, l.Items); err != nil {
			return err
		}
	}

	for _, b := range books {
		args := map[string]any{
			"item": b.Title, "authors": []any{b.Author}, "narrators": []any{b.Narrator},
			"publisher": b.Publisher, "year": b.Year, "language": b.Language,
			"tags": toAny(b.Tags), "genres": toAny(b.Genres),
		}
		if len(b.Series) > 0 {
			args["series"] = toAny(b.Series)
		}
		if _, err := invoke("item_edit", args); err != nil {
			return fmt.Errorf("seeding %s: %w", b.Title, err)
		}
	}

	return nil
}

// toAny widens a string slice for an MCP argument map.
func toAny(s []string) []any {
	out := make([]any, 0, len(s))
	for _, v := range s {
		out = append(out, v)
	}
	return out
}

// waitForItems polls library_items until a scan has settled on want.
func waitForItems(library string, want int) error {
	var last string
	for range 60 {
		out, err := invoke("library_items", map[string]any{"library": library, "limit": 1})
		switch {
		case err != nil:
			last = err.Error()
		default:
			if total, ok := out["total"].(float64); ok {
				if int(total) == want {
					return nil
				}
				last = fmt.Sprintf("at %d items", int(total))
			}
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("library %s never reached %d items (%s)", library, want, last)
}

// invoke calls a tool and returns its structured result.
func invoke(name string, args map[string]any) (map[string]any, error) {
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
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

// call invokes a tool, skipping the test when the container is not configured
// and failing it when the tool errors.
func call(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()

	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set; run: eval \"$(scripts/abs-testenv.sh up)\"")
	}
	out, err := invoke(name, args)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// callErr invokes a tool expecting it to fail, and returns the error message.
func callErr(t *testing.T, name string, args map[string]any) string {
	t.Helper()

	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}
	out, err := invoke(name, args)
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded: %v", name, out)
	}

	return err.Error()
}

// toolNames lists every tool the server registered, so a test can assert that
// a family is complete rather than only that the tools it knows about work.
func toolNames(t *testing.T) []string {
	t.Helper()

	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}

	return out
}

// strs pulls a []string out of a decoded JSON field.
func strs(t *testing.T, v any, field string) []string {
	t.Helper()

	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is %T, want a list", field, v)
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("%s contains %T, want strings", field, e)
		}
		out = append(out, s)
	}

	return out
}

// rows pulls a list of objects out of a decoded JSON field.
func rows(t *testing.T, v any, field string) []map[string]any {
	t.Helper()

	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is %T, want a list", field, v)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		row, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("%s contains %T, want objects", field, e)
		}
		out = append(out, row)
	}

	return out
}

// num pulls a JSON number out of a decoded field.
func num(t *testing.T, v any, field string) int {
	t.Helper()

	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s is %T, want a number", field, v)
	}

	return int(f)
}
