package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// serveNamed answers a paged author or series listing of n records named
// "Name 000" on, paged by the limit and page asked for.
func serveNamed(f *fakeABS, path string, n int) {
	f.mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if limit <= 0 {
			limit = n
		}
		var rows []string
		for i := max(pg, 0) * limit; i < min((max(pg, 0)+1)*limit, n); i++ {
			rows = append(rows, fmt.Sprintf(`{"id":"r%03d","name":"Name %03d","books":[]}`, i, i))
		}
		_, _ = fmt.Fprintf(w, `{"results":[%s],"total":%d}`, strings.Join(rows, ","), n)
	})
}

// author_list and series_list rounded an offset down to a page, did not say
// the offset they used, sent a negative one as a negative page, capped no
// limit and passed an unknown sort through to come back in no order.
func TestAuthorAndSeriesListPageFromTheOffsetAsked(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	serveNamed(f, "/api/libraries/"+libID+"/authors", 12)
	serveNamed(f, "/api/libraries/"+libID+"/series", 12)
	call := toolCaller(t, f)

	for _, tc := range []struct{ tool, rows, path string }{
		{"author_list", "authors", "/api/libraries/" + libID + "/authors"},
		{"series_list", "series", "/api/libraries/" + libID + "/series"},
	} {
		out, err := call(tc.tool, map[string]any{"limit": 5, "offset": 3})
		if err != nil {
			t.Fatal(err)
		}
		if got := column(t, "name", out[tc.rows]); !slices.Equal(got, []string{"Name 003", "Name 004", "Name 005", "Name 006", "Name 007"}) {
			t.Errorf("%s limit 5 offset 3 = %v", tc.tool, got)
		}
		if num(t, out["offset"]) != 3 || num(t, out["next_offset"]) != 8 {
			t.Errorf("%s: offset %v next_offset %v, want 3 and 8", tc.tool, out["offset"], out["next_offset"])
		}

		out, err = call(tc.tool, map[string]any{"limit": 5, "offset": 10})
		if err != nil {
			t.Fatal(err)
		}
		if got := column(t, "name", out[tc.rows]); !slices.Equal(got, []string{"Name 010", "Name 011"}) || out["next_offset"] != nil {
			t.Errorf("%s the last page = %v, next_offset %v", tc.tool, got, out["next_offset"])
		}

		before := len(f.requests(tc.path))
		out, err = call(tc.tool, map[string]any{"limit": 1000000, "offset": -120})
		if err != nil {
			t.Fatal(err)
		}
		if num(t, out["offset"]) != 0 || len(list(t, out[tc.rows])) != 12 {
			t.Errorf("%s negative offset: %v", tc.tool, out)
		}
		for _, r := range f.requests(tc.path)[before:] {
			if q := parseQuery(t, r.Query); q.Get("page") != "0" || q.Get("limit") != "1000" {
				t.Errorf("%s asked for %s, want page 0 of at most 1000", tc.tool, r.Query)
			}
		}

		before = len(f.requests(tc.path))
		if _, err := call(tc.tool, map[string]any{"sort": "bogus"}); err == nil || !strings.Contains(err.Error(), "choose one of") {
			t.Errorf("%s sort=bogus: %v", tc.tool, err)
		}
		if got := f.requests(tc.path)[before:]; len(got) != 0 {
			t.Errorf("%s sent an unknown sort: %v", tc.tool, got)
		}
	}

	if _, err := call("author_list", map[string]any{"sort": "Books"}); err != nil {
		t.Error(err)
	}
	sent := f.requests("/api/libraries/" + libID + "/authors")
	if got := parseQuery(t, sent[len(sent)-1].Query).Get("sort"); got != "numBooks" {
		t.Errorf("sort=Books sent %q", got)
	}
}

// seriesBooks read one page of 500: series_get cut a longer series short
// without saying so, and series_merge moved 500 books and reported success.
// Every page is read now, the batch reads and writes go in hundreds, a batch
// that fails says how many moved before it, and whether the emptied series
// is gone is read back rather than promised.
func TestSeriesMergeMovesEveryBookInBatches(t *testing.T) {
	t.Parallel()

	const n = 620
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/series/"+seriesA, `{"id":"`+seriesA+`","name":"Big","libraryId":"`+libID+`"}`)
	f.json("GET /api/series/"+seriesB, `{"id":"`+seriesB+`","name":"Bigger","libraryId":"`+libID+`"}`)
	var mu sync.Mutex
	merged, failAt, updates := false, 0, 0
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/series", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if merged {
			_, _ = io.WriteString(w, `{"results":[{"id":"`+seriesB+`","name":"Bigger"}],"total":1}`)
			return
		}
		_, _ = io.WriteString(w, `{"results":[{"id":"`+seriesA+`","name":"Big"},{"id":"`+seriesB+`","name":"Bigger"}],"total":2}`)
	})
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/items", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var rows []string
		for i := pg * limit; i < min((pg+1)*limit, n); i++ {
			rows = append(rows, item(fmt.Sprintf("b%03d", i), fmt.Sprintf("Book %03d", i), `"series":{"id":"`+seriesA+`","name":"Big","sequence":"`+strconv.Itoa(i+1)+`"}`, ""))
		}
		_, _ = fmt.Fprintf(w, `{"results":[%s],"total":%d}`, strings.Join(rows, ","), n)
	})
	var batchSizes []int
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"libraryItemIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		batchSizes = append(batchSizes, len(body.IDs))
		mu.Unlock()
		rows := make([]string, 0, len(body.IDs))
		for _, id := range body.IDs {
			seq, _ := strconv.Atoi(strings.TrimPrefix(id, "b"))
			rows = append(rows, `{"id":"`+id+`","libraryId":"`+libID+`","mediaType":"book","media":{"metadata":{"title":"x","series":[{"id":"`+seriesA+`","name":"Big","sequence":"`+strconv.Itoa(seq+1)+`"}]}}}`)
		}
		_, _ = io.WriteString(w, `{"libraryItems":[`+strings.Join(rows, ",")+`]}`)
	})
	f.mux.HandleFunc("POST /api/items/batch/update", func(w http.ResponseWriter, r *http.Request) {
		var body []json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		updates++
		if updates == failAt {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		if updates == 7 {
			merged = true
		}
		_, _ = fmt.Fprintf(w, `{"updates":%d}`, len(body))
	})
	call := toolCaller(t, f)

	out, err := call("series_get", map[string]any{"series": seriesA})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(list(t, out["books"])); got != n {
		t.Errorf("series_get = %d books, want all %d", got, n)
	}
	mu.Lock()
	for _, size := range batchSizes {
		if size > sweepBatchSize {
			t.Errorf("a batch read of %d items, want at most %d", size, sweepBatchSize)
		}
	}
	failAt = 3
	mu.Unlock()

	// the third batch fails: two hundred moved, and the error says so
	_, err = call("series_merge", map[string]any{"from": seriesA, "into": seriesB})
	if err == nil || !strings.Contains(err.Error(), "200 of the 620 books") {
		t.Fatalf("a failed batch: %v, want how many moved before it", err)
	}

	mu.Lock()
	updates, failAt = 0, 0
	mu.Unlock()
	out, err = call("series_merge", map[string]any{"from": seriesA, "into": seriesB})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["moved"]) != n || len(strs(t, out["books"])) != n {
		t.Errorf("moved %v, %d books listed, want all %d", out["moved"], len(strs(t, out["books"])), n)
	}
	mu.Lock()
	if updates != 7 {
		t.Errorf("%d batch updates, want 7 of at most %d", updates, sweepBatchSize)
	}
	mu.Unlock()
	if !boolOf(t, out["from_removed"]) || out["note"] != nil {
		t.Errorf("from_removed %v note %v, want the emptied series read back as gone", out["from_removed"], out["note"])
	}
}

