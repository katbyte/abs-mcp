//go:build acceptance

// Live tests for the client generated into sdk/abs, against a real Audiobookshelf in Docker.
//
// What the server answers to a read is the read sweep's (sweep_test.go): it calls every GET of the API document and fails one that does not
// decode. What is here is what a sweep cannot do: the calls that change something, in the order a caller makes them, read back to see they
// took; and the few reads that once came back wrong in a way that decoding alone does not catch. It makes no assertions about projection,
// name resolution or worklists, which are the tool layer's job and are covered in ../tools.
//
//	make test-sdk
//
// It runs against its own container, on its own port, so it cannot interfere with the tool suite's fixtures.
package abs_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg" // the covers the suite sets are JPEGs
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/katbyte/abs-mcp/sdk/pandorest/answers"
	"github.com/katbyte/go-kt/test/env"
	"github.com/katbyte/go-kt/test/replayproxy"
	pclient "github.com/katbyte/pandorest/client"
	"github.com/katbyte/pandorest/definitions"
	"github.com/katbyte/pandorest/sweep"
)

var (
	client   *abs.Client
	calls    = &pclient.Calls{} // every call the suite's clients make, for the operations it never reached
	recorder *answers.Recorder  // saves what the server answers when ABS_TEST_ANSWERS is set, for the API document; nil otherwise
	fiction  abs.LibraryID      // the book library these tests create and scan
	podcasts abs.LibraryID      // the podcast library, for the episode endpoints
)

var (
	proxy       *replayproxy.Proxy
	proxyMisses []string
	proxyDrifts []replayproxy.Drift
)

// suiteEnv is the environment .scripts/abs-testenv.sh exports for a live
// suite, read the way go-kt's test plumbing reads it for every tool.
var suiteEnv = env.New("ABS")

