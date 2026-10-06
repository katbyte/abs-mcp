package tools

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// A share not mounted during a scan marks every book on it missing, and one
// call used to drop all their records and everyone's progress unseen. Without
// confirm it says how many and which and sends nothing; with it, what it
// removed is read back rather than taken from the count before.
func TestLibraryIssuesRemoveNeedsConfirm(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	var mu sync.Mutex
	deleted := false
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "issues" {
			http.Error(w, "unfiltered", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if deleted { // one record the server would not delete
			_, _ = io.WriteString(w, `{"results":[`+item("i3", "Stuck", "", "")+`],"total":1}`)
			return
		}
		_, _ = io.WriteString(w, `{"results":[`+item("i1", "Gone One", "", "")+`,`+item("i2", "Gone Two", "", "")+`,`+item("i3", "Stuck", "", "")+`],"total":3}`)
	})
	f.mux.HandleFunc("DELETE /api/libraries/"+libID+"/issues", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		deleted = true
		mu.Unlock()
		_, _ = io.WriteString(w, "OK")
	})
	call := toolCaller(t, f)

	out, err := call("library_issues_remove", nil)
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["found"]) != 3 || num(t, out["removed"]) != 0 || num(t, out["remaining"]) != 3 {
		t.Errorf("preview = %v, want 3 found, none removed", out)
	}
	if got := column(t, "title", out["items"]); !slices.Equal(got, []string{"Gone One", "Gone Two", "Stuck"}) {
		t.Errorf("items = %v, want the titles", got)
	}
	if first := list(t, out["items"])[0]; str(t, first["id"]) != "i1" {
		t.Errorf("the first row = %v, want its id", first)
	}
	if got := f.requests("/api/libraries/" + libID + "/issues"); len(got) != 0 {
		t.Fatalf("the preview deleted: %v", got)
	}

	out, err = call("library_issues_remove", map[string]any{"confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/libraries/" + libID + "/issues"); len(got) != 1 {
		t.Errorf("confirm sent %v, want one DELETE", got)
	}
	if num(t, out["removed"]) != 2 || num(t, out["remaining"]) != 1 {
		t.Errorf("confirm = %v, want 2 removed and 1 left, read back", out)
	}
}

// The server ignores a filter group or value it does not know and answers the
// whole library, which library_items presented as the filtered answer and
// item_match_tag would tag. Only what the server knows is sent.
func TestLibraryItemsRefusesAFilterTheServerDoesNotKnow(t *testing.T) {
	t.Parallel()

	const podLib = "55555555-5555-4555-8555-555555555501"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"},{"id":"`+podLib+`","name":"Pods","mediaType":"podcast"}]}`)
	f.json("GET /api/libraries/"+libID+"/items", page())
	f.json("GET /api/libraries/"+podLib+"/items", page())
	call := toolCaller(t, f)

	for _, tc := range []struct{ library, filter, want string }{
		{"Books", "authr:Heinlein", "unknown filter group"},
		{"Books", "missing:asn", "choose one of"},
		{"Books", "progress:done", "choose one of"},
		{"Books", "tracks:many", "choose one of"},
		{"Books", "ebooks:yes", "choose one of"},
		{"Books", "issues:yes", "takes no value"},
		{"Books", "abridged:not-abridged", "takes no value"},
		{"Pods", "progress:finished", "podcast library"},
		{"Pods", "narrators:Jim Dale", "podcast library"},
	} {
		_, err := call("library_items", map[string]any{"library": tc.library, "filter": tc.filter})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s in %s: %v, want %q", tc.filter, tc.library, err, tc.want)
		}
	}
	for _, lib := range []string{libID, podLib} {
		if got := f.requests("/api/libraries/" + lib + "/items"); len(got) != 0 {
			t.Fatalf("a refused filter was sent: %v", got)
		}
	}

	// what the server knows goes as it spells it; a bare group goes bare
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	for filter, want := range map[string]string{
		"abridged":          "abridged",
		"Missing:ASIN":      "missing." + b64("asin"),
		"Genres:Fantasy":    "genres." + b64("Fantasy"),
		"series:no-series":  "series." + b64("no-series"),
		"progress:Finished": "progress." + b64("finished"),
	} {
		if _, err := call("library_items", map[string]any{"library": "Books", "filter": filter}); err != nil {
			t.Errorf("%s: %v", filter, err)
			continue
		}
		sent := f.requests("/api/libraries/" + libID + "/items")
		if got := parseQuery(t, sent[len(sent)-1].Query).Get("filter"); got != want {
			t.Errorf("%s sent filter=%s, want %s", filter, got, want)
		}
	}
	if _, err := call("library_items", map[string]any{"library": "Pods", "filter": "genres:Comedy"}); err != nil {
		t.Errorf("a podcast genre: %v", err)
	}
}