// The server can keep a series whose books have all gone. series_merge said
// the emptied series goes away; it now says what it found.
func TestSeriesMergeSaysTheEmptiedSeriesIsStillThere(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f) // the listing never drops the series moved from
	call := toolCaller(t, f)

	out, err := call("series_merge", map[string]any{"from": "The Stormlight Archive", "into": "Stormlight Archive"})
	if err != nil {
		t.Fatal(err)
	}
	if boolOf(t, out["from_removed"]) || !strings.Contains(str(t, out["note"]), seriesB) {
		t.Errorf("from_removed %v note %q, want it reported as still listed", out["from_removed"], out["note"])
	}
}

// A rename that merged into another author deleted this one, and then the
// photo was cleared on the record that was gone: the call failed after the
// merge had happened. The photo goes first now, and the surviving author's
// is left alone.
func TestAuthorEditMergeAndClearImage(t *testing.T) {
	t.Parallel()

	const (
		oldID  = "77777777-7777-4777-8777-777777777771"
		keepID = "77777777-7777-4777-8777-777777777772"
	)
	old := `{"id":"` + oldID + `","name":"JRR Tolkien","imagePath":"/wrong.jpg","libraryItems":[]}`
	keep := `{"id":"` + keepID + `","name":"J.R.R. Tolkien","imagePath":"/right.jpg","libraryItems":[]}`
	f := newFakeABS(t)
	var mu sync.Mutex
	gone := false
	f.mux.HandleFunc("GET /api/authors/"+oldID, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if gone {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, old)
	})
	f.json("GET /api/authors/"+keepID, keep)
	f.mux.HandleFunc("DELETE /api/authors/"+oldID+"/image", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if gone {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"author":`+old+`}`)
	})
	f.mux.HandleFunc("PATCH /api/authors/"+oldID, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		gone = true
		mu.Unlock()
		_, _ = io.WriteString(w, `{"author":`+keep+`,"merged":true}`)
	})
	call := toolCaller(t, f)

	out, err := call("author_edit", map[string]any{"author": oldID, "name": "J.R.R. Tolkien", "clear": []any{"image"}})
	if err != nil {
		t.Fatalf("the merge happened and the call failed: %v", err)
	}
	author, ok := out["author"].(map[string]any)
	if !ok {
		t.Fatalf("author = %T", out["author"])
	}
	if !boolOf(t, out["merged"]) || str(t, author["id"]) != keepID || !boolOf(t, author["has_image"]) {
		t.Errorf("out = %v, want the surviving author with their own photo", out)
	}
	if got := f.requests("/api/authors/" + oldID + "/image"); len(got) != 1 {
		t.Errorf("image requests on the old record = %v, want one DELETE before the merge", got)
	}
	if got := f.requests("/api/authors/" + keepID + "/image"); len(got) != 0 {
		t.Errorf("the surviving author's photo was touched: %v", got)
	}
}

// A name of spaces was sent as the series' new name.
func TestSeriesEditRefusesABlankName(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	f.json("PATCH /api/series/"+seriesB, `{"id":"`+seriesB+`","name":"Stormlight"}`)
	call := toolCaller(t, f)

	if _, err := call("series_edit", map[string]any{"series": seriesB, "name": "  "}); err == nil || !strings.Contains(err.Error(), "blank") {
		t.Errorf("a blank name: %v", err)
	}
	if _, err := call("series_edit", map[string]any{"series": seriesB, "name": " Stormlight "}); err != nil {
		t.Fatal(err)
	}
	var patches []request
	for _, r := range f.requests("/api/series/" + seriesB) {
		if r.Method == http.MethodPatch {
			patches = append(patches, r)
		}
	}
	if len(patches) != 1 || !strings.Contains(patches[0].Body, `"name":"Stormlight"`) {
		t.Errorf("patches = %v, want one with the name trimmed", patches)
	}
}

// series_list's sequence column is how a gap stands out, and the server's
// series listing carries each book minified: no series list, only the joined
// "The Expanse #1" string. Read from the series list alone, every row came
// back with no numbers at all.
func TestSeriesListReadsTheNumbersFromTheMinifiedBooks(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	minified := func(id, title, series string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","media":{"metadata":{"title":"` + title + `","seriesName":"` + series + `"}}}`
	}
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"The Expanse","books":[`+
		minified("b1", "Leviathan Wakes", "The Expanse #1")+`,`+
		minified("b3", "Abaddon's Gate", "The Expanse #3, Other Saga #9")+`]}],"total":1}`)
	call := toolCaller(t, f)

	out, err := call("series_list", map[string]any{"library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["series"])
	if len(rows) != 1 {
		t.Fatalf("series = %v, want The Expanse", rows)
	}
	var seq []string
	if rows[0]["sequence"] != nil {
		seq = strs(t, rows[0]["sequence"])
	}
	if !slices.Equal(seq, []string{"1", "3"}) {
		t.Errorf("sequence = %v, want [1 3]: this series' numbers, not the other one's", seq)
	}
}

func TestAnAuthorEditThatCannotBeReadBackSaysItWasMade(t *testing.T) {
	t.Parallel()

	const authorID = "55555555-5555-4555-8555-55555555ffff"
	author := `{"id":"` + authorID + `","name":"Emily Andras","libraryItems":[]}`
	f := newFakeABS(t)
	f.failsAfter("GET /api/authors/"+authorID, 1, author)
	f.json("PATCH /api/authors/"+authorID, `{"author":`+author+`,"merged":false}`)
	call := toolCaller(t, f)

	_, err := call("author_edit", map[string]any{"author": authorID, "description": "new"})
	wantErr(t, "an edit whose read-back failed", err, "was made", "reading it back", "500")
}

// A merge the server carried out for fewer books than it was sent says so,
// rather than listing every book as moved.
func TestASeriesMergeThatMovedTooFewSaysSo(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	f.json("POST /api/items/batch/update", `{"updates":1}`) // of the two sent
	call := toolCaller(t, f)

	_, err := call("series_merge", map[string]any{"from": "The Stormlight Archive", "into": "Stormlight Archive"})
	wantErr(t, "a merge the server moved one of two books for", err, "moved 1 of the 2 books")
}

// A series whose book the full read leaves out is not listed from the
// one-series copy the listing gave, which a merge would write back.
func TestASeriesBookLeftOutOfTheFullReadIsAnError(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/series/"+seriesA, `{"id":"`+seriesA+`","name":"Stormlight Archive","libraryId":"`+libID+`"}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"`+seriesA+`","name":"Stormlight Archive","libraryId":"`+libID+`"}],"total":1}`)
	f.json("GET /api/libraries/"+libID+"/items", page(`{"id":"i3","libraryId":"`+libID+`","mediaType":"book","media":{"metadata":{"title":"Oathbringer","series":{"id":"`+seriesA+`","name":"Stormlight Archive","sequence":"3"}}}}`))
	f.json("POST /api/items/batch/get", `{"libraryItems":[]}`)
	call := toolCaller(t, f)

	_, err := call("series_get", map[string]any{"series": seriesA})
	wantErr(t, "a series book left out of the full read", err, "Oathbringer", "not in the server's reply")
}

// An author on the second page of the listing is still an author: a name
// lookup pages until the server has no more.
func TestResolveAuthorPagesPastTheFirst(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/authors", func(w http.ResponseWriter, r *http.Request) {
		var rows []string
		if r.URL.Query().Get("page") == "0" {
			for i := range authorPageSize {
				rows = append(rows, fmt.Sprintf(`{"id":"a%d","name":"Author %d"}`, i, i))
			}
		} else {
			rows = append(rows, `{"id":"a-zed","name":"Zed Last"}`)
		}
		_, _ = fmt.Fprintf(w, `{"results":[%s],"total":%d}`, strings.Join(rows, ","), authorPageSize+1)
	})
	f.json("GET /api/authors/a-zed", `{"id":"a-zed","name":"Zed Last","libraryItems":[]}`)
	call := toolCaller(t, f)

	out, err := call("author_get", map[string]any{"author": "Zed Last"})
	if err != nil {
		t.Fatal(err)
	}
	if got := str(t, out["id"]); got != "a-zed" {
		t.Errorf("resolved %q, want a-zed", got)
	}
	if got := f.requests("/api/libraries/" + libID + "/authors"); len(got) != 2 {
		t.Errorf("fetched %d author pages, want 2", len(got))
	}
}

// clear is how a wrong author_match is undone: an empty description and asin
// have to reach the server as empty strings, and the photo has its own route.
func TestAuthorEditClear(t *testing.T) {
	t.Parallel()

	const authorID = "55555555-5555-4555-8555-555555555555"
	author := `{"id":"` + authorID + `","name":"Emily Andras","asin":"B00OKO2VB8","description":"someone else","imagePath":"/a.jpg","libraryItems":[]}`
	f := newFakeABS(t)
	f.json("GET /api/authors/"+authorID, author)
	f.json("PATCH /api/authors/"+authorID, `{"author":`+author+`,"merged":false}`)
	f.json("DELETE /api/authors/"+authorID+"/image", `{"author":`+author+`}`)
	call := toolCaller(t, f)

	if _, err := call("author_edit", map[string]any{"author": authorID, "clear": []any{"name"}}); err == nil {
		t.Error("clearing a field that cannot be blank was not refused")
	}

	if _, err := call("author_edit", map[string]any{"author": authorID, "clear": []any{"asin", "description", "image"}}); err != nil {
		t.Fatal(err)
	}
	// the reply to a PATCH carries no books, so the author is read back
	patched := f.requests("/api/authors/" + authorID)
	if len(patched) != 3 || patched[1].Method != http.MethodPatch || patched[2].Method != http.MethodGet {
		t.Fatalf("author requests = %v, want a GET, a PATCH, and a GET to read it back", patched)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patched[1].Body), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["asin"]) != `""` || string(body["description"]) != `""` {
		t.Errorf("PATCH sent %s, want asin and description as empty strings", patched[1].Body)
	}
	if _, present := body["name"]; present {
		t.Errorf("the name was sent: %s", patched[1].Body)
	}
	if got := f.requests("/api/authors/" + authorID + "/image"); len(got) != 1 || got[0].Method != http.MethodDelete {
		t.Errorf("image requests = %v, want one DELETE", got)
	}

	// clearing only the image sends no PATCH at all
	if _, err := call("author_edit", map[string]any{"author": authorID, "clear": []any{"image"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/authors/" + authorID); len(got) != 5 || got[3].Method != http.MethodGet || got[4].Method != http.MethodGet {
		t.Errorf("author requests after an image-only clear = %v, want only a GET and the read back", got)
	}
}

// an author with no photo can still have their image cleared: the server
// answers 400 to a DELETE with nothing to remove, so that route is not called.
func TestAuthorEditClearImageWithoutOne(t *testing.T) {
	t.Parallel()

	const authorID = "55555555-5555-4555-8555-555555555556"
	author := `{"id":"` + authorID + `","name":"Steve Wolf","asin":"B002BMG2T8","description":"","imagePath":"","libraryItems":[]}`
	f := newFakeABS(t)
	f.json("GET /api/authors/"+authorID, author)
	f.json("PATCH /api/authors/"+authorID, `{"author":`+author+`,"merged":false}`)
	call := toolCaller(t, f)

	if _, err := call("author_edit", map[string]any{"author": authorID, "clear": []any{"asin", "description", "image"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/authors/" + authorID); len(got) != 3 || got[1].Method != http.MethodPatch {
		t.Errorf("author requests = %v, want a GET, a PATCH and the read back", got)
	}
	if got := f.requests("/api/authors/" + authorID + "/image"); len(got) != 0 {
		t.Errorf("image requests = %v, want none for an author with no photo", got)
	}
}

// author_match only looks: the provider's name lookup is tolerant, so it can
// hand back someone else, and what it found is returned for a decision, never
// applied. author_match_apply is the write, by asin.
func TestAuthorMatchLooksAndApplyWrites(t *testing.T) {
	t.Parallel()

	const authorID = "66666666-6666-4666-8666-666666666666"
	author := `{"id":"` + authorID + `","name":"Sarah Diemer","libraryItems":[]}`
	f := newFakeABS(t)
	f.json("GET /api/authors/"+authorID, author)
	f.mux.HandleFunc("GET /api/search/authors", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("q") {
		case "Sarah Diemer":
			_, _ = io.WriteString(w, `{"asin":"B002LTD1MC","name":"Sarah Miller","description":"Wrote The Borden Murders.","image":"https://img/miller.jpg"}`)
		case "S. Diemer":
			_, _ = io.WriteString(w, `{"asin":"B0DIEMER","name":"Sarah Diemer","description":"Wrote The Dark Wife.","image":""}`)
		default:
			_, _ = io.WriteString(w, `null`)
		}
	})
	f.json("POST /api/authors/"+authorID+"/match", `{"updated":true,"author":`+author+`}`)
	call := toolCaller(t, f)

	// someone else: offered, flagged, not applied
	out, err := call("author_match", map[string]any{"author": authorID})
	if err != nil {
		t.Fatal(err)
	}
	cand, ok := out["candidate"].(map[string]any)
	if !ok {
		t.Fatalf("candidate = %T, want the author Audible found", out["candidate"])
	}
	if str(t, cand["name"]) != "Sarah Miller" || str(t, cand["asin"]) != "B002LTD1MC" || !boolOf(t, cand["has_image"]) || boolOf(t, cand["name_matches"]) {
		t.Errorf("candidate = %v", cand)
	}

	// the author's own name: still only offered, with name_matches set
	out, err = call("author_match", map[string]any{"author": authorID, "query": "S. Diemer"})
	if err != nil {
		t.Fatal(err)
	}
	if cand, ok := out["candidate"].(map[string]any); !ok || !boolOf(t, cand["name_matches"]) {
		t.Errorf("own name: candidate = %v, want name_matches", out["candidate"])
	}

	// nobody close enough: no candidate, no error
	out, err = call("author_match", map[string]any{"author": authorID, "query": "Nobody Atall"})
	if err != nil {
		t.Fatal(err)
	}
	if _, offered := out["candidate"]; offered {
		t.Errorf("a lookup that found nobody offered %v", out["candidate"])
	}
	if got := f.requests("/api/authors/" + authorID + "/match"); len(got) != 0 {
		t.Fatalf("author_match wrote something: %v", got)
	}

	// applying is a separate call, by asin, and nothing else
	if _, err := call("author_match_apply", map[string]any{"author": authorID}); err == nil {
		t.Error("author_match_apply with no asin was not refused")
	}
	out, err = call("author_match_apply", map[string]any{"author": authorID, "asin": "B0DIEMER", "region": "uk"})
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(t, out["updated"]) {
		t.Errorf("apply reported no update: %v", out)
	}
	sent := f.requests("/api/authors/" + authorID + "/match")
	if len(sent) != 1 || !strings.Contains(sent[0].Body, `"asin":"B0DIEMER"`) || !strings.Contains(sent[0].Body, `"region":"uk"`) || strings.Contains(sent[0].Body, `"q"`) {
		t.Errorf("apply sent %v, want one POST by asin and region, no name query", sent)
	}
	if got := f.requests("/api/search/authors"); len(got) != 3 {
		t.Errorf("lookups = %d, want the 3 from author_match: apply needs none", len(got))
	}
}

func TestSameName(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"Ursula K. Le Guin", "ursula k le guin", true},
		{"N. K. Jemisin", "N.K. Jemisin", true},
		{"isaac asimov", "Isaac Asimov", true},
		{"Emily Andras", "Emily Adrian", false},
		{"Sarah Diemer", "Sarah Miller", false},
		{"Smedley D. Butler", "Smedley Butler", false},
	} {
		if got := sameName(c.a, c.b); got != c.same {
			t.Errorf("sameName(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// The author routes answer a write with the record alone, so the row is read
// back: a count built from the reply says the author has no books.
func TestAuthorEditCountsBooksAfterTheWrite(t *testing.T) {
	t.Parallel()

	const authorID = "55555555-5555-4555-8555-555555555557"
	f := newFakeABS(t)
	f.json("GET /api/authors/"+authorID, `{"id":"`+authorID+`","name":"Tad Williams","libraryItems":[{"id":"`+bookB1+`"},{"id":"`+bookB2+`"}]}`)
	f.json("PATCH /api/authors/"+authorID, `{"author":{"id":"`+authorID+`","name":"Tad Williams","description":"new"},"merged":false}`)
	call := toolCaller(t, f)

	out, err := call("author_edit", map[string]any{"author": authorID, "description": "new"})
	if err != nil {
		t.Fatal(err)
	}
	author, ok := out["author"].(map[string]any)
	if !ok || num(t, author["books"]) != 2 {
		t.Errorf("books = %v, want 2", author["books"])
	}
}

// A series whose books are all gone can stay listed, with none, and the
// server answers 404 to opening it: by name it is not a series to open.
func TestSeriesWithNoBooksLeftIsNotOpened(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"Gone","books":[]}],"total":1}`)
	call := toolCaller(t, f)

	if _, err := call("series_get", map[string]any{"series": "Gone"}); err == nil || !strings.Contains(err.Error(), "with a book in it") {
		t.Errorf("series_get of an emptied series: %v", err)
	}
	if got := f.requests("/api/series/s1"); len(got) != 0 {
		t.Errorf("opened the emptied series: %v", got)
	}
}

