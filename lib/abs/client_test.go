package abs

import (
	"bytes"
	"encoding/base64"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNewRejectsBadURLs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ url, token string }{
		{"", "k"},
		{"http://nas:13378", ""},
		{"nas:13378", "k"},
		{"http://user:pw@nas:13378", "k"},
	} {
		if _, err := New(tc.url, tc.token); err == nil {
			t.Errorf("New(%q, %q) accepted", tc.url, tc.token)
		}
	}

	c, err := New("http://nas:13378/", "k")
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL() != "http://nas:13378" {
		t.Errorf("trailing slash kept: %q", c.BaseURL())
	}
}

func TestEncodeFilter(t *testing.T) {
	t.Parallel()

	if got := EncodeFilter("issues", ""); got != "issues" {
		t.Errorf("bare group: %q", got)
	}
	got := EncodeFilter("genres", "Science Fiction")
	want := "genres." + base64.StdEncoding.EncodeToString([]byte("Science Fiction"))
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestHTTPErrorMessage(t *testing.T) {
	t.Parallel()

	c := newClient(t, newJSONServer(t, always(http.StatusForbidden, "nope")))
	err := c.ScanLibrary(t.Context(), "lib", false)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"HTTP 403", "nope", "permission"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// intQuery omits a zero rather than sending it, because the server treats an
// explicit 0 as a real limit.
func TestIntQueryOmitsZero(t *testing.T) {
	t.Parallel()

	q := map[string][]string{}
	intQuery(q, "limit", 0)
	if _, present := q["limit"]; present {
		t.Error("intQuery sent a zero")
	}
	intQuery(q, "limit", 5)
	if q["limit"][0] != "5" {
		t.Errorf("intQuery = %v", q["limit"])
	}
}

func TestTruncateAndBoolQuery(t *testing.T) {
	t.Parallel()

	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate under the limit = %q", got)
	}
	if got := truncate("exactly-10", 10); got != "exactly-10" {
		t.Errorf("truncate at the limit = %q", got)
	}
	if got := truncate("far too long to keep", 5); got != "far t..." {
		t.Errorf("truncate over the limit = %q", got)
	}
	if boolQuery(true) != "1" || boolQuery(false) != "0" {
		t.Errorf("boolQuery = %q/%q, want 1/0", boolQuery(true), boolQuery(false))
	}
}

// A refusal keeps its status when its body cannot be read: a 404 cut short
// is still no progress, not a failed request.
func TestANotFoundCutShortIsStillNotFound(t *testing.T) {
	t.Parallel()

	c := newClient(t, newRawServer(t, cutShort(http.StatusNotFound, "Not")))

	if p, err := c.Progress(t.Context(), "li_1", ""); err != nil || p != nil {
		t.Errorf("a 404 cut short = %v, %v; want no progress and no error", p, err)
	}
}

// A reply over the limit is refused as too big, not cut to the limit and
// handed on as a reply that will not decode.
func TestAReplyOverTheLimitIsRefused(t *testing.T) {
	t.Parallel()

	c := newClient(t, newRawServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[`))
		chunk := bytes.Repeat([]byte(" "), 1<<20)
		for range maxResponseBytes >> 20 {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write([]byte(`],"total":0}`))
	}))

	if _, err := c.Libraries(t.Context()); err == nil || !strings.Contains(err.Error(), "over 64 MiB") {
		t.Errorf("a reply over the limit = %v, want it refused as too big", err)
	}
}

// Nothing at all where a record is expected is something in front of the
// server answering, not the server: decoding it would hand back a blank item
// that reads as a real one with every field empty. A write that expects no
// record still takes an empty answer.
func TestAnEmptyAnswerIsNotABlankRecord(t *testing.T) {
	t.Parallel()

	c := newClient(t, newJSONServer(t, always(http.StatusOK, "")))
	if it, err := c.Item(t.Context(), "li_1"); err == nil || !strings.Contains(err.Error(), "answered with nothing") {
		t.Errorf("an empty answer to a read = %+v, %v; want it refused", it, err)
	}
	if err := c.DeleteItem(t.Context(), "li_1", false); err != nil {
		t.Errorf("an empty answer to a delete, which expects nothing: %v", err)
	}
}

// A session closed with no final position sends no body. A nil map
// marshalled is the JSON null, which the server's parser refuses, so the
// session stayed open: a later run of the live suite found it still playing.
func TestANilBodyIsNoBody(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, "OK"))
	c := newClient(t, s)
	if err := c.CloseSession(t.Context(), "ps_1", nil); err != nil {
		t.Fatal(err)
	}
	if contentType := s.header.Get("Content-Type"); len(s.body) != 0 || contentType != "" {
		t.Errorf("closing with no final position sent %q as %q, want no body", s.body, contentType)
	}
}

// A server url that is redirected - http moved to https by a proxy, or a
// login wall - fails the call rather than following it: Go would turn the
// DELETE into a GET, the GET would answer 200, and the delete would report
// success having done nothing. A page of HTML answered in the API's place is
// refused the same way.
func TestRedirectsAndWebPagesAreRefused(t *testing.T) {
	t.Parallel()

	srv := newRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/moved/"):
			http.Redirect(w, r, "/"+strings.TrimPrefix(r.URL.Path, "/moved/"), http.StatusMovedPermanently) //nolint:gosec // a test server moving its own paths

		case r.URL.Path == "/login/api/items/li_1":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<!DOCTYPE html>\n<html><body>Sign in</body></html>"))
		case r.URL.Path == "/api/backups/path":
			// the server's own bare reply, which Express labels text/html
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("OK"))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	})

	moved, err := New(srv.URL+"/moved", "k")
	if err != nil {
		t.Fatal(err)
	}
	err = moved.DeleteItem(t.Context(), "li_1", false)
	if err == nil || !strings.Contains(err.Error(), "redirected") {
		t.Errorf("a redirected delete = %v, want it refused", err)
	}
	for _, s := range srv.requests() {
		if strings.HasPrefix(s, "GET ") {
			t.Errorf("the redirect was followed: %v", srv.requests())
		}
	}

	login, err := New(srv.URL+"/login", "k")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := login.Item(t.Context(), "li_1"); err == nil || !strings.Contains(err.Error(), "web page") {
		t.Errorf("a web page in the API's place = %v, want it refused", err)
	}

	direct, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	if err := direct.SetBackupPath(t.Context(), "/backups"); err != nil {
		t.Errorf("the server's own OK, labelled text/html, was refused: %v", err)
	}
	if err := direct.DeleteItem(t.Context(), "li_1", false); err != nil {
		t.Errorf("a delete answered where it was asked failed: %v", err)
	}
}

// A rejected key says so, which is the first thing a misconfigured
// deployment hits.
func TestHTTPErrorNamesTheKeyOn401(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(*http.Request) (int, string) { return http.StatusUnauthorized, "Unauthorized" })
	c := newClient(t, s)

	_, err := c.Me(t.Context())
	if err == nil || !strings.Contains(err.Error(), "ABS_TOKEN") {
		t.Errorf("401 error = %v", err)
	}
	if IsNotFound(err) {
		t.Error("a 401 is not a 404")
	}
}

// Every method of the client is called by the live suite, which runs it
// against a real Audiobookshelf: the server publishes no schema, so that is
// the only proof a method's request and the shape it decodes are right. A
// method added without a live test fails here, in the unit tests, rather
// than going unnoticed until someone counts.
func TestEveryMethodHasALiveTest(t *testing.T) {
	t.Parallel()

	const suite = "../../integration"
	files, err := filepath.Glob(filepath.Join(suite, "*_test.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no live tests found in %s: %v", suite, err)
	}
	called := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					called[sel.Sel.Name] = true
				}
			}
			return true
		})
	}

	client := reflect.TypeFor[*Client]()
	if client.NumMethod() < 200 {
		t.Fatalf("the client has %d methods, which is too few to be all of them", client.NumMethod())
	}
	for method := range client.Methods() {
		if !called[method.Name] {
			t.Errorf("%s has no live test: nothing in %s calls it", method.Name, suite)
		}
	}
}