// Audiobookshelf lets two authors or series share a name. A name that is
// both is refused with the ids, not settled by whichever came first.
func TestBuildFilterRefusesANameTwoRecordsShare(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"au1","name":"Jim Dale"},{"id":"au2","name":"jim dale"},{"id":"au3","name":"Stephen Fry"}],"total":3}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"se1","name":"Discworld"},{"id":"se2","name":"Discworld"}],"total":2}`)
	f.json("GET /api/libraries/"+libID+"/items", page())
	call := toolCaller(t, f)

	for _, tc := range []struct{ filter, a, b string }{
		{"authors:Jim Dale", "au1", "au2"},
		{"series:discworld", "se1", "se2"},
	} {
		_, err := call("library_items", map[string]any{"filter": tc.filter})
		if err == nil || !strings.Contains(err.Error(), tc.a) || !strings.Contains(err.Error(), tc.b) {
			t.Errorf("%s: %v, want both ids", tc.filter, err)
		}
	}
	if got := f.requests("/api/libraries/" + libID + "/items"); len(got) != 0 {
		t.Fatalf("an ambiguous name was sent: %v", got)
	}

	// one record by that name, or an id, still resolves
	for _, filter := range []string{"authors:stephen fry", "authors:au2"} {
		if _, err := call("library_items", map[string]any{"filter": filter}); err != nil {
			t.Errorf("%s: %v", filter, err)
		}
	}
}

// Audiobookshelf pages by page number, and an offset that was not a whole
// page was rounded down: limit=50 offset=25 answered items 0-49 as offset 0.
// A negative offset was sent as a negative page and no limit was capped.
func TestLibraryItemsPagesFromTheOffsetAsked(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	servePages(f, libID, 120)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		limit, offset       int
		first, last         string
		count, offsetOut    int
		next                any
		sentLimit, sentPage []string
	}{
		{50, 25, "Book 025", "Book 074", 50, 25, 75.0, []string{"50", "50"}, []string{"0", "1"}},
		{50, 100, "Book 100", "Book 119", 20, 100, nil, []string{"50"}, []string{"2"}},
		{50, 110, "Book 110", "Book 119", 10, 110, nil, []string{"50"}, []string{"2"}},
		{50, -120, "Book 000", "Book 049", 50, 0, 50.0, []string{"50"}, []string{"0"}},
		{1000000, 0, "Book 000", "Book 119", 120, 0, nil, []string{"1000"}, []string{"0"}},
	} {
		before := len(f.requests("/api/libraries/" + libID + "/items"))
		out, err := call("library_items", map[string]any{"limit": tc.limit, "offset": tc.offset})
		if err != nil {
			t.Fatal(err)
		}
		got := column(t, "title", out["items"])
		if len(got) != tc.count || got[0] != tc.first || got[len(got)-1] != tc.last {
			t.Errorf("limit %d offset %d: %d items %v..%v, want %d from %s to %s", tc.limit, tc.offset, len(got), got[0], got[len(got)-1], tc.count, tc.first, tc.last)
		}
		if num(t, out["offset"]) != tc.offsetOut || out["next_offset"] != tc.next || num(t, out["total"]) != 120 {
			t.Errorf("limit %d offset %d: offset %v next_offset %v total %v, want %d, %v, 120", tc.limit, tc.offset, out["offset"], out["next_offset"], out["total"], tc.offsetOut, tc.next)
		}
		sent := f.requests("/api/libraries/" + libID + "/items")[before:]
		limits, pages := make([]string, 0, len(sent)), make([]string, 0, len(sent))
		for _, r := range sent {
			q := parseQuery(t, r.Query)
			limits, pages = append(limits, q.Get("limit")), append(pages, q.Get("page"))
		}
		if !slices.Equal(limits, tc.sentLimit) || !slices.Equal(pages, tc.sentPage) {
			t.Errorf("limit %d offset %d asked for limits %v pages %v, want %v %v", tc.limit, tc.offset, limits, pages, tc.sentLimit, tc.sentPage)
		}
	}
}

// The server orders by nothing at all for a sort it does not know, so a typo
// came back unsorted as if sorted; a podcast library knows fewer sorts.
func TestLibraryItemsRefusesAnUnknownSort(t *testing.T) {
	t.Parallel()

	const podLib = "55555555-5555-4555-8555-555555555502"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"},{"id":"`+podLib+`","name":"Pods","mediaType":"podcast"}]}`)
	f.json("GET /api/libraries/"+libID+"/items", page())
	f.json("GET /api/libraries/"+podLib+"/items", page())
	call := toolCaller(t, f)

	for _, tc := range []struct{ library, sort string }{
		{"Books", "titel"},
		{"Books", "episodes"},
		{"Pods", "duration"},
		{"Pods", "sequence"},
	} {
		_, err := call("library_items", map[string]any{"library": tc.library, "sort": tc.sort})
		if err == nil || !strings.Contains(err.Error(), "choose one of") {
			t.Errorf("sort %s in %s: %v, want the valid ones named", tc.sort, tc.library, err)
		}
	}
	if len(f.requests("/api/libraries/"+libID+"/items"))+len(f.requests("/api/libraries/"+podLib+"/items")) != 0 {
		t.Fatal("a refused sort was sent")
	}

	for _, tc := range []struct{ library, sort, want string }{
		{"Books", "Duration", "media.duration"},
		{"Books", "addedAt", "addedAt"},
		{"Pods", "episodes", "media.numTracks"},
	} {
		if _, err := call("library_items", map[string]any{"library": tc.library, "sort": tc.sort}); err != nil {
			t.Errorf("sort %s: %v", tc.sort, err)
			continue
		}
		lib := libID
		if tc.library == "Pods" {
			lib = podLib
		}
		sent := f.requests("/api/libraries/" + lib + "/items")
		if got := parseQuery(t, sent[len(sent)-1].Query).Get("sort"); got != tc.want {
			t.Errorf("sort %s sent %q, want %q", tc.sort, got, tc.want)
		}
	}
}