// A name stored with a space at an end is what audit_whitespace reports, and
// the tools its fix names must find it as written: not refuse it, and not
// take its tidy twin for it. A name given tidy still finds the spaced one
// when that is the only one, several matches say which records they are,
// and a rename that would change nothing says so rather than succeed.
func TestNamesStoredWithSpacesAreFoundAsWritten(t *testing.T) {
	t.Parallel()

	const (
		spaced   = "a1111111-1111-4111-8111-111111111111" // "Brandon Sanderson "
		tidy     = "a2222222-2222-4222-8222-222222222222" // "Brandon Sanderson"
		lonely   = "a3333333-3333-4333-8333-333333333333" // "Ursula Le Guin " alone
		mistSp   = "b1111111-1111-4111-8111-111111111111" // "Mistborn "
		mistTidy = "b2222222-2222-4222-8222-222222222222" // "Mistborn"
	)
	f := newFakeABS(t)
	oneLibrary(f)
	author := func(id, name string) string {
		return `{"id":"` + id + `","name":"` + name + `","libraryId":"` + libID + `","numBooks":1}`
	}
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[`+author(spaced, "Brandon Sanderson ")+`,`+author(tidy, "Brandon Sanderson")+`,`+author(lonely, "Ursula Le Guin ")+`],"total":3}`)
	for id, name := range map[string]string{spaced: "Brandon Sanderson ", tidy: "Brandon Sanderson", lonely: "Ursula Le Guin "} {
		f.json("GET /api/authors/"+id, author(id, name))
	}
	series := func(id, name string) string {
		return `{"id":"` + id + `","name":"` + name + `","libraryId":"` + libID + `","books":[{"id":"i1"}]}`
	}
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[`+series(mistSp, "Mistborn ")+`,`+series(mistTidy, "Mistborn")+`],"total":2}`)
	f.json("GET /api/series/"+mistSp, series(mistSp, "Mistborn "))
	f.json("GET /api/series/"+mistTidy, series(mistTidy, "Mistborn"))
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Michael Kramer ","numBooks":2},{"name":"Michael Kramer","numBooks":9},{"name":"Kate Reading","numBooks":4}]}`)
	f.json("PATCH /api/libraries/{lib}/narrators/{id}", `{"updated":2}`)
	f.json("PATCH /api/authors/{id}", `{"author":{"id":"`+spaced+`","name":"Brandon Sanderson"},"merged":true}`)
	call := toolCaller(t, f)

	// authors: as written, and a tidy name case aside, never a stored name
	// trimmed to answer it
	for given, want := range map[string]string{"Brandon Sanderson ": spaced, "Brandon Sanderson": tidy, "brandon sanderson": tidy, "Ursula Le Guin ": lonely} {
		out, err := call("author_get", map[string]any{"author": given})
		if err != nil || str(t, out["id"]) != want {
			t.Errorf("author_get %q = %v, %v; want %s", given, out["id"], err, want)
		}
	}
	// the only Ursula Le Guin is spelled with a space: the tidy name does not
	// reach it, as a delete by it must not, and the answer names it
	_, err := call("author_get", map[string]any{"author": "Ursula Le Guin"})
	if err == nil || !strings.Contains(err.Error(), "no author named") || !strings.Contains(err.Error(), lonely) {
		t.Errorf("the tidy name of a spaced author = %v, want not found, naming %s", err, lonely)
	}
	// a name given with a space that no record has exactly is none of them
	if _, err := call("author_get", map[string]any{"author": "Isaac Asimov "}); err == nil || !strings.Contains(err.Error(), "no author named") {
		t.Errorf("a spaced name no record has = %v, want not found", err)
	}

	// series_edit: the spaced series is the one renamed, so the merge it
	// points at goes from it into the tidy one, not the other way
	_, err = call("series_edit", map[string]any{"series": "Mistborn ", "name": "Mistborn"})
	if err == nil || !strings.Contains(err.Error(), "series_merge from="+mistSp+" into="+mistTidy) {
		t.Errorf("renaming the spaced series onto the tidy name = %v, want a merge from %s into %s", err, mistSp, mistTidy)
	}
	// a rename to the name it has writes nothing and says so: an
	// idempotent call, such as a cleanup renaming a series back, still works
	out, err := call("series_edit", map[string]any{"series": mistTidy, "name": "Mistborn"})
	if err != nil || !isTrue(out["unchanged"]) {
		t.Errorf("renaming a series to its own name = %v, %v; want unchanged", out, err)
	}

	// metadata_rename: the narrator as written is the one renamed
	out, err = call("metadata_rename", map[string]any{"field": "narrators", "from": "Michael Kramer ", "to": "Michael Kramer"})
	if err != nil || num(t, out["items_updated"]) != 2 {
		t.Fatalf("renaming the spaced narrator = %v, %v", out, err)
	}
	wantPath := "/api/libraries/" + libID + "/narrators/" + base64.StdEncoding.EncodeToString([]byte("Michael Kramer "))
	if sent := f.requests(wantPath); len(sent) != 1 || sent[0].Method != http.MethodPatch {
		t.Errorf("the rename went to %v, want the spaced name's %s", f.requests("/narrators/"), wantPath)
	}
	if _, err := call("metadata_rename", map[string]any{"field": "narrators", "from": "Nobody", "to": "Somebody"}); err == nil || !strings.Contains(err.Error(), `no narrator named "Nobody"`) {
		t.Errorf("renaming a narrator no library has = %v, want no narrator named", err)
	}
	for _, args := range []map[string]any{
		{"field": "narrators", "from": "Michael Kramer", "to": "Michael Kramer"},
		{"field": "authors", "from": "Brandon Sanderson", "to": "Brandon Sanderson"},
	} {
		before := len(f.seen)
		got, cerr := call("metadata_rename", args)
		if cerr != nil || !isTrue(got["unchanged"]) || num(t, got["items_updated"]) != 0 {
			t.Errorf("%v = %v, %v; want unchanged, nothing updated", args, got, cerr)
		}
		for _, r := range f.seen[before:] {
			if r.Method != http.MethodGet {
				t.Errorf("%v wrote %s %s, want nothing written", args, r.Method, r.Path)
			}
		}
	}
	// the authors rename found the spaced record as written
	if _, err := call("metadata_rename", map[string]any{"field": "authors", "from": "Brandon Sanderson ", "to": "Brandon Sanderson"}); err != nil {
		t.Errorf("renaming the spaced author onto the tidy one: %v", err)
	}
	if sent := f.requests("/api/authors/" + spaced); !slices.ContainsFunc(sent, func(r request) bool { return r.Method == http.MethodPatch }) {
		t.Errorf("the spaced author was not the one renamed: %v", f.requests("/api/authors/"))
	}
	// author_edit: a rename to the name it has writes nothing and says so
	if edited, eerr := call("author_edit", map[string]any{"author": tidy, "name": "Brandon Sanderson"}); eerr != nil || !isTrue(edited["unchanged"]) {
		t.Errorf("author_edit to its own name = %v, %v; want unchanged", edited, eerr)
	}
	// the remove preview counts the name as written
	f.json("GET /api/libraries/"+libID+"/items", `{"results":[],"total":0}`)
	out, err = call("metadata_rename", map[string]any{"field": "narrators", "from": "Michael Kramer ", "remove": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["would_remove"].(map[string]any); !ok {
		t.Fatalf("remove without confirm = %v, want a preview", out)
	}
	filter := "narrators." + base64.StdEncoding.EncodeToString([]byte("Michael Kramer "))
	if !slices.ContainsFunc(f.requests("/api/libraries/"+libID+"/items"), func(r request) bool { return parseQuery(t, r.Query).Get("filter") == filter }) {
		t.Errorf("the preview did not ask for the name as written, %s", filter)
	}
}

// A name that only an empty series has exactly is not taken for the series a
// trim or a fold would reach: after "Mistborn " is merged into "Mistborn",
// a rename of "Mistborn " must not land on the tidy series, nor a lookup of
// "Mistborn" on the spaced one when the tidy one is emptied. The lookup is
// refused, naming both, and either id resolves.
func TestSeriesLookupRefusesAnEmptyExactName(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"a1111111-1111-4111-8111-111111111111","name":"Mistborn","libraryId":"`+libID+`","books":[]},{"id":"b2222222-2222-4222-8222-222222222222","name":"Mistborn ","libraryId":"`+libID+`","books":[{"id":"i1"}]}],"total":2}`)
	f.json("GET /api/series/b2222222-2222-4222-8222-222222222222", `{"id":"b2222222-2222-4222-8222-222222222222","name":"Mistborn ","libraryId":"`+libID+`"}`)
	client := f.client(t)
	s, err := resolveSeries(t.Context(), client, "", "Mistborn")
	if err == nil || !strings.Contains(err.Error(), "b2222222-2222-4222-8222-222222222222") {
		t.Errorf("resolving Mistborn = %+v, %v; want refused, naming the spaced series that has the books", s, err)
	}
	if s, err := resolveSeries(t.Context(), client, "", "Mistborn "); err != nil || s.ID != "b2222222-2222-4222-8222-222222222222" {
		t.Errorf("resolving the spaced name = %+v, %v; want the spaced series", s, err)
	}
	if s, err := resolveSeries(t.Context(), client, "", "b2222222-2222-4222-8222-222222222222"); err != nil || s.ID != "b2222222-2222-4222-8222-222222222222" {
		t.Errorf("resolving by id = %+v, %v; want the spaced series", s, err)
	}
}

// A delete by the tidy name does not reach the only record, which is spelled
// with a space: the tool answers that none is named so, naming the one that
// differs, and deletes nothing.
func TestAuthorDeleteByTheTidyNameDeletesNothing(t *testing.T) {
	t.Parallel()

	const spacedID = "a1111111-1111-4111-8111-111111111111"
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"`+spacedID+`","name":"Brandon Sanderson ","libraryId":"`+libID+`","numBooks":5}],"total":1}`)
	_, err := toolCaller(t, f)("author_delete", map[string]any{"author": "Brandon Sanderson"})
	if err == nil || !strings.Contains(err.Error(), spacedID) {
		t.Errorf("author_delete by the tidy name = %v, want refused, naming the spaced record", err)
	}
	for _, r := range f.seen {
		if r.Method == http.MethodDelete {
			t.Errorf("deleted %s", r.Path)
		}
	}
}

