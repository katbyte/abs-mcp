package tools

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestClient(t *testing.T) *abs.Client {
	t.Helper()

	c, err := abs.New("http://127.0.0.1:1", "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func register(t *testing.T, opts Options) []string {
	t.Helper()

	names, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestRegisterAllKinds(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	dflt := register(t, Options{})
	ro := register(t, Options{ReadOnly: true})

	if len(all) <= len(dflt) || len(dflt) <= len(ro) || len(ro) == 0 {
		t.Fatalf("counts all=%d default=%d read-only=%d", len(all), len(dflt), len(ro))
	}
	for _, name := range []string{"item_delete", "podcast_episode_delete", "author_delete", "library_issues_remove"} {
		if slices.Contains(dflt, name) {
			t.Errorf("%s registered without --enable-delete", name)
		}
		if !slices.Contains(all, name) {
			t.Errorf("%s missing with --enable-delete", name)
		}
	}
	for _, name := range ro {
		if strings.HasSuffix(name, "_set") || strings.HasSuffix(name, "_delete") || strings.HasSuffix(name, "_scan") || strings.HasSuffix(name, "_edit") {
			t.Errorf("%s registered under --read-only", name)
		}
	}
	for _, name := range EssentialTools {
		if !slices.Contains(dflt, name) {
			t.Errorf("essential tool %s does not exist", name)
		}
	}
	if !slices.IsSorted(dflt) {
		t.Error("registered names not sorted")
	}
}

func TestRegisterAllFilters(t *testing.T) {
	t.Parallel()

	got := register(t, Options{Allow: []string{"essential"}})
	want := slices.Clone(EssentialTools)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("essential = %v", got)
	}

	got = register(t, Options{Allow: []string{"library_*,me_get"}, Deny: []string{"*_scan"}})
	for _, name := range got {
		if !strings.HasPrefix(name, "library_") && name != "me_get" {
			t.Errorf("unexpected %s", name)
		}
		if name == "library_scan" {
			t.Error("denied tool registered")
		}
	}
	if !slices.Contains(got, "library_list") || !slices.Contains(got, "me_get") {
		t.Errorf("allow list not honoured: %v", got)
	}

	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Allow: []string{"bogus_*"}}); err == nil {
		t.Error("unknown allow pattern accepted")
	}
	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Deny: []string{"nope"}}); err == nil {
		t.Error("unknown deny pattern accepted")
	}
}

func TestMatchPattern(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"item_get", "item_get", true},
		{"item_get", "item_gets", false},
		{"item_*", "item_get", true},
		{"item_*", "library_items", false},
		{"*_delete", "item_delete", true},
		{"*", "anything", true},
	} {
		if got := matchPattern(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v", tc.pattern, tc.name, got)
		}
	}
}

func TestSortKey(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in, want string
		podcast  bool
	}{
		{"", "media.metadata.title", false},
		{"Title", "media.metadata.title", false},
		{"author", "media.metadata.authorName", false},
		{"author", "media.metadata.author", true},
		{"added", "addedAt", false},
		{"duration", "media.duration", false},
		{"random", "random", false},
		{"sequence", "sequence", false},
	} {
		if got := sortKey(tc.in, tc.podcast); got != tc.want {
			t.Errorf("sortKey(%q, %v) = %q want %q", tc.in, tc.podcast, got, tc.want)
		}
	}
}

