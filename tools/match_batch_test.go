package tools

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// The comparator on the cases that motivated it: the Adams editions that
// match the book and not the recording, a subtitle on one side only, a
// narrator marked in the folder, and a candidate that is another book.
func TestScoreMatch(t *testing.T) {
	t.Parallel()

	book := func(title, author, narrator, relPath string, seconds float64) *abs.Item {
		it := &abs.Item{RelPath: relPath}
		it.Media.Metadata.Title, it.Media.Metadata.AuthorName, it.Media.Metadata.NarratorName = title, author, narrator
		it.Media.Duration = seconds
		return it
	}
	hit := func(title, author, narrator string, minutes float64) *abs.BookSearchResult {
		return &abs.BookSearchResult{Title: title, Author: author, Narrator: narrator, Duration: minutes}
	}
	for _, tc := range []struct {
		name string
		item *abs.Item
		hit  *abs.BookSearchResult
		want string
	}{
		{"same recording", book("Killingly", "Katharine Beutner", "Rachel Botchan", "", 12*3600+23*60), hit("Killingly", "Katharine Beutner", "Rachel Botchan", 12*60+23), confExact},
		{"same book, shorter recording", book("The Hitchhiker's Guide To The Galaxy", "Douglas Adams", "", "", 4*3600+56*60), hit("The Hitchhiker's Guide to the Galaxy", "Douglas Adams", "Stephen Fry", 5*60+51), confEdition},
		{"same length, other narrator in the folder", book("The Bat", "Jo Nesbø", "", "Jo Nesbø/Harry Hole - 01 - The Bat [John Lee]", 9*3600+39*60), hit("The Bat", "Jo Nesbo", "Robin Sachs", 9*60+39), confEdition},
		{"folder narrator agrees", book("The Bat", "Jo Nesbø", "", "Jo Nesbø/Harry Hole - 01 - The Bat [John Lee]", 9*3600+39*60), hit("The Bat", "Jo Nesbo", "John Lee", 9*60+39), confExact},
		{"subtitle on one side", book("Reckoners #2: Firefight", "Brandon Sanderson", "", "", 11*3600+37*60), hit("Firefight", "Brandon Sanderson", "MacLeod Andrews", 11*60+37), confLikely},
		{"unabridged tag", book("System Collapse (Unabridged)", "Martha Wells", "", "", 6*3600+36*60), hit("System Collapse", "Martha Wells", "Kevin R. Free", 6*60+36), confExact},
		{"duration unknown", book("Dune", "Frank Herbert", "", "", 0), hit("Dune", "Frank Herbert", "Scott Brick", 21*60), confLikely},
		{"other book", book("Dune", "Frank Herbert", "", "", 21*3600), hit("Dune Messiah", "Frank Herbert", "Scott Brick", 21*60), confUnsure},
		{"other author", book("Dune", "Frank Herbert", "", "", 21*3600), hit("Dune", "Someone Else", "", 21*60), confUnsure},
		{"initials in the author", book("Dune", "Frank Herbert", "", "", 21*3600), hit("Dune", "F. Herbert", "", 21*60), confExact},
	} {
		if got := scoreMatch(tc.item, tc.hit, 0); got.Confidence != tc.want {
			t.Errorf("%s: confidence = %s (%s), want %s", tc.name, got.Confidence, got.Reason, tc.want)
		}
	}

	// ranking puts the recording before the book
	it := book("The Hitchhiker's Guide To The Galaxy", "Douglas Adams", "", "", 4*3600+56*60)
	ranked := rankCandidates(it, []abs.BookSearchResult{
		*hit("The Hitchhiker's Guide to the Galaxy", "Douglas Adams", "Stephen Fry", 5*60+51),
		*hit("So Long, and Thanks for All the Fish", "Douglas Adams", "Martin Freeman", 4*60+39),
		*hit("The Hitchhiker's Guide to the Galaxy", "Douglas Adams", "Stephen Moore", 4*60+56),
	}, 0)
	if ranked[0].Result.Narrator != "Stephen Moore" || ranked[0].Score.Confidence != confExact || ranked[1].Score.Confidence != confEdition || ranked[2].Score.Confidence != confUnsure {
		t.Errorf("ranking = %v", []string{ranked[0].Score.Confidence, ranked[1].Score.Confidence, ranked[2].Score.Confidence})
	}

	for _, tc := range []struct{ in, want string }{
		{"The Hitchhiker's Guide To The Galaxy", "hitchhiker's guide to the galaxy"},
		{"System Collapse (Unabridged)", "system collapse"},
		{"Killingly: A Novel", "killingly a novel"},
	} {
		if got := cleanTitle(tc.in); got != strings.TrimPrefix(norm(tc.want), "the ") {
			t.Errorf("cleanTitle(%q) = %q", tc.in, got)
		}
	}
	if got := titleForms("Reckoners #2: Firefight"); !slices.Contains(got, "firefight") {
		t.Errorf("titleForms = %v, want the part after the colon", got)
	}
}