// A name of spaces was sent as the new name, and a provider the server does
// not have was stored as the library's default.
func TestLibraryEditAndCreateRefuseABlankNameAndAnUnknownProvider(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"}]}`)
	f.json("GET /api/search/providers", `{"providers":{"books":[{"value":"google","text":"Google Books"},{"value":"audible.ca","text":"Audible.ca"}],"podcasts":[{"value":"itunes","text":"iTunes"}]}}`)
	f.json("PATCH /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible.ca"}`)
	f.json("POST /api/libraries", `{"id":"new","name":"New","mediaType":"book"}`)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"library_edit", map[string]any{"library": "Books", "name": "   "}, "blank"},
		{"library_edit", map[string]any{"library": "Books", "provider": "audable"}, "google, audible.ca"},
		{"library_edit", map[string]any{"library": "Books", "provider": "itunes"}, "no book metadata provider"},
		{"library_create", map[string]any{"name": "New", "folders": []any{"/new"}, "provider": "goggle"}, "google, audible.ca"},
		{"library_create", map[string]any{"name": "New", "folders": []any{"/new"}, "media_type": "podcast", "provider": "google"}, "itunes"},
	} {
		_, err := call(tc.tool, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %v: %v, want %q", tc.tool, tc.args, err, tc.want)
		}
	}
	for _, r := range f.requests("/api/libraries/" + libID) {
		t.Errorf("a refused edit was sent: %s %s", r.Method, r.Body)
	}
	for _, r := range f.requests("/api/libraries") {
		if r.Method != http.MethodGet {
			t.Errorf("a refused create was sent: %s", r.Body)
		}
	}

	// the provider as the server spells it, and the name trimmed
	if _, err := call("library_edit", map[string]any{"library": "Books", "provider": "Audible.CA", "name": " Audiobooks "}); err != nil {
		t.Fatal(err)
	}
	sent := f.requests("/api/libraries/" + libID)
	if len(sent) != 1 || !strings.Contains(sent[0].Body, `"provider":"audible.ca"`) || !strings.Contains(sent[0].Body, `"name":"Audiobooks"`) {
		t.Errorf("edit sent %v", sent)
	}
}

// library_search is how a title someone typed becomes an item id, and the
// first row is the one taken. The server lists its hits in an order of its
// own - a live server answers "Foundation" with Second Foundation, then
// Foundation and Empire, then Foundation - so the book whose title is the
// query goes first, and the rest keep the server's order.
func TestLibrarySearchPutsTheTitleAskedForFirst(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}}`)
	f.json("GET /api/libraries/"+libID+"/search", `{"book":[`+
		`{"libraryItem":`+item("i1", "Second Foundation", "", "")+`},`+
		`{"libraryItem":`+item("i2", "Foundation and Empire", "", "")+`},`+
		`{"libraryItem":`+item("i3", "Foundation", "", "")+`}]}`)
	call := toolCaller(t, f)

	out, err := call("library_search", map[string]any{"query": " foundation "})
	if err != nil {
		t.Fatal(err)
	}
	items := list(t, out["items"])
	titles := make([]string, 0, len(items))
	for _, row := range items {
		titles = append(titles, str(t, row["title"]))
	}
	if want := []string{"Foundation", "Second Foundation", "Foundation and Empire"}; !slices.Equal(titles, want) {
		t.Errorf("items = %v, want %v", titles, want)
	}
}

func TestLibraryStatsThatFailAreAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"accessAllTags":true,"accessExplicitContent":true}}`)
	f.json("GET /api/libraries/"+libID, `{"library":{"id":"`+libID+`","name":"Books","mediaType":"book"},"filterdata":{},"issues":0}`)
	f.fails("GET /api/libraries/" + libID + "/stats")
	call := toolCaller(t, f)

	_, err := call("library_get", map[string]any{"library": "Books"})
	wantErr(t, "library_get with its stats failing", err, "library's stats", "500")
}

func TestSortKey(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in, want string
		podcast  bool
	}{
		{"", "media.metadata.title", false},
		{"Title", "media.metadata.title", false},
		{"author", "media.metadata.authorName", false},
		{"author", "media.metadata.author", true},
		{"added", "addedAt", false},
		{"duration", "media.duration", false},
		{"random", "random", false},
		{"sequence", "sequence", false},
	} {
		if got := sortKey(tc.in, tc.podcast); got != tc.want {
			t.Errorf("sortKey(%q, %v) = %q want %q", tc.in, tc.podcast, got, tc.want)
		}
	}
}

func TestBuildFilter(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries/lib/authors", `{"results":[{"id":"a1","name":"Frank Herbert"}],"total":1}`)
	f.json("GET /api/libraries/lib/series", `{"results":[{"id":"s1","name":"Dune"}],"total":1}`)
	client := f.client(t)
	lib := &abs.Library{ID: "lib", Name: "Books"}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"issues", "issues"},
		{"genre:Fantasy", "genres." + b64("Fantasy")},
		{"tag: Read ", "tags." + b64("Read")},
		{"progress:in-progress", "progress." + b64("in-progress")},
		{"missing:asin", "missing." + b64("asin")},
		{"author:frank herbert", "authors." + b64("a1")},
		{"authors:a1", "authors." + b64("a1")},
		{"series:Dune", "series." + b64("s1")},
		{"abridged", "abridged"},
	} {
		got, err := buildFilter(context.Background(), client, lib, tc.in)
		if err != nil {
			t.Errorf("buildFilter(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("buildFilter(%q) = %q want %q", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{"genres", "author:Nobody", "series:Unknown"} {
		if _, err := buildFilter(context.Background(), client, lib, bad); err == nil {
			t.Errorf("buildFilter(%q) accepted", bad)
		}
	}
}

func TestLibraryGetInSecondsAndBytes(t *testing.T) {
	t.Parallel()

	t.Run("from the server's stats", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		oneLibrary(f)
		f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"accessAllTags":true,"accessExplicitContent":true}}`)
		f.json("GET /api/libraries/"+libID, `{"library":{"id":"`+libID+`","name":"Books","mediaType":"book"},"filterdata":{},"issues":0}`)
		f.json("GET /api/libraries/"+libID+"/stats", `{"totalItems":2,"totalDuration":90000.6,"totalSize":1610612736,"numAudioTracks":2,`+
			`"longestItems":[{"id":"i1","title":"Long","duration":60000.4}],"largestItems":[{"id":"i1","title":"Long","size":1073741824}]}`)
		call := toolCaller(t, f)

		out, err := call("library_get", map[string]any{"library": "Books"})
		if err != nil {
			t.Fatal(err)
		}
		// 1.5 GB, which total_size_gb gave as 1
		wantNumbers(t, "library_get", out, map[string]float64{
			"total_duration_s": 90001, "total_size": 1610612736, "longest.0.duration_s": 60000, "largest.0.size": 1073741824,
		})
		wantAbsent(t, "library_get", out, "total_duration", "total_size_gb", "longest.0.duration", "largest.0.size_mb")
	})

	t.Run("from the books a restricted key sees", func(t *testing.T) {
		t.Parallel()

		f := newFakeABS(t)
		oneLibrary(f)
		f.json("GET /api/me", `{"id":"u1","username":"kid","type":"user","permissions":{"accessAllLibraries":true,"accessAllTags":false,"accessExplicitContent":true}}`)
		// the listing gives the size beside the id, not in the media
		visible := strings.Replace(item(bookB1, "Visible Book", "", `"duration":600.6,"numAudioFiles":1`), `{"id":`, `{"size":52428800,"id":`, 1)
		f.json("GET /api/libraries/"+libID+"/items", page(visible))
		f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
		f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
		call := toolCaller(t, f)

		out, err := call("library_get", map[string]any{"library": "Books"})
		if err != nil {
			t.Fatal(err)
		}
		wantNumbers(t, "library_get", out, map[string]float64{
			"total_duration_s": 601, "total_size": 52428800, "longest.0.duration_s": 601, "largest.0.size": 52428800,
		})
	})
}

