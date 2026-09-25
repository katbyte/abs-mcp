//go:build integration

// Abridged recordings filed as the book, found two ways: by length against
// the store's editions, and chapter by chapter against another reading in the
// library. The store is played here through the provider proxy, Audible's
// catalog search and Audnexus's book records both, so its editions are what
// the journey says and nothing is recorded. The books are silent m4bs made at
// runtime, a quarter of an hour or so each.
package acceptance

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// abridgedStore plays Audible's catalog search and Audnexus's book records
// for a few titles, noting every title searched.
type abridgedStore struct {
	editions map[string][]map[string]any // title -> the Audnexus records of its editions

	mu    sync.Mutex
	asked []string
}

// edition is an Audnexus book record with what the server reads from it.
func edition(asin, title, narrator string, minutes int, format string) map[string]any {
	return map[string]any{
		"asin": asin, "title": title, "language": "english", "formatType": format, "runtimeLengthMin": minutes,
		"authors": []any{map[string]any{"name": "Zzyzx Edition Author"}}, "narrators": []any{map[string]any{"name": narrator}},
	}
}

// catalog answers the title search with the asins of the title's editions;
// the server then fetches each record from Audnexus.
func (s *abridgedStore) catalog(w http.ResponseWriter, r *http.Request) {
	title := r.URL.Query().Get("title")
	s.mu.Lock()
	s.asked = append(s.asked, title)
	s.mu.Unlock()

	products := []any{}
	for _, e := range s.editions[title] {
		products = append(products, map[string]any{"asin": e["asin"]})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"products": products, "total_results": len(products)})
}

// audnex answers a book record, and an author search with nothing.
func (s *abridgedStore) audnex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/authors" {
		_, _ = w.Write([]byte(`[]`))
		return
	}
	for _, editions := range s.editions {
		for _, e := range editions {
			if r.URL.Path == "/books/"+e["asin"].(string) {
				_ = json.NewEncoder(w).Encode(e)
				return
			}
		}
	}
	http.NotFound(w, r)
}

// searched is every title the store was asked about.
func (s *abridgedStore) searched() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.asked)
}

