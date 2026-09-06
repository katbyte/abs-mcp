package tools

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

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
	for _, name := range []string{"item_delete", "podcast_episode_delete", "author_delete", "library_remove_issues"} {
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
	s := summarise(it)
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
	if _, bad := auditChecksByName["single_file"](book(abs.Metadata{}, abs.Media{Duration: 3 * 3600, NumTracks: 5})); bad {
		t.Error("single_file: multi-track flagged")
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
