package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
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

func namesOf(t *testing.T, v any) []string {
	t.Helper()

	rows := list(t, v)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, str(t, row["name"]))
	}
	return out
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
		if got := namesOf(t, out[tc.rows]); !slices.Equal(got, []string{"Name 003", "Name 004", "Name 005", "Name 006", "Name 007"}) {
			t.Errorf("%s limit 5 offset 3 = %v", tc.tool, got)
		}
		if num(t, out["offset"]) != 3 || num(t, out["next_offset"]) != 8 {
			t.Errorf("%s: offset %v next_offset %v, want 3 and 8", tc.tool, out["offset"], out["next_offset"])
		}

		out, err = call(tc.tool, map[string]any{"limit": 5, "offset": 10})
		if err != nil {
			t.Fatal(err)
		}
		if got := namesOf(t, out[tc.rows]); !slices.Equal(got, []string{"Name 010", "Name 011"}) || out["next_offset"] != nil {
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
	if num(t, out["moved"]) != n || len(anyStrings(out["books"])) != n {
		t.Errorf("moved %v, %d books listed, want all %d", out["moved"], len(anyStrings(out["books"])), n)
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