// A library that hides one-book series lists none of them, so a lookup by
// name says so, and to pass the id.
func TestSeriesLookupSaysTheLibraryHidesOneBookSeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","settings":{"hideSingleBookSeries":true}}]}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	_, err := toolCaller(t, f)("series_get", map[string]any{"series": "Mistborn "})
	if err == nil || !strings.Contains(err.Error(), "hides one-book series") {
		t.Errorf("series_get of a hidden series = %v, want the hint to pass its id", err)
	}
}

// A rename of a spaced series to its own spaced name asks for nothing.
func TestSeriesEditToItsOwnSpacedName(t *testing.T) {
	t.Parallel()

	const id = "b1111111-1111-4111-8111-111111111111"
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/series/"+id, `{"id":"`+id+`","name":"Dune ","libraryId":"`+libID+`"}`)
	out, err := toolCaller(t, f)("series_edit", map[string]any{"series": id, "name": "Dune "})
	if err != nil || !isTrue(out["unchanged"]) {
		t.Errorf("series_edit to its own spaced name = %v, %v; want unchanged", out, err)
	}
	for _, r := range f.seen {
		if r.Method != http.MethodGet {
			t.Errorf("wrote %s %s", r.Method, r.Path)
		}
	}
}

// A tidy name that two records answer, one exactly and one case aside, is
// refused, naming both, as before: neither is picked for a write.
func TestExactCaseDoesNotWinOverACaseVariant(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+otherLibID+`","name":"B","mediaType":"book"}]}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1111111-1111-4111-8111-111111111111","name":"Brandon Sanderson","libraryId":"`+libID+`","numBooks":3}],"total":1}`)
	f.json("GET /api/libraries/"+otherLibID+"/authors", `{"results":[{"id":"b1111111-1111-4111-8111-111111111111","name":"brandon sanderson","libraryId":"`+otherLibID+`","numBooks":1}],"total":1}`)
	_, err := toolCaller(t, f)("author_delete", map[string]any{"author": "Brandon Sanderson"})
	if err == nil || !strings.Contains(err.Error(), "a1111111") || !strings.Contains(err.Error(), "b1111111") {
		t.Errorf("author_delete of a name two records answer = %v, want refused, naming both", err)
	}
	for _, r := range f.seen {
		if r.Method == http.MethodDelete {
			t.Errorf("deleted %s", r.Path)
		}
	}
}

