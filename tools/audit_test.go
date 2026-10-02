package tools

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// A podcast library's listing ignores the missing filters and answers with
// every podcast, so a filtered count there was every podcast in it.
func TestAuditMissingSweepsAPodcastLibrary(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+podLibID+`","name":"Pods","mediaType":"podcast"}]}`)
	f.json("GET /api/libraries/"+podLibID+"/items", `{"results":[`+
		`{"id":"p1","libraryId":"`+podLibID+`","mediaType":"podcast","media":{"coverPath":"/c1.jpg","metadata":{"title":"Pod One","author":"A","genres":["News"],"language":"en"}}},`+
		`{"id":"p2","libraryId":"`+podLibID+`","mediaType":"podcast","media":{"metadata":{"title":"Pod Two","author":"B","genres":["Comedy"],"language":"en"}}}`+
		`],"total":2}`)
	call := toolCaller(t, f)

	for field, want := range map[string]int{"cover": 1, "author": 0, "genres": 0, "language": 0} {
		out, err := call("audit_missing", map[string]any{"field": field})
		if err != nil {
			t.Fatal(err)
		}
		if got := num(t, out["total_findings"]); got != want || len(list(t, out["findings"])) != want {
			t.Errorf("%s: total_findings = %d, findings = %v, want %d", field, got, out["findings"], want)
		}
	}
	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list(t, all["audits"]) {
		if field := str(t, row["field"]); str(t, row["audit"]) == "audit_missing" && slices.Contains([]string{"author", "genres", "language"}, field) {
			t.Errorf("audit_all reports %v in a library where every podcast has it", row)
		}
	}
}

// norm kept only what it could fold to ASCII, so a Russian or Japanese name
// was nothing at all: every such folder disagreed with its title, and two
// Japanese tags read as one.
func TestNormKeepsOtherScripts(t *testing.T) {
	t.Parallel()

	it := &abs.Item{MediaType: "book", RelPath: "Михаил Булгаков/Мастер и Маргарита"}
	it.Media.Metadata.Title = "Мастер и Маргарита"
	it.Media.Metadata.AuthorName = "Михаил Булгаков"
	if detail, suspect := checkPath(it); suspect {
		t.Errorf("a Cyrillic folder that names its book: %s", detail)
	}

	c := newSpellingCounts([]string{"tags", "narrators"})
	c.addValue("tags", "SF小説", 1)
	c.addValue("tags", "SF映画", 1)
	if got := c.report("tags"); len(got) != 0 {
		t.Errorf("two Japanese tags grouped: %+v", got)
	}
	c.addValue("narrators", "Read by Иван Петров", 1)
	got := c.report("narrators")
	if len(got) != 1 || got[0].Kind != "affix" || got[0].Keep != "Иван Петров" {
		t.Errorf("a Cyrillic narrator behind 'Read by' = %+v, want an affix group keeping the name", got)
	}
}