// A page of unmatched books goes to the provider once each and comes back as
// one scored row per book; nothing is written.
func TestItemMatchBatch(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("11111111-1111-4111-8111-000000000001", "Killingly", `"authorName":"Katharine Beutner"`, `"duration":44580`),
		item("11111111-1111-4111-8111-000000000002", "Something Else Entirely", `"authorName":"Nobody"`, `"duration":3600`),
	))
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("provider") == "audible.ca" { // the store the books came from has the recording
			_, _ = w.Write([]byte(`[
				{"title":"Killingly","author":"Katharine Beutner","narrator":"Rachel Botchan","asin":"B0BZGB56RL","duration":743,"series":[{"series":"None","sequence":""}]},
				{"title":"Killingly","author":"Katharine Beutner","narrator":"Someone","asin":"B0OTHER","duration":600}
			]`))
			return
		}
		_, _ = w.Write([]byte(`[{"title":"Killingly","author":"Katharine Beutner","narrator":"Someone","asin":"B0US","duration":600}]`))
	})
	call := toolCaller(t, f)

	out, err := call("item_match_batch", map[string]any{"candidates": 2, "providers": []any{"audible", "audible.ca"}})
	if err != nil {
		t.Fatal(err)
	}
	if str(t, out["filter"]) != "missing:asin" || num(t, out["total"]) != 2 {
		t.Errorf("filter/total = %v/%v", out["filter"], out["total"])
	}
	rows := list(t, out["rows"])
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if str(t, rows[0]["confidence"]) != confExact || str(t, rows[0]["provider"]) != "audible.ca" {
		t.Errorf("Killingly = %v, want exact from the second provider", rows[0])
	}
	best, ok := rows[0]["best"].(map[string]any)
	if !ok || str(t, best["asin"]) != "B0BZGB56RL" {
		t.Errorf("best = %v, want the recording of the right length first", best)
	}
	if others := list(t, rows[0]["others"]); len(others) != 1 || str(t, others[0]["confidence"]) != confEdition {
		t.Errorf("others = %v, want the shorter recording as an edition", rows[0]["others"])
	}
	if str(t, rows[1]["confidence"]) != confUnsure {
		t.Errorf("other book = %v, want unsure", rows[1])
	}
	counts, ok := out["counts"].(map[string]any)
	if !ok || num(t, counts["exact"]) != 1 || num(t, counts["unsure"]) != 1 {
		t.Errorf("counts = %v", counts)
	}
	if _, next := out["next_offset"]; next || num(t, out["offset"]) != 0 {
		t.Errorf("offset %v, next_offset %v on the only window", out["offset"], out["next_offset"])
	}
	// Killingly: the US store first (an edition), then the Canadian one (exact,
	// stop); the other book: both stores, nothing fits at either
	if got := f.requests("/api/search/books"); len(got) != 4 {
		t.Errorf("%d provider searches, want two per book", len(got))
	}
	if got := f.requests("/api/items/11111111-1111-4111-8111-000000000001/match"); len(got) != 0 {
		t.Error("the batch applied something")
	}
	if got := f.requests("/api/libraries/" + libID + "/items"); len(got) != 1 || !strings.Contains(got[0].Query, "filter=") {
		t.Errorf("listing = %v, want one filtered request", got)
	}

	if _, err := call("item_match_apply_batch", nil); err == nil {
		t.Error("an empty apply was not refused")
	}
	f.json("GET /api/items/11111111-1111-4111-8111-000000000001", item("11111111-1111-4111-8111-000000000001", "Killingly", `"authorName":"Katharine Beutner"`, ""))
	f.json("POST /api/items/11111111-1111-4111-8111-000000000001/match", `{"updated":true,"libraryItem":`+item("11111111-1111-4111-8111-000000000001", "Killingly", `"asin":"B0BZGB56RL"`, "")+`}`)
	f.json("GET /api/items/11111111-1111-4111-8111-000000000002", item("11111111-1111-4111-8111-000000000002", "Something Else Entirely", "", ""))
	f.json("POST /api/items/11111111-1111-4111-8111-000000000002/match", `{"updated":false,"libraryItem":`+item("11111111-1111-4111-8111-000000000002", "Something Else Entirely", `"asin":"OLD"`, "")+`}`)
	out, err = call("item_match_apply_batch", map[string]any{"matches": []any{
		map[string]any{"item": "11111111-1111-4111-8111-000000000001", "asin": "B0BZGB56RL", "provider": "audible.ca"},
		map[string]any{"item": "11111111-1111-4111-8111-000000000002", "asin": "B0X"},
		map[string]any{"item": "11111111-1111-4111-8111-000000000003"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["applied"]) != 1 || num(t, out["unchanged"]) != 1 || num(t, out["failed"]) != 1 {
		t.Errorf("applied/unchanged/failed = %v/%v/%v, want the match that changed its book, the one that did not, and the row with no asin", out["applied"], out["unchanged"], out["failed"])
	}
	results := list(t, out["results"])
	if got, ok := results[0]["updated"].(bool); !ok || !got {
		t.Errorf("i1 = %v, want updated", results[0])
	}
	if w := str(t, results[1]["warning"]); !strings.Contains(w, "OLD") {
		t.Errorf("i2 warning = %q, want the asin it kept", w)
	}
	if e := str(t, results[2]["error"]); !strings.Contains(e, "no asin") {
		t.Errorf("i3 error = %q", e)
	}
	matches := f.requests("/api/items/11111111-1111-4111-8111-000000000001/match")
	if len(matches) != 1 || matches[0].Method != http.MethodPost || !strings.Contains(matches[0].Body, "B0BZGB56RL") || !strings.Contains(matches[0].Body, "audible.ca") {
		t.Errorf("i1 match = %v, want the asin and the provider it came from", matches)
	}
}

// audit_matched looks each asin up and reports the books whose recording the
// asin does not describe.
// audibleLibrary is oneLibrary on an Audible store, where the library's own
// provider can look an asin up.
func audibleLibrary(f *fakeABS) {
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"}]}`)
}

func TestAuditMatched(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Killingly", `"authorName":"Katharine Beutner","asin":"B0BZGB56RL"`, `"duration":44580`),
		item("i2", "The Hitchhiker's Guide To The Galaxy", `"authorName":"Douglas Adams","asin":"B002VA9SWS"`, `"duration":17760`),
		item("i3", "Unmatched", `"authorName":"Nobody"`, `"duration":100`),
		item("i4", "Horus Rising", `"authorName":"Dan Abnett","asin":"B0UK"`, `"duration":45300`),
	))
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("title") == "B0UK" && r.URL.Query().Get("provider") != "audible.uk" {
			_, _ = w.Write([]byte(`[]`)) // a UK asin the US store has never heard of
			return
		}
		switch r.URL.Query().Get("title") {
		case "B0UK":
			_, _ = w.Write([]byte(`[{"title":"Horus Rising","author":"Dan Abnett","narrator":"Toby Longworth","asin":"B0UK","duration":755}]`))
		case "B0BZGB56RL":
			_, _ = w.Write([]byte(`[{"title":"Killingly","author":"Katharine Beutner","narrator":"Rachel Botchan","asin":"B0BZGB56RL","duration":743}]`))
		case "B002VA9SWS":
			_, _ = w.Write([]byte(`[{"title":"The Hitchhiker's Guide to the Galaxy","author":"Douglas Adams","narrator":"Stephen Fry","asin":"B002VA9SWS","duration":351}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	call := toolCaller(t, f)

	out, err := call("audit_matched", nil)
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_scanned"]) != 3 {
		t.Errorf("items_scanned = %v, want the three with an asin", out["items_scanned"])
	}
	findings := list(t, out["findings"])
	if num(t, out["total_findings"]) != 2 || len(findings) != 2 || str(t, findings[0]["title"]) != "The Hitchhiker's Guide To The Galaxy" {
		t.Fatalf("findings = %v, want the Adams edition and the UK asin", findings)
	}
	probs, ok := findings[0]["problems"].([]any)
	if !ok || len(probs) != 1 || probs[0] != "duration_off" {
		t.Errorf("problems = %v, want duration_off", probs)
	}
	if probs, ok := findings[1]["problems"].([]any); !ok || len(probs) != 1 || probs[0] != "not_found" {
		t.Errorf("UK asin in the US store = %v, want not_found", findings[1])
	}
	if got := f.requests("/api/search/books"); len(got) != 3 {
		t.Errorf("%d provider requests, want one per matched book", len(got))
	}

	// with the UK store as a fallback the same book is found there and fits
	out, err = call("audit_matched", map[string]any{"providers": []any{"audible", "audible.uk"}})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["total_findings"]) != 1 {
		t.Errorf("with a fallback region findings = %v, want only the Adams edition", out["findings"])
	}

	// a filter goes through the filtered listing and checks the matched
	// books it selects
	out, err = call("audit_matched", map[string]any{"filter": "genres:Fiction"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.requests("/api/libraries/" + libID + "/items"); !strings.Contains(got[len(got)-1].Query, "filter=") {
		t.Errorf("filtered audit listed without a filter: %v", got[len(got)-1])
	}
	if num(t, out["items_scanned"]) != 3 {
		t.Errorf("filtered items_scanned = %v", out["items_scanned"])
	}

	// a window of one: the first book, and a pointer to the next
	out, err = call("audit_matched", map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_scanned"]) != 1 || num(t, out["next_offset"]) != 1 {
		t.Errorf("offset 0 = %v, want one checked and next_offset 1", out)
	}
}

// serveListing answers a library's item listing from books, paged by the
// limit and page asked for, the way the server pages.
func serveListing(f *fakeABS, library string, books ...string) {
	f.mux.HandleFunc("GET /api/libraries/"+library+"/items", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if limit <= 0 {
			limit = max(len(books), 1)
		}
		lo, hi := min(pg*limit, len(books)), min((pg+1)*limit, len(books))
		_, _ = fmt.Fprintf(w, `{"results":[%s],"total":%d}`, strings.Join(books[lo:hi], ","), len(books))
	})
}

// idsOf reads the id off every row of a list.
func idsOf(t *testing.T, v any) []string {
	t.Helper()

	rows := list(t, v)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, str(t, row["id"]))
	}
	return out
}

// Under a filter, audit_matched, item_match_tag and the store side of
// audit_covers paged the filtered listing, unmatched books and all, so a
// window of two checked one book or none; offset counts matched books alone,
// so every window is limit books to look up, and one that starts inside a
// window is honoured.
func TestMatchedWindowsCountMatchedBooks(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f)
	serveListing(f, libID,
		item("m1", "One", `"asin":"B001"`, ""),
		item("u1", "Unmatched A", "", ""),
		item("u2", "Unmatched B", "", ""),
		item("m2", "Two", `"asin":"B002"`, ""),
		item("u3", "Unmatched C", "", ""),
		item("m3", "Three", `"asin":"B003"`, ""),
		item("m4", "Four", `"isbn":"9780000000004"`, ""),
	)
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		offset int
		want   []string
		next   any
	}{
		{0, []string{"m1", "m2"}, float64(2)},
		{1, []string{"m2", "m3"}, nil},
		{2, []string{"m3"}, nil},
		{3, nil, nil},
	} {
		out, err := call("audit_matched", map[string]any{"filter": "genres:Fiction", "library": "Books", "limit": 2, "offset": tc.offset})
		if err != nil {
			t.Fatal(err)
		}
		if got := idsOf(t, out["findings"]); !slices.Equal(got, tc.want) || num(t, out["items_scanned"]) != len(tc.want) || out["next_offset"] != tc.next {
			t.Errorf("audit_matched offset %d = %v (%v scanned, next %v), want %v and next %v", tc.offset, got, out["items_scanned"], out["next_offset"], tc.want, tc.next)
		}
	}

	// the provider tag counts an isbn as matched too
	for _, tc := range []struct {
		offset int
		want   []string
		next   any
	}{
		{0, []string{"m1", "m2"}, float64(2)},
		{2, []string{"m3", "m4"}, nil},
	} {
		out, err := call("item_match_tag", map[string]any{"filter": "genres:Fiction", "library": "Books", "limit": 2, "offset": tc.offset})
		if err != nil {
			t.Fatal(err)
		}
		if got := idsOf(t, out["rows"]); !slices.Equal(got, tc.want) || num(t, out["checked"]) != len(tc.want) || out["next_offset"] != tc.next {
			t.Errorf("item_match_tag offset %d = %v (%v checked, next %v), want %v and next %v", tc.offset, got, out["checked"], out["next_offset"], tc.want, tc.next)
		}
	}

	first, err := call("audit_covers", map[string]any{"store": true, "library": "Books", "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, first["store_checked"]) != 2 || num(t, first["next_offset"]) != 2 || num(t, first["items_scanned"]) != 7 {
		t.Errorf("audit_covers offset 0 = %v, want two compared of the seven looked at, and next_offset 2", first)
	}
	rest, err := call("audit_covers", map[string]any{"store": true, "library": "Books", "limit": 2, "offset": 2})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, rest["store_checked"]) != 1 || rest["next_offset"] != nil || num(t, rest["items_scanned"]) != 1 || len(list(t, rest["findings"])) != 0 {
		t.Errorf("audit_covers offset 2 = %v, want the last matched book alone and no library-wide rows", rest)
	}
	// every listing is in the order added, which no fix changes
	for _, r := range f.requests("/api/libraries/" + libID + "/items") {
		if q := parseQuery(t, r.Query); q.Get("sort") != "addedAt" && q.Get("sort") != "" {
			t.Errorf("listing asked %q, want the order added", r.Query)
		}
	}
	if _, err := call("audit_covers", map[string]any{"library": "Books", "offset": 2}); err == nil || !strings.Contains(err.Error(), "only means something with store") {
		t.Errorf("offset without store: %v", err)
	}
}

// item_match_batch takes an offset the way library_items does: one that is
// not a whole window is honoured, and next_offset is where the next starts.
func TestItemMatchBatchOffset(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	servePages(f, libID, 5)
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	out, err := call("item_match_batch", map[string]any{"limit": 2, "offset": 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := titlesOf(t, out["rows"]); !slices.Equal(got, []string{"Book 001", "Book 002"}) || num(t, out["offset"]) != 1 || num(t, out["next_offset"]) != 3 || num(t, out["total"]) != 5 {
		t.Errorf("offset 1 = %v, offset %v, next_offset %v, total %v; want Book 001 and 002, then 3 of 5", got, out["offset"], out["next_offset"], out["total"])
	}
	if !strings.Contains(str(t, out["paging"]), "offset 1 again") {
		t.Errorf("paging = %q, want offset 1 asked for again after applying", out["paging"])
	}
	last, err := call("item_match_batch", map[string]any{"limit": 2, "offset": 4})
	if err != nil {
		t.Fatal(err)
	}
	if got := titlesOf(t, last["rows"]); !slices.Equal(got, []string{"Book 004"}) || last["next_offset"] != nil || last["paging"] != nil {
		t.Errorf("offset 4 = %v, next_offset %v, paging %v; want the last book and no more", got, last["next_offset"], last["paging"])
	}
}

// page is gone from every tool that paged by number: a caller still sending
// it is refused by the schema, which names it, rather than read from the start.
func TestPageIsRefused(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page())
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"item_match_batch", map[string]any{"page": 1}},
		{"item_match_tag", map[string]any{"library": "Books", "page": 1}},
		{"audit_matched", map[string]any{"page": 1}},
		{"audit_covers", map[string]any{"library": "Books", "store": true, "page": 1}},
	} {
		if _, err := call(tc.tool, tc.args); err == nil || !strings.Contains(err.Error(), `unexpected additional properties ["page"]`) {
			t.Errorf("%s with page: %v", tc.tool, err)
		}
	}
	if got := f.requests("/api/libraries/" + libID + "/items"); len(got) != 0 {
		t.Errorf("a call with page listed the library: %v", got)
	}
}

// A library left on google (a new library's provider) with no providers
// named or configured had every asin looked up there, and every matched book
// came back not_found. The tools that look an asin up refuse it before any
// work, naming the library and the fix; audit_all deep leaves audit_matched
// out and says why.
func TestLookupRefusesALibraryOnGoogle(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"google"}]}`)
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"google"}`)
	book := item("li_1", "Dune", `"asin":"B0DUNE"`, "")
	f.json("GET /api/libraries/"+libID+"/items", page(book))
	f.json("GET /api/items/li_1", book)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"audit_matched", nil},
		{"audit_matched", map[string]any{"library": "Books", "filter": "genres:Fiction"}},
		{"audit_covers", map[string]any{"library": "Books", "store": true}},
		{"item_match_tag", map[string]any{"library": "Books"}},
		{"item_cover_upgrade", map[string]any{"library": "Books", "items": []any{"li_1"}}},
		{"item_cover_upgrade", map[string]any{"items": []any{"li_1"}}}, // the book's own library
	} {
		_, err := call(tc.tool, tc.args)
		if err == nil || !strings.Contains(err.Error(), `library "Books" is on the google provider, which cannot look up an asin`) || !strings.Contains(err.Error(), "--providers (ABS_PROVIDERS), e.g. audible.ca,audible") {
			t.Errorf("%s %v: %v, want the library, its provider and the fix", tc.tool, tc.args, err)
		}
	}
	// refused before the library was read or a store asked
	if got := f.requests("/api/libraries/" + libID + "/items"); len(got) != 0 {
		t.Errorf("a refused call listed the library: %v", got)
	}
	if got := f.requests("/api/search/books"); len(got) != 0 {
		t.Errorf("a refused call asked a store: %v", got)
	}

	all, err := call("audit_all", map[string]any{"deep": true})
	if err != nil {
		t.Fatal(err)
	}
	if skipped, ok := all["skipped"].([]any); !ok || !slices.Equal(skipped, []any{"audit_matched", "audit_abridged"}) {
		t.Errorf("audit_all deep skipped %v, want audit_matched and audit_abridged", all["skipped"])
	}
	var why string
	for _, row := range list(t, all["not_run"]) {
		if str(t, row["audit"]) == "audit_matched" {
			why = str(t, row["reason"])
		}
	}
	if !strings.Contains(why, `library "Books" is on the google provider`) {
		t.Errorf("audit_matched not run because %q, want the library and its provider", why)
	}
	if got := f.requests("/api/search/books"); len(got) != 0 {
		t.Errorf("audit_all deep asked google for an asin: %v", got)
	}

	// named providers, or the server's, are asked instead
	if out, err := call("audit_matched", map[string]any{"providers": []any{"audible"}}); err != nil || num(t, out["items_scanned"]) != 1 {
		t.Errorf("with providers: %v %v", out, err)
	}
	if out, err := callerWith(t, f, Options{Providers: []string{"audible.ca", "audible"}})("audit_matched", nil); err != nil || num(t, out["items_scanned"]) != 1 {
		t.Errorf("with --providers: %v %v", out, err)
	}
	// and a title search takes google as it is
	if _, err := call("item_match_batch", map[string]any{"filter": "genres:Fiction"}); err != nil {
		t.Errorf("item_match_batch on google: %v", err)
	}
}
