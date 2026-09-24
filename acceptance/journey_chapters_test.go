//go:build integration

// A long book chaptered: the one journey with a book long enough for the
// chapter audits to have anything to say. A two-and-a-half-hour book is one
// silent m4b made at runtime (half a megabyte, about two seconds of ffmpeg),
// in a scratch library of its own beside a one-second book the audits must
// leave alone.
package acceptance

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// longBookChapters reads a book's chapters back as title@start pairs.
func longBookChapters(t *testing.T, id string) []string {
	t.Helper()

	var out []string
	for _, ch := range rows(t, call(t, "item_get", map[string]any{"item": id, "chapters": true})["chapter_list"], "chapter_list") {
		sec, _ := ch["start_s"].(float64)
		out = append(out, text(ch["title"])+"@"+time.Duration(sec*float64(time.Second)).String())
	}
	return out
}

// endsAt reports whether a chapter ends within a second of the given one: the
// end of the audio, as ffmpeg made it, is a fraction off the length asked for.
func endsAt(ch map[string]any, seconds int) bool {
	end, ok := ch["end_s"].(float64)
	return ok && end > float64(seconds-1) && end < float64(seconds+1)
}

// longBookStore plays Audnexus's chapter lookup for one asin through the
// provider proxy, noting the region of every request, so a journey can see
// which store a lookup asked and that a refused call asked nothing.
type longBookStore struct {
	asin     string
	chapters []map[string]any

	mu      sync.Mutex
	regions []string
}

func (a *longBookStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.regions = append(a.regions, r.URL.Query().Get("region"))
	a.mu.Unlock()

	if r.URL.Path != "/books/"+a.asin+"/chapters" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"asin": a.asin, "isAccurate": true, "region": r.URL.Query().Get("region"),
		"brandIntroDurationMs": 0, "brandOutroDurationMs": 0,
		"runtimeLengthMs": 9000000, "runtimeLengthSec": 9000, "chapters": a.chapters,
	})
}

func (a *longBookStore) asked() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.regions)
}

