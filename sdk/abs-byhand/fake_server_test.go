// The client against a canned Audiobookshelf. The live suite proves the shapes
// match a real server; the tests on this one prove the client keeps building
// each request, and reading each answer, the same way without needing one.
//
// Every test server is made in this file: newJSONServer for one that answers
// a status and a body, newRawServer for one that does what a body cannot say,
// such as stream, stop part way, read an upload or redirect. Both record what
// they were sent.
package abs

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/go-kt/chttp"
)

// fakeServer is a canned Audiobookshelf that records the requests made to it.
type fakeServer struct {
	*httptest.Server

	method, path, query string      // the last request's
	header              http.Header // its headers
	body                []byte      // and its body

	mu   sync.Mutex
	seen []string // every request in order, as "METHOD /path"
}

// newJSONServer is a server that answers every request with the status and
// body the handler gives.
func newJSONServer(t *testing.T, handle func(r *http.Request) (int, string)) *fakeServer {
	t.Helper()

	return newRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		status, resp := handle(r)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	})
}

// newRawServer is a server whose handler writes the answer itself. The
// request's body is read before the handler runs, and left for it to read
// again.
func newRawServer(t *testing.T, handle http.HandlerFunc) *fakeServer {
	t.Helper()

	s := &fakeServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.method, s.path, s.query = r.Method, r.URL.Path, r.URL.RawQuery
		s.header, s.body = r.Header.Clone(), body
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		handle(w, r)
	}))
	t.Cleanup(s.Close)

	return s
}

// always answers every request the same.
func always(status int, body string) func(*http.Request) (int, string) {
	return func(*http.Request) (int, string) { return status, body }
}

// cutShort answers with a status and the start of a body, then drops the
// connection before the length it promised has been sent.
func cutShort(status int, start string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "200")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, start)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}
}

// requests is every request made so far, as "METHOD /path".
func (s *fakeServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.seen...)
}

// values is the last request's query.
func (s *fakeServer) values(t *testing.T) url.Values {
	t.Helper()

	q, err := url.ParseQuery(s.query)
	if err != nil {
		t.Fatalf("the query %q: %v", s.query, err)
	}

	return q
}

// sent is the last request's body, read as a JSON object.
func (s *fakeServer) sent(t *testing.T) map[string]any {
	t.Helper()

	var body map[string]any
	if err := json.Unmarshal(s.body, &body); err != nil {
		t.Fatalf("the body %q: %v", s.body, err)
	}

	return body
}

func newClient(t *testing.T, s *fakeServer, opts ...Option) *Client {
	t.Helper()

	c, err := New(s.URL, "k", append([]Option{atOnce()}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

// atOnce has a client try again as it does by default, with no wait between
// tries: what a test is about is whether a request is sent again, not how
// long after.
func atOnce() Option {
	return WithRetry(chttp.Retry{Wait: func(int) time.Duration { return 0 }})
}
