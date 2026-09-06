package abs

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestFlexStringAndSeriesRefs(t *testing.T) {
	t.Parallel()

	var m Metadata
	raw := `{"title":"Dune","publishedYear":1965,"series":{"id":"s1","name":"Dune","sequence":"1"},"itunesId":null}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.PublishedYear != "1965" || m.PublishedYear.Int() != 1965 {
		t.Errorf("publishedYear = %q", m.PublishedYear)
	}
	if len(m.Series) != 1 || m.Series[0].Sequence != "1" {
		t.Errorf("single-object series not decoded: %+v", m.Series)
	}
	if got := m.SeriesDisplay(); len(got) != 1 || got[0] != "Dune #1" {
		t.Errorf("SeriesDisplay = %v", got)
	}

	raw = `{"publishedYear":"1965","series":[{"name":"A","sequence":"2"},{"name":"B"}],"narrators":["X","Y"],"authors":[{"id":"a","name":"Frank Herbert"}]}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Series) != 2 || m.AuthorDisplay() != "Frank Herbert" || m.NarratorDisplay() != "X, Y" {
		t.Errorf("array shapes: %+v", m)
	}
}

func TestItemsQueryAndAuth(t *testing.T) {
	t.Parallel()

	var gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		if r.URL.Path != "/api/libraries/lib1/items" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"id":"i1","mediaType":"book","media":{"metadata":{"title":"T"}}}],"total":1,"limit":25,"page":0}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "key123")
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.Items(t.Context(), "lib1", ItemsOptions{Limit: 25, Sort: "addedAt", Desc: true, Filter: EncodeFilter("missing", "asin"), Minified: true})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer key123" {
		t.Errorf("auth header = %q", gotAuth)
	}
	q, _ := parseQuery(gotQuery)
	for k, want := range map[string]string{"limit": "25", "page": "0", "sort": "addedAt", "desc": "1", "minified": "1", "filter": "missing.YXNpbg=="} {
		if q.Get(k) != want {
			t.Errorf("query %s = %q want %q (raw %s)", k, q.Get(k), want, gotQuery)
		}
	}
	if page.Total != 1 || len(page.Results) != 1 || page.Results[0].Title() != "T" {
		t.Errorf("page = %+v", page)
	}

	if _, err := c.Item(t.Context(), "missing"); !IsNotFound(err) {
		t.Errorf("expected not-found, got %v", err)
	}
}

func TestProgressNotFoundIsNil(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "k")
	p, err := c.Progress(t.Context(), "i1", "")
	if err != nil || p != nil {
		t.Errorf("got %v, %v", p, err)
	}
}

func TestHTTPErrorMessage(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	c, _ := New(srv.URL, "k")
	err := c.ScanLibrary(t.Context(), "lib", false)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"HTTP 403", "nope", "permission"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
