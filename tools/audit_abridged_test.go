package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// shelved is a minified book as the listing returns it, at a folder of its
// own, with extra metadata and media fields spliced in as raw JSON.
func shelved(id, path, title, author string, seconds float64, meta, media string) string {
	if meta != "" {
		meta = "," + meta
	}
	if media != "" {
		media = "," + media
	}
	return fmt.Sprintf(`{"id":%q,"libraryId":%q,"mediaType":"book","relPath":%q,"media":{"metadata":{"title":%q,"authorName":%q%s},"duration":%g%s}}`,
		id, libID, path, title, author, meta, seconds, media)
}

// readingOf is a book fetched whole, its chapters laid end to end from the
// titles and lengths given; the listing's copy carries only the count.
func readingOf(id, path, title, author string, titles []string, lengths []float64, meta string) (listed, whole string) {
	chapters := make([]string, 0, len(lengths))
	start := 0.0
	for i, l := range lengths {
		chapters = append(chapters, fmt.Sprintf(`{"id":%d,"start":%g,"end":%g,"title":%q}`, i, start, start+l, titles[i]))
		start += l
	}
	return shelved(id, path, title, author, start, meta, fmt.Sprintf(`"numChapters":%d`, len(lengths))),
		shelved(id, path, title, author, start, meta, `"chapters":[`+strings.Join(chapters, ",")+`]`)
}

