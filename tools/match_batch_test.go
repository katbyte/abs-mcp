package tools

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

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
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"}`) // a row naming no provider takes the library's
	out, err = call("item_match_apply_batch", map[string]any{"confirm": true, "matches": []any{
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
	if got := column(t, "title", out["rows"]); !slices.Equal(got, []string{"Book 001", "Book 002"}) || num(t, out["offset"]) != 1 || num(t, out["next_offset"]) != 3 || num(t, out["total"]) != 5 {
		t.Errorf("offset 1 = %v, offset %v, next_offset %v, total %v; want Book 001 and 002, then 3 of 5", got, out["offset"], out["next_offset"], out["total"])
	}
	if !strings.Contains(str(t, out["paging"]), "offset 1 again") {
		t.Errorf("paging = %q, want offset 1 asked for again after applying", out["paging"])
	}
	last, err := call("item_match_batch", map[string]any{"limit": 2, "offset": 4})
	if err != nil {
		t.Fatal(err)
	}
	if got := column(t, "title", last["rows"]); !slices.Equal(got, []string{"Book 004"}) || last["next_offset"] != nil || last["paging"] != nil {
		t.Errorf("offset 4 = %v, next_offset %v, paging %v; want the last book and no more", got, last["next_offset"], last["paging"])
	}
}

// applied counts the books a batch changed, not the rows it sent: a preview
// changes nothing, and neither does a match with nothing new or one that
// found nothing.
func TestItemMatchApplyBatchCountsWhatChanged(t *testing.T) {
	t.Parallel()

	const (
		changed = "33333333-3333-4333-8333-000000000001"
		same    = "33333333-3333-4333-8333-000000000002"
		missing = "33333333-3333-4333-8333-000000000003"
	)
	f := newFakeABS(t)
	for _, id := range []string{changed, same, missing} {
		f.json("GET /api/items/"+id, item(id, "Dune", `"authorName":"Frank Herbert"`, ""))
		f.json("PATCH /api/items/"+id+"/media", `{"updated":true}`)
	}
	f.json("POST /api/items/"+changed+"/match", `{"updated":true,"libraryItem":`+item(changed, "Dune", `"asin":"B0DUNE"`, "")+`}`)
	f.json("POST /api/items/"+same+"/match", `{"updated":false,"libraryItem":`+item(same, "Dune", `"asin":"B0DUNE"`, "")+`}`)
	f.json("POST /api/items/"+missing+"/match", `{"warning":"No audible match found"}`)
	f.json("GET /api/search/books", `[{"title":"Dune","author":"Frank Herbert","asin":"B0DUNE"}]`)
	call := toolCaller(t, f)

	rows := []any{
		map[string]any{"item": changed, "asin": "B0DUNE", "provider": "audible"},
		map[string]any{"item": same, "asin": "B0DUNE", "provider": "audible"},
		map[string]any{"item": missing, "asin": "B0DUNE", "provider": "audible"},
		map[string]any{"item": changed},
	}
	out, err := call("item_match_apply_batch", map[string]any{"confirm": true, "matches": rows})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["applied"]) != 1 || num(t, out["unchanged"]) != 2 || num(t, out["failed"]) != 1 {
		t.Errorf("applied/unchanged/failed = %v/%v/%v, want 1/2/1", out["applied"], out["unchanged"], out["failed"])
	}

	out, err = call("item_match_apply_batch", map[string]any{"matches": rows[:1], "smart": true})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["applied"]) != 0 || num(t, out["would_apply"]) != 1 {
		t.Errorf("without confirm: applied/would_apply = %v/%v, want 0/1", out["applied"], out["would_apply"])
	}
}

// Applying a window's rows takes those books out of missing:asin and the
// listing closes up behind them, so next_offset would skip as many books as
// were applied: the answer says to ask for the same offset again.
func TestItemMatchBatchSaysToAskForTheOffsetAgain(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", `{"results":[`+item("i1", "Dune", "", "")+`],"total":3,"limit":1,"page":0}`)
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	out, err := call("item_match_batch", map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["next_offset"]) != 1 || !strings.Contains(str(t, out["paging"]), "offset 0 again") {
		t.Errorf("next_offset/paging = %v/%q, want offset 0 asked for again once rows are applied", out["next_offset"], out["paging"])
	}

	out, err = call("item_match_batch", map[string]any{"limit": 1, "filter": "genres:Fiction"})
	if err != nil {
		t.Fatal(err)
	}
	if out["paging"] != nil {
		t.Errorf("paging = %v under a filter a match does not change", out["paging"])
	}
}

// A tolerance wide enough takes in the shorter editions it is there to tell
// apart, so every row comes back exact.
func TestItemMatchBatchBoundsTheTolerance(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	call := toolCaller(t, f)

	for _, tolerance := range []float64{0.5, 5, -0.1} {
		if _, err := call("item_match_batch", map[string]any{"tolerance": tolerance}); err == nil || !strings.Contains(err.Error(), "tolerance") {
			t.Errorf("tolerance %v: %v, want it refused", tolerance, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) != 0 {
		t.Errorf("a refused call reached the server: %v", f.seen)
	}
}

// A row's hold on its book is let go however the row ends: one left behind
// by a panic, which the server now survives, stalled every later edit of the
// book for good.
func TestARowThatPanicsLetsGoOfItsBook(t *testing.T) {
	t.Parallel()

	for name, row := range map[string]func(r *registry){
		"item_match_apply_batch": func(r *registry) {
			r.applyRow(t.Context(), itemID, "audible", rowMatch{}, &applyResult{})
		},
		"item_match_tag": func(r *registry) {
			_ = r.tagOne(t.Context(), itemID, "audible")
		},
	} {
		r := &registry{} // no client: the read after the hold panics
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: the row did not panic", name)
				}
			}()
			row(r)
		}()
		if !r.locks.Idle() {
			t.Errorf("%s: the row that panicked left a lock behind", name)
		}
	}
}

// The length compared against the provider's recording, on both sides.
func TestMatchDurationsInSeconds(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("11111111-1111-4111-8111-000000000001", "Killingly", `"authorName":"Katharine Beutner"`, `"duration":44580.4`),
		item("11111111-1111-4111-8111-000000000002", "Horus Rising", `"authorName":"Dan Abnett","asin":"B0UK"`, `"duration":40000.6`),
	))
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("title") {
		case "Killingly":
			_, _ = w.Write([]byte(`[{"title":"Killingly","author":"Katharine Beutner","asin":"B0BZ","duration":743}]`))
		case "B0UK":
			_, _ = w.Write([]byte(`[{"title":"Horus Rising","author":"Dan Abnett","asin":"B0UK","duration":755}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	call := toolCaller(t, f)

	out, err := call("item_match_batch", nil)
	if err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, "item_match_batch", out, map[string]float64{"rows.0.duration_s": 44580, "rows.0.best.duration_s": 44580})
	wantAbsent(t, "item_match_batch", out, "rows.0.duration", "rows.0.best.duration")

	out, err = call("audit_matched", map[string]any{"providers": []any{"audible"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := dig(out, "findings.0.problems.0"); got != "duration_off" {
		t.Fatalf("audit_matched = %v, want Horus Rising's recording a different length", out["findings"])
	}
	wantNumbers(t, "audit_matched", out, map[string]float64{"findings.0.duration_s": 40001, "findings.0.provider.duration_s": 45300})
	wantAbsent(t, "audit_matched", out, "findings.0.provider.duration")
}
