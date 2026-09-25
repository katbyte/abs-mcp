package abs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A server url that is redirected - http moved to https by a proxy, or a
// login wall - fails the call rather than following it: Go would turn the
// DELETE into a GET, the GET would answer 200, and the delete would report
// success having done nothing. A page of HTML answered in the API's place is
// refused the same way.
func TestRedirectsAndWebPagesAreRefused(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
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
	}))
	defer srv.Close()

	moved, err := New(srv.URL+"/moved", "k")
	if err != nil {
		t.Fatal(err)
	}
	err = moved.DeleteItem(t.Context(), "li_1", false)
	if err == nil || !strings.Contains(err.Error(), "redirected") {
		t.Errorf("a redirected delete = %v, want it refused", err)
	}
	mu.Lock()
	for _, s := range seen {
		if strings.HasPrefix(s, "GET ") {
			t.Errorf("the redirect was followed: %v", seen)
		}
	}
	mu.Unlock()

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

// Nothing at all where a record is expected is something in front of the
// server answering, not the server: decoding it would hand back a blank item
// that reads as a real one with every field empty. A write that expects no
// record still takes an empty answer.
func TestAnEmptyAnswerIsNotABlankRecord(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
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

	var got []byte
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		contentType = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte("OK"))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CloseSession(t.Context(), "ps_1", nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || contentType != "" {
		t.Errorf("closing with no final position sent %q as %q, want no body", got, contentType)
	}
}
