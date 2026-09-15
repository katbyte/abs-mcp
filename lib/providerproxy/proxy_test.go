package providerproxy

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clientThrough returns an http.Client that reaches the given proxy and
// accepts its minted certificates, the way the container does with
// NODE_TLS_REJECT_UNAUTHORIZED=0.
func clientThrough(t *testing.T, p *Proxy) *http.Client {
	t.Helper()

	proxyURL, err := url.Parse("http://" + p.Addr())
	if err != nil {
		t.Fatal(err)
	}

	return &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the proxy mints its own certs on purpose
		},
	}
}

func writeCassette(t *testing.T, dir string, c cassette) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hostFile(c.Host)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A recorded request must come back through the CONNECT tunnel byte for byte.
func TestReplayServesRecording(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCassette(t, dir, cassette{
		Host: "api.audnex.us",
		Interactions: []*interaction{{
			Key:     "GET api.audnex.us/authors?name=Isaac+Asimov",
			Method:  "GET",
			Host:    "api.audnex.us",
			Path:    "/authors",
			Query:   "name=Isaac+Asimov",
			Status:  200,
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    `[{"asin":"B000AP9A6K","name":"Isaac Asimov"}]`,
		}},
	})

	p, err := New(Options{CassetteDir: dir, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.audnex.us/authors?name=Isaac+Asimov", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientThrough(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.Contains(string(body), "Isaac Asimov") {
		t.Errorf("body = %q", body)
	}
	if misses := p.Misses(); len(misses) != 0 {
		t.Errorf("misses = %v, want none", misses)
	}
}

// A request with no recording must fail loudly rather than look like an empty
// but successful response, which would let a test pass for the wrong reason.
func TestReplayMissIsLoud(t *testing.T) {
	t.Parallel()

	p, err := New(Options{CassetteDir: t.TempDir(), Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.audible.com/1.0/catalog/products?keywords=nothing", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientThrough(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	misses := p.Misses()
	if len(misses) != 1 {
		t.Fatalf("misses = %v, want one", misses)
	}
	if !strings.Contains(misses[0], "api.audible.com/1.0/catalog/products") {
		t.Errorf("miss does not name the request: %q", misses[0])
	}
}

// A host a test serves itself answers from its handler, over plain http and
// through a tunnel alike, and is never a miss; once stopped it is a host like
// any other.
func TestServeAnswersALocalHost(t *testing.T) {
	t.Parallel()

	p, err := New(Options{CassetteDir: t.TempDir(), Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	stop := p.Serve("Feed.Test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/show.xml" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "served /show.xml")
	}))
	get := func(u string) (int, string) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := clientThrough(t, p).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(body)
	}

	for _, u := range []string{"http://feed.test/show.xml", "http://feed.test:8080/show.xml", "https://feed.test/show.xml"} {
		if status, body := get(u); status != http.StatusOK || body != "served /show.xml" {
			t.Errorf("%s = %d %q", u, status, body)
		}
	}
	if misses := p.Misses(); len(misses) != 0 {
		t.Errorf("misses = %v, want none", misses)
	}

	stop()
	if status, _ := get("http://feed.test/show.xml"); status != http.StatusBadGateway {
		t.Errorf("after stop: status = %d, want 502", status)
	}
}

// Query parameter order must not matter, or a cassette would miss on a request
// that is the same in every way that affects the response.
func TestKeyIsOrderIndependent(t *testing.T) {
	t.Parallel()

	a := key("get", "API.Audible.com", "/1.0/catalog", url.Values{"b": {"2"}, "a": {"1"}})
	b := key("GET", "api.audible.com", "/1.0/catalog", url.Values{"a": {"1"}, "b": {"2"}})
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
}

// A body over the cap is elided rather than committed, and a binary one is
// stored base64 so the cassette stays valid JSON.
func TestBodyStorage(t *testing.T) {
	t.Parallel()

	var big interaction
	big.setBody(make([]byte, maxBodyBytes+1), "text/xml")
	if !big.Elided || big.ElidedSize != maxBodyBytes+1 {
		t.Errorf("oversized body not elided: %+v", big)
	}

	// audio is elided however small, so no episode audio reaches the repository
	var audio interaction
	audio.setBody([]byte("ID3short"), "audio/mpeg")
	if !audio.Elided {
		t.Errorf("audio body not elided: %+v", audio)
	}

	// an elided image still has to replay as something decodable, or the
	// server will not accept it as a cover
	var jpeg interaction
	jpeg.setBody(make([]byte, 500<<10), "image/jpeg")
	if !jpeg.Elided {
		t.Fatalf("image not elided: %+v", jpeg)
	}
	if got := jpeg.bytes(); len(got) < 4 || got[0] != 0xff || got[1] != 0xd8 {
		t.Errorf("elided image did not replay as a JPEG: %v", got[:min(4, len(got))])
	}

	var png interaction
	png.setBody(make([]byte, 10), "image/png")
	if got := png.bytes(); len(got) < 4 || got[1] != 'P' {
		t.Errorf("elided png did not replay as a PNG: %v", got[:min(4, len(got))])
	}

	// but a large RSS feed must survive intact or it will not parse on replay
	feed := make([]byte, 900<<10)
	for n := range feed {
		feed[n] = 'x'
	}
	var rss interaction
	rss.setBody(feed, "application/rss+xml")
	if rss.Elided || len(rss.Body) != len(feed) {
		t.Errorf("large feed was not kept intact: elided=%v len=%d", rss.Elided, len(rss.Body))
	}

	var binary interaction
	binary.setBody([]byte{0xff, 0xfe, 0x00}, "application/octet-stream")
	if binary.BodyBase64 == "" || binary.Body != "" {
		t.Errorf("binary body not base64: %+v", binary)
	}

	var text interaction
	text.setBody([]byte(`{"ok":true}`), "application/json")
	if text.Body != `{"ok":true}` || text.BodyBase64 != "" {
		t.Errorf("text body not stored as text: %+v", text)
	}
}

// Record mode against a real provider. Off by default so `go test ./...` stays
// hermetic; this is the check that the recording path still works when a
// cassette needs refreshing.
//
//	ABS_TEST_PROVIDERS_LIVE=1 go test ./lib/providerproxy/ -run Record -v
func TestRecordAgainstRealProvider(t *testing.T) {
	t.Parallel()

	if os.Getenv("ABS_TEST_PROVIDERS_LIVE") == "" {
		t.Skip("set ABS_TEST_PROVIDERS_LIVE=1 to record against the real providers")
	}

	dir := t.TempDir()
	p, err := New(Options{Mode: Record, CassetteDir: dir, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.audnex.us/authors?name=Isaac%20Asimov", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientThrough(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	// and the cassette it wrote must replay without touching the network
	raw, err := os.ReadFile(filepath.Join(dir, "api.audnex.us.json")) //nolint:gosec // a path this test just wrote
	if err != nil {
		t.Fatalf("no cassette written: %v", err)
	}
	if !strings.Contains(string(raw), "Asimov") {
		t.Errorf("cassette does not contain the response: %s", raw)
	}

	replay, err := New(Options{CassetteDir: dir, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()

	req2, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.audnex.us/authors?name=Isaac%20Asimov", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := clientThrough(t, replay).Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if !bytes.Equal(body, body2) {
		t.Error("replayed body differs from the recorded one")
	}
	if misses := replay.Misses(); len(misses) != 0 {
		t.Errorf("replay missed: %v", misses)
	}
}
