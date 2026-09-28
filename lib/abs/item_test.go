package abs

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// A cover that is absent and a cover that is empty are both ErrNoCover rather
// than a decode failure, because audit_cover_ratio has to tell "no cover" from
// "broken cover".
func TestCoverSizeNoCover(t *testing.T) {
	t.Parallel()

	var status int
	var body []byte
	c := newClient(t, newJSONServer(t, func(*http.Request) (int, string) { return status, string(body) }))

	status, body = http.StatusNotFound, []byte(`{"error":"nope"}`)
	if _, _, err := c.CoverSize(t.Context(), "i1"); !errors.Is(err, ErrNoCover) {
		t.Errorf("a missing cover gave %v, want ErrNoCover", err)
	}

	status, body = http.StatusOK, nil
	if _, _, err := c.CoverSize(t.Context(), "i1"); !errors.Is(err, ErrNoCover) {
		t.Errorf("an empty cover gave %v, want ErrNoCover", err)
	}

	// the file's own problem, which an audit skips, apart from a server
	// that failed to send it, which it must not
	status, body = http.StatusOK, []byte("not an image at all")
	if _, _, err := c.CoverSize(t.Context(), "i1"); !errors.Is(err, ErrCoverUnreadable) || errors.Is(err, ErrNoCover) {
		t.Errorf("an undecodable cover gave %v, want ErrCoverUnreadable", err)
	}
	status, body = http.StatusInternalServerError, []byte("boom")
	if _, _, err := c.CoverSize(t.Context(), "i1"); err == nil || errors.Is(err, ErrCoverUnreadable) {
		t.Errorf("a server failing to send the cover gave %v, want an error that is not ErrCoverUnreadable", err)
	}
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
	if q := s.values(t); q.Get("expanded") != "1" || q.Get("include") != "progress" {
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
	if q := s.values(t); q.Get("region") != "uk" || q.Get("asin") != "B0" {
		t.Errorf("query %q", s.query)
	}
	want := []Chapter{{ID: 0, Start: 0, End: 1.5, Title: "One"}, {ID: 1, Start: 1.5, End: 4, Title: "Two"}}
	if !slices.Equal(chapters, want) {
		t.Errorf("chapters = %+v, want %+v", chapters, want)
	}

	// the server's word, not a 404 it did not send: a failed lookup at
	// Audible must not read as "no chapters"
	if _, err := c.SearchChapters(t.Context(), "MISSING", ""); err == nil || !strings.Contains(err.Error(), "Chapters not found") || IsNotFound(err) {
		t.Errorf("a miss gave %v", err)
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