// A session putting chapters on a long book that has none. audit_missing
// chapters finds the book; one chapter over the whole of it is still
// unnavigable, which audit_chapters finds as single; a real list clears both.
// The lists a player could not seek by - a chapter at or past the end, one
// out of order, a list given with an asin to fetch instead - are refused
// before anything is sent, and the chapters read back unchanged. A lookup by
// asin asks the store the book's provider tag names. Finally the chapters are
// embedded into the m4b: an m4b takes no publisher tag, so the embed has to
// read clean with a publisher set. Beside it a book of two files has a file
// taken out and put back, which the server does not rebuild the chapters for:
// audit_chapters finds chapters past the end, then audio after the last one,
// and item_chapters_set fit clears each. It would catch either audit going
// blind (neither had found anything live before this), a refused list
// half-applied, a lookup that asks the US store for a Canadian book, an m4b
// reported as due an embed forever, and a moved file whose chapters nothing
// reports or clears.
func TestJourneyALongBookChaptered(t *testing.T) {
	const author = "Zzyzx Chapter Author"
	longPath, shortPath, twoPath := author+"/Zzyzx Unchaptered Book", author+"/Zzyzx Short Book", author+"/Zzyzx Two File Book"
	const long = 9000 // seconds: two and a half hours, over the audits' two
	const half = 1500 // seconds in each of the two-file book's files

	s := newDiskShelf(t, "Zzyzx Chapter Shelf", "zzyzx-chapters")
	made := time.Now()
	diskSilence(t, filepath.Join(s.root, longPath, "Zzyzx Unchaptered Book.m4b"), long)
	t.Logf("a %ds m4b took ffmpeg %v", long, time.Since(made).Round(time.Millisecond))
	s.write(t, shortPath+"/01.mp3")
	diskSilence(t, filepath.Join(s.root, twoPath, "01.m4b"), half)
	diskSilence(t, filepath.Join(s.root, twoPath, "02.m4b"), half)
	s.open(t, 3)
	id, two := s.ids(t)[longPath], s.ids(t)[twoPath]

	book := call(t, "item_get", map[string]any{"item": id})
	if d := num(t, book["duration_s"], "duration_s"); d < long-1 || d > long+1 || book["chapters"] != nil || num(t, book["audio_tracks"], "audio_tracks") != 1 {
		t.Fatalf("the long book reads as %vs long with %v chapters in %v tracks, want %ds, none, one file", book["duration_s"], book["chapters"], book["audio_tracks"], long)
	}
	audits := func(t *testing.T) (missing, chapters []map[string]any) {
		t.Helper()
		return rows(t, call(t, "audit_missing", map[string]any{"library": s.name, "field": "chapters"})["findings"], "findings"),
			rows(t, call(t, "audit_chapters", map[string]any{"library": s.name})["findings"], "findings")
	}
	bothClear := func(t *testing.T) {
		t.Helper()
		if missing, chapters := audits(t); len(missing)+len(chapters) != 0 {
			t.Errorf("audit_missing chapters = %v, audit_chapters = %v; want both clear", missing, chapters)
		}
	}

	t.Run("none: audit_missing finds it", func(t *testing.T) {
		missing, chapters := audits(t)
		// the short book has none either, and is too short to matter
		if len(missing) != 1 || missing[0]["id"] != id || missing[0]["detail"] != "one unchaptered 2h 30m file: no way to navigate it" {
			t.Errorf("audit_missing chapters = %v, want the long book alone", missing)
		}
		// the two-file book the server chaptered one per file, which is fine
		if len(chapters) != 0 {
			t.Errorf("audit_chapters = %v, want nothing yet", chapters)
		}
	})

	t.Run("one over the whole book: audit_chapters finds it single", func(t *testing.T) {
		out := call(t, "item_chapters_set", map[string]any{"item": id, "chapters": []any{map[string]any{"title": "Zzyzx Whole Book", "start_s": 0}}})
		if num(t, out["chapters"], "chapters") != 1 || out["updated"] != true {
			t.Errorf("item_chapters_set = %v", out)
		}
		chapters := rows(t, call(t, "item_get", map[string]any{"item": id, "chapters": true})["chapter_list"], "chapter_list")
		if len(chapters) != 1 || !endsAt(chapters[0], long) {
			t.Errorf("chapters = %v, want one running to the end", chapters)
		}
		missing, found := audits(t)
		if len(missing) != 0 {
			t.Errorf("audit_missing chapters = %v once it has one", missing)
		}
		if len(found) != 1 || found[0]["id"] != id || found[0]["problem"] != "single" || found[0]["detail"] != `one chapter, "Zzyzx Whole Book", over the whole 2h 30m` {
			t.Errorf("audit_chapters = %v, want the long book as single", found)
		}
	})

	parts := []any{
		map[string]any{"title": "Zzyzx Part One", "start_s": 0},
		map[string]any{"title": "Zzyzx Part Two", "start_s": 1800},
		map[string]any{"title": "Zzyzx Part Three", "start_s": 3600},
		map[string]any{"title": "Zzyzx Part Four", "start_s": 5400},
		map[string]any{"title": "Zzyzx Part Five", "start_s": 7200},
	}
	wantParts := []string{"Zzyzx Part One@0s", "Zzyzx Part Two@30m0s", "Zzyzx Part Three@1h0m0s", "Zzyzx Part Four@1h30m0s", "Zzyzx Part Five@2h0m0s"}

	t.Run("a real list clears both", func(t *testing.T) {
		if n := num(t, call(t, "item_chapters_set", map[string]any{"item": id, "chapters": parts})["chapters"], "chapters"); n != 5 {
			t.Errorf("chapters = %d, want 5", n)
		}
		if got := longBookChapters(t, id); !slices.Equal(got, wantParts) {
			t.Errorf("chapters read back = %v, want %v", got, wantParts)
		}
		chapters := rows(t, call(t, "item_get", map[string]any{"item": id, "chapters": true})["chapter_list"], "chapter_list")
		if last := chapters[len(chapters)-1]; !endsAt(last, long) {
			t.Errorf("the last chapter ends at %vs, want the end of the book", last["end_s"])
		}
		bothClear(t)
	})

	t.Run("lists a player cannot seek by, refused", func(t *testing.T) {
		requireProviders(t)
		// the store is played here, so a refused call that asked it anyway shows
		store := &longBookStore{asin: "B0ZZYZX001"}
		t.Cleanup(proxy.Serve("api.audnex.us", store))

		ch := func(title string, start float64) map[string]any {
			return map[string]any{"title": title, "start_s": start}
		}
		for _, c := range []struct {
			name string
			args map[string]any
			want string
		}{
			{"a start a second past the end", map[string]any{"chapters": []any{ch("Zzyzx A", 0), ch("Zzyzx B", long+1)}}, "at or past the end"},
			{"a start an hour past the end", map[string]any{"chapters": []any{ch("Zzyzx A", 0), ch("Zzyzx B", 3600), ch("Zzyzx C", long+3600)}}, "at or past the end"},
			{"out of order", map[string]any{"chapters": []any{ch("Zzyzx A", 0), ch("Zzyzx B", 3600), ch("Zzyzx C", 1800)}}, "not after chapter 2"},
			{"two at one start", map[string]any{"chapters": []any{ch("Zzyzx A", 0), ch("Zzyzx B", 0)}}, "not after chapter 1"},
			{"before the audio", map[string]any{"chapters": []any{ch("Zzyzx A", -5)}}, "before the audio"},
			{"a list and an asin", map[string]any{"chapters": []any{ch("Zzyzx A", 0)}, "from_asin": store.asin}, "one or the other"},
		} {
			args := map[string]any{"item": id}
			maps.Copy(args, c.args)
			if msg := callErr(t, "item_chapters_set", args); !strings.Contains(msg, c.want) {
				t.Errorf("%s: %s, want it refused as %q", c.name, msg, c.want)
			}
		}
		if got := longBookChapters(t, id); !slices.Equal(got, wantParts) {
			t.Errorf("after the refusals the chapters are %v, want them unchanged", got)
		}
		if asked := store.asked(); len(asked) != 0 {
			t.Errorf("a refused call asked the store %d times", len(asked))
		}
		bothClear(t)
	})

	t.Run("by asin, from the store the provider tag names", func(t *testing.T) {
		requireProviders(t)
		store := &longBookStore{asin: "B0ZZYZX001", chapters: []map[string]any{
			{"title": "Zzyzx Opening Credits", "startOffsetMs": 0, "startOffsetSec": 0, "lengthMs": 60000},
			{"title": "Zzyzx Chapter 1", "startOffsetMs": 60000, "startOffsetSec": 60, "lengthMs": 4440000},
			{"title": "Zzyzx Chapter 2", "startOffsetMs": 4500000, "startOffsetSec": 4500, "lengthMs": 4500000},
		}}
		t.Cleanup(proxy.Serve("api.audnex.us", store))

		// matched from the Canadian store, as the collection's books are
		call(t, "item_edit", map[string]any{"item": id, "asin": store.asin, "add_tags": []any{"zz-provider:audible.ca"}})
		out := call(t, "item_chapters_set", map[string]any{"item": id})
		if out["region"] != "ca" || num(t, out["chapters"], "chapters") != 3 {
			t.Errorf("item_chapters_set = %v, want the three chapters from ca", out)
		}
		if asked := store.asked(); !slices.Equal(asked, []string{"ca"}) {
			t.Errorf("the store was asked for regions %v, want ca once", asked)
		}
		want := []string{"Zzyzx Opening Credits@0s", "Zzyzx Chapter 1@1m0s", "Zzyzx Chapter 2@1h15m0s"}
		if got := longBookChapters(t, id); !slices.Equal(got, want) {
			t.Errorf("chapters read back = %v, want %v", got, want)
		}
		bothClear(t)
	})

	t.Run("embedded into the m4b, publisher and all", func(t *testing.T) {
		before := longBookChapters(t, id)
		call(t, "item_edit", map[string]any{"item": id, "publisher": "Zzyzx Press", "year": "2001", "genres": []any{"Zzyzx Genre"}})
		if _, listed := unembedded(t, s.name, id); !listed {
			t.Fatal("audit_unembedded does not list a book that was never embedded")
		}
		// the server writes an m4b's publisher as its copyright, which a scan
		// does not read back as the publisher; it must not count as stale
		embed(t, s.name, id, map[string]any{"item": id})
		if got := longBookChapters(t, id); !slices.Equal(got, before) {
			t.Errorf("chapters after the embed and its rescan = %v, want %v", got, before)
		}
		if got := call(t, "item_get", map[string]any{"item": id})["publisher"]; got != "Zzyzx Press" {
			t.Errorf("publisher after the embed = %v", got)
		}
	})

	// the two-file book's rows in audit_chapters, which reports nothing else
	twoRows := func(t *testing.T) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, r := range rows(t, call(t, "audit_chapters", map[string]any{"library": s.name})["findings"], "findings") {
			if r["id"] != two {
				t.Errorf("audit_chapters reports another book: %v", r)
				continue
			}
			out = append(out, r)
		}
		return out
	}
	twoChapters := func(t *testing.T) []map[string]any {
		t.Helper()
		return rows(t, call(t, "item_get", map[string]any{"item": two, "chapters": true})["chapter_list"], "chapter_list")
	}
	// rescan scans the shelf and waits for the two-file book to hold files
	rescan := func(t *testing.T, files int) {
		t.Helper()
		s.scan(t, false)
		diskUntil(t, fmt.Sprintf("the two-file book scanned with %d files", files), func() (bool, string) {
			got := num(t, call(t, "item_get", map[string]any{"item": two})["audio_tracks"], "audio_tracks")
			return got == files, fmt.Sprintf("%d files", got)
		})
	}
	second := filepath.Join(s.root, twoPath, "02.m4b")
	away := filepath.Join(s.root+"-away", "02.m4b") // outside the library, until it is put back
	if err := os.MkdirAll(filepath.Dir(away), 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(away)) })

	t.Run("a file taken out: chapters past the end, and fit", func(t *testing.T) {
		sides := []any{
			map[string]any{"title": "Zzyzx Side A", "start_s": 0},
			map[string]any{"title": "Zzyzx Side B", "start_s": 600},
			map[string]any{"title": "Zzyzx Side C", "start_s": 1800},
			map[string]any{"title": "Zzyzx Side D", "start_s": 2400},
		}
		if n := num(t, call(t, "item_chapters_set", map[string]any{"item": two, "chapters": sides})["chapters"], "chapters"); n != 4 {
			t.Fatalf("chapters = %d, want 4", n)
		}
		if err := os.Rename(second, away); err != nil {
			t.Fatal(err)
		}
		rescan(t, 1)

		// the server keeps all four over the one file left
		if got := twoChapters(t); len(got) != 4 {
			t.Fatalf("with a file gone the book has chapters %v, want the four it had", got)
		}
		found := twoRows(t)
		if len(found) != 1 || found[0]["problem"] != "past_end" ||
			!strings.Contains(text(found[0]["detail"]), `2 of 4 chapters start at or past the end of the audio at`) ||
			!strings.Contains(text(found[0]["detail"]), `from chapter 3, "Zzyzx Side C", at 1800s`) || !strings.Contains(text(found[0]["fix"]), "fit=true") {
			t.Fatalf("audit_chapters = %v, want the two-file book past the end, fixed by fit", found)
		}

		out := call(t, "item_chapters_set", map[string]any{"item": two, "fit": true})
		if num(t, out["chapters"], "chapters") != 2 || num(t, out["dropped"], "dropped") != 2 || out["updated"] != true {
			t.Errorf("fit = %v, want two kept and two dropped", out)
		}
		got := twoChapters(t)
		if len(got) != 2 || got[0]["title"] != "Zzyzx Side A" || got[1]["title"] != "Zzyzx Side B" || !endsAt(got[1], half) {
			t.Errorf("chapters after fit = %v, want A and B, B ending with the one file", got)
		}
		if found := twoRows(t); len(found) != 0 {
			t.Errorf("audit_chapters after fit = %v, want it clear", found)
		}
	})

	t.Run("a track added: chapters short, and fit", func(t *testing.T) {
		if err := os.Rename(away, second); err != nil {
			t.Fatal(err)
		}
		rescan(t, 2)

		// the server keeps the two chapters over the first file alone
		if got := twoChapters(t); len(got) != 2 || !endsAt(got[1], half) {
			t.Fatalf("with the file back the book has chapters %v, want the two fit left", got)
		}
		found := twoRows(t)
		if len(found) != 1 || found[0]["problem"] != "short" || !strings.Contains(text(found[0]["detail"]), `the last chapter, "Zzyzx Side B", ends at`) {
			t.Fatalf("audit_chapters = %v, want the two-file book short", found)
		}

		out := call(t, "item_chapters_set", map[string]any{"item": two, "fit": true})
		if num(t, out["chapters"], "chapters") != 2 || out["dropped"] != nil || out["updated"] != true {
			t.Errorf("fit = %v, want both kept and nothing dropped", out)
		}
		if got := twoChapters(t); len(got) != 2 || !endsAt(got[1], 2*half) {
			t.Errorf("chapters after fit = %v, want B running to the end of both files", got)
		}
		if found := twoRows(t); len(found) != 0 {
			t.Errorf("audit_chapters after fit = %v, want it clear", found)
		}
	})
}
