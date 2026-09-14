package tools

import (
	"net/http"
	"slices"
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
	if _, next := out["next_page"]; next {
		t.Error("next_page on the only page")
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
	if num(t, out["applied"]) != 2 || num(t, out["failed"]) != 1 {
		t.Errorf("applied/failed = %v/%v", out["applied"], out["failed"])
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
func TestAuditMatched(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
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

	// a filter goes through the filtered listing and checks what it selects
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

	// a page of one: the first book, and a pointer to the next
	out, err = call("audit_matched", map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_scanned"]) != 1 || num(t, out["next_page"]) != 1 {
		t.Errorf("page 0 = %v, want one checked and next_page 1", out)
	}
}
