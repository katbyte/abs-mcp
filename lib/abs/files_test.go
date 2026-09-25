package abs

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A download runs as long as the file takes: the two-minute limit on an API
// call covers reading the body too, and would cut an audiobook off part way.
// Here the API limit is shrunk to show the download does not live under it.
func TestADownloadOutlastsTheCallLimit(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first half "))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("second half"))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	c.http.Timeout = 100 * time.Millisecond

	body, err := c.DownloadItem(t.Context(), "li_1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "first half second half" {
		t.Errorf("download = %q, %v; want the whole file", got, err)
	}
}

// An upload is written as it is sent, and arrives whole: the fields, then the
// file under its name.
func TestAnUploadArrivesWhole(t *testing.T) {
	t.Parallel()

	content := bytes.Repeat([]byte("audio "), 200_000)
	var gotFields map[string]string
	var gotFile []byte
	var gotName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		if err := r.ParseMultipartForm(1 << 20); err != nil { //nolint:gosec // a test server reading one bounded upload
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotFields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			gotFields[k] = v[0]
		}
		f, h, err := r.FormFile("0")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer func() { _ = f.Close() }()
		gotName = h.Filename
		gotFile, _ = io.ReadAll(f)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Upload(t.Context(), "lib1", "fol1", "Dune", "Frank Herbert", "", "dune.m4b", bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if gotName != "dune.m4b" || !bytes.Equal(gotFile, content) {
		t.Errorf("file %q of %d bytes, want dune.m4b of %d", gotName, len(gotFile), len(content))
	}
	if gotFields["library"] != "lib1" || gotFields["title"] != "Dune" || !strings.HasPrefix(gotFields["author"], "Frank") {
		t.Errorf("fields = %v", gotFields)
	}
}

// A ranged read passes the Range through with the key, and hands back the
// 206 whole, Content-Range included, which is what a seeking reader needs; a
// refusal is an HTTPError like any other.
func TestARangedReadPassesTheRangeThrough(t *testing.T) {
	t.Parallel()

	file := []byte("0123456789abcdef")
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/items/li_1/file/42" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		http.ServeContent(w, r, "a.mp3", time.Time{}, bytes.NewReader(file))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.ItemFileRange(t.Context(), "li_1", "42", "bytes=10-")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(got) != "abcdef" || resp.Header.Get("Content-Range") != "bytes 10-15/16" || gotAuth != "Bearer k" {
		t.Errorf("ranged read = %d %q %q with %q; want 206 \"abcdef\" \"bytes 10-15/16\" with the key", resp.StatusCode, got, resp.Header.Get("Content-Range"), gotAuth)
	}

	missing, err := c.ItemFileRange(t.Context(), "li_1", "43", "")
	if err == nil {
		_ = missing.Body.Close()
	}
	if !IsNotFound(err) {
		t.Errorf("a file the server does not have = %v, want a 404", err)
	}
}