func TestBuildFilter(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/filterdata") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"authors":[{"id":"a1","name":"Frank Herbert"}],"series":[{"id":"s1","name":"Dune"}],"genres":[]}`))
	}))
	defer srv.Close()

	client, _ := abs.New(srv.URL, "k")
	lib := &abs.Library{ID: "lib", Name: "Books"}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"issues", "issues"},
		{"genre:Fantasy", "genres." + b64("Fantasy")},
		{"tag: Read ", "tags." + b64("Read")},
		{"progress:in-progress", "progress." + b64("in-progress")},
		{"missing:asin", "missing." + b64("asin")},
		{"author:frank herbert", "authors." + b64("a1")},
		{"authors:a1", "authors." + b64("a1")},
		{"series:Dune", "series." + b64("s1")},
		{"abridged", "abridged." + b64("abridged")},
	} {
		got, err := buildFilter(context.Background(), client, lib, tc.in)
		if err != nil {
			t.Errorf("buildFilter(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("buildFilter(%q) = %q want %q", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{"genres", "author:Nobody", "series:Unknown"} {
		if _, err := buildFilter(context.Background(), client, lib, bad); err == nil {
			t.Errorf("buildFilter(%q) accepted", bad)
		}
	}
}

func TestFormatting(t *testing.T) {
	t.Parallel()

	for secs, want := range map[float64]string{0: "", 45: "45s", 125: "2m 5s", 3600: "1h 0m", 45296: "12h 34m"} {
		if got := fmtDuration(secs); got != want {
			t.Errorf("fmtDuration(%v) = %q want %q", secs, got, want)
		}
	}
	if got := fmtDate(1_700_000_000_000); got != "2023-11-14" {
		t.Errorf("fmtDate = %q", got)
	}
	if got := fmtTime(0); got != "" {
		t.Errorf("fmtTime(0) = %q", got)
	}
	if got := plain("<p>Hi <b>there</b></p>"); got != "Hi there" {
		t.Errorf("plain = %q", got)
	}
	if got := clip("one two three four five", 12); got != "one two..." {
		t.Errorf("clip = %q", got)
	}
	if !looksLikeID("f0e9d8c7-b6a5-4321-8765-0123456789ab") || looksLikeID("Dune") {
		t.Error("looksLikeID")
	}
}

func TestSummariseItem(t *testing.T) {
	t.Parallel()

	it := &abs.Item{
		ID: "i1", MediaType: "book", RelPath: "Frank Herbert/Dune", AddedAt: 1_700_000_000_000, Size: 500 << 20,
		Media: abs.Media{
			Metadata:  abs.Metadata{Title: "Dune", AuthorName: "Frank Herbert", NarratorName: "Scott Brick", SeriesName: "Dune #1", PublishedYear: "1965", ASIN: "B0"},
			CoverPath: "/x/cover.jpg", Duration: 75_000, NumTracks: 3, NumChapters: 40, Tags: []string{"sf"},
		},
		UserMediaProgress: &abs.MediaProgress{Progress: 0.5, CurrentTime: 37_500, ID: "p1"},
	}
	s := summarize(it)
	if s.Title != "Dune" || s.Author != "Frank Herbert" || s.Narrator != "Scott Brick" || s.Year != "1965" {
		t.Errorf("summary = %+v", s)
	}
	if len(s.Series) != 1 || s.Series[0] != "Dune #1" {
		t.Errorf("series = %v", s.Series)
	}
	if s.Duration != "20h 50m" || s.SizeMB != 500 || s.Tracks != 3 || s.Chapters != 40 || s.NoCover {
		t.Errorf("facts = %+v", s)
	}
	if s.Progress == nil || s.Progress.Percent != 50 || s.Progress.ProgressID != "p1" {
		t.Errorf("progress = %+v", s.Progress)
	}
	if s.Added != "2023-11-14" {
		t.Errorf("added = %q", s.Added)
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
	// chapters check treats any chapter count above zero as fine
	if _, bad := auditChecksByName["single_chapter"](book(abs.Metadata{}, abs.Media{Duration: 3 * 3600, NumChapters: 1})); !bad {
		t.Error("single_chapter: one chapter over 3h not flagged")
	}
	if _, bad := auditChecksByName["single_chapter"](book(abs.Metadata{}, abs.Media{Duration: 3 * 3600, NumChapters: 30})); bad {
		t.Error("single_chapter: a properly chaptered book flagged")
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
		"Gödel, Escher, Bach":    "gdel escher bach",
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

func TestLimitOr(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ limit, def, want int }{
		{0, 50, 50}, {-1, 50, 50}, {10, 50, 10}, {1, 50, 1},
	} {
		if got := limitOr(tc.limit, tc.def); got != tc.want {
			t.Errorf("limitOr(%d, %d) = %d, want %d", tc.limit, tc.def, got, tc.want)
		}
	}
}

// progressOf is the projection every progress-bearing tool goes through, and
// nil (no progress record) is a normal state rather than an error.
func TestProgressOf(t *testing.T) {
	t.Parallel()

	if got := progressOf(nil); got != nil {
		t.Errorf("progressOf(nil) = %v, want nil", got)
	}

	got := progressOf(&abs.MediaProgress{
		ID: "p1", Progress: 0.256, CurrentTime: 3725, IsFinished: false,
		LastUpdate: 1_700_000_000_000, HideFromContinueListening: true,
	})
	if got == nil {
		t.Fatal("progressOf returned nil for a real record")
	}
	if got.Percent != 26 {
		t.Errorf("percent = %d, want 26 (rounded)", got.Percent)
	}
	if got.CurrentTime != "1h 2m" {
		t.Errorf("current_time = %q, want 1h 2m", got.CurrentTime)
	}
	if got.Seconds != 3725 {
		t.Errorf("seconds = %d", got.Seconds)
	}
	if !got.Hidden || got.ProgressID != "p1" {
		t.Errorf("hidden/id did not carry through: %+v", got)
	}
}

// vocabKey decides what counts as "the same value spelled differently", which
// is the whole basis of audit_terminology. Languages are special: en, eng and
// English are the same language but share no normalized spelling.
func TestVocabKey(t *testing.T) {
	t.Parallel()

	same := func(field string, values ...string) {
		t.Helper()
		first := vocabKey(field, values[0])
		for _, v := range values[1:] {
			if got := vocabKey(field, v); got != first {
				t.Errorf("%s: %q (%q) and %q (%q) should group together", field, values[0], first, v, got)
			}
		}
	}
	differ := func(field, a, b string) {
		t.Helper()
		if vocabKey(field, a) == vocabKey(field, b) {
			t.Errorf("%s: %q and %q should NOT group together", field, a, b)
		}
	}

	// the real mess found in a live library
	same("languages", "en", "eng", "English", "english", "ENGLISH")
	same("languages", "de", "ger", "deu", "German", "Deutsch")
	differ("languages", "en", "de")
	differ("languages", "en", "XXX")

	// everything else groups on spelling alone
	same("narrators", "Jim Dale", "jim dale", "JIM DALE")
	same("genres", "Sci-Fi", "sci fi", "Sci Fi")
	same("tags", "space-opera", "Space Opera", "space_opera")
	same("authors", "J.R.R. Tolkien", "J R R Tolkien", "j.r.r. tolkien") //nolint:dupword // initials, not a repeated word
	differ("narrators", "Jim Dale", "Jim Dales")
	differ("genres", "Science Fiction", "Science")

	// a language code is not folded when it is not one we know
	if knownLanguage("XXX") {
		t.Error("XXX should not be a recognized language")
	}
	if !knownLanguage("eng") || !knownLanguage("English") {
		t.Error("eng/English should be recognized")
	}
}

func TestVocabKeyEmpty(t *testing.T) {
	t.Parallel()

	for _, field := range vocabFields {
		if got := vocabKey(field, "   "); got != "" {
			t.Errorf("%s: blank value gave key %q", field, got)
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
			Title: "Dune", AuthorName: "Frank Herbert", ASIN: "B0", Description: "A book.",
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
		// one chapter over three hours is as unnavigable as none
		"single_chapter": book(abs.Metadata{}, abs.Media{Duration: 3 * 3600, NumTracks: 1, NumChapters: 1}),
		"no_audio":       book(abs.Metadata{Title: "Dune"}, abs.Media{}),
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
		"single_chapter":  clean(),
		"no_audio":        clean(),
		"path":            clean(),
		"author_as_title": clean(),
		"issues":          clean(),
		// a podcast checked recently with episodes downloaded
		"stale_feed":  pod(abs.Metadata{FeedURL: "http://f"}, abs.Media{LastEpisodeCheck: time.Now().UnixMilli(), NumEpisodes: 3}),
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
	for check := range auditChecksByName {
		if _, ok := byCheck[check]; !ok {
			t.Errorf("check %q has no audit tool, so audit_all never reports it", check)
		}
	}
}
