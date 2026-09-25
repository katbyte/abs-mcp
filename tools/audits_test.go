package tools

import (
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// The audits against the shapes the reviews found them wrong on: each test
// here failed before its fix.

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

// The server joins genres with "; " on embed and writes an m4b's publisher
// only as its copyright, so neither could ever read back as embedded.
func TestEmbeddedReadsTheTagsTheServerWrites(t *testing.T) {
	t.Parallel()

	m := &abs.Metadata{Title: "X", Genres: []string{"Mystery, Thriller & Suspense", "Fiction"}, Publisher: "Tantor"}
	genre := "Mystery, Thriller & Suspense; Fiction"
	if got := embedMismatches(m, &abs.AudioFile{MimeType: "audio/mp4", Metadata: abs.FileMetadata{Ext: ".m4b"}, MetaTags: map[string]string{"tagTitle": "X", "tagGenre": genre}}); len(got) != 0 {
		t.Errorf("an m4b embedded by the server = %v, want nothing stale", got)
	}
	mp3 := &abs.AudioFile{MimeType: "audio/mpeg", MetaTags: map[string]string{"tagTitle": "X", "tagGenre": genre}}
	if got := embedMismatches(m, mp3); !slices.Equal(got, []string{"no publisher tag"}) {
		t.Errorf("an mp3 without its publisher = %v, want the publisher missing", got)
	}
	mp3.MetaTags["tagPublisher"] = "Tantor"
	if got := embedMismatches(m, mp3); len(got) != 0 {
		t.Errorf("an mp3 embedded by the server = %v, want nothing stale", got)
	}
	// a genre really split three ways is still stale, and another tool's
	// slashes still read as a list
	if sameList("Mystery; Thriller & Suspense; Fiction", m.Genres) {
		t.Error("three genres read as the two the item has")
	}
	if !sameList("Fiction/Classic", []string{"Classic", "Fiction"}) {
		t.Error("a slash-separated tag no longer reads as a list")
	}
}

// A matched copy and the unmatched copy scanned in beside it share a title
// and author and nothing else; two recordings with their own asins are
// editions and stay apart, whichever copy their title reaches.
func TestDuplicatesJoinACopyByAnyKey(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("a", "Dune", `"authorName":"Frank Herbert","asin":"B002V1OF70"`, ""),
		item("b", "Dune", `"authorName":"Frank Herbert"`, ""),
		item("c", "Small Gods", `"authorName":"Terry Pratchett","asin":"B0FULLCAST"`, ""),
		item("d", "Small Gods", `"authorName":"Terry Pratchett","asin":"B0NIGEL"`, ""),
		item("g", "Small Gods", `"authorName":"Terry Pratchett"`, ""),
		item("e", "Solo", `"authorName":"X","isbn":"978-1"`, ""),
		item("h", "Solo Again", `"authorName":"Y","isbn":"9781"`, ""),
	))
	call := toolCaller(t, f)

	out, err := call("audit_duplicates", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_scanned"]); got != 7 {
		t.Errorf("items_scanned = %d, want 7", got)
	}
	groups := map[string][]string{}
	for _, g := range list(t, out["groups"]) {
		var ids []string
		for _, it := range list(t, g["items"]) {
			ids = append(ids, str(t, it["id"]))
		}
		groups[str(t, g["key"])] = ids
	}
	want := map[string][]string{
		"title:dune|frank herbert":         {"a", "b"},
		"title:small gods|terry pratchett": {"c", "g"},
		"isbn:9781":                        {"e", "h"},
	}
	if !reflect.DeepEqual(groups, want) || num(t, out["total_findings"]) != 3 {
		t.Errorf("groups = %v, want %v", groups, want)
	}
}

// "Author/Series/01 - Title" is the other common layout: the folder the book
// sits in is the series, not the whole path above it.
func TestNumberingReadsANestedSeriesFolder(t *testing.T) {
	t.Parallel()

	c := newNumberingCollector()
	c.add(numbered("a", "Brandon Sanderson/Mistborn/01 - The Final Empire", "Mistborn", "3"))
	got := c.findings()
	if len(got) != 1 || got[0].Problem != "folder_disagrees" || got[0].Suggest != "Mistborn #1" {
		t.Errorf("findings = %+v, want the folder's #1 against the series' #3", got)
	}
	for in, want := range map[string]string{
		"Brandon Sanderson/Mistborn/01 - The Final Empire": "Mistborn",
		"Mistborn/01 - The Final Empire":                   "Mistborn",
		"Discworld - 09 - Eric.m4b":                        "",
	} {
		if got := parentFolder(in); got != want {
			t.Errorf("parentFolder(%q) = %q, want %q", in, got, want)
		}
	}
}