// library_filters pages every list alike, says how long each is in full, and
// with fields answers only the lists asked for.
func TestLibraryFiltersPagesAndNarrows(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"accessAllTags":true,"accessExplicitContent":true}}`)
	f.json("GET /api/libraries/"+libID+"/filterdata", `{"genres":["Fantasy","History","Science Fiction"],"tags":["sf"],"narrators":["Jim Dale","Kate Reading"],"languages":["English"],"publishers":[],"publishedDecades":[]}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1","name":"Frank Herbert"},{"id":"a2","name":"Isaac Asimov"},{"id":"a3","name":"Terry Pratchett"}],"total":3}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"Dune"}],"total":1}`)
	call := toolCaller(t, f)

	out, err := call("library_filters", map[string]any{"limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	totals, ok := out["totals"].(map[string]any)
	if !ok || num(t, totals["genres"]) != 3 || num(t, totals["authors"]) != 3 || num(t, totals["series"]) != 1 || num(t, totals["publishers"]) != 0 {
		t.Fatalf("totals = %v, want each list's full length", out["totals"])
	}
	if !slices.Equal(strs(t, out["genres"]), []string{"Fantasy", "History"}) || len(list(t, out["authors"])) != 2 || num(t, out["next_offset"]) != 2 {
		t.Errorf("the first page = %v, want two of each longer list and the next at 2", out)
	}

	out, err = call("library_filters", map[string]any{"limit": 2, "offset": 2, "fields": []any{"genres", "authors"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(strs(t, out["genres"]), []string{"Science Fiction"}) || !slices.Equal(column(t, "name", out["authors"]), []string{"Terry Pratchett"}) || out["next_offset"] != nil {
		t.Errorf("the second page = %v, want the last genre and author and no next", out)
	}
	if out["tags"] != nil || out["series"] != nil || out["narrators"] != nil {
		t.Errorf("lists not asked for came back: %v", out)
	}
	if totals, ok = out["totals"].(map[string]any); !ok || len(totals) != 2 {
		t.Errorf("totals = %v, want only the lists asked for", totals)
	}

	_, err = call("library_filters", map[string]any{"fields": []any{"genre"}})
	wantErr(t, "a list that does not exist", err, `"genre"`, "genres")
}
