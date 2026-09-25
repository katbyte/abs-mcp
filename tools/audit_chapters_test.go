package tools

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// chaptered is a whole book with its chapters, each running to the next's
// start and the last to end.
func chaptered(duration float64, starts ...float64) *abs.Item {
	it := &abs.Item{ID: "b1", MediaType: "book", RelPath: "A/Book", Media: abs.Media{Duration: duration, Metadata: abs.Metadata{Title: "Book"}}}
	for i, s := range starts {
		end := duration
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		it.Media.Chapters = append(it.Media.Chapters, abs.Chapter{ID: i, Title: "C" + string(rune('1'+i)), Start: s, End: end})
	}
	return it
}

// Each problem has a case that trips it and one just inside its tolerance
// that does not, so a threshold cannot drift into flagging every book or
// none.
func TestChapterProblems(t *testing.T) {
	t.Parallel()

	withEnd := func(it *abs.Item, i int, end float64) *abs.Item {
		it.Media.Chapters[i].End = end
		return it
	}
	const tenHours = 36000
	for _, tc := range []struct {
		name   string
		it     *abs.Item
		want   []string
		detail string // of the first row
	}{
		{"chapters that fit", chaptered(3600, 0, 1800), nil, ""},
		// a two-file book chaptered one per file, with its second file gone
		{
			"a chapter starting at the end", withEnd(chaptered(3000, 0, 3000), 1, 6000),
			[]string{"past_end"},
			`1 of 2 chapters start at or past the end of the audio at 3000s, from chapter 2, "C2", at 3000s`,
		},
		{
			"chapters starting past the end", chaptered(3000, 0, 1500, 4500, 6000),
			[]string{"past_end"},
			`2 of 4 chapters start at or past the end of the audio at 3000s, from chapter 3, "C3", at 4500s`,
		},
		{
			"the last running past the end", withEnd(chaptered(3000, 0, 2000), 1, 6000),
			[]string{"past_end"},
			`the last chapter, "C2", ends at 6000s, 3000s past the end of the audio at 3000s`,
		},
		{"the last a few seconds over", withEnd(chaptered(3600, 0, 1800), 1, 3604), nil, ""},
		// a two-file book chaptered, with a third file added
		{
			"audio after the last chapter", withEnd(chaptered(6000, 0, 1500), 1, 3000),
			[]string{"short"},
			`the last chapter, "C2", ends at 3000s, 3000s (50%) before the audio does at 6000s`,
		},
		{"a minute short", withEnd(chaptered(3600, 0, 1800), 1, 3550), nil, ""},
		{"a percent short of a long book", withEnd(chaptered(tenHours, 0, 1800), 1, tenHours-300), nil, ""},
		{"more than a percent short of a long book", withEnd(chaptered(tenHours, 0, 1800), 1, tenHours-400), []string{"short"}, ""},
		{
			"out of order", chaptered(3600, 0, 1800, 900),
			[]string{"out_of_order"},
			`chapter 3, "C3", starts at 900s, not after chapter 2, "C2", at 1800s`,
		},
		{"two at one start", chaptered(3600, 0, 0, 1800), []string{"out_of_order"}, ""},
		{
			"overlapping", withEnd(chaptered(3600, 0, 1800), 0, 2400),
			[]string{"out_of_order"},
			`chapter 1, "C1", runs to 2400s, past the start of chapter 2, "C2", at 1800s`,
		},
		{"rounding at a boundary", withEnd(chaptered(3600, 0, 1800), 0, 1800.4), nil, ""},
		{"one chapter over a long book", chaptered(9000, 0), []string{"single"}, `one chapter, "C1", over the whole 2h 30m`},
		{"one chapter over a short book", chaptered(3600, 0), nil, ""},
		{"one chapter over part of a long book", withEnd(chaptered(9000, 0), 0, 3000), []string{"short", "single"}, ""},
		{"no chapters", chaptered(9000), nil, ""},
		{"a podcast", &abs.Item{MediaType: "podcast", Media: abs.Media{Duration: 60, Chapters: []abs.Chapter{{Start: 120, End: 180}}}}, nil, ""},
	} {
		rows := chapterProblems(tc.it)
		var got []string
		for _, r := range rows {
			got = append(got, r.Problem)
			if r.Fix == "" || r.ID != tc.it.ID || r.Path != tc.it.RelPath {
				t.Errorf("%s: row %+v has no fix, or not the book's id and path", tc.name, r)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: problems %v, want %v (%+v)", tc.name, got, tc.want, rows)
			continue
		}
		if tc.detail != "" && rows[0].Detail != tc.detail {
			t.Errorf("%s: detail %q, want %q", tc.name, rows[0].Detail, tc.detail)
		}
	}
	for _, problem := range []string{"past_end", "short"} {
		if !strings.Contains(chapterFixes[problem], "fit=true") {
			t.Errorf("the fix for %s does not name fit: %q", problem, chapterFixes[problem])
		}
	}
}

// audit_chapters fetches the books with chapters and no others, reports them
// by problem, worst first, and audit_all counts what it reports.
func TestAuditChaptersFetchesOnlyChapteredBooks(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("b1", "Alpha", "", `"numChapters":1,"duration":9000`),
		item("b2", "Beta", "", `"numChapters":2,"duration":3000`),
		item("b3", "Gamma", "", `"numChapters":0,"duration":9000`),
		item("b4", "Delta", "", `"numChapters":2,"duration":3600`),
	))
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+
		item("b1", "Alpha", "", `"duration":9000,"chapters":[{"id":0,"start":0,"end":9000,"title":"Whole"}]`)+`,`+
		item("b2", "Beta", "", `"duration":3000,"chapters":[{"id":0,"start":0,"end":3000,"title":"01"},{"id":1,"start":3000,"end":6000,"title":"02"}]`)+`,`+
		item("b4", "Delta", "", `"duration":3600,"chapters":[{"id":0,"start":0,"end":1800,"title":"One"},{"id":1,"start":1800,"end":3600,"title":"Two"}]`)+
		`]}`)
	call := toolCaller(t, f)

	out, err := call("audit_chapters", nil)
	if err != nil {
		t.Fatal(err)
	}
	var asked struct {
		IDs []string `json:"libraryItemIds"`
	}
	reqs := f.requests("/api/items/batch/get")
	if len(reqs) != 1 || json.Unmarshal([]byte(reqs[0].Body), &asked) != nil || !slices.Equal(asked.IDs, []string{"b1", "b2", "b4"}) {
		t.Fatalf("fetched %v, want the three books with chapters in one request", reqs)
	}
	if num(t, out["items_scanned"]) != 4 || num(t, out["chapters_read"]) != 3 || num(t, out["total_findings"]) != 2 {
		t.Errorf("scanned %v, read %v, found %v; want 4, 3, 2", out["items_scanned"], out["chapters_read"], out["total_findings"])
	}
	rows := list(t, out["findings"])
	if len(rows) != 2 || rows[0]["id"] != "b2" || rows[0]["problem"] != "past_end" || rows[1]["id"] != "b1" || rows[1]["problem"] != "single" {
		t.Fatalf("findings = %v, want Beta past_end then Alpha single", rows)
	}
	if !strings.Contains(str(t, rows[0]["fix"]), "fit=true") || str(t, rows[0]["path"]) != "A/Beta" {
		t.Errorf("row = %v, want the fit fix and the path", rows[0])
	}
	counts, ok := out["counts"].(map[string]any)
	if !ok || num(t, counts["past_end"]) != 1 || num(t, counts["single"]) != 1 || num(t, counts["short"]) != 0 {
		t.Errorf("counts = %v", counts)
	}
	if limited, err := call("audit_chapters", map[string]any{"limit": 1}); err != nil || len(list(t, limited["findings"])) != 1 || num(t, limited["total_findings"]) != 2 {
		t.Errorf("limit 1 = %v, %v; want one row of two", limited, err)
	}

	// audit_all with deep reads what audit_chapters reads, and counts the same
	chaptersIn := func(all map[string]any) int {
		for _, row := range list(t, all["audits"]) {
			if row["audit"] == "audit_chapters" {
				return num(t, row["found"])
			}
		}
		return 0
	}
	fetched := len(f.requests("/api/items/batch/get"))
	all, err := call("audit_all", map[string]any{"deep": true})
	if err != nil {
		t.Fatal(err)
	}
	if found := chaptersIn(all); found != 2 {
		t.Errorf("audit_all deep counts audit_chapters %d, want the 2 it reports: %v", found, all["audits"])
	}

	// without deep it reads nothing more than the listing: the one chapter over
	// a long book is counted from that, and partial says what was left out
	fetched = len(f.requests("/api/items/batch/get")) - fetched
	before := len(f.requests("/api/items/batch/get"))
	if all, err = call("audit_all", nil); err != nil {
		t.Fatal(err)
	}
	if found := chaptersIn(all); found != 1 {
		t.Errorf("audit_all counts audit_chapters %d without deep, want the 1 one-chapter book", found)
	}
	if extra := len(f.requests("/api/items/batch/get")) - before; fetched == 0 || extra >= fetched {
		t.Errorf("audit_all read %d batches without deep and %d with it, want fewer without", extra, fetched)
	}
	partial := list(t, all["partial"])
	if len(partial) != 1 || partial[0]["audit"] != "audit_chapters" || !strings.Contains(str(t, partial[0]["reason"]), "deep") {
		t.Errorf("partial = %v, want audit_chapters and why", all["partial"])
	}
}