// serveWhole answers the batch fetch with the books asked for, from whole.
func serveWhole(f *fakeABS, whole map[string]string) {
	f.mux.HandleFunc("POST /api/items/batch/get", func(w http.ResponseWriter, r *http.Request) {
		var asked struct {
			IDs []string `json:"libraryItemIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&asked)
		var out []string
		for _, id := range asked.IDs {
			if b, ok := whole[id]; ok {
				out = append(out, b)
			}
		}
		_, _ = fmt.Fprintf(w, `{"libraryItems":[%s]}`, strings.Join(out, ","))
	})
}

// searched is the titles a store was searched for, each with the store, in
// the order asked.
func searched(f *fakeABS) []string {
	reqs := f.requests("/api/search/books")
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		q, _ := url.ParseQuery(r.Query)
		out = append(out, q.Get("provider")+" "+q.Get("title"))
	}
	return out
}

// The cases from the zbooks sorting session against a store that answers
// them: Heartfire read by Nana Visitor is Audible's abridged edition to the
// minute and Enchantment 3% off its; The Shock Doctrine is 41% of the
// unabridged. A fast reader at 77%, a book the length of the unabridged, and
// the books already marked, stubs and ones tagged as having nothing to match
// are left alone, and the last four are not searched at all. A book's own
// store is asked first and the next only when it has no edition.
func TestAuditAbridgedAgainstTheStore(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	f.json("POST /api/items/batch/get", `{"libraryItems":[]}`)
	serveListing(f, libID,
		shelved("heartfire", "Orson Scott Card/Alvin Maker - 05 - Heartfire (Nana Visitor)", "Heartfire (Nana Visitor)", "Orson Scott Card", 21420, `"narratorName":"Nana Visitor"`, `"tags":["zz-provider:audible.ca"]`),
		shelved("enchantment", "Orson Scott Card/Enchantment (Alyssa Bresnahan)", "Enchantment", "Orson Scott Card", 23364, "", ""),
		shelved("shock", "politics/The Shock Doctrine", "The Shock Doctrine: The Rise of Disaster Capitalism, Part 1", "Naomi Klein", 32580, "", ""),
		shelved("destroyer", "Larry Niven/Ringworld Prequels - 03 - Destroyer of Worlds (Weiner)", "Destroyer of Worlds", "Larry Niven", 39420, "", ""),
		shelved("red", "Orson Scott Card/Alvin Maker - 02 - Red Prophet", "Red Prophet", "Orson Scott Card", 43992, "", ""),
		shelved("marked", "Stephen King/It", "It", "Stephen King", 20000, `"abridged":true`, ""),
		shelved("folder", "Isaac Asimov/The Gods Themselves (Morgan, Abridged)", "The Gods Themselves", "Isaac Asimov", 27972, "", ""),
		shelved("stub", "Isaac Asimov/Sample", "Sample", "Isaac Asimov", 30, "", ""),
		shelved("none", "Nobody/Homebrew", "Homebrew", "Nobody", 20000, "", `"tags":["zz-provider:none"]`),
		shelved("seventh", "Orson Scott Card/Alvin Maker - 01 - Seventh Son (Orson Scott Card)", "Seventh Son", "Orson Scott Card", 26028, "", ""),
	)
	store := map[string]string{
		"audible.ca Heartfire": `[{"title":"Heartfire","author":"Orson Scott Card","narrator":"Nana Visitor","asin":"B0HFABRIDG","duration":357,"abridged":true},
			{"title":"Heartfire","author":"Orson Scott Card","narrator":"Scott Brick","asin":"B0HFUNABRI","duration":726}]`,
		"audible Enchantment": `[{"title":"Enchantment","author":"Orson Scott Card","narrator":"Alyssa Bresnahan","asin":"B0ENABRIDG","duration":377,"abridged":true},
			{"title":"Enchantment","author":"Orson Scott Card","narrator":"Stefan Rudnicki","asin":"B0ENUNABRI","duration":1044}]`,
		"audible The Shock Doctrine":  `[{"title":"The Shock Doctrine","subtitle":"The Rise of Disaster Capitalism","author":"Naomi Klein","narrator":"Jennifer Van Dyck","asin":"B0SHOCKUNA","duration":1338}]`,
		"audible Destroyer of Worlds": `[{"title":"Destroyer of Worlds","author":"Larry Niven, Edward M. Lerner","narrator":"James","asin":"B0DESTROYR","duration":853}]`,
		"audible Red Prophet": `[{"title":"Red Prophet","author":"Orson Scott Card","narrator":"Scott Brick","asin":"B0REDUNABR","duration":733},
			{"title":"Red Prophet","author":"Orson Scott Card","narrator":"Someone","asin":"B0REDABRID","duration":360,"abridged":true}]`,
		"audible Seventh Son": `[{"title":"Seventh Son","author":"Orson Scott Card","narrator":"Scott Brick","asin":"B0SEVENTHU","duration":548}]`,
	}
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		asked := r.URL.Query().Get("provider") + " " + r.URL.Query().Get("title")
		for k, body := range store {
			if strings.HasPrefix(asked, k) {
				_, _ = w.Write([]byte(body))
				return
			}
		}
		_, _ = w.Write([]byte(`[]`))
	})
	call := toolCaller(t, f)

	out, err := call("audit_abridged", map[string]any{"providers": []any{"audible.ca", "audible"}})
	if err != nil {
		t.Fatal(err)
	}
	if num(t, out["items_scanned"]) != 10 || num(t, out["store_checked"]) != 6 || num(t, out["total_findings"]) != 3 || out["next_offset"] != nil {
		t.Errorf("scanned %v, searched %v, found %v, next %v; want 10, 6, 3 and no more", out["items_scanned"], out["store_checked"], out["total_findings"], out["next_offset"])
	}
	findings := list(t, out["findings"])
	if got := idsOf(t, out["findings"]); !slices.Equal(got, []string{"heartfire", "enchantment", "shock"}) {
		t.Fatalf("findings = %v, want Heartfire and Enchantment by length, then The Shock Doctrine", findings)
	}
	wantNumbers(t, "audit_abridged", out, map[string]float64{
		"findings.0.duration_s": 21420, "findings.0.edition.duration_s": 21420,
		"findings.1.duration_s": 23364, "findings.1.edition.duration_s": 22620,
		"findings.2.duration_s": 32580, "findings.2.edition.duration_s": 80280,
		"counts.abridged_length": 2, "counts.far_shorter": 1, "counts.uneven_chapters": 0,
	})
	for i, want := range []struct{ problem, asin, provider string }{
		{"abridged_length", "B0HFABRIDG", "audible.ca"},
		{"abridged_length", "B0ENABRIDG", "audible"},
		{"far_shorter", "B0SHOCKUNA", "audible"},
	} {
		row := findings[i]
		ed, ok := row["edition"].(map[string]any)
		if !ok || row["problem"] != want.problem || ed["asin"] != want.asin || ed["provider"] != want.provider || ed["abridged"] != (want.problem == "abridged_length") {
			t.Errorf("finding %d = %v, want %s against %s at %s", i, row, want.problem, want.asin, want.provider)
		}
		if !strings.HasPrefix(str(t, row["fix"]), "item_edit abridged=true") || str(t, row["path"]) == "" {
			t.Errorf("finding %d has fix %q and path %q", i, row["fix"], row["path"])
		}
	}
	if d := str(t, findings[2]["detail"]); !strings.Contains(d, "41% of the shortest unabridged edition") {
		t.Errorf("The Shock Doctrine detail = %q, want the share it is of the unabridged", d)
	}

	asked := searched(f)
	for _, title := range []string{"It", "The Gods Themselves", "Sample", "Homebrew"} {
		if slices.ContainsFunc(asked, func(a string) bool { return strings.HasSuffix(a, " "+title) }) {
			t.Errorf("%s was searched: %v", title, asked)
		}
	}
	count := func(title string) []string {
		var out []string
		for _, a := range asked {
			if strings.HasSuffix(a, " "+title) {
				out = append(out, a)
			}
		}
		return out
	}
	// Heartfire's tag names the Canadian store, which has it, and it is
	// searched without the reader its folder adds; Seventh Son is not there,
	// so the US store is asked next
	if got := count("Heartfire"); !slices.Equal(got, []string{"audible.ca Heartfire"}) {
		t.Errorf("Heartfire searched %v, want once at audible.ca, as Heartfire", got)
	}
	if got := count("Seventh Son"); !slices.Equal(got, []string{"audible.ca Seventh Son", "audible Seventh Son"}) {
		t.Errorf("Seventh Son searched %v, want audible.ca then audible", got)
	}
	if got := f.requests("/api/items/batch/get"); len(got) != 0 {
		t.Errorf("no book has chapters, yet %d were fetched whole", len(got))
	}

	// windows of four walk every book once, and together find the same
	var paged []string
	for offset, calls := 0, 0; ; calls++ {
		if calls > 3 {
			t.Fatal("the windows never end")
		}
		window, werr := call("audit_abridged", map[string]any{"providers": []any{"audible.ca", "audible"}, "limit": 4, "offset": offset})
		if werr != nil {
			t.Fatal(werr)
		}
		paged = append(paged, idsOf(t, window["findings"])...)
		if window["next_offset"] == nil {
			if n := num(t, window["items_scanned"]); n != 2 {
				t.Errorf("the last window scanned %d, want 2", n)
			}
			break
		}
		if next := num(t, window["next_offset"]); next != offset+4 {
			t.Errorf("next_offset %d from %d, want %d", next, offset, offset+4)
		}
		offset += 4
	}
	slices.Sort(paged)
	if !slices.Equal(paged, []string{"enchantment", "heartfire", "shock"}) {
		t.Errorf("the windows found %v, want the three", paged)
	}

	// audit_all deep counts what the audit reports, asking the library's own
	// store and each book's tagged one
	all, err := call("audit_all", map[string]any{"deep": true})
	if err != nil {
		t.Fatal(err)
	}
	found := -1
	for _, row := range list(t, all["audits"]) {
		if row["audit"] == "audit_abridged" {
			found = num(t, row["found"])
		}
	}
	if found != 3 {
		t.Errorf("audit_all deep counts audit_abridged %d, want 3: %v", found, all["audits"])
	}
}

// Two readings of one book compared chapter by chapter, with the cases'
// shapes: The Gods Themselves read abridged by Morgan against Brick and
// Leclercq, cut far more in some chapters than others; Destroyer of Worlds
// read fast by Weiner against James, 77% of the length at a steady pace. A
// book whose shorter reading is already marked is compared and not reported,
// two copies of one length are one recording chaptered two ways, and
// chapters named only by the book and a number say nothing of the text.
func TestAuditAbridgedReadings(t *testing.T) {
	t.Parallel()

	chapters := func(n int) []string {
		var out []string
		for i := 1; i <= n; i++ {
			out = append(out, fmt.Sprintf("Chapter %d", i))
		}
		return out
	}
	scaled := func(base []float64, by ...float64) []float64 {
		out := make([]float64, len(base))
		for i, b := range base {
			out[i] = math.Round(b * by[i])
		}
		return out
	}
	gods := []float64{1500, 1200, 1800, 1400, 1600, 1500, 1300, 1700}
	james := []float64{1500, 1300, 1700, 1400, 1600, 1200, 1500, 1800}
	ringworld := []string{"Sol", "Achilles", "Nessus", "Beowulf", "Louis", "Hindmost", "Ring", "Fleet"}
	numbered := func(format string, names []string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = fmt.Sprintf(format, i+1, n)
		}
		return out
	}
	tracks := func(n int) []string {
		var out []string
		for i := 1; i <= n; i++ {
			out = append(out, fmt.Sprintf("Heartfire %02d", i))
		}
		return out
	}
	var listed []string
	whole := map[string]string{}
	shelve := func(id, path, title, author string, titles []string, lengths []float64, meta string) {
		l, w := readingOf(id, path, title, author, titles, lengths, meta)
		listed = append(listed, l)
		whole[id] = w
	}
	shelve("brick", "Isaac Asimov/The Gods Themselves (Brick)", "The Gods Themselves (Brick)", "Isaac Asimov", chapters(8), gods, "")
	shelve("leclercq", "Isaac Asimov/The Gods Themselves (Leclercq)", "The Gods Themselves (Leclercq)", "Isaac Asimov", chapters(8), scaled(gods, 0.86, 0.84, 0.88, 0.85, 0.87, 0.86, 0.84, 0.88), "")
	shelve("morgan", "Isaac Asimov/The Gods Themselves (Morgan)", "The Gods Themselves (Morgan)", "Isaac Asimov", chapters(8), scaled(gods, 0.9, 0.5, 0.8, 0.45, 0.7, 0.85, 0.5, 0.75), "")
	shelve("james", "Larry Niven/Destroyer of Worlds (James)", "Destroyer of Worlds", "Larry Niven", numbered("%02d - %s", ringworld), james, "")
	shelve("weiner", "Larry Niven/Destroyer of Worlds (Weiner)", "Destroyer of Worlds", "Larry Niven", numbered("%02d: %s", ringworld), scaled(james, 0.77, 0.75, 0.79, 0.78, 0.75, 0.79, 0.76, 0.8), "")
	shelve("foundation", "Isaac Asimov/Foundation", "Foundation", "Isaac Asimov", chapters(8), gods, "")
	shelve("foundation-cut", "Isaac Asimov/Foundation (Abridged)", "Foundation", "Isaac Asimov", chapters(8), scaled(gods, 0.9, 0.5, 0.8, 0.45, 0.7, 0.85, 0.5, 0.75), "")
	shelve("split", "Liu Cixin/The Redemption of Time", "The Redemption of Time", "Baoshu", chapters(8), gods, "")
	shelve("single", "Liu Cixin/The Redemption of Time (single file)", "The Redemption of Time", "Baoshu", chapters(8), []float64{1000, 1700, 1300, 1900, 1100, 1800, 1400, 1800}, "")
	shelve("heartfire", "Orson Scott Card/Heartfire", "Heartfire", "Orson Scott Card", tracks(10), []float64{4000, 4000, 4000, 4000, 4000, 4000, 4000, 4000, 4000, 4000}, "")
	shelve("visitor", "Orson Scott Card/Heartfire (Nana Visitor)", "Heartfire (Nana Visitor)", "Orson Scott Card", tracks(5), []float64{4284, 4284, 4284, 4284, 4284}, "")

	f := newFakeABS(t)
	audibleLibrary(f)
	serveListing(f, listed...)
	serveWhole(f, whole)
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	out, err := call("audit_abridged", nil)
	if err != nil {
		t.Fatal(err)
	}
	// three pairs of The Gods Themselves, Destroyer of Worlds and Foundation
	if n := num(t, out["readings_compared"]); n != 5 {
		t.Errorf("readings_compared = %d, want 5", n)
	}
	findings := list(t, out["findings"])
	if len(findings) != 1 || findings[0]["id"] != "morgan" || findings[0]["problem"] != "uneven_chapters" {
		t.Fatalf("findings = %v, want Morgan's reading alone", findings)
	}
	row := findings[0]
	other, ok := row["other_reading"].(map[string]any)
	if !ok || other["id"] != "brick" || num(t, other["duration_s"]) != 12000 || num(t, row["duration_s"]) != 8340 || num(t, row["chapters_compared"]) != 8 {
		t.Errorf("Morgan = %v, want against Brick's 12000s over 8 chapters", row)
	}
	// section_ratio.py gives 2.03 for these chapters
	if sp, ok := row["spread"].(float64); !ok || sp != 2.03 {
		t.Errorf("spread = %v, want 2.03", row["spread"])
	}
	if str(t, row["fix"]) != "item_edit abridged=true" {
		t.Errorf("fix = %q", row["fix"])
	}
	// the books held more than once, fetched in one request
	var asked struct {
		IDs []string `json:"libraryItemIds"`
	}
	if reqs := f.requests("/api/items/batch/get"); len(reqs) != 1 || json.Unmarshal([]byte(reqs[0].Body), &asked) != nil || len(asked.IDs) != 11 {
		t.Errorf("fetched %v, want the eleven readings in one request", reqs)
	}

	// past offset 0 the readings are not compared again
	out, err = call("audit_abridged", map[string]any{"offset": 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(list(t, out["findings"])) != 0 || num(t, out["readings_compared"]) != 0 || len(f.requests("/api/items/batch/get")) != 1 {
		t.Errorf("offset 5 = %v, want no readings and nothing fetched", out)
	}
}

// The spread is section_ratio.py's: Python's default deciles, the 90th over
// the 10th, on chapters matched by title with a leading number stripped.
func TestChapterSpreadIsSectionRatios(t *testing.T) {
	t.Parallel()

	tenths := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if lo, hi := decile(tenths, 1), decile(tenths, 9); math.Abs(lo-1.1) > 1e-9 || math.Abs(hi-9.9) > 1e-9 {
		t.Errorf("deciles of 1..10 = %v, %v; statistics.quantiles gives 1.1 and 9.9", lo, hi)
	}
	book := func(titles []string, lengths []float64) *abs.Item {
		it := &abs.Item{}
		it.Media.Metadata.Title, it.Media.Metadata.AuthorName = "Zzyzx Gods", "Zzyzx Author"
		start := 0.0
		for i, l := range lengths {
			it.Media.Chapters = append(it.Media.Chapters, abs.Chapter{ID: i, Start: start, End: start + l, Title: titles[i]})
			start += l
		}
		it.Media.Duration = start
		return it
	}
	titles := []string{"1: Chapter One", "2. Chapter Two", "03 - Chapter Three", "Chapter Four", "Chapter Five", "Chapter Six"}
	brick := book(titles, []float64{300, 240, 360, 280, 320, 300})
	morgan := book([]string{"Chapter One", "Chapter Two", "Chapter Three", "Chapter Four", "Chapter Five", "Chapter Six"}, []float64{200, 100, 300, 120, 250, 150})
	sp, _, _, n, ok := chapterSpread(brick, morgan)
	if !ok || n != 6 || math.Abs(sp-2.0578) > 1e-4 {
		t.Errorf("spread = %v over %d (%v), section_ratio.py gives 2.0578 over 6", sp, n, ok)
	}
	if sp, _, _, _, ok := chapterSpread(brick, book(titles, []float64{255, 204, 306, 238, 272, 255})); !ok || math.Abs(sp-1) > 0.01 {
		t.Errorf("a steady reader's spread = %v (%v), want 1", sp, ok)
	}
	// a chapter under a minute on either side is not compared, and four are
	// too few
	if _, _, _, n, ok := chapterSpread(brick, book(titles, []float64{300, 240, 360, 280, 50, 50})); ok || n != 4 {
		t.Errorf("four chapters over a minute compared %d (%v), want not enough", n, ok)
	}
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"Chapter 12", true},
		{"12: Twelve", true},
		{"Opening Credits", true},
		{"Track 03", false},
		{"Disc 2 Track 4", false},
		{"Zzyzx Gods - 07", false},
		{"07", false},
		{"Part 3 of 5", false},
	} {
		if got := chapterKey(tc.title, map[string]bool{"zzyzx": true, "gods": true}) != ""; got != tc.want {
			t.Errorf("chapterKey(%q) kept %v, want %v", tc.title, got, tc.want)
		}
	}
}

// A book that already says so is not the audit's to report: the flag, or
// abridged in the title, subtitle or folder, and not unabridged. A
// dramatisation is shorter by design.
func TestSaysAbridged(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		title, subtitle, path string
		flag, want            bool
	}{
		{"Children of the Mind (Unabridged)", "", "Orson Scott Card/Children of the Mind (Unabridged)", false, false},
		{"The Gods Themselves", "", "Isaac Asimov/The Gods Themselves (Morgan, Abridged)", false, true},
		{"Science Fiction Favorites", "Abridged", "Isaac Asimov/Science Fiction Favorites", false, true},
		{"Heartfire", "", "Orson Scott Card/Heartfire (Nana Visitor)", true, true},
		{"Heartfire", "", "Orson Scott Card/Heartfire (Nana Visitor)", false, false},
		{"Dune (Dramatized Adaptation)", "", "Frank Herbert/Dune", false, true},
	} {
		it := &abs.Item{RelPath: tc.path}
		it.Media.Metadata.Title, it.Media.Metadata.Subtitle, it.Media.Metadata.Abridged = tc.title, tc.subtitle, tc.flag
		if got := saysAbridged(it); got != tc.want {
			t.Errorf("saysAbridged(%q, %q, %q, %v) = %v", tc.title, tc.subtitle, tc.path, tc.flag, got)
		}
	}
}

// A library whose store gives no lengths - google, where a new library is -
// is refused before anything is read or asked, naming the library and the
// fix, and so is a provider named that is not an Audible store; audit_all
// deep leaves audit_abridged out saying why.
func TestAuditAbridgedRefusesAStoreWithNoLengths(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"google"}]}`)
	f.json("GET /api/libraries/"+libID+"/items", page(shelved("b1", "A/Dune", "Dune", "Frank Herbert", 3600, "", "")))
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	if _, err := call("audit_abridged", nil); err == nil || !strings.Contains(err.Error(), `library "Books" is on the google provider, which gives no edition lengths`) || !strings.Contains(err.Error(), "--providers") {
		t.Errorf("on google: %v, want the library, its provider and the fix", err)
	}
	if _, err := call("audit_abridged", map[string]any{"providers": []any{"google"}}); err == nil || !strings.Contains(err.Error(), "google gives no edition lengths") {
		t.Errorf("providers=[google]: %v", err)
	}
	if got := f.requests("/api/libraries/" + libID + "/items"); len(got) != 0 {
		t.Errorf("a refused call listed the library: %v", got)
	}
	all, err := call("audit_all", map[string]any{"deep": true})
	if err != nil {
		t.Fatal(err)
	}
	var why string
	for _, row := range list(t, all["not_run"]) {
		if row["audit"] == "audit_abridged" {
			why = str(t, row["reason"])
		}
	}
	if !strings.Contains(why, `library "Books" is on the google provider`) {
		t.Errorf("audit_abridged not run because %q, want the library and its provider", why)
	}
	if got := f.requests("/api/search/books"); len(got) != 0 {
		t.Errorf("a store was asked: %v", got)
	}
	if out, err := call("audit_abridged", map[string]any{"providers": []any{"audible"}}); err != nil || num(t, out["store_checked"]) != 1 {
		t.Errorf("with providers: %v %v", out, err)
	}
}

// Only the book's own name says it is abridged: a shelf folder called
// "Abridged" above it says nothing about a book filed there by mistake.
func TestSaysAbridgedReadsTheBooksOwnName(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]bool{
		"Orson Scott Card/Heartfire (Abridged)":          true,
		"Orson Scott Card/Heartfire (Abridged).m4b":      true,
		"Abridged/Orson Scott Card/Heartfire":            false,
		"Orson Scott Card/Abridged Readings/Enchantment": false,
	} {
		it := &abs.Item{ID: "i", MediaType: "book", RelPath: path}
		it.Media.Metadata.Title = "Heartfire"
		if got := saysAbridged(it); got != want {
			t.Errorf("saysAbridged(%q) = %v, want %v", path, got, want)
		}
	}
}

// Two releases of one book, neither cut, that the zbooks sort scored uneven:
// Protector read by Sherman and by Weiner, 7.07h and 7.10h, and World of
// Ptavvs plain and read by Hastings, 6.83h and 5.92h, whose sections share
// names but split the book at different points (a middle ratio of 2.60
// against 1.15 for the whole). Neither is reported; the real cut beside
// them, 32% shorter and cut unevenly along its chapters, still is.
func TestAuditAbridgedReadingsThatDoNotLineUp(t *testing.T) {
	t.Parallel()

	names := []string{"Sol", "Achilles", "Nessus", "Beowulf", "Louis", "Hindmost", "Ring", "Fleet"}
	var listed []string
	whole := map[string]string{}
	shelve := func(id, path, title, author string, lengths []float64) {
		l, w := readingOf(id, path, title, author, names, lengths, "")
		listed = append(listed, l)
		whole[id] = w
	}
	// a few percent apart, and uneven section by section: one recording
	// split two ways, not a cut
	shelve("sherman", "Larry Niven/Protector (Sherman)", "Protector", "Larry Niven", []float64{3000, 3200, 3100, 3300, 3000, 3200, 3100, 3500})
	shelve("weiner", "Larry Niven/Protector (Weiner)", "Protector", "Larry Niven", []float64{2000, 4200, 2400, 4100, 2300, 4000, 2600, 4400})
	// 13% apart, the same names on sections cut at other points
	shelve("plain", "Larry Niven/World of Ptavvs", "World of Ptavvs", "Larry Niven", []float64{3000, 3000, 3000, 3000, 3000, 3000, 3000, 3000})
	shelve("hastings", "Larry Niven/World of Ptavvs (Hastings)", "World of Ptavvs", "Larry Niven", []float64{1000, 1100, 1200, 1000, 1300, 1100, 1200, 12000})
	// the real cut: a third shorter, each chapter cut by its own share
	shelve("brick", "Isaac Asimov/The Gods Themselves (Brick)", "The Gods Themselves", "Isaac Asimov", []float64{1500, 1200, 1800, 1400, 1600, 1500, 1300, 1700})
	shelve("morgan", "Isaac Asimov/The Gods Themselves (Morgan)", "The Gods Themselves", "Isaac Asimov", []float64{1350, 600, 1440, 630, 1120, 1275, 650, 1275})

	f := newFakeABS(t)
	audibleLibrary(f)
	serveListing(f, listed...)
	serveWhole(f, whole)
	f.json("GET /api/search/books", `[]`)
	call := toolCaller(t, f)

	out, err := call("audit_abridged", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(t, out["findings"]); !slices.Equal(got, []string{"morgan"}) {
		t.Errorf("findings = %v, want Morgan's cut alone", got)
	}
}
