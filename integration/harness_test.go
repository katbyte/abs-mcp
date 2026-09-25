//go:build integration

// Live tests for the client against a real Audiobookshelf in Docker.
//
// This layer tests one thing: that every response the server sends decodes
// into the types here with the fields actually populated. It makes no
// assertions about projection, name resolution or worklists - those are the
// tool layer's job and are covered in ../acceptance.
//
// The split matters because lib/abs is typed against an API with no OpenAPI
// spec and no versioning (the published docs say outright that they are
// unmaintained), so this is the layer that notices when the server changes
// shape underneath us. Every bug found in the first pass of these suites was
// here, not in the tools.
//
//	make test-sdk
//
// It runs against its own container, on its own port, so it cannot interfere
// with the tool suite's fixtures.
package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/katbyte/abs-mcp/lib/providerproxy"
)

var (
	client   *abs.Client
	fiction  string // the book library these tests create and scan
	podcasts string // the podcast library, for the episode endpoints
)

func recording() bool { return os.Getenv("ABS_TEST_RECORD") != "" }
func verifying() bool { return os.Getenv("ABS_TEST_VERIFY") != "" }

var (
	proxy       *providerproxy.Proxy
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

func TestMain(m *testing.M) {
	server, token := os.Getenv("ABS_SERVER"), os.Getenv("ABS_TOKEN")
	if server == "" || token == "" {
		os.Exit(m.Run()) // every test skips
	}

	var err error
	if client, err = abs.New(server, token); err != nil {
		panic(err)
	}
	if err := startProxy(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		os.Exit(1)
	}

	code := m.Run()
	stopProxy()

	if misses := proxyMisses; len(misses) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d request(s) had no recording:\n", len(misses))
		for _, m := range misses {
			fmt.Fprintln(os.Stderr, "  "+m)
		}
		fmt.Fprintln(os.Stderr, "record them with ABS_TEST_RECORD=1 make testacc-integration, which records only what is missing")
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

	os.Exit(code)
}

func skipUnlessLive(t *testing.T) context.Context {
	t.Helper()

	if client == nil {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set; run: eval \"$(scripts/abs-testenv.sh up)\"")
	}

	return t.Context()
}

// must unwraps a call that must not fail. Go only allows a multi-value call as
// a function's sole argument, so this cannot also take *testing.T - it panics
// instead, which the test framework reports as a failure. That is the right
// severity here: if the server will not answer, nothing downstream is
// meaningful.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}
