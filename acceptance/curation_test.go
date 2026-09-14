//go:build integration

// The curation tools added for the series and cover passes: what they conclude
// about a real library, and what they change in it.
package acceptance

import (
	"slices"
	"strings"
	"testing"
)

// The non-fiction genre is History on all three books, which is under the
// audit's idea of a real genre, and two of them also carry a "history" tag
// that says the same thing again. Both are findings; nothing else is.
func TestAuditGenres(t *testing.T) {
	out := call(t, "audit_genres", map[string]any{"library": "Non-Fiction"})

	if scanned := num(t, out["items_scanned"], "items_scanned"); scanned != 3 {
		t.Errorf("items_scanned = %d, want 3", scanned)
	}
	if found := num(t, out["total_findings"], "total_findings"); found != 2 {
		t.Errorf("total_findings = %d, want the narrow genre and the redundant tag", found)
	}
	for _, section := range []string{"placeholders", "compound"} {
		if len(rows(t, out[section], section)) != 0 {
			t.Errorf("%s = %v, want nothing", section, out[section])
		}
	}
	if v := out["no_genres"]; v != nil && len(rows(t, v, "no_genres")) != 0 {
		t.Errorf("no_genres = %v, want none: every fixture has a genre", v)
	}

	narrow := rows(t, out["narrow"], "narrow")
	if len(narrow) != 1 || narrow[0]["value"] != "History" || num(t, narrow[0]["items"], "items") != 3 {
		t.Errorf("narrow = %v, want History on 3 books", narrow)
	} else if suggest, _ := narrow[0]["suggest"].(string); !strings.Contains(suggest, "to_field=tags") {
		t.Errorf("narrow suggests %q, want a move to tags", suggest)
	}
	redundant := rows(t, out["redundant"], "redundant")
	if len(redundant) != 1 || redundant[0]["value"] != "history" || num(t, redundant[0]["items"], "items") != 2 {
		t.Errorf("redundant = %v, want the history tag on 2 books", redundant)
	} else if suggest, _ := redundant[0]["suggest"].(string); !strings.Contains(suggest, "remove=true") {
		t.Errorf("redundant suggests %q, want a removal", suggest)
	}
}

// series_merge moves the books of one spelling into the other, keeping their
// numbers, and the emptied series is gone afterwards.
func TestSeriesMerge(t *testing.T) {
	const book = "Foundation"
	t.Cleanup(func() { restoreBook(t, book) })

	// a second spelling with one book in it, beside the three-book series
	call(t, "item_edit", map[string]any{"item": book, "add_series": []any{"Foundation Saga #1"}})
	if series := strs(t, call(t, "item_get", map[string]any{"item": book})["series"], "series"); len(series) != 2 {
		t.Fatalf("series after add_series = %v, want both", series)
	}

	if msg := callErr(t, "series_merge", map[string]any{"library": "Fiction", "from": "Foundation", "into": "Foundation"}); msg == "" {
		t.Error("merging a series into itself should be refused")
	}

	out := call(t, "series_merge", map[string]any{"library": "Fiction", "from": "Foundation Saga", "into": "Foundation"})
	if moved := num(t, out["moved"], "moved"); moved != 1 {
		t.Errorf("moved = %d, want 1", moved)
	}
	if books := strs(t, out["books"], "books"); !slices.Contains(books, "Foundation #1") {
		t.Errorf("books = %v, want Foundation at #1", books)
	}

	// the book is in the target once, at its number
	if series := strs(t, call(t, "item_get", map[string]any{"item": book})["series"], "series"); !slices.Equal(series, []string{"Foundation #1"}) {
		t.Errorf("series after merge = %v, want [Foundation #1]", series)
	}
	if books := rows(t, call(t, "series_get", map[string]any{"library": "Fiction", "series": "Foundation"})["books"], "books"); len(books) != 3 {
		t.Errorf("Foundation has %d books after the merge, want 3", len(books))
	}
	if msg := callErr(t, "series_get", map[string]any{"library": "Fiction", "series": "Foundation Saga"}); msg == "" {
		t.Error("the emptied series is still there")
	}
}

// item_match_batch scores a page of books against the store and writes
// nothing. The fixtures are one-second files, so nothing can be exact, but
// every Foundation book has a real catalogue entry to be judged against.
func TestItemMatchBatch(t *testing.T) {
	requireProviders(t)

	out := call(t, "item_match_batch", map[string]any{
		"library": "Fiction", "filter": "series:Foundation", "providers": []any{"audible"}, "candidates": 2,
	})
	if total := num(t, out["total"], "total"); total != 3 {
		t.Errorf("total = %d, want the 3 Foundation books", total)
	}
	if _, more := out["next_page"]; more {
		t.Errorf("next_page = %v on a three-book filter", out["next_page"])
	}

	found := rows(t, out["rows"], "rows")
	if len(found) != 3 {
		t.Fatalf("rows = %d, want 3", len(found))
	}
	confidences := []string{"exact", "likely", "edition", "unsure", "none"}
	for _, row := range found {
		conf, _ := row["confidence"].(string)
		if !slices.Contains(confidences, conf) {
			t.Errorf("%v: confidence = %q", row["title"], conf)
		}
		if conf == "exact" {
			t.Errorf("%v: a one-second file cannot be the recording", row["title"])
		}
		best, ok := row["best"].(map[string]any)
		if !ok {
			t.Errorf("%v: no best candidate from the store", row["title"])
			continue
		}
		if asin, _ := best["asin"].(string); asin == "" {
			t.Errorf("%v: the best candidate has no asin", row["title"])
		}
		if title, _ := best["title"].(string); !strings.Contains(strings.ToLower(title), "foundation") {
			t.Errorf("%v: best candidate is %q", row["title"], title)
		}
	}

	counts, ok := out["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts = %T", out["counts"])
	}
	var sum int
	for _, v := range counts {
		n, _ := v.(float64)
		sum += int(n)
	}
	if sum != 3 {
		t.Errorf("counts add up to %d, want 3: %v", sum, counts)
	}

	// these are scores, not matches
	if item := call(t, "item_get", map[string]any{"item": "Foundation"}); item["asin"] != nil {
		t.Errorf("item_match_batch set an asin (%v); it must only score", item["asin"])
	}
}

// item_cover_upgrade fetches the store's picture itself, straight from the
// image host, which the proxy in front of the server cannot replay; what it
// makes of the pictures is under the unit tests. Here: a book with nothing to
// look up is said so, and nothing is fetched.
func TestItemCoverUpgradeNeedsAnASIN(t *testing.T) {
	if msg := callErr(t, "item_cover_upgrade", map[string]any{"library": "Fiction"}); msg == "" {
		t.Error("a call naming no items should be refused")
	}

	out := call(t, "item_cover_upgrade", map[string]any{
		"library": "Fiction", "items": []any{"Foundation", "Leviathan Wakes"}, "preview": true,
	})
	if upgraded := num(t, out["upgraded"], "upgraded"); upgraded != 0 {
		t.Errorf("upgraded = %d, want 0", upgraded)
	}
	items := rows(t, out["items"], "items")
	if len(items) != 2 {
		t.Fatalf("items = %v, want a row per book", items)
	}
	for _, row := range items {
		if action, _ := row["action"].(string); action != "no_asin" {
			t.Errorf("%v: action = %q, want no_asin on an unmatched book", row["title"], action)
		}
	}
}
