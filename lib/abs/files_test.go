package abs

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A download runs as long as the file takes: the two-minute limit on an API
// call covers reading the body too, and would cut an audiobook off part way.
// Here the API limit is shrunk to show the download does not live under it.
func TestADownloadOutlastsTheCallLimit(t *testing.T) {
	t.Parallel()

	c := newClient(t, newRawServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first half "))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("second half"))
	}))
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
	c := newClient(t, newRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
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
	s := newRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/items/li_1/file/42" {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "a.mp3", time.Time{}, bytes.NewReader(file))
	})
	c := newClient(t, s)
	resp, err := c.ItemFileRange(t.Context(), "li_1", "42", "bytes=10-")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	gotAuth := s.header.Get("Authorization")
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

// A refusal whose reply breaks off says so beside what arrived, rather than
// passing off the part as the server's whole answer.
func TestARefusalCutShortSaysSo(t *testing.T) {
	t.Parallel()

	c := newClient(t, newRawServer(t, cutShort(http.StatusForbidden, "Forbidden: this")))

	resp, err := c.ItemFileRange(t.Context(), "li_1", "9", "")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "Forbidden: this") || !strings.Contains(err.Error(), "reading the rest of the reply failed") {
		t.Errorf("a 403 cut short = %v, want the status, what arrived, and that the rest could not be read", err)
	}
}

// The server wraps the author in an author key on this route, like it does for
// the image upload and the match; the record used to come back empty.
func TestDeleteAuthorImageDecodesTheAuthor(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"author":{"id":"a1","name":"Frank Herbert","imagePath":null}}`
	})
	c := newClient(t, s)

	a, err := c.DeleteAuthorImage(t.Context(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodDelete || s.path != "/api/authors/a1/image" {
		t.Errorf("sent %s %s", s.method, s.path)
	}
	if a.ID != "a1" || a.Name != "Frank Herbert" || a.ImagePath != "" {
		t.Errorf("author = %+v", a)
	}
}

// A streamed endpoint reports a refusal as an HTTPError like everything else,
// rather than handing back a body that is an error message.
func TestStreamErrorsCarryTheStatus(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(*http.Request) (int, string) {
		return http.StatusForbidden, `{"error":"no download permission"}`
	})
	c := newClient(t, s)

	_, err := c.DownloadItem(t.Context(), "i1")
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusForbidden {
		t.Fatalf("DownloadItem error = %v, want HTTP 403", err)
	}
	if !strings.Contains(he.Body, "no download permission") {
		t.Errorf("body not carried: %q", he.Body)
	}

	s2 := newJSONServer(t, func(*http.Request) (int, string) { return http.StatusOK, "bytes" })
	body, err := newClient(t, s2).DownloadItem(t.Context(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	if got, _ := io.ReadAll(body); string(got) != "bytes" {
		t.Errorf("streamed %q", got)
	}
}

// The upload is a multipart form with the fields first and the file as its own
// part named the way the server reads it.
func TestUploadIsMultipart(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, ""))
	c := newClient(t, s)

	if err := c.Upload(t.Context(), "lib1", "f1", "Dune", "Frank Herbert", "", "01.mp3", strings.NewReader("audio")); err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodPost || s.path != "/api/upload" {
		t.Errorf("sent %s %s", s.method, s.path)
	}
	contentType := s.header.Get("Content-Type")
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("content type %q: %v", contentType, err)
	}
	form, err := multipart.NewReader(bytes.NewReader(s.body), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"library": "lib1", "folder": "f1", "title": "Dune", "author": "Frank Herbert"} {
		if got := form.Value[k]; len(got) != 1 || got[0] != want {
			t.Errorf("field %s = %v, want %s", k, got, want)
		}
	}
	files := form.File["0"]
	if len(files) != 1 || files[0].Filename != "01.mp3" {
		t.Fatalf("file part = %+v, want one file named 01.mp3 under key 0", files)
	}
	f, err := files[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if got, _ := io.ReadAll(f); string(got) != "audio" {
		t.Errorf("file content %q", got)
	}
}