// A library set to hide single-book series lists none of them, and a
// one-book series is where a stray name sits: the books still carry it.
func TestAuditSeriesReadsTheSeriesTheListingHides(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	book := func(id, title, series string) string {
		return item(id, title, `"authorName":"Robert Jordan","seriesName":"`+series+`"`, "")
	}
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s-wot","name":"The Wheel of Time","books":[`+
		book("w1", "The Eye of the World", "The Wheel of Time #1")+","+book("w2", "The Great Hunt", "The Wheel of Time #2")+`]}],"total":1}`)
	f.json("GET /api/libraries/"+libID+"/items", page(
		book("w1", "The Eye of the World", "The Wheel of Time #1"),
		book("w2", "The Great Hunt", "The Wheel of Time #2"),
		book("w3", "The Dragon Reborn", "Wheel of Time #3"),
		book("h1", "New Spring", "Wheel of Time Prequels Series"),
	))
	f.json("POST /api/items/batch/get", `{"libraryItems":[{"id":"h1","libraryId":"`+libID+`","mediaType":"book","media":{"metadata":{"title":"New Spring","series":[{"id":"s-prequels","name":"Wheel of Time Prequels Series"}]}}}]}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	call := toolCaller(t, f)

	out, err := call("audit_series", nil)
	if err != nil {
		t.Fatal(err)
	}
	var spellings []string
	for _, g := range list(t, out["names"]) {
		for _, sp := range list(t, g["spellings"]) {
			spellings = append(spellings, str(t, sp["value"]))
		}
	}
	if !slices.Contains(spellings, "Wheel of Time") || !slices.Contains(spellings, "The Wheel of Time") {
		t.Errorf("names = %v, want the hidden one-book spelling beside the listed one", out["names"])
	}
	odd := list(t, out["odd"])
	if len(odd) != 1 || str(t, odd[0]["name"]) != "Wheel of Time Prequels Series" || str(t, odd[0]["id"]) != "s-prequels" || str(t, odd[0]["suggest"]) != "Wheel of Time Prequels" {
		t.Errorf("odd = %v, want the hidden series, with the id its book carries", odd)
	}
	if got := num(t, out["items_scanned"]); got != 4 {
		t.Errorf("items_scanned = %d, want 4", got)
	}
	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list(t, all["audits"]) {
		if str(t, row["audit"]) == "audit_series" && num(t, row["found"]) != num(t, out["total_findings"]) {
			t.Errorf("audit_all counts %v, audit_series %v", row["found"], out["total_findings"])
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

// The provider tags of two stores are a letter apart by design.
func TestAuditSpellingLeavesMarkerTagsOut(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "One", "", `"tags":["zz-provider:audible.ca"]`),
		item("i2", "Two", "", `"tags":["zz-provider:audible.uk"]`),
		item("i3", "Three", "", `"tags":["Romance"]`),
		item("i4", "Four", "", `"tags":["Romances"]`),
	))
	call := toolCaller(t, f)

	out, err := call("audit_spelling", map[string]any{"field": "tags"})
	if err != nil {
		t.Fatal(err)
	}
	groups := list(t, out["groups"])
	if len(groups) != 1 || str(t, groups[0]["keep"]) != "Romance" && str(t, groups[0]["keep"]) != "Romances" {
		t.Errorf("groups = %v, want Romance alone", groups)
	}

	// a prefix the server was given, whatever its shape
	c := newSpellingCounts([]string{"tags"})
	c.addValue("tags", "Store/audible.ca", 3)
	c.addValue("tags", "Store/audible.uk", 1)
	c.dropMarkers((&registry{opts: Options{ProviderTag: "Store/"}}).providerConfig())
	if got := c.report("tags"); len(got) != 0 {
		t.Errorf("configured provider tags grouped: %+v", got)
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
	f.json("GET /api/libraries/"+podLibID+"/items", page(minified("ended", nowMs, 2), minified("going", nowMs, 1), minified("unchecked", 0, 1)))
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+pod("ended", nowMs, oldMs-1000, oldMs)+","+pod("going", nowMs, nowMs)+`]}`)
	call := toolCaller(t, f)

	out, err := call("audit_podcast_stale_feed", nil)
	if err != nil {
		t.Fatal(err)
	}
	detail := map[string]string{}
	for _, row := range list(t, out["findings"]) {
		detail[str(t, row["id"])] = str(t, row["detail"])
	}
	if len(detail) != 2 || !strings.HasPrefix(detail["ended"], "no new episode in 200 days") || detail["unchecked"] != "feed never checked" {
		t.Errorf("findings = %v, want the ended show and the unchecked one", detail)
	}
	if batches := f.requests("/api/items/batch/get"); len(batches) != 1 || strings.Contains(batches[0].Body, "unchecked") {
		t.Errorf("fetched %v, want the two checked shows only", batches)
	}
	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list(t, all["audits"]) {
		if str(t, row["audit"]) == "audit_podcast_stale_feed" && num(t, row["found"]) != 2 {
			t.Errorf("audit_all counts %v stale feeds, want 2", row["found"])
		}
	}
}

// storeFixture is a library of three matched books: one whose cover is the
// store's picture too small, one whose store image is gone, and one whose
// own cover file is gone.
func storeFixture(t *testing.T) *fakeABS {
	t.Helper()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID, `{"id":"`+libID+`","name":"Books","mediaType":"book","provider":"audible"}`)
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img/a._SL500_.jpg":
			_, _ = io.WriteString(w, encodeJPEG(t, artwork(500, 0), 85))
		case "/img/a.jpg":
			_, _ = io.WriteString(w, encodeJPEG(t, artwork(1500, 0), 85))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(store.Close)
	books := []string{
		item("li_1", "Small", `"asin":"B001"`, `"coverPath":"/1.jpg"`),
		item("li_2", "Store Gone", `"asin":"B002"`, `"coverPath":"/2.jpg"`),
		item("li_3", "File Gone", `"asin":"B003"`, `"coverPath":"/3.jpg"`),
	}
	f.json("GET /api/libraries/"+libID+"/items", page(books...))
	for i, b := range books {
		f.json(fmt.Sprintf("GET /api/items/li_%d", i+1), b)
	}
	f.mux.HandleFunc("GET /api/search/books", func(w http.ResponseWriter, r *http.Request) {
		asin := r.URL.Query().Get("title")
		cover := map[string]string{"B001": "a", "B002": "gone", "B003": "a"}[asin]
		_, _ = fmt.Fprintf(w, `[{"title":"x","asin":%q,"cover":"%s/img/%s._SL500_.jpg"}]`, asin, store.URL, cover) //nolint:gosec // a test fixture echoing its own query
	})
	onDisk := map[string]image.Image{"li_1": artwork(300, 0), "li_2": artwork(500, 0.5)}
	f.mux.HandleFunc("GET /api/items/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		img, ok := onDisk[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, encodeJPEG(t, img, 85))
	})
	f.json("POST /api/items/{id}/cover", `{"success":true}`)
	return f
}

// One book's store or cover failing stopped the whole window, and it could
// never be got past; every later window repeated the library-wide rows.
func TestAuditCoversStoreCarriesOnPastABook(t *testing.T) {
	t.Parallel()

	f := storeFixture(t)
	call := toolCaller(t, f)

	out, err := call("audit_covers", map[string]any{"store": true, "providers": []any{"audible"}, "library": "Books"})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string][]string{}
	for _, row := range list(t, out["findings"]) {
		rows[str(t, row["id"])] = append(rows[str(t, row["id"])], str(t, row["problem"]))
	}
	if !slices.Contains(rows["li_1"], "upgrade") || !slices.Equal(rows["li_2"], []string{"skipped"}) || !slices.Equal(rows["li_3"], []string{"upgrade"}) {
		t.Errorf("rows = %v, want an upgrade, a skipped store, and an upgrade over the gone file", rows)
	}
	if num(t, out["items_scanned"]) != 3 || num(t, out["total_findings"]) != 3 {
		t.Errorf("items_scanned %v total_findings %v, want 3 books and small + two upgrades", out["items_scanned"], out["total_findings"])
	}

	// a book at a time: the library-wide rows come with the first only
	first, err := call("audit_covers", map[string]any{"store": true, "providers": []any{"audible"}, "library": "Books", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := call("audit_covers", map[string]any{"store": true, "providers": []any{"audible"}, "library": "Books", "limit": 1, "offset": 1})
	if err != nil {
		t.Fatal(err)
	}
	problems := func(out map[string]any) []string {
		rows := list(t, out["findings"])
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, str(t, row["id"])+" "+str(t, row["problem"]))
		}
		return got
	}
	if got := problems(first); !slices.Equal(got, []string{"li_1 small", "li_1 upgrade"}) || num(t, first["next_offset"]) != 1 {
		t.Errorf("offset 0 = %v, next_offset %v", got, first["next_offset"])
	}
	if got := problems(second); !slices.Equal(got, []string{"li_2 skipped"}) || num(t, second["total_findings"]) != 0 || num(t, second["items_scanned"]) != 1 || num(t, second["next_offset"]) != 2 {
		t.Errorf("offset 1 = %v %v, want the store row alone", got, second)
	}

	// the one-library rule is checked before anything is swept
	two := newFakeABS(t)
	two.json("GET /api/libraries", `{"libraries":[{"id":"`+libID+`","name":"A","mediaType":"book"},{"id":"`+otherLibID+`","name":"B","mediaType":"book"}]}`)
	if _, err := toolCaller(t, two)("audit_covers", map[string]any{"store": true}); err == nil || len(two.requests("/api/libraries/"+libID+"/items")) != 0 {
		t.Errorf("store over two libraries: err %v, requests %v", err, two.requests("/api/libraries/"+libID+"/items"))
	}
}

// A batch that fails part way has already set covers: those are reported,
// with the failure and what was never tried, rather than only an error.
func TestItemCoverUpgradeReportsWhatItDidBeforeAFailure(t *testing.T) {
	t.Parallel()

	f := storeFixture(t)
	call := toolCaller(t, f)

	out, err := call("item_cover_upgrade", map[string]any{"items": []any{"li_1", "li_3", "li_2", "li_1"}, "providers": []any{"audible"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["items"])
	actions := make([]string, 0, len(rows))
	for _, row := range rows {
		actions = append(actions, str(t, row["id"])+" "+str(t, row["action"]))
	}
	if !slices.Equal(actions, []string{"li_1 upgraded", "li_3 upgraded", "li_2 failed"}) || num(t, out["upgraded"]) != 2 {
		t.Errorf("actions = %v, want two upgrades then the failure", actions)
	}
	if str(t, rows[2]["error"]) == "" {
		t.Errorf("the failed row says nothing: %v", rows[2])
	}
	if nt, ok := out["not_tried"].([]any); !ok || len(nt) != 1 || nt[0] != "li_1" {
		t.Errorf("not_tried = %v, want the last li_1", out["not_tried"])
	}
}

// A year typed as the sequence read as two thousand missing books, a date
// as twenty million, and made the series pad to four digits.
func TestSeriesNumbersFarFromTheRest(t *testing.T) {
	t.Parallel()

	if got := missingBetween([]float64{1, 2, 2019}); got != nil {
		t.Errorf("missing = %d numbers, want none", len(got))
	}
	if kept, outliers := splitOutliers([]float64{1, 2, 3, 20190315}); !slices.Equal(kept, []float64{1, 2, 3}) || !slices.Equal(outliers, []float64{20190315}) {
		t.Errorf("kept %v outliers %v", kept, outliers)
	}
	// five books of a forty-book series still have their gaps read
	if got := missingBetween([]float64{1, 7, 20, 35, 41}); len(got) != 36 {
		t.Errorf("a sparse series: %d missing, want 36", len(got))
	}
	for _, s := range []string{"inf", "-Inf", "NaN"} {
		if _, ok := sequenceNumber(s); ok {
			t.Errorf("%q read as a number", s)
		}
	}

	c := newNumberingCollector()
	for n := 1; n <= 9; n++ {
		c.add(numbered(strconv.Itoa(n), "Series/"+strconv.Itoa(n), "Series", strconv.Itoa(n)))
	}
	c.add(numbered("y", "Series/Year", "Series", "2019"))
	if w := c.padWidth(seriesKey("Series")); w != 1 {
		t.Errorf("width %d, want 1: the year does not set it", w)
	}

	f := newFakeABS(t)
	oneLibrary(f)
	ref := func(id, seq string) string {
		return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","media":{"metadata":{"title":"` + id + `","series":[{"id":"s1","name":"Series","sequence":"` + seq + `"}]}}}`
	}
	books := ref("b1", "1") + "," + ref("b2", "2") + "," + ref("b3", "2019")
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[{"id":"s1","name":"Series","books":[`+books+`]}],"total":1}`)
	f.json("GET /api/libraries/"+libID+"/items", `{"results":[`+books+`],"total":3}`)
	out, err := toolCaller(t, f)("audit_series", nil)
	if err != nil {
		t.Fatal(err)
	}
	gaps := list(t, out["gaps"])
	if len(gaps) != 1 || fmt.Sprint(gaps[0]["missing"]) != "[]" || fmt.Sprint(gaps[0]["outliers"]) != "[2019]" {
		t.Errorf("gaps = %v, want no missing and 2019 as the outlier", gaps)
	}
}

// The listing joins an item's series with ", ", which a series name may hold.
func TestSeriesWithACommaInItsName(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/items", page(
		`{"id":"l1","libraryId":"`+libID+`","mediaType":"book","relPath":"Love, Death & Robots - 03 - Zima Blue","media":{"metadata":{"title":"Zima Blue","seriesName":"Love, Death & Robots #2"}}}`,
		`{"id":"f1","libraryId":"`+libID+`","mediaType":"book","relPath":"Foundation - 2 - Foundation and Empire","media":{"metadata":{"title":"Foundation and Empire","seriesName":"Foundation #2, Robot #9"}}}`,
	))
	f.json("POST /api/items/batch/get", `{"libraryItems":[{"id":"l1","libraryId":"`+libID+`","mediaType":"book","relPath":"Love, Death & Robots - 03 - Zima Blue","media":{"metadata":{"title":"Zima Blue","series":[{"id":"s-ldr","name":"Love, Death & Robots","sequence":"2"}]}}}]}`)
	out, err := toolCaller(t, f)("audit_series", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := list(t, out["numbering"])
	if len(rows) != 1 || str(t, rows[0]["problem"]) != "folder_disagrees" || str(t, rows[0]["suggest"]) != "Love, Death & Robots #3" {
		t.Errorf("numbering = %v, want the folder's #3 against #2 in the one series", rows)
	}
	// two numbered series read plainly; only the ambiguous item is fetched
	if batches := f.requests("/api/items/batch/get"); len(batches) != 1 || strings.Contains(batches[0].Body, "f1") {
		t.Errorf("fetched %v, want l1 alone", batches)
	}
}

// en-US was reported as an unknown language, and no unknown language was
// counted as a finding anywhere.
func TestAuditSpellingCountsUnrecognisedLanguages(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "One", `"language":"en-US"`, ""),
		item("i2", "Two", `"language":"English"`, ""),
		item("i3", "Three", `"language":"pt_BR"`, ""),
		item("i4", "Four", `"language":"XXX"`, ""),
	))
	f.json("GET /api/libraries/"+libID+"/series", `{"results":[],"total":0}`)
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[],"total":0}`)
	call := toolCaller(t, f)

	out, err := call("audit_spelling", nil)
	if err != nil {
		t.Fatal(err)
	}
	odd := list(t, out["unrecognized_languages"])
	if len(odd) != 1 || str(t, odd[0]["value"]) != "XXX" {
		t.Errorf("unrecognized = %v, want XXX alone", odd)
	}
	groups := list(t, out["groups"])
	if len(groups) != 1 || num(t, out["total_findings"]) != 2 {
		t.Errorf("groups %v, total_findings %v: want en-US beside English, and XXX counted", groups, out["total_findings"])
	}
	all, err := call("audit_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list(t, all["audits"]) {
		if str(t, row["audit"]) == "audit_spelling" && num(t, row["found"]) != 2 {
			t.Errorf("audit_all counts %v, audit_spelling 2", row["found"])
		}
	}
}