// A description set beside the name a series already has, spaces and all,
// leaves the name as it is.
func TestSeriesEditDescriptionKeepsASpacedName(t *testing.T) {
	t.Parallel()

	const id = "b1111111-1111-4111-8111-111111111111"
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/series/"+id, `{"id":"`+id+`","name":"Mistborn ","libraryId":"`+libID+`"}`)
	f.json("PATCH /api/series/"+id, `{"id":"`+id+`","name":"Mistborn ","libraryId":"`+libID+`"}`)
	if _, err := toolCaller(t, f)("series_edit", map[string]any{"series": id, "name": "Mistborn ", "description": "d"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.seen {
		if r.Method == http.MethodPatch && strings.Contains(r.Body, `"name"`) {
			t.Errorf("sent %s, want the description alone", r.Body)
		}
	}
}

const (
	seriesA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" // Stormlight Archive
	seriesB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" // The Stormlight Archive
)

// seriesRoutes is a canned library where the item listing filtered by series
// collapses each book's series to the one matched, as the server does, and
// the batch fetch has the whole list.
func seriesRoutes(f *fakeABS) {
	oneLibrary(f)
	f.json("GET /api/series/"+seriesA, `{"id":"`+seriesA+`","name":"Stormlight Archive","libraryId":"`+libID+`"}`)
	f.json("GET /api/series/"+seriesB, `{"id":"`+seriesB+`","name":"The Stormlight Archive","libraryId":"`+libID+`"}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"`+seriesA+`","name":"Stormlight Archive","libraryId":"`+libID+`"},{"id":"`+seriesB+`","name":"The Stormlight Archive","libraryId":"`+libID+`"}],"total":2}`)
	// the server's filter data is cached and keeps names from before a
	// rename; nothing may resolve a series through it
	f.json("GET /api/libraries/"+libID+"/filterdata", `{"series":[{"id":"`+seriesA+`","name":"Stale Name"},{"id":"`+seriesB+`","name":"Older Name"}]}`)
	collapsed := func(id, title, seriesID, name, seq string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"A/` + title + `","media":{"metadata":{"title":"` + title + `","series":{"id":"` + seriesID + `","name":"` + name + `","sequence":"` + seq + `"}}}}`
	}
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/items", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("filter") {
		case abs.EncodeFilter("series", seriesA):
			_, _ = io.WriteString(w, page(collapsed("i3", "Oathbringer", seriesA, "Stormlight Archive", "3")))
		case abs.EncodeFilter("series", seriesB):
			_, _ = io.WriteString(w, page(
				collapsed("i2", "Words of Radiance", seriesB, "The Stormlight Archive", "2"),
				collapsed("i4", "Rhythm of War", seriesB, "The Stormlight Archive", "4"),
			))
		default:
			_, _ = io.WriteString(w, page())
		}
	})
	full := func(id, title, series string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"A/` + title + `","media":{"metadata":{"title":"` + title + `","series":[` + series + `]}}}`
	}
	ref := func(id, name, seq string) string {
		return `{"id":"` + id + `","name":"` + name + `","sequence":"` + seq + `"}`
	}
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"libraryItemIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		items := map[string]string{
			"i2": full("i2", "Words of Radiance", ref(seriesB, "The Stormlight Archive", "2")+","+ref("c", "Cosmere", "")),
			"i3": full("i3", "Oathbringer", ref(seriesA, "Stormlight Archive", "3")),
			"i4": full("i4", "Rhythm of War", ref(seriesB, "The Stormlight Archive", "4")+","+ref("c", "Cosmere", "")+","+ref(seriesA, "Stormlight Archive", "")),
		}
		out := make([]string, 0, len(body.IDs))
		for _, id := range body.IDs {
			out = append(out, items[id])
		}
		_, _ = io.WriteString(w, `{"libraryItems":[`+strings.Join(out, ",")+`]}`)
	})
	f.json("POST /api/items/batch/update", `{"updates":2}`)
}

func TestSeriesGetShowsEverySeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	call := toolCaller(t, f)

	out, err := call("series_get", map[string]any{"series": seriesB})
	if err != nil {
		t.Fatal(err)
	}
	books := list(t, out["books"])
	if len(books) != 2 {
		t.Fatalf("books = %v", out["books"])
	}
	if got := books[0]["series"]; !slices.Equal(strs(t, got), []string{"The Stormlight Archive #2", "Cosmere"}) || str(t, books[0]["sequence"]) != "2" {
		t.Errorf("Words of Radiance = %v #%v, want both series and sequence 2", got, books[0]["sequence"])
	}
}

func TestSeriesMergeKeepsNumbersAndOtherSeries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	call := toolCaller(t, f)

	out, err := call("series_merge", map[string]any{"from": "The Stormlight Archive", "into": "Stormlight Archive"})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["moved"]) != 2 || str(t, out["into_id"]) != seriesA {
		t.Errorf("out = %v", out)
	}
	if got := strs(t, out["books"]); !slices.Equal(got, []string{"Words of Radiance #2", "Rhythm of War #4"}) {
		t.Errorf("books = %v", got)
	}

	sent := f.requests("/api/items/batch/update")
	if len(sent) != 1 {
		t.Fatalf("batch update sent %d times", len(sent))
	}
	var updates []struct {
		ID      string `json:"id"`
		Payload struct {
			Metadata struct {
				Series []abs.SeriesRef `json:"series"`
			} `json:"metadata"`
		} `json:"mediaPayload"`
	}
	if err := json.Unmarshal([]byte(sent[0].Body), &updates); err != nil {
		t.Fatal(err, sent[0].Body)
	}
	got := map[string][]abs.SeriesRef{}
	for _, u := range updates {
		got[u.ID] = u.Payload.Metadata.Series
	}
	// Words of Radiance: the old entry becomes the target in its place with its number, Cosmere stays
	if want := []abs.SeriesRef{{Name: "Stormlight Archive", Sequence: "2"}, {ID: "c", Name: "Cosmere"}}; !sameRefs(got["i2"], want) {
		t.Errorf("i2 series = %v, want %v", got["i2"], want)
	}
	// Rhythm of War was already in the target without a number: it takes the one it had
	if want := []abs.SeriesRef{{ID: "c", Name: "Cosmere"}, {ID: seriesA, Name: "Stormlight Archive", Sequence: "4"}}; !sameRefs(got["i4"], want) {
		t.Errorf("i4 series = %v, want %v", got["i4"], want)
	}

	if _, err := call("series_merge", map[string]any{"from": seriesA, "into": seriesA}); err == nil || !strings.Contains(err.Error(), "one series") {
		t.Errorf("merging a series into itself: %v", err)
	}
}