// lastEpisodeCheck is stamped on every check whatever it finds, so a show
// that ended a year ago and is checked daily was never stale.
func TestStaleFeedReadsTheNewestEpisode(t *testing.T) {
	t.Parallel()

	nowMs := time.Now().UnixMilli()
	oldMs := time.Now().Add(-200 * 24 * time.Hour).UnixMilli()
	pod := func(id string, check int64, episodes ...int64) string {
		eps := make([]string, 0, len(episodes))
		for i, at := range episodes {
			eps = append(eps, fmt.Sprintf(`{"id":"%s-e%d","publishedAt":%d}`, id, i, at))
		}
		return fmt.Sprintf(`{"id":%q,"libraryId":%q,"mediaType":"podcast","media":{"metadata":{"title":%q,"feedUrl":"http://feed/%s"},"lastEpisodeCheck":%d,"numEpisodes":%d,"episodes":[%s]}}`,
			id, podLibID, id, id, check, len(episodes), strings.Join(eps, ","))
	}
	minified := func(id string, check int64, n int) string {
		return fmt.Sprintf(`{"id":%q,"libraryId":%q,"mediaType":"podcast","media":{"metadata":{"title":%q,"feedUrl":"http://feed/%s"},"lastEpisodeCheck":%d,"numEpisodes":%d}}`,
			id, podLibID, id, id, check, n)
	}
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+podLibID+`","name":"Pods","mediaType":"podcast"}]}`)
	f.json("GET /api/libraries/"+podLibID+"/items", page(minified("ended", nowMs, 2), minified("going", nowMs, 1), minified("unchecked", 0, 1), minified("empty", nowMs, 0)))
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+pod("ended", nowMs, oldMs-1000, oldMs)+","+pod("going", nowMs, nowMs)+`]}`)
	call := toolCaller(t, f)

	out, err := call("audit_podcasts", nil)
	if err != nil {
		t.Fatal(err)
	}
	detail, problem := map[string]string{}, map[string]string{}
	for _, row := range list(t, out["findings"]) {
		detail[str(t, row["id"])], problem[str(t, row["id"])] = str(t, row["detail"]), str(t, row["problem"])
	}
	// each show is looked at once, though two checks run over them
	if num(t, out["items_scanned"]) != 4 || num(t, out["total_findings"]) != 3 {
		t.Errorf("scanned %v and found %v, want the 4 shows once and 3 findings", out["items_scanned"], out["total_findings"])
	}
	if len(detail) != 3 || !strings.HasPrefix(detail["ended"], "no new episode in 200 days") || detail["unchecked"] != "feed never checked" || detail["empty"] != "no episodes downloaded" {
		t.Errorf("findings = %v, want the ended show, the unchecked one and the empty one", detail)
	}
	// both checks answer in one list, each finding saying which it is
	if problem["ended"] != "stale_feed" || problem["unchecked"] != "stale_feed" || problem["empty"] != "no_episodes" {
		t.Errorf("problems = %v", problem)
	}
	if batches := f.requests("/api/items/batch/get"); len(batches) != 1 || strings.Contains(batches[0].Body, "unchecked") {
		t.Errorf("fetched %v, want the two checked shows only", batches)
	}
	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list(t, all["audits"]) {
		if str(t, row["audit"]) == "audit_podcasts" && num(t, row["found"]) != 3 {
			t.Errorf("audit_all counts %v podcast findings, want the 3 audit_podcasts lists", row["found"])
		}
	}
}

// A description that opens with a credit is a credit line only when that is
// about all it is.
func TestStubDescriptionLongCredit(t *testing.T) {
	t.Parallel()

	if detail, stub := stubDescription("Introduction by Neil Gaiman. " + strings.Repeat("In the plague year a clerk keeps a ledger of the dead, street by street. ", 5)); stub {
		t.Errorf("a long description with a credit in front: %s", detail)
	}
	if _, stub := stubDescription("Read by Paul Heck and a full cast of actors from the original stage production of the play"); !stub {
		t.Error("a short credit line passed")
	}
}

// Ties broke on map order, so two runs over the same library read
// differently.
func TestAuditOrderIsStable(t *testing.T) {
	t.Parallel()

	c := newGenresCollector()
	for i, g := range []string{"Thriller", "Horror", "Romance", "Fantasy", "Mystery"} {
		it := &abs.Item{ID: strconv.Itoa(i), MediaType: "book"}
		it.Media.Metadata.Genres = []string{g}
		it.Media.Tags = []string{g}
		c.add(it)
	}
	redundant := c.findings(1, 50).Redundant
	order := make([]string, 0, len(redundant))
	for _, r := range redundant {
		order = append(order, r.Value)
	}
	if !slices.Equal(order, []string{"Fantasy", "Horror", "Mystery", "Romance", "Thriller"}) {
		t.Errorf("redundant = %v, want alphabetical among equals", order)
	}

	// the same folder in two libraries
	n := newNumberingCollector()
	n.add(numbered("z", "Series/Series - 01 - One", "Series", "01"))
	n.add(numbered("a", "Series/Series - 01 - One", "Series", "01"))
	var ids []string
	for _, f := range n.findings() {
		if f.Problem == "padding" {
			ids = append(ids, f.ID)
		}
	}
	if !slices.Equal(ids, []string{"a", "z"}) {
		t.Errorf("padding rows %v, want by id", ids)
	}
}

// The descriptions promised what the code did not do.
func TestAuditDescriptionsSayWhatTheCodeDoes(t *testing.T) {
	t.Parallel()

	r := &registry{opts: Options{EnableDelete: true}}
	queueTools(r)
	desc := map[string]string{}
	for _, p := range r.pending {
		desc[p.name] = p.description
	}
	for tool, gone := range map[string]string{
		"audit_unembedded": "not part of audit_all",
		"audit_series":     "unlike the rest",
		"audit_chapters":   "spans the whole recording",
	} {
		if strings.Contains(desc[tool], gone) {
			t.Errorf("%s still says %q", tool, gone)
		}
	}
	if !strings.Contains(desc["audit_series"], "fixed convention") || !strings.Contains(desc["audit_podcasts"], "newest episode") {
		t.Error("the padding convention or the stale feed's measure is not described")
	}
	if !strings.Contains(desc["audit_unmatched"], "--provider-tag") {
		t.Error("audit_unmatched names zz-provider:none as if the prefix were fixed")
	}
	tag := func(v any, field string) string {
		f, _ := reflect.TypeOf(v).FieldByName(field)
		return f.Tag.Get("jsonschema")
	}
	if !strings.Contains(tag(seriesOut{}, "Found"), "titles") || !strings.Contains(tag(coversOut{}, "Skipped"), "fetch that failed") {
		t.Error("total_findings or skipped still leaves something out")
	}
}

