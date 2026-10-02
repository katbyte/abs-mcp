package abs

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestItemsQueryAndAuth(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, func(r *http.Request) (int, string) {
		if r.URL.Path != "/api/libraries/lib1/items" {
			return http.StatusNotFound, "Not Found"
		}
		return http.StatusOK, `{"results":[{"id":"i1","mediaType":"book","media":{"metadata":{"title":"T"}}}],"total":1,"limit":25,"page":0}`
	})
	c := newClient(t, s)
	page, err := c.Items(t.Context(), "lib1", ItemsOptions{Limit: 25, Sort: "addedAt", Desc: true, Filter: EncodeFilter("missing", "asin"), Minified: true})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth := s.header.Get("Authorization"); gotAuth != "Bearer k" {
		t.Errorf("auth header = %q", gotAuth)
	}
	q, gotQuery := s.values(t), s.query
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

// SplitCSV parses the comma-separated fields the API returns for genres, tags
// and narrators, where blanks and stray spacing are common.
func TestSplitCSV(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",,,", nil},
		{"Science Fiction", []string{"Science Fiction"}},
		{"sf, classic", []string{"sf", "classic"}},
		{" sf ,, classic ,", []string{"sf", "classic"}},
	} {
		got := SplitCSV(c.in)
		if len(got) != len(c.want) {
			t.Errorf("SplitCSV(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("SplitCSV(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
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
	if q := s.values(t); q.Get("sort") != "numBooks" || q.Get("desc") != "1" || q.Get("limit") != "10" {
		t.Errorf("query %q", s.query)
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

// A library's series, a page of them: the sort, order and filter go in the
// query only when chosen, and the count is of every series, not the page.
func TestSeriesListPages(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"results":[{"id":"se1","name":"Dune","books":[{"id":"i1"},{"id":"i2"}]}],"total":120}`))
	c := newClient(t, s)

	series, total, err := c.SeriesList(t.Context(), "lib1", ListOptions{Limit: 50, Page: 2, Sort: "name", Desc: true, Filter: "progress.ZmluaXNoZWQ="})
	if err != nil {
		t.Fatal(err)
	}
	if total != 120 || len(series) != 1 || series[0].Name != "Dune" || len(series[0].Books) != 2 {
		t.Errorf("answer = %+v of %d", series, total)
	}
	q := s.values(t)
	if s.path != "/api/libraries/lib1/series" || q.Get("limit") != "50" || q.Get("page") != "2" || q.Get("sort") != "name" || q.Get("desc") != "1" || q.Get("filter") != "progress.ZmluaXNoZWQ=" {
		t.Errorf("asked %s?%s", s.path, s.query)
	}

	if _, _, err := c.SeriesList(t.Context(), "lib1", ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if q = s.values(t); q.Has("limit") || q.Has("sort") || q.Has("desc") || q.Has("filter") || q.Get("page") != "0" {
		t.Errorf("with no options asked ?%s", s.query)
	}
}

// The libraries' order is sent as each id with its place, counted from one,
// and the answer is the libraries in their new order.
func TestReorderLibrariesNumbersFromOne(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"libraries":[{"id":"b","name":"Second"},{"id":"a","name":"First"}]}`))
	c := newClient(t, s)

	libs, err := c.ReorderLibraries(t.Context(), []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 2 || libs[0].ID != "b" || libs[1].Name != "First" {
		t.Errorf("answer = %+v", libs)
	}
	if s.method != http.MethodPost || s.path != "/api/libraries/order" || string(s.body) != `[{"id":"b","newOrder":1},{"id":"a","newOrder":2}]` {
		t.Errorf("sent %s %s %s", s.method, s.path, s.body)
	}

	// no libraries is an empty list, which the server reads, not null
	if _, err := c.ReorderLibraries(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if string(s.body) != `[]` {
		t.Errorf("no libraries sent %s, want an empty list", s.body)
	}
}

// The newest episodes across a podcast library, a page of them.
func TestRecentEpisodesPages(t *testing.T) {
	t.Parallel()

	s := newJSONServer(t, always(http.StatusOK, `{"episodes":[{"id":"e1","libraryItemId":"i1","title":"Pilot","season":2,"episode":"7"}]}`))
	c := newClient(t, s)

	episodes, err := c.RecentEpisodes(t.Context(), "pod lib", 5, 4)
	if err != nil {
		t.Fatal(err)
	}
	// the season comes as a number or as text, show by show
	if len(episodes) != 1 || episodes[0].LibraryItemID != "i1" || episodes[0].Title != "Pilot" || episodes[0].Season.String() != "2" || episodes[0].Episode.String() != "7" {
		t.Errorf("answer = %+v", episodes)
	}
	if q := s.values(t); s.path != "/api/libraries/pod lib/recent-episodes" || q.Get("limit") != "5" || q.Get("page") != "4" {
		t.Errorf("asked %s?%s", s.path, s.query)
	}
}