// A rename onto a name that already exists would leave two series with one
// name, not one series; the tool says which one and points at series_merge.
func TestSeriesEditRefusesAnExistingName(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	f.json("PATCH /api/series/"+seriesB, `{"id":"`+seriesB+`","name":"Stormlight"}`)
	call := toolCaller(t, f)

	_, err := call("series_edit", map[string]any{"series": seriesB, "name": "stormlight archive"})
	if err == nil || !strings.Contains(err.Error(), "series_merge") {
		t.Fatalf("renaming onto an existing name: %v", err)
	}
	if got := f.requests("/api/series/" + seriesB); slices.ContainsFunc(got, func(r request) bool { return r.Method == http.MethodPatch }) {
		t.Error("the rename was sent anyway")
	}

	out, err := call("series_edit", map[string]any{"series": seriesB, "name": "Stormlight"})
	if err != nil || str(t, out["name"]) != "Stormlight" {
		t.Errorf("a fresh name: %v %v", out, err)
	}
}

func TestSeriesListDefaultsToEveryBookLibrary(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"},{"id":"`+otherLibID+`","name":"More","mediaType":"book"},{"id":"p","name":"Pods","mediaType":"podcast"}]}`)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"Foundation","books":[]}],"total":1}`)
	f.json("GET /api/libraries/"+otherLibID+"/series", `{"results":[{"id":"s2","name":"Culture","books":[]}],"total":1}`)
	call := toolCaller(t, f)

	out, err := call("series_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["series"])
	if num(t, out["total"]) != 2 || len(rows) != 2 || str(t, rows[0]["library"]) != "Books" || str(t, rows[1]["library"]) != "More" {
		t.Errorf("series_list = %v, want both book libraries, each row saying which", out)
	}
	if got := f.requests("/api/libraries/p/series"); len(got) != 0 {
		t.Error("the podcast library was asked for series")
	}

	one, err := call("series_list", map[string]any{"library": "More"})
	if err != nil || num(t, one["total"]) != 1 {
		t.Errorf("one library: %v %v", one, err)
	}
	if rows := list(t, one["series"]); len(rows) != 1 || rows[0]["library"] != nil {
		t.Errorf("one library names it on the rows: %v", rows)
	}
}

func TestMergeSeriesRefs(t *testing.T) {
	t.Parallel()

	from := &abs.Series{ID: "f", Name: "The Wheel of Time"}
	into := &abs.Series{ID: "i", Name: "Wheel of Time"}
	for _, tc := range []struct {
		name string
		have []abs.SeriesRef
		want []abs.SeriesRef
	}{
		{"moves the number", []abs.SeriesRef{{ID: "f", Name: "The Wheel of Time", Sequence: "5"}}, []abs.SeriesRef{{Name: "Wheel of Time", Sequence: "5"}}},
		{"keeps the rest", []abs.SeriesRef{{Name: "Cosmere"}, {Name: "the wheel of time", Sequence: "5"}}, []abs.SeriesRef{{Name: "Cosmere"}, {Name: "Wheel of Time", Sequence: "5"}}},
		{"already in both, target numbered", []abs.SeriesRef{{ID: "f", Name: "The Wheel of Time", Sequence: "5"}, {ID: "i", Name: "Wheel of Time", Sequence: "6"}}, []abs.SeriesRef{{ID: "i", Name: "Wheel of Time", Sequence: "6"}}},
		{"already in both, target unnumbered", []abs.SeriesRef{{ID: "i", Name: "Wheel of Time"}, {ID: "f", Name: "The Wheel of Time", Sequence: "5"}}, []abs.SeriesRef{{ID: "i", Name: "Wheel of Time", Sequence: "5"}}},
	} {
		if got := mergeSeriesRefs(tc.have, from, into); !sameRefs(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	// two records whose names differ only by a space: each entry goes by its
	// id, so the book keeps the tidy series once, with its own number
	spaced, tidy := &abs.Series{ID: "sp", Name: "Mistborn "}, &abs.Series{ID: "ti", Name: "Mistborn"}
	have := []abs.SeriesRef{{ID: "sp", Name: "Mistborn ", Sequence: "1"}, {ID: "ti", Name: "Mistborn", Sequence: "3"}}
	if got, want := mergeSeriesRefs(have, spaced, tidy), []abs.SeriesRef{{ID: "ti", Name: "Mistborn", Sequence: "3"}}; !sameRefs(got, want) {
		t.Errorf("merging the spaced series into the tidy one: got %v, want %v", got, want)
	}
	// an entry whose name is out of date still goes by its id
	have = []abs.SeriesRef{{ID: "sp", Name: "Mistborn Era One", Sequence: "1"}}
	if got, want := mergeSeriesRefs(have, spaced, tidy), []abs.SeriesRef{{Name: "Mistborn", Sequence: "1"}}; !sameRefs(got, want) {
		t.Errorf("an entry with the spaced series' id under an old name: got %v, want %v", got, want)
	}
	// with no ids, a name as written picks its series before a name folded
	have = []abs.SeriesRef{{Name: "Mistborn ", Sequence: "1"}}
	if got, want := mergeSeriesRefs(have, spaced, tidy), []abs.SeriesRef{{Name: "Mistborn", Sequence: "1"}}; !sameRefs(got, want) {
		t.Errorf("an entry with no id named as the spaced series: got %v, want %v", got, want)
	}
}

// A series renamed a moment ago is still listed under its old name in the
// server's filter data for up to half an hour. Lookups by name read the live
// series route, so the new name is found and the old one is not.
func TestSeriesResolvesByTheNameItHasNow(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	seriesRoutes(f)
	call := toolCaller(t, f)

	out, err := call("series_get", map[string]any{"series": "The Stormlight Archive"})
	if err != nil {
		t.Fatalf("by the current name: %v", err)
	}
	if str(t, out["id"]) != seriesB {
		t.Errorf("id = %v, want %s", out["id"], seriesB)
	}
	if _, err := call("series_get", map[string]any{"series": "Older Name"}); err == nil {
		t.Error("a name only the stale filter data has was resolved")
	}
	if _, err := call("library_items", map[string]any{"filter": "series:The Stormlight Archive"}); err != nil {
		t.Errorf("a filter by the current name: %v", err)
	}
	if got := f.requests("/api/libraries/" + libID + "/filterdata"); len(got) != 0 {
		t.Errorf("filter data was read %d times, want never", len(got))
	}
}

func TestSeriesListInSeconds(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"Dune","books":[],"totalDuration":75600.4}],"total":1}`)
	call := toolCaller(t, f)

	out, err := call("series_list", map[string]any{"library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "series_list", out, map[string]float64{"series.0.duration_s": 75600})
	wantAbsent(t, "series_list", out, "series.0.duration")
}

// author_edit image_url sets the photo, after any rename, on the record the
// rename leaves; beside clear image it is refused.
func TestAuthorEditSetsThePhoto(t *testing.T) {
	t.Parallel()

	const authorID = "77777777-7777-4777-8777-777777777781"
	plain := `{"id":"` + authorID + `","name":"Frank Herbert","libraryItems":[]}`
	renamed := `{"id":"` + authorID + `","name":"Frank P. Herbert","libraryItems":[]}`
	photo := `{"id":"` + authorID + `","name":"Frank P. Herbert","imagePath":"/metadata/authors/a.jpg","libraryItems":[]}`
	f := newFakeABS(t)
	f.inTurn("GET /api/authors/"+authorID, plain, photo)
	f.json("PATCH /api/authors/"+authorID, `{"author":`+renamed+`,"merged":false}`)
	f.json("POST /api/authors/"+authorID+"/image", `{"author":`+photo+`}`)
	call := toolCaller(t, f)

	_, err := call("author_edit", map[string]any{"author": authorID, "image_url": "https://img.test/h.jpg", "clear": []any{"image"}})
	wantErr(t, "a photo set and cleared", err, "one or the other")
	if got := f.changes(); len(got) != 0 {
		t.Fatalf("a refused edit sent %v", got)
	}

	out, err := call("author_edit", map[string]any{"author": authorID, "name": "Frank P. Herbert", "image_url": "https://img.test/h.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	author, ok := out["author"].(map[string]any)
	if !ok || str(t, author["name"]) != "Frank P. Herbert" || !boolOf(t, author["has_image"]) {
		t.Errorf("answer = %v, want the renamed author with a photo", out)
	}
	sent := f.changes()
	if len(sent) != 2 || sent[0].Method != http.MethodPatch || sent[1].Path != "/api/authors/"+authorID+"/image" || !strings.Contains(sent[1].Body, "https://img.test/h.jpg") {
		t.Errorf("sent %v, want the rename and then the photo", sent)
	}
}

// narrator_list answers a page by name, whatever order the server lists in.
func TestNarratorListPagesByName(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/me", `{"id":"u1","username":"kt","type":"root","permissions":{"accessAllTags":true,"accessExplicitContent":true}}`)
	f.json("GET /api/libraries/"+libID+"/narrators", `{"narrators":[{"name":"Scott Brick","numBooks":3},{"name":"Jim Dale","numBooks":7},{"name":"Kate Reading","numBooks":2}]}`)
	call := toolCaller(t, f)

	out, err := call("narrator_list", map[string]any{"limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["total"]) != 3 || num(t, out["next_offset"]) != 2 || !slices.Equal(column(t, "name", out["narrators"]), []string{"Jim Dale", "Kate Reading"}) {
		t.Errorf("page = %v, want the first two by name of 3 and the next at 2", out)
	}
	if out, err = call("narrator_list", map[string]any{"limit": 2, "offset": 2}); err != nil || out["next_offset"] != nil || !slices.Equal(column(t, "name", out["narrators"]), []string{"Scott Brick"}) {
		t.Errorf("the last page = %v, %v", out, err)
	}
}