// audit_all called a book audit clean over podcasts, and the podcast audits
// clean over books, and gave no reason for anything it left out.
func TestAuditAllSaysWhatDoesNotApply(t *testing.T) {
	t.Parallel()

	pods := newFakeABS(t)
	pods.json("GET /api/libraries", `{"libraries":[{"id":"`+podLibID+`","name":"Pods","mediaType":"podcast"}]}`)
	pods.json("GET /api/libraries/"+podLibID+"/items", page())
	out, err := toolCaller(t, pods)("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	na := strs(t, out["not_applicable"])
	clean := strs(t, out["clean"])
	for _, name := range []string{"audit_unmatched", "audit_missing narrator", "audit_series", "audit_covers", "audit_path"} {
		if !slices.Contains(na, name) || slices.Contains(clean, name) {
			t.Errorf("%s over podcasts: not_applicable %v, clean %v", name, na, clean)
		}
	}
	for _, name := range []string{"audit_podcasts", "audit_missing cover", "audit_duplicates"} {
		if !slices.Contains(clean, name) {
			t.Errorf("%s should run over podcasts: clean %v", name, clean)
		}
	}
	if _, present := out["skipped"]; present {
		t.Errorf("skipped %v: the deep audits do not apply to podcasts", out["skipped"])
	}
	reasons(t, out, len(na))

	books := newFakeABS(t)
	oneLibrary(books)
	books.json("GET /api/libraries/"+libID+"/items", page())
	books.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	books.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	out, err = toolCaller(t, books)("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	if na := strs(t, out["not_applicable"]); !slices.Equal(na, []string{"audit_podcasts"}) {
		t.Errorf("not_applicable over books = %v", na)
	}
	if skipped := strs(t, out["skipped"]); !slices.Equal(skipped, []string{"audit_covers", "audit_unembedded", "audit_matched", "audit_abridged", "audit_unplayable"}) {
		t.Errorf("skipped = %v", skipped)
	}
	reasons(t, out, 6)
}

// reasons checks audit_all gave every audit it left out a reason.
func reasons(t *testing.T, out map[string]any, want int) {
	t.Helper()

	rows := list(t, out["not_run"])
	if len(rows) != want {
		t.Errorf("not_run has %d rows, want %d: %v", len(rows), want, rows)
	}
	for _, row := range rows {
		if str(t, row["reason"]) == "" {
			t.Errorf("no reason for %v", row)
		}
	}
}

// Every audit answers how many items it looked at and how many findings.
func TestEveryAuditAnswersScannedAndFound(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(item("i1", "Dune", "", "")))
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	call := toolCaller(t, f)

	for _, tool := range []string{"audit_duplicates", "audit_series", "audit_covers"} {
		out, err := call(tool, nil)
		if err != nil {
			t.Fatal(err)
		}
		if num(t, out["items_scanned"]) != 1 {
			t.Errorf("%s: items_scanned = %v", tool, out["items_scanned"])
		}
		if _, ok := out["total_findings"]; !ok {
			t.Errorf("%s has no total_findings", tool)
		}
	}
}

// A huge limit went to the server as one request for that many rows.
func TestAuditLimitIsCapped(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(item("i1", "Dune", "", "")))
	if _, err := toolCaller(t, f)("audit_missing", map[string]any{"field": "cover", "limit": 1000000}); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests("/api/libraries/" + libID + "/items") {
		if strings.Contains(r.Query, "filter=") && !strings.Contains(r.Query, "limit=1000&") {
			t.Errorf("filtered request %q, want limit=1000", r.Query)
		}
	}
}