// fit keeps the book's own chapters: those past the end go, the last ends at
// the end of the audio. It is refused beside a list or an asin, and on a list
// out of order, before anything is sent.
func TestItemChaptersSetFit(t *testing.T) {
	t.Parallel()

	sent := func(f *fakeABS) []abs.Chapter {
		t.Helper()
		reqs := f.requests("/api/items/" + itemID + "/chapters")
		if len(reqs) == 0 {
			return nil
		}
		var body struct{ Chapters []abs.Chapter }
		if err := json.Unmarshal([]byte(reqs[len(reqs)-1].Body), &body); err != nil {
			t.Fatal(err)
		}
		return body.Chapters
	}
	book := func(chapters string) *fakeABS {
		f := newFakeABS(t)
		f.json("GET /api/items/"+itemID, item(itemID, "Dune", `"asin":"B0DUNE"`, `"duration":3000,"chapters":[`+chapters+`]`))
		f.json("POST /api/items/"+itemID+"/chapters", `{"success":true,"updated":true}`)
		return f
	}

	// a file gone from a book chaptered one per file, and a chapter straddling the end
	f := book(`{"id":0,"start":0,"end":1500,"title":"One"},{"id":1,"start":1500,"end":4500,"title":"Two"},{"id":2,"start":4500,"end":6000,"title":"Three"}`)
	out, err := toolCaller(t, f)("item_chapters_set", map[string]any{"item": itemID, "fit": true})
	if err != nil {
		t.Fatal(err)
	}
	want := []abs.Chapter{{ID: 0, Title: "One", Start: 0, End: 1500}, {ID: 1, Title: "Two", Start: 1500, End: 3000}}
	if got := sent(f); !slices.Equal(got, want) {
		t.Errorf("sent %+v, want %+v", got, want)
	}
	if num(t, out["chapters"]) != 2 || num(t, out["dropped"]) != 1 || num(t, out["end_s"]) != 3000 || !boolOf(t, out["updated"]) {
		t.Errorf("out = %v, want 2 chapters, 1 dropped, ending at 3000s", out)
	}

	// a track added: the last chapter runs on over it
	f = book(`{"id":0,"start":0,"end":1000,"title":"One"},{"id":1,"start":1000,"end":2000,"title":"Two"}`)
	out, err = toolCaller(t, f)("item_chapters_set", map[string]any{"item": itemID, "fit": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := sent(f); len(got) != 2 || got[1].End != 3000 || got[0].End != 1000 {
		t.Errorf("sent %+v, want the last ending at 3000", got)
	}
	if _, present := out["dropped"]; present {
		t.Errorf("dropped = %v when nothing was", out["dropped"])
	}

	f = book(`{"id":0,"start":0,"end":2000,"title":"One"},{"id":1,"start":1000,"end":3000,"title":"Two"}`)
	call := toolCaller(t, f)
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"fit": true, "chapters": []any{map[string]any{"title": "A", "start_s": 0}}}, "one of the three"},
		{map[string]any{"fit": true, "from_asin": "B0DUNE"}, "one of the three"},
		{map[string]any{"fit": true}, `chapter 2, "Two", at 1000s is out of order with chapter 1`},
	} {
		tc.args["item"] = itemID
		if _, err := call("item_chapters_set", tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
	if got := sent(f); got != nil {
		t.Errorf("a refused fit sent %+v", got)
	}
	if got := f.requests("/api/search/chapters"); len(got) != 0 {
		t.Errorf("fit with from_asin asked the store: %v", got)
	}

	f = book("")
	if _, err := toolCaller(t, f)("item_chapters_set", map[string]any{"item": itemID, "fit": true}); err == nil || !strings.Contains(err.Error(), "no chapters to fit") {
		t.Errorf("fit on a book with none: %v", err)
	}
}
