package abs

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The request each method builds and the response shape it decodes, against a
// canned server. The live suite proves the shapes match a real Audiobookshelf;
// these prove the client keeps building them the same way without needing one.

// jsonServer answers every request from the handler and records the last one.
type jsonServer struct {
	*httptest.Server
	method, path, query string
	body                []byte
}

func newJSONServer(t *testing.T, handle func(r *http.Request) (int, string)) *jsonServer {
	t.Helper()

	s := &jsonServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.method, s.path, s.query = r.Method, r.URL.Path, r.URL.RawQuery
		s.body, _ = io.ReadAll(r.Body)
		status, resp := handle(r)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(s.Close)

	return s
}

func newClient(t *testing.T, s *jsonServer) *Client {
	t.Helper()

	c, err := New(s.URL, "k")
	if err != nil {
		t.Fatal(err)
	}

	return c
}

// A nil list leaves the field alone and an empty one clears it. With
// omitempty an empty list vanished from the payload, so clearing narrators,
// series, genres or tags through the client was a silent no-op.
func TestListFieldsClearWithEmptySlices(t *testing.T) {
	t.Parallel()

	wipe := MediaUpdate{
		Metadata: &MetadataUpdate{Narrators: []string{}, Series: []SeriesRef{}, Genres: []string{}, Authors: []NameRef{}},
		Tags:     []string{},
	}
	b, err := json.Marshal(wipe)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"narrators":[]`, `"series":[]`, `"genres":[]`, `"authors":[]`, `"tags":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("clearing payload %s lacks %s", b, want)
		}
	}

	leave := MediaUpdate{Metadata: &MetadataUpdate{Title: new("T")}}
	b, err = json.Marshal(leave)
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"narrators", "series", "genres", "authors", "tags"} {
		if strings.Contains(string(b), absent) {
			t.Errorf("an untouched list was sent: %s", b)
		}
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// The dimensions have to be the file's own. The server's default answer is a
// cached copy resized to 400 wide, so without raw=1 every cover measured 400
// pixels and the "too small" check could never fire.
func TestCoverSizeMeasuresTheOriginalFile(t *testing.T) {
	t.Parallel()

	cover := pngBytes(t, 640, 480)
	s := newJSONServer(t, func(r *http.Request) (int, string) {
		if r.URL.Query().Get("raw") != "1" {
			return http.StatusBadRequest, "not the raw file"
		}
		// a large trailer stands in for the rest of a real scan
		return http.StatusOK, string(cover) + strings.Repeat("\x00", 1<<20)
	})
	c := newClient(t, s)

	w, h, err := c.CoverSize(t.Context(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	if w != 640 || h != 480 {
		t.Errorf("CoverSize = %dx%d, want 640x480", w, h)
	}
	if s.path != "/api/items/i1/cover" {
		t.Errorf("asked %s", s.path)
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

// ItemsAll is the sweep under every audit: it has to walk every page in the
// minified shape, stop at the total, and stop early when told to.
func TestItemsAllWalksEveryPage(t *testing.T) {
	t.Parallel()

	const total = sweepPageSize*2 + 1
	var pages []string
	s := newJSONServer(t, func(r *http.Request) (int, string) {
		q := r.URL.Query()
		pages = append(pages, q.Get("page"))
		if q.Get("limit") != strconv.Itoa(sweepPageSize) || q.Get("minified") != "1" {
			return http.StatusBadRequest, "wrong page shape"
		}
		page, _ := strconv.Atoi(q.Get("page"))
		n := min(sweepPageSize, total-page*sweepPageSize)
		items := make([]string, 0, n)
		for i := range n {
			items = append(items, `{"id":"i`+strconv.Itoa(page*sweepPageSize+i)+`"}`)
		}
		return http.StatusOK, `{"results":[` + strings.Join(items, ",") + `],"total":` + strconv.Itoa(total) + `}`
	})
	c := newClient(t, s)

	seen := 0
	if err := c.ItemsAll(t.Context(), "lib", ItemsOptions{}, func(items []Item) bool {
		seen += len(items)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if seen != total {
		t.Errorf("saw %d items, want %d", seen, total)
	}
	if !slices.Equal(pages, []string{"0", "1", "2"}) {
		t.Errorf("pages asked for: %v", pages)
	}

	pages = nil
	if err := c.ItemsAll(t.Context(), "lib", ItemsOptions{}, func([]Item) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Errorf("a callback returning false still fetched %v", pages)
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

	var contentType string
	s := newJSONServer(t, func(r *http.Request) (int, string) {
		contentType = r.Header.Get("Content-Type")
		return http.StatusOK, ""
	})
	c := newClient(t, s)

	if err := c.Upload(t.Context(), "lib1", "f1", "Dune", "Frank Herbert", "", "01.mp3", strings.NewReader("audio")); err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodPost || s.path != "/api/upload" {
		t.Errorf("sent %s %s", s.method, s.path)
	}
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

// Audible reports chapters in milliseconds with a length; the client hands
// back seconds with an end, which is what SetChapters takes. A miss comes as a
// 200 with an error field, which has to surface as an error.
func TestSearchChaptersConverts(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(r *http.Request) (int, string) {
		if r.URL.Query().Get("asin") == "MISSING" {
			return http.StatusOK, `{"error":"Chapters not found"}`
		}
		return http.StatusOK, `{"chapters":[{"startOffsetMs":0,"lengthMs":1500,"title":"One"},{"startOffsetMs":1500,"lengthMs":2500,"title":"Two"}]}`
	})
	c := newClient(t, s)

	chapters, err := c.SearchChapters(t.Context(), "B0", "uk")
	if err != nil {
		t.Fatal(err)
	}
	if q, _ := parseQuery(s.query); q.Get("region") != "uk" || q.Get("asin") != "B0" {
		t.Errorf("query %q", s.query)
	}
	want := []Chapter{{ID: 0, Start: 0, End: 1.5, Title: "One"}, {ID: 1, Start: 1.5, End: 4, Title: "Two"}}
	if !slices.Equal(chapters, want) {
		t.Errorf("chapters = %+v, want %+v", chapters, want)
	}

	if _, err := c.SearchChapters(t.Context(), "MISSING", ""); err == nil || !strings.Contains(err.Error(), "Chapters not found") {
		t.Errorf("a miss gave %v", err)
	}
}

// The feed search wraps each hit in an episode key, which collides with the
// episode-number field when decoded flat.
func TestSearchFeedEpisodesUnwraps(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"episodes":[{"episode":{"title":"Part 1","episode":"12","season":2}},{"episode":{"title":"Part 2","episode":13}}]}`
	})
	c := newClient(t, s)

	eps, err := c.SearchFeedEpisodes(t.Context(), "p1", "Part")
	if err != nil {
		t.Fatal(err)
	}
	if q, _ := parseQuery(s.query); q.Get("title") != "Part" {
		t.Errorf("query %q", s.query)
	}
	if len(eps) != 2 || eps[0].Title != "Part 1" || eps[0].Episode != "12" || eps[0].Season != "2" || eps[1].Episode != "13" {
		t.Errorf("episodes = %+v", eps)
	}
}

// Older servers answer the authors listing with an authors key and no total.
func TestAuthorsAcceptsTheLegacyShape(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"authors":[{"id":"a1","name":"A"},{"id":"a2","name":"B"}]}`
	})
	c := newClient(t, s)

	authors, total, err := c.Authors(t.Context(), "lib", ListOptions{Limit: 10, Sort: "numBooks", Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 || total != 2 {
		t.Errorf("authors = %d, total = %d", len(authors), total)
	}
	if q, _ := parseQuery(s.query); q.Get("sort") != "numBooks" || q.Get("desc") != "1" || q.Get("limit") != "10" {
		t.Errorf("query %q", s.query)
	}
}

// CreateBackup handles both the backups list new servers answer with and the
// single backup older ones return.
func TestCreateBackupShapes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"backups":[{"id":"b1"},{"id":"b2"}]}`, 2},
		{`{"id":"b1","filename":"x.audiobookshelf"}`, 1},
		{`{}`, 0},
	} {
		s := newJSONServer(t, func(*http.Request) (int, string) { return http.StatusOK, tc.body })
		got, err := newClient(t, s).CreateBackup(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.want {
			t.Errorf("%s: %d backups, want %d", tc.body, len(got), tc.want)
		}
	}
}

// The collection and playlist listings arrive under one key from /api and
// another from inside a library.
func TestListWrappersAcceptBothKeys(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(r *http.Request) (int, string) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/libraries/"):
			return http.StatusOK, `{"results":[{"id":"x1"}]}`
		case strings.HasSuffix(r.URL.Path, "collections"):
			return http.StatusOK, `{"collections":[{"id":"c1"},{"id":"c2"}]}`
		default:
			return http.StatusOK, `{"playlists":[{"id":"p1"},{"id":"p2"}]}`
		}
	})
	c := newClient(t, s)

	if cols, err := c.Collections(t.Context(), ""); err != nil || len(cols) != 2 {
		t.Errorf("Collections() = %v, %v", cols, err)
	}
	if cols, err := c.Collections(t.Context(), "lib"); err != nil || len(cols) != 1 || s.path != "/api/libraries/lib/collections" {
		t.Errorf("Collections(lib) = %v, %v at %s", cols, err, s.path)
	}
	if pls, err := c.Playlists(t.Context(), ""); err != nil || len(pls) != 2 {
		t.Errorf("Playlists() = %v, %v", pls, err)
	}
	if pls, err := c.Playlists(t.Context(), "lib"); err != nil || len(pls) != 1 || s.path != "/api/libraries/lib/playlists" {
		t.Errorf("Playlists(lib) = %v, %v at %s", pls, err, s.path)
	}
}

// Narrators, tags and genres are addressed by base64 of the value, and the
// narrator removal is a DELETE whose body carries the count.
func TestVocabularyPathsAreBase64(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"updated":3,"numItemsUpdated":4}`
	})
	c := newClient(t, s)
	enc := func(v string) string { return base64.StdEncoding.EncodeToString([]byte(v)) }

	n, err := c.RemoveNarrator(t.Context(), "lib", "Jim Dale")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || s.method != http.MethodDelete || s.path != "/api/libraries/lib/narrators/"+enc("Jim Dale") {
		t.Errorf("RemoveNarrator: %d via %s %s", n, s.method, s.path)
	}

	n, err = c.RenameNarrator(t.Context(), "lib", "jim dale", "Jim Dale")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	_ = json.Unmarshal(s.body, &body)
	if n != 3 || s.method != http.MethodPatch || body["name"] != "Jim Dale" {
		t.Errorf("RenameNarrator: %d via %s with %s", n, s.method, s.body)
	}

	n, err = c.DeleteTag(t.Context(), "Sci Fi")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || s.path != "/api/tags/"+enc("Sci Fi") {
		t.Errorf("DeleteTag: %d via %s", n, s.path)
	}
	if _, err := c.DeleteGenre(t.Context(), "a/b"); err != nil || s.path != "/api/genres/"+enc("a/b") {
		t.Errorf("DeleteGenre: %v via %s", err, s.path)
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

// The item endpoint always asks for the expanded shape with progress, and
// ItemsBatch posts the ids under the key the server reads.
func TestItemRequests(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(r *http.Request) (int, string) {
		if r.Method == http.MethodPost {
			return http.StatusOK, `{"libraryItems":[{"id":"i1"},{"id":"i2"}]}`
		}
		return http.StatusOK, `{"id":"i1","media":{"metadata":{"title":"Dune"}}}`
	})
	c := newClient(t, s)

	it, err := c.Item(t.Context(), "i1")
	if err != nil {
		t.Fatal(err)
	}
	if q, _ := parseQuery(s.query); q.Get("expanded") != "1" || q.Get("include") != "progress" {
		t.Errorf("Item query %q", s.query)
	}
	if it.Title() != "Dune" {
		t.Errorf("item = %+v", it)
	}

	items, err := c.ItemsBatch(t.Context(), []string{"i1", "i2"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string][]string
	_ = json.Unmarshal(s.body, &body)
	if len(items) != 2 || s.path != "/api/items/batch/get" || !slices.Equal(body["libraryItemIds"], []string{"i1", "i2"}) {
		t.Errorf("ItemsBatch: %d items via %s with %s", len(items), s.path, s.body)
	}
}