// A worklist stays at its limit across libraries, and the request for a
// library whose findings are not wanted still asks for one row rather than
// no limit at all, which the server reads as everything.
func TestAuditMissingLimitHoldsAcrossLibraries(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+otherLibID+`","name":"B","mediaType":"book"}]}`)
	f.json("GET /api/libraries/"+libID+"/items", `{"results":[`+item("a1", "One", "", "")+`],"total":3}`)
	f.json("GET /api/libraries/"+otherLibID+"/items", `{"results":[`+item("b1", "Two", "", "")+`],"total":2}`)
	call := toolCaller(t, f)

	out, err := call("audit_missing", map[string]any{"field": "cover", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["total_findings"]); got != 5 {
		t.Errorf("total_findings = %d, want 3+2", got)
	}
	if got := list(t, out["findings"]); len(got) != 1 {
		t.Errorf("findings = %d, want the limit of 1", len(got))
	}
	// the second library is asked twice: once for its size, once filtered,
	// and the filtered request carries the limit
	var filtered []request
	for _, r := range f.requests("/api/libraries/" + otherLibID + "/items") {
		if strings.Contains(r.Query, "filter=") {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) != 1 || !strings.Contains(filtered[0].Query, "limit=1") {
		t.Errorf("second library asked with %v, want one filtered request with limit=1", filtered)
	}
}

// audit_all agrees with the individual tools on the same library, covers the
// cross-item audits too, and names the two it leaves out unless asked to go
// deep.
func TestAuditAllMatchesTheAudits(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	audibleLibrary(f) // a library whose own store can look its asins up
	f.json("GET /api/search/books", `[]`)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Dune", `"authorName":"Frank Herbert","asin":"B0","genres":["Sci-Fi"]`, `"coverPath":"/c.jpg","numTracks":1,"numAudioFiles":1`),
		item("i2", "Neuromancer", `"authorName":"Neuromancer"`, ""),
		item("i3", "Dune", `"authorName":"Frank Herbert","asin":"b0","genres":["sci fi"]`, `"coverPath":"/c.jpg","numTracks":1,"numAudioFiles":1`),
	))
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[{"id":"a1","name":"Frank Herbert","numBooks":2}],"total":1}`)
	tiny := pngOf(t, 100, 100)
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, tiny)
	})
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+item("i1", "Dune", "", `"audioFiles":[{"index":1,"metaTags":{}}]`)+`]}`)
	call := toolCaller(t, f)

	counts := func(out map[string]any) map[string]int {
		found := map[string]int{}
		for _, row := range list(t, out["audits"]) {
			name := str(t, row["audit"])
			if field := str(t, row["field"]); field != "" {
				name += " " + field
			}
			found[name] = num(t, row["found"])
		}
		return found
	}

	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, all["items_scanned"]); got != 3 {
		t.Errorf("items_scanned = %d", got)
	}
	found := counts(all)
	for name, want := range map[string]int{
		"audit_unmatched": 1, "audit_missing cover": 1, "audit_no_audio": 1,
		"audit_duplicates": 1, "audit_spelling": 1, "audit_authors": 2,
	} {
		if found[name] != want {
			t.Errorf("%s = %d, want %d (all: %v)", name, found[name], want, found)
		}
	}
	clean, ok := all["clean"].([]any)
	if !ok {
		t.Fatalf("clean is %T, want a list", all["clean"])
	}
	for _, name := range []string{"audit_issues", "audit_series"} {
		if !slices.Contains(clean, any(name)) {
			t.Errorf("%s should be clean: %v", name, all["clean"])
		}
	}
	skipped, ok := all["skipped"].([]any)
	if !ok {
		t.Fatalf("skipped is %T, want a list", all["skipped"])
	}
	if !slices.Equal(skipped, []any{"audit_covers", "audit_unembedded", "audit_matched", "audit_abridged", "audit_unplayable"}) {
		t.Errorf("skipped = %v, want the five per-item-request audits", all["skipped"])
	}
	if _, ran := found["audit_covers"]; ran || slices.Contains(clean, any("audit_covers")) {
		t.Errorf("audit_covers was reported without deep: %v", all)
	}
	if got := f.requests("/api/items/i1/cover"); len(got) != 0 {
		t.Errorf("a cover was fetched without deep: %v", got)
	}

	deep, err := call("audit_all", map[string]any{"deep": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := deep["skipped"]; present {
		t.Errorf("deep still skipped something: %v", deep["skipped"])
	}
	if deepFound := counts(deep); deepFound["audit_covers"] != 3 || deepFound["audit_unembedded"] != 1 {
		t.Errorf("deep found %v, want 2 tiny covers plus 1 missing, and 1 unembedded book", deepFound)
	}

	// and every count is what the audit itself says
	for _, name := range []string{"audit_duplicates", "audit_spelling", "audit_authors"} {
		one, err := call(name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := num(t, one["total_findings"]); got != found[name] {
			t.Errorf("%s says %d, audit_all said %d", name, got, found[name])
		}
	}
}

// An audit the server can filter for reports the library's size as scanned,
// not the number of hits, and a check that never fires for a podcast does not
// ask a podcast library with a book filter it does not know.
func TestNativeAuditCountsTheLibrary(t *testing.T) {
	t.Parallel()

	const podLib = "44444444-4444-4444-8444-444444444444"
	f := newFakeABS(t)
	f.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"Books","mediaType":"book"},{"id":"`+podLib+`","name":"Pods","mediaType":"podcast"}]}`)
	f.mux.HandleFunc("GET /api/libraries/"+libID+"/items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "" {
			_, _ = io.WriteString(w, `{"results":[`+item("i2", "Quiet", "", "")+`],"total":1}`)
			return
		}
		_, _ = io.WriteString(w, `{"results":[`+item("i1", "Dune", "", "")+`],"total":3}`)
	})
	f.mux.HandleFunc("GET /api/libraries/"+podLib+"/items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "" {
			_, _ = io.WriteString(w, `{"results":[],"total":0}`)
			return
		}
		pods := make([]string, 0, 5)
		for i := range 5 {
			pods = append(pods, fmt.Sprintf(`{"id":"p%d","mediaType":"podcast","media":{"coverPath":"/c.jpg","metadata":{"title":"Show %d"}}}`, i, i))
		}
		_, _ = io.WriteString(w, `{"results":[`+strings.Join(pods, ",")+`],"total":5}`)
	})
	call := toolCaller(t, f)

	out, err := call("audit_missing", map[string]any{"field": "narrator"})
	if err != nil {
		t.Fatal(err)
	}
	if scanned, found := num(t, out["items_scanned"]), num(t, out["total_findings"]); scanned != 3 || found != 1 {
		t.Errorf("narrator: scanned %d found %d, want 3 and 1", scanned, found)
	}
	if got := f.requests("/api/libraries/" + podLib + "/items"); len(got) != 0 {
		t.Errorf("the podcast library was asked about narrators: %v", got)
	}

	// a field podcasts have too still covers the podcast library
	out, err = call("audit_missing", map[string]any{"field": "cover"})
	if err != nil {
		t.Fatal(err)
	}
	if scanned, found := num(t, out["items_scanned"]), num(t, out["total_findings"]); scanned != 8 || found != 1 {
		t.Errorf("cover: scanned %d found %d, want 3+5 and 1", scanned, found)
	}
}