// A book wrongly matched to "It" passed in a folder called "The Institute":
// the letters are there, the word is not.
func TestPathAgreesWordForWord(t *testing.T) {
	t.Parallel()

	it := &abs.Item{MediaType: "book", RelPath: "Stephen King/The Institute"}
	it.Media.Metadata.Title, it.Media.Metadata.AuthorName = "It", "Stephen King"
	if _, suspect := checkPath(it); !suspect {
		t.Error("It in The Institute passed")
	}
	it.RelPath = "Stephen King/It (Unabridged)"
	if detail, suspect := checkPath(it); suspect {
		t.Errorf("It in its own folder: %s", detail)
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
	if !strings.Contains(desc["audit_series"], "fixed convention") || !strings.Contains(desc["audit_podcast_stale_feed"], "newest episode") {
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
	for _, name := range []string{"audit_podcast_stale_feed", "audit_missing cover", "audit_duplicates"} {
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
	if na := strs(t, out["not_applicable"]); !slices.Equal(na, []string{"audit_podcast_stale_feed", "audit_podcast_no_episodes"}) {
		t.Errorf("not_applicable over books = %v", na)
	}
	if skipped := strs(t, out["skipped"]); !slices.Equal(skipped, []string{"audit_covers", "audit_unembedded", "audit_matched", "audit_abridged"}) {
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

// strs reads a list of strings, treating an absent one as empty.
func strs(t *testing.T, v any) []string {
	t.Helper()

	if v == nil {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%v is %T, want a list", v, v)
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		out = append(out, str(t, e))
	}
	return out
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

// metadata_rename remove only previews without confirm, so a suggestion
// without it fixed nothing.
func TestGenreSuggestionsConfirmTheirRemoves(t *testing.T) {
	t.Parallel()

	c := newGenresCollector()
	it := &abs.Item{ID: "i1", MediaType: "book"}
	it.Media.Metadata.Genres = []string{"Audiobook", "Fantasy"}
	it.Media.Tags = []string{"Fantasy"}
	c.add(it)
	out := c.findings(1, 50)
	for _, v := range slices.Concat(out.Placeholders, out.Redundant) {
		if !strings.Contains(v.Suggest, "remove=true confirm=true") {
			t.Errorf("%s: suggest %q, want the remove confirmed", v.Value, v.Suggest)
		}
	}
	if len(out.Placeholders) != 1 || len(out.Redundant) != 1 {
		t.Errorf("placeholders %v redundant %v", out.Placeholders, out.Redundant)
	}
}

// A title alone does not join two readings: both name their readers and
// share none, so they are two recordings of one book - a full-cast and a
// single-narrator set kept on purpose - not one held twice. The same reader
// written two ways is one reader, and a copy naming none still joins.
func TestDuplicatesKeepTwoReadingsApart(t *testing.T) {
	t.Parallel()

	book := func(id, narrator string) *abs.Item {
		it := &abs.Item{ID: id, MediaType: "book"}
		it.Media.Metadata.Title = "Wyrd Sisters"
		it.Media.Metadata.AuthorName = "Terry Pratchett"
		it.Media.Metadata.NarratorName = narrator
		return it
	}
	groupsOf := func(items ...*abs.Item) [][]string {
		d := newDupCollector()
		for _, it := range items {
			d.add(it)
		}
		groups := d.groups()
		out := make([][]string, 0, len(groups))
		for _, g := range groups {
			var ids []string
			for _, it := range g.Items {
				ids = append(ids, it.ID)
			}
			out = append(out, ids)
		}
		return out
	}

	if got := groupsOf(book("full", "Peter Serafinowicz, Bill Nighy"), book("single", "Nigel Planer")); len(got) != 0 {
		t.Errorf("two readings were grouped: %v", got)
	}
	if got := groupsOf(book("a", "Nigel Planer"), book("b", "Planer, Nigel")); len(got) != 1 {
		t.Errorf("one reader written two ways was not grouped: %v", got)
	}
	if got := groupsOf(book("a", "Nigel Planer"), book("b", "")); len(got) != 1 {
		t.Errorf("a copy naming no reader was not grouped: %v", got)
	}
}
