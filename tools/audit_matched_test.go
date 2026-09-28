package tools

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Unsorted, the listing's order was the server's own, and a book fixed
// between two calls moved into a window already read.
func TestAuditMatchedPagesInAddedOrder(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(item("i1", "Dune", `"asin":"B0"`, "")))
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	for _, args := range []map[string]any{nil, {"filter": "genres:Fiction", "library": "Books"}} {
		if _, err := call("audit_matched", args); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range f.requests("/api/libraries/" + libID + "/items") {
		if !strings.Contains(r.Query, "sort=addedAt") {
			t.Errorf("listing asked %q, want sort=addedAt", r.Query)
		}
	}
}

// A tolerance wide enough calls every shorter edition the same recording, so
// duration_off could never be reported.
func TestAuditMatchedBoundsTolerance(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	call := toolCaller(t, f)
	for _, tol := range []float64{-0.1, 0.5} {
		if _, err := call("audit_matched", map[string]any{"tolerance": tol}); err == nil {
			t.Errorf("tolerance %g was taken", tol)
		}
	}
	if got := f.requests("/api/libraries"); len(got) != 0 {
		t.Errorf("asked the server before refusing: %v", got)
	}
}

// audit_matched with no library runs its windows on through every book
// library, as audit_all counts it, rather than refusing.
func TestAuditMatchedPagesAcrossBookLibraries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"},{"id":"`+podLibID+`","name":"Shows","mediaType":"podcast"},{"id":"`+otherLibID+`","name":"Other","mediaType":"book","provider":"audible"}]}`)
	f.json("GET /api/libraries/"+libID+"/items", page(item(bookB1, "First", `"asin":"B001"`, "")))
	f.json("GET /api/libraries/"+otherLibID+"/items", page(`{"id":"`+otherBook+`","libraryId":"`+otherLibID+`","mediaType":"book","media":{"metadata":{"title":"Elsewhere","asin":"B009"}}}`))
	f.json("GET /api/libraries/"+podLibID+"/items", page())
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	first, err := call("audit_matched", map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, first["items_scanned"]) != 1 || num(t, first["next_offset"]) != 1 {
		t.Errorf("offset 0 = %v, want one book and a next offset", first)
	}
	second, err := call("audit_matched", map[string]any{"limit": 1, "offset": 1})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, second["items_scanned"]) != 1 || second["next_offset"] != nil {
		t.Errorf("offset 1 = %v, want the other library's book and no next offset", second)
	}
	findings := list(t, second["findings"])
	if len(findings) != 1 || str(t, findings[0]["title"]) != "Elsewhere" {
		t.Errorf("offset 1 findings = %v, want Elsewhere", second["findings"])
	}
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

// Under a filter, audit_matched, item_match_tag and the store side of
// audit_covers paged the filtered listing, unmatched books and all, so a
// window of two checked one book or none; offset counts matched books alone,
// so every window is limit books to look up, and one that starts inside a
// window is honoured.
func TestMatchedWindowsCountMatchedBooks(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f)
	serveListing(f,
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
		if got := column(t, "id", out["findings"]); !slices.Equal(got, tc.want) || num(t, out["items_scanned"]) != len(tc.want) || out["next_offset"] != tc.next {
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
		if got := column(t, "id", out["rows"]); !slices.Equal(got, tc.want) || num(t, out["checked"]) != len(tc.want) || out["next_offset"] != tc.next {
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