// A description that says nothing is as missing as none.
func TestStubDescription(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]string{
		"":                             "no description",
		"  <p></p> ":                   "no description",
		"Read by Paul Heck":            "credit line",
		"<b>Narrated by</b> Jim Dale.": "credit line",
		"https://example.com/book":     "only a url",
		"Unabridged.":                  "stub, 11 characters",
		strings.Repeat("A real description of the book. ", 5): "",
	} {
		detail, flagged := stubDescription(text)
		if (want == "") == flagged || !strings.Contains(detail, want) {
			t.Errorf("stubDescription(%q) = %q, %v; want %q", text, detail, flagged, want)
		}
	}
}

// Accented letters fold to their plain spelling rather than vanishing, so a
// record and a biography that disagree only on an ø are the same name.
func TestNormFoldsAccents(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"Jo Nesbø":           "jo nesbo",
		"Étienne Mailloux":   "etienne mailloux",
		"Jussi Adler-Olsen":  "jussi adler olsen",
		"Caitlín R. Kiernan": "caitlin r kiernan",
		"Straße":             "strasse",
		// a letter with nothing to fold to is kept, not dropped
		"田中芳樹":               "田中芳樹",
		"Мастер и Маргарита": "мастер и маргарита",
	} {
		if got := norm(in); got != want {
			t.Errorf("norm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAuditChecks(t *testing.T) {
	t.Parallel()

	book := func(m abs.Metadata, media abs.Media) *abs.Item {
		media.Metadata = m
		return &abs.Item{MediaType: "book", RelPath: "Frank Herbert/Dune", Media: media}
	}

	if _, bad := auditChecksByName["unmatched"](book(abs.Metadata{Title: "Dune"}, abs.Media{})); !bad {
		t.Error("unmatched: no ids not flagged")
	}
	if _, bad := auditChecksByName["unmatched"](book(abs.Metadata{Title: "Dune", ASIN: "B0"}, abs.Media{})); bad {
		t.Error("unmatched: asin flagged")
	}
	if _, bad := auditChecksByName["chapters"](book(abs.Metadata{}, abs.Media{Duration: 3 * 3600, NumTracks: 1})); !bad {
		t.Error("chapters: 3h without chapters not flagged")
	}
	if _, bad := auditChecksByName["chapters"](book(abs.Metadata{}, abs.Media{Duration: 3600})); bad {
		t.Error("chapters: short book flagged")
	}
	// one chapter across a long book is as unnavigable as none, and the
	// chapters check treats any chapter count above zero as fine: audit_chapters
	// reports it, from the whole book (TestChapterProblems)
	if _, bad := auditChecksByName["chapters"](book(abs.Metadata{}, abs.Media{Duration: 3 * 3600, NumChapters: 1})); bad {
		t.Error("chapters: a book with one chapter flagged as having none")
	}

	// path check: Author/Title layout matches; a foreign folder does not
	it := book(abs.Metadata{Title: "Dune", AuthorName: "Frank Herbert"}, abs.Media{})
	if detail, bad := checkPath(it); bad {
		t.Errorf("path: good layout flagged: %s", detail)
	}
	it.RelPath = "Herbert, Frank/Dune (1965)"
	if detail, bad := checkPath(it); bad {
		t.Errorf("path: Last, First layout flagged: %s", detail)
	}
	it.RelPath = "Someone Else/Unrelated Folder"
	if _, bad := checkPath(it); !bad {
		t.Error("path: foreign folder not flagged")
	}
	it.RelPath = "Dune Saga/Dune"
	it.Media.Metadata.SeriesName = "Dune Saga"
	it.Media.Metadata.Series = abs.SeriesRefs{{Name: "Dune Saga", Sequence: "1"}}
	if detail, bad := checkPath(it); bad {
		t.Errorf("path: series folder flagged: %s", detail)
	}

	pod := &abs.Item{MediaType: "podcast", Media: abs.Media{Metadata: abs.Metadata{FeedURL: "http://f"}}}
	if _, bad := auditChecksByName["stale_feed"](pod); !bad {
		t.Error("stale_feed: never-checked feed not flagged")
	}
	if _, bad := auditChecksByName["unmatched"](pod); bad {
		t.Error("unmatched: podcast flagged")
	}
}

func TestSeriesSequences(t *testing.T) {
	t.Parallel()

	// items as a series-filtered query returns them: the series listing has no
	// sequence numbers at all
	series := func(seqs ...string) []abs.Item {
		items := make([]abs.Item, 0, len(seqs))
		for _, seq := range seqs {
			it := abs.Item{}
			it.Media.Metadata.Series = abs.SeriesRefs{{ID: "s1", Sequence: seq}}
			items = append(items, it)
		}
		return items
	}

	for _, tc := range []struct {
		name    string
		seqs    []string
		have    []string
		missing []string
	}{
		{"complete", []string{"1", "2", "3"}, []string{"1", "2", "3"}, nil},
		{"one gap", []string{"1", "3"}, []string{"1", "3"}, []string{"2"}},
		{"run of gaps", []string{"3", "6"}, []string{"3", "6"}, []string{"4", "5"}},
		{"out of order", []string{"4", "1", "2"}, []string{"1", "2", "4"}, []string{"3"}},
		{"novella is not a gap", []string{"1", "1.5", "2"}, []string{"1", "1.5", "2"}, nil},
		{"gap around a novella", []string{"1", "2.5", "4"}, []string{"1", "2.5", "4"}, []string{"2", "3"}},
		{"does not assume a start", []string{"3", "4"}, []string{"3", "4"}, nil},
		{"single book", []string{"1"}, []string{"1"}, nil},
		{"unnumbered", []string{"", "", ""}, nil, nil},
		{"non-numeric ignored", []string{"one", "2", "4"}, []string{"2", "4"}, []string{"3"}},
		{"duplicate sequences", []string{"1", "1", "3"}, []string{"1", "3"}, []string{"2"}},
	} {
		if have, missing := seriesSequences(series(tc.seqs...), "s1"); !slices.Equal(have, tc.have) || !slices.Equal(missing, tc.missing) {
			t.Errorf("%s: have=%v missing=%v, want have=%v missing=%v", tc.name, have, missing, tc.have, tc.missing)
		}
	}

	// a ref for another series in the same book must not count
	it := abs.Item{}
	it.Media.Metadata.Series = abs.SeriesRefs{{ID: "s2", Sequence: "9"}, {ID: "s1", Sequence: "1"}}
	if have, missing := seriesSequences([]abs.Item{it}, "s1"); !slices.Equal(have, []string{"1"}) || missing != nil {
		t.Errorf("cross-series: have=%v missing=%v", have, missing)
	}
}

// norm and lastFirstMatch carry the folder-vs-metadata heuristic for the path
// audit. They are pure string logic with real branching, and enumerating the
// shapes here is far cheaper than staging folders on a real server.
func TestNorm(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"":                       "",
		"Dune":                   "dune",
		"The Hitchhiker's Guide": "the hitchhikers guide",
		"Foundation_and-Empire":  "foundation and empire",
		"A.B.C.":                 "a b c",
		"  spaced   out  ":       "spaced out",
		"Gödel, Escher, Bach":    "godel escher bach",
		"2001: A Space Odyssey":  "2001 a space odyssey",
		"!!!":                    "",
	} {
		if got := norm(in); got != want {
			t.Errorf("norm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLastFirstMatch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		folder, author string
		want           bool
	}{
		{"herbert frank", "frank herbert", true},
		{"tolkien j r r", "j r tolkien", true},
		{"the herbert frank collection", "frank herbert", true},
		{"frank herbert", "frank herbert", false}, // already first-last; the caller's Contains handles it
		{"asimov", "isaac asimov", false},         // a surname alone is not the reordering
		{"herbert frank", "herbert", false},       // single-word authors cannot reorder
		{"", "frank herbert", false},
	} {
		if got := lastFirstMatch(tc.folder, tc.author); got != tc.want {
			t.Errorf("lastFirstMatch(%q, %q) = %v, want %v", tc.folder, tc.author, got, tc.want)
		}
	}
}

// Every audit must have a case that trips it and a case that does not, so a
// predicate cannot silently degenerate into "flags everything" (which is how
// audit_series_gaps shipped broken) or "flags nothing".
func TestEveryAuditTripsAndClears(t *testing.T) {
	t.Parallel()

	book := func(m abs.Metadata, media abs.Media) *abs.Item {
		media.Metadata = m
		return &abs.Item{MediaType: "book", RelPath: "Frank Herbert/Dune", Media: media}
	}
	pod := func(m abs.Metadata, media abs.Media) *abs.Item {
		media.Metadata = m
		return &abs.Item{MediaType: "podcast", RelPath: "Behind the Bastards", Media: media}
	}
	// a book with nothing wrong with it
	clean := func() *abs.Item {
		return book(abs.Metadata{
			Title: "Dune", AuthorName: "Frank Herbert", ASIN: "B0", Description: "Set on the desert planet Arrakis, Dune is the story of Paul Atreides, heir to a noble family tasked with ruling an inhospitable world where the only thing of value is the spice.",
			NarratorName: "Scott Brick", SeriesName: "Dune", Genres: []string{"Science Fiction"},
			PublishedYear: "1965", Publisher: "Bantam", Language: "English",
		}, abs.Media{Duration: 3600, NumTracks: 3, NumChapters: 20, NumAudioFiles: 3, CoverPath: "/c.jpg"})
	}

	trips := map[string]*abs.Item{
		"unmatched":   book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		"cover":       book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		"description": book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		"narrator":    book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		"series":      book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		"author":      book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		"genres":      book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		"year":        book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		"publisher":   book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		"language":    book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		"chapters":    book(abs.Metadata{}, abs.Media{Duration: 3 * 3600, NumTracks: 3}),
		"no_audio":    book(abs.Metadata{Title: "Dune"}, abs.Media{}),
		// the server flags these itself; the predicate only reads the flags
		"issues":          {MediaType: "book", IsMissing: true, Media: abs.Media{Metadata: abs.Metadata{Title: "Gone"}}},
		"path":            book(abs.Metadata{Title: "Neuromancer", AuthorName: "William Gibson"}, abs.Media{}),
		"author_as_title": book(abs.Metadata{Title: "Mark of Calth", AuthorName: "Mark of Calth"}, abs.Media{}),
		"stale_feed":      pod(abs.Metadata{FeedURL: "http://f"}, abs.Media{}),
		"no_episodes":     pod(abs.Metadata{FeedURL: "http://f"}, abs.Media{}),
	}
	clears := map[string]*abs.Item{
		"unmatched":       clean(),
		"cover":           clean(),
		"description":     clean(),
		"narrator":        clean(),
		"series":          clean(),
		"author":          clean(),
		"genres":          clean(),
		"year":            clean(),
		"publisher":       clean(),
		"language":        clean(),
		"chapters":        clean(),
		"no_audio":        clean(),
		"path":            clean(),
		"author_as_title": clean(),
		"issues":          clean(),
		// a podcast whose newest episode came out today
		"stale_feed":  pod(abs.Metadata{FeedURL: "http://f"}, abs.Media{LastEpisodeCheck: time.Now().UnixMilli(), NumEpisodes: 1, Episodes: []abs.Episode{{PublishedAt: time.Now().UnixMilli()}}}),
		"no_episodes": pod(abs.Metadata{FeedURL: "http://f"}, abs.Media{NumEpisodes: 3}),
	}

	cases := map[string]string{} // check -> the tool or field that exposes it
	for _, spec := range auditSpecs {
		cases[spec.Check] = spec.Tool
	}
	for _, field := range missingFields {
		cases[field] = "audit_missing " + field
	}

	for check, label := range cases {
		spec := struct{ Tool, Check string }{label, check}
		check, ok := auditChecksByName[spec.Check]
		if !ok {
			t.Errorf("%s: no predicate for check %q", spec.Tool, spec.Check)
			continue
		}

		item, ok := trips[spec.Check]
		if !ok {
			t.Errorf("%s: no case that trips it - add one", spec.Tool)
			continue
		}
		detail, bad := check(item)
		if !bad {
			t.Errorf("%s: did not flag the item it should have", spec.Tool)
		}
		if detail == "" {
			t.Errorf("%s: flagged without saying why", spec.Tool)
		}

		item, ok = clears[spec.Check]
		if !ok {
			t.Errorf("%s: no case that clears it - add one", spec.Tool)
			continue
		}
		if detail, bad := check(item); bad {
			t.Errorf("%s: flagged a clean item: %s", spec.Tool, detail)
		}
	}
}

// audit_all must cover every audit that has a per-item predicate, or a library
// could look clean while an audit it never ran has findings.
func TestAuditSpecsAreComplete(t *testing.T) {
	t.Parallel()

	byCheck := map[string]string{}
	for _, spec := range auditSpecs {
		if prev, dup := byCheck[spec.Check]; dup {
			t.Errorf("check %q is exposed by both %s and %s", spec.Check, prev, spec.Tool)
		}
		byCheck[spec.Check] = spec.Tool
		if !strings.HasPrefix(spec.Tool, "audit_") {
			t.Errorf("%s does not use the audit_ prefix", spec.Tool)
		}
		if spec.Description == "" {
			t.Errorf("%s has no description", spec.Tool)
		}
	}
	for _, field := range missingFields {
		if prev, dup := byCheck[field]; dup {
			t.Errorf("check %q is exposed by both %s and audit_missing", field, prev)
		}
		byCheck[field] = "audit_missing"
	}
	// the one per-item check that lives inside a wider audit rather than a
	// spec of its own
	if prev, dup := byCheck["author_as_title"]; dup {
		t.Errorf("check author_as_title is exposed by both %s and audit_authors", prev)
	}
	byCheck["author_as_title"] = "audit_authors"
	// and the two that are one tool, each a problem its findings name
	for _, check := range podcastChecks {
		if prev, dup := byCheck[check]; dup {
			t.Errorf("check %s is exposed by both %s and audit_podcasts", check, prev)
		}
		byCheck[check] = "audit_podcasts"
	}
	for check := range auditChecksByName {
		if _, ok := byCheck[check]; !ok {
			t.Errorf("check %q has no audit tool, so audit_all never reports it", check)
		}
	}
}

// audit_all must name every audit, as run, skipped or not applicable. The
// ones with no per-item predicate (audit_chapters needs the whole book) are
// listed in it by hand, and one left off would never be counted.
func TestAuditAllNamesEveryAudit(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page())
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	out, err := toolCaller(t, f)("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, row := range list(t, out["audits"]) {
		named[str(t, row["audit"])] = true
	}
	for _, key := range []string{"clean", "skipped", "not_applicable"} {
		for _, name := range strs(t, out[key]) {
			named[strings.Fields(name)[0]] = true // "audit_missing cover"
		}
	}
	for _, name := range register(t, Options{EnableDelete: true}) {
		if strings.HasPrefix(name, "audit_") && name != "audit_all" && !named[name] {
			t.Errorf("audit_all does not name %s", name)
		}
	}
}