// A session looking for abridged recordings the library does not know are
// abridged. Heartfire is the length of the store's abridged edition,
// Enchantment 38% of its only unabridged one, and The Gods Themselves read by
// Morgan is cut unevenly against two whole readings; Destroyer read fast at
// 78% of the store's, and Leclercq's steady reading of The Gods Themselves,
// are left alone, and a book whose folder says abridged, or whose flag is
// set, is not even searched. Once each finding is fixed with item_edit the
// audit is clean. It would catch the store's abridged flag not reaching the
// tool, a reader's pace taken for a cut, chapters not compared across
// readings, and a book already marked searched or reported again.
func TestJourneyAbridgedNotMarked(t *testing.T) {
	requireProviders(t)

	const author = "Zzyzx Edition Author"
	seconds := map[string]int{
		"Zzyzx Heartfire":         1200,
		"Zzyzx Enchantment":       900,
		"Zzyzx Destroyer":         1500,
		"Zzyzx Marked (Abridged)": 900,
		"Zzyzx Flagged":           900,
		"Zzyzx Gods (Brick)":      1800,
		"Zzyzx Gods (Leclercq)":   1530,
		"Zzyzx Gods (Morgan)":     1120,
	}
	s := newDiskShelf(t, "Zzyzx Abridged Shelf", "zzyzx-abridged")
	for name, secs := range seconds {
		diskSilence(t, filepath.Join(s.root, author, name, name+".m4b"), secs)
	}
	s.open(t, len(seconds))
	ids := s.ids(t)
	id := func(name string) string { return ids[author+"/"+name] }
	audit := func(t *testing.T, extra map[string]any) map[string]any {
		t.Helper()
		args := map[string]any{"library": s.name, "providers": []any{"audible"}}
		for k, v := range extra {
			args[k] = v
		}
		return call(t, "audit_abridged", args)
	}

	store := &abridgedStore{editions: map[string][]map[string]any{
		"Zzyzx Heartfire": {
			edition("B0ZZYZXA01", "Zzyzx Heartfire", "Zzyzx Visitor", 20, "abridged"),
			edition("B0ZZYZXU01", "Zzyzx Heartfire", "Zzyzx Brick", 41, "unabridged"),
		},
		"Zzyzx Enchantment": {edition("B0ZZYZXU02", "Zzyzx Enchantment", "Zzyzx Rudnicki", 40, "unabridged")},
		"Zzyzx Destroyer":   {edition("B0ZZYZXU03", "Zzyzx Destroyer", "Zzyzx James", 32, "unabridged")},
		"Zzyzx Flagged":     {edition("B0ZZYZXA04", "Zzyzx Flagged", "Zzyzx Reader", 15, "abridged")},
	}}
	t.Cleanup(proxy.Serve("api.audible.com", http.HandlerFunc(store.catalog)))
	t.Cleanup(proxy.Serve("api.audnex.us", http.HandlerFunc(store.audnex)))

	t.Run("a library on google is refused, and no store asked", func(t *testing.T) {
		if msg := callErr(t, "audit_abridged", map[string]any{"library": s.name}); !strings.Contains(msg, `library "Zzyzx Abridged Shelf" is on the google provider`) || !strings.Contains(msg, "--providers") {
			t.Errorf("audit_abridged on google = %q, want the library, its provider and the fix", msg)
		}
		if asked := store.searched(); len(asked) != 0 {
			t.Errorf("the store was asked %v", asked)
		}
	})

	// one reading whole, one read steadily faster, one cut more in some
	// chapters than others
	chapters := func(lengths ...float64) []any {
		var out []any
		start := 0.0
		for i, l := range lengths {
			out = append(out, map[string]any{"title": "Zzyzx Chapter " + []string{"One", "Two", "Three", "Four", "Five", "Six"}[i], "start_s": start})
			start += l
		}
		return out
	}
	call(t, "item_chapters_set", map[string]any{"item": id("Zzyzx Gods (Brick)"), "chapters": chapters(300, 240, 360, 280, 320, 300)})
	call(t, "item_chapters_set", map[string]any{"item": id("Zzyzx Gods (Leclercq)"), "chapters": chapters(255, 210, 300, 240, 270, 255)})
	call(t, "item_chapters_set", map[string]any{"item": id("Zzyzx Gods (Morgan)"), "chapters": chapters(200, 100, 300, 120, 250, 150)})
	call(t, "item_edit", map[string]any{"item": id("Zzyzx Flagged"), "abridged": true})

	var found []map[string]any
	t.Run("the store's abridged edition, a book far shorter, a reading cut unevenly", func(t *testing.T) {
		out := audit(t, nil)
		if num(t, out["items_scanned"], "items_scanned") != 8 || num(t, out["store_checked"], "store_checked") != 6 || num(t, out["readings_compared"], "readings_compared") != 3 {
			t.Errorf("scanned %v, searched %v, compared %v; want 8 books, 6 searched, 3 pairs", out["items_scanned"], out["store_checked"], out["readings_compared"])
		}
		found = rows(t, out["findings"], "findings")
		var got []string
		for _, f := range found {
			got = append(got, text(f["problem"])+" "+text(f["title"]))
		}
		want := []string{"abridged_length Zzyzx Heartfire", "uneven_chapters Zzyzx Gods (Morgan)", "far_shorter Zzyzx Enchantment"}
		if !slices.Equal(got, want) {
			t.Fatalf("findings = %v, want %v", got, want)
		}
		near := func(v any, want int) bool {
			n, ok := v.(float64)
			return ok && n > float64(want-2) && n < float64(want+2)
		}

		heartfire, _ := found[0]["edition"].(map[string]any)
		if heartfire["asin"] != "B0ZZYZXA01" || heartfire["abridged"] != true || heartfire["provider"] != "audible" || num(t, heartfire["duration_s"], "duration_s") != 1200 || !near(found[0]["duration_s"], 1200) {
			t.Errorf("Heartfire = %v, want the abridged edition of its length", found[0])
		}
		// the steady reading cut against is Leclercq's: their chapters differ
		// most, 2.18 by section_ratio.py
		other, _ := found[1]["other_reading"].(map[string]any)
		spread, _ := found[1]["spread"].(float64)
		if other["id"] != id("Zzyzx Gods (Leclercq)") || spread < 2.1 || spread > 2.3 || num(t, found[1]["chapters_compared"], "chapters_compared") != 6 || !near(found[1]["duration_s"], 1120) {
			t.Errorf("Morgan = %v, want against Leclercq, spread about 2.18 over 6 chapters", found[1])
		}
		enchantment, _ := found[2]["edition"].(map[string]any)
		if enchantment["asin"] != "B0ZZYZXU02" || enchantment["abridged"] != false || num(t, enchantment["duration_s"], "duration_s") != 2400 || !strings.Contains(text(found[2]["detail"]), "38% of the shortest unabridged edition") {
			t.Errorf("Enchantment = %v, want 38%% of the unabridged edition", found[2])
		}
		for _, f := range found {
			if !strings.HasPrefix(text(f["fix"]), "item_edit abridged=true") {
				t.Errorf("%s fix = %q", f["title"], f["fix"])
			}
		}
		asked := store.searched()
		for _, title := range []string{"Zzyzx Marked (Abridged)", "Zzyzx Flagged"} {
			if slices.Contains(asked, title) {
				t.Errorf("%s was searched, though it says it is abridged: %v", title, asked)
			}
		}
		if !slices.Contains(asked, "Zzyzx Destroyer") {
			t.Errorf("the fast reader was never searched: %v", asked)
		}
	})

	t.Run("fixed with item_edit, the audit is clean and searches them no more", func(t *testing.T) {
		if len(found) == 0 {
			t.Skip("nothing was found to fix")
		}
		for _, f := range found {
			call(t, "item_edit", map[string]any{"item": text(f["id"]), "abridged": true})
		}
		before := len(store.searched())
		out := audit(t, nil)
		if n := num(t, out["total_findings"], "total_findings"); n != 0 {
			t.Errorf("after the fixes %d findings: %v", n, out["findings"])
		}
		// Heartfire, Enchantment and Morgan are marked now: Destroyer, Brick
		// and Leclercq are all that is left to search
		if n := num(t, out["store_checked"], "store_checked"); n != 3 {
			t.Errorf("store_checked = %d, want the three unmarked books", n)
		}
		for _, title := range store.searched()[before:] {
			if title == "Zzyzx Heartfire" || title == "Zzyzx Enchantment" || strings.HasPrefix(title, "Zzyzx Gods (Morgan)") {
				t.Errorf("%s was searched after it was marked", title)
			}
		}
	})

	t.Run("windows of three walk every book once, the readings in the first", func(t *testing.T) {
		scanned := 0
		for offset, calls := 0, 0; ; calls++ {
			if calls > 3 {
				t.Fatal("the windows never end")
			}
			out := audit(t, map[string]any{"limit": 3, "offset": offset})
			scanned += num(t, out["items_scanned"], "items_scanned")
			if compared := num(t, out["readings_compared"], "readings_compared"); (offset == 0) != (compared == 3) {
				t.Errorf("offset %d compared %d readings, want 3 at offset 0 and none after", offset, compared)
			}
			if out["next_offset"] == nil {
				break
			}
			offset = num(t, out["next_offset"], "next_offset")
		}
		if scanned != len(seconds) {
			t.Errorf("the windows scanned %d books, want %d", scanned, len(seconds))
		}
	})
}