// startProxy brings up the record/replay proxy the container's HTTP_PROXY
// already points at. Audiobookshelf makes the provider calls, not us, so this
// is the only layer that can intercept them. The proxy is go-kt's: replaying
// unless ABS_TEST_RECORD or ABS_TEST_VERIFY says otherwise.
func startProxy() error {
	port, err := suiteEnv.ProxyPort()
	if err != nil {
		return err
	}

	p, err := replayproxy.New(replayproxy.Options{
		Mode:        suiteEnv.Mode(),
		CassetteDir: filepath.Join("testdata", "cassettes"),
		// all interfaces: the container reaches this through host.docker.internal
		Addr:       "0.0.0.0:" + strconv.Itoa(port),
		RecordHint: "record it with " + suiteEnv.Var("TEST_RECORD") + "=1, which records only what is missing",
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

func TestMain(m *testing.M) {
	server, token := os.Getenv("ABS_SERVER"), os.Getenv("ABS_TOKEN")
	if server == "" || token == "" {
		os.Exit(m.Run()) // every test skips
	}

	var err error
	if os.Getenv(answers.Env) != "" {
		if recorder, err = answers.Open(context.Background(), server, "../.."); err != nil {
			fmt.Fprintln(os.Stderr, "answers:", err)
			os.Exit(1)
		}
	}
	if client, err = connect(token); err != nil {
		panic(err)
	}
	if err := startProxy(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		os.Exit(1)
	}

	code := m.Run()
	stopProxy()

	if code == 0 {
		if err := everyOperationCalled(); err != nil {
			fmt.Fprintln(os.Stderr, "\n"+err.Error())
			code = 1
		}
	}

	if misses := proxyMisses; len(misses) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d request(s) had no recording:\n", len(misses))
		for _, m := range misses {
			fmt.Fprintln(os.Stderr, "  "+m)
		}
		fmt.Fprintln(os.Stderr, "record them with ABS_TEST_RECORD=1 make testacc-sdk, which records only what is missing")
		if code == 0 {
			code = 1
		}
	}
	if drifts := proxyDrifts; len(drifts) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d response(s) changed shape since recording:\n", len(drifts))
		for _, d := range drifts {
			fmt.Fprintln(os.Stderr, "  "+d.String())
		}
		fmt.Fprintln(os.Stderr, "\nreview the changes, then run `make record-sdk` to accept them")
		if code == 0 {
			code = 1
		}
	}
	if err := recorder.Report(os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "\nanswers:", err)
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

// connect is a client for the suite's server with an account's key, which records what it reads when the suite records, and counts every
// call it makes.
func connect(key string, opts ...pclient.Option) (*abs.Client, error) {
	return abs.New(os.Getenv("ABS_SERVER"), key, append(opts, pclient.WithTransport(recorder.Transport()), pclient.WithCalls(calls))...)
}

// everyOperationCalled fails a whole run that left an operation of the API document uncalled: what nothing calls against a real server is
// unproven, and a route the document gains is noticed here. A run of some tests only is not held to it.
func everyOperationCalled() error {
	if f := flag.Lookup("test.run"); f == nil || f.Value.String() != "" {
		return nil
	}
	if f := flag.Lookup("test.skip"); f == nil || f.Value.String() != "" {
		return nil
	}

	defs, err := definitions.Load(filepath.Join("..", "..", "api-defs", "audiobookshelf-"+abs.DocumentVersion))
	if err != nil {
		return err
	}
	if problems := sweep.NeverCalled(defs, calls.Seen(), nil); len(problems) > 0 {
		return fmt.Errorf("operations of the API document the suite never called:\n  %s", strings.Join(problems, "\n  "))
	}

	return nil
}

func skipUnlessLive(t *testing.T) context.Context {
	t.Helper()

	if client == nil {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set; run: eval \"$(.scripts/abs-testenv.sh up)\"")
	}

	return t.Context()
}

// must is what a call that must not fail answered. Go only allows a multi-value call as a function's sole argument, so this cannot also take
// *testing.T - it panics instead, which the test framework reports as a failure. That is the right severity here: if the server will not
// answer, nothing downstream is meaningful.
func must[T any, R interface{ Record() (T, error) }](res R, err error) T {
	v, err := record(res, err)
	if err != nil {
		panic(err)
	}

	return v
}

// record is what a call answered, or the call's error.
func record[T any, R interface{ Record() (T, error) }](res R, err error) (T, error) {
	if err != nil {
		var none T
		return none, err
	}

	return res.Record()
}

// afterwards is the context for a clean-up step, which runs once the test's own context is cancelled.
func afterwards(t *testing.T) context.Context {
	t.Helper()

	return context.WithoutCancel(t.Context())
}

// errorOf is a call's error, its answer dropped, for a step that only needs to know it went through.
func errorOf[R any](_ R, err error) error { return err }

// firstItems is up to n of a library's items, in the server's own order.
func firstItems(ctx context.Context, lib abs.LibraryID, n int) []abs.LibraryItemMinified {
	return must(client.LibraryGetLibraryItems(ctx, lib, abs.LibraryGetLibraryItemsOptions{Limit: &n})).Results
}

// firstAuthors is up to n of a library's authors. The server pages them only when given a page as well as a limit, and answers a page as
// results where it answers all of them as authors.
func firstAuthors(ctx context.Context, lib abs.LibraryID, n int) []abs.Author {
	return must(client.LibraryGetAuthors(ctx, lib, abs.LibraryGetAuthorsOptions{Limit: &n, Page: new(0)})).Results
}

// form is a file sent as one field of a form, with the form's other fields beside it, and the content type that names the form's boundary.
func form(field, filename string, content []byte, fields map[string]string) (body io.Reader, contentType string, err error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", err
		}
	}
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(content); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}

	return &buf, w.FormDataContentType(), nil
}

// coverSize is the size of an item's cover, read from the file itself rather than the copy the server cuts to size.
func coverSize(ctx context.Context, id abs.LibraryItemID) (width, height int, err error) {
	got, err := client.LibraryItemGetCover(ctx, id, abs.LibraryItemGetCoverOptions{Raw: "1"})
	if err != nil {
		return 0, 0, err
	}
	body, _ := got.Reader()
	defer func() { _ = body.Close() }()

	cfg, _, err := image.DecodeConfig(body)

	return cfg.Width, cfg.Height, err
}

// whole is one item with everything the server keeps on it, its files and chapters among them.
func whole(ctx context.Context, id abs.LibraryItemID) *abs.LibraryItemExpanded {
	it := must(client.LibraryItemFindOne(ctx, id, abs.LibraryItemFindOneOptions{Expanded: "1"}))
	return &it
}
