package tools

import (
	"slices"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

func TestCleanPeople(t *testing.T) {
	t.Parallel()

	got := cleanPeople([]string{"Dr. Nicole LePera", "Larry Page - introduction", "Gabor Maté MD", "The Great Courses", "PhD Marshall Rosenberg", "Malcolm X"})
	want := []string{"Nicole LePera", "Gabor Maté", "Marshall Rosenberg", "Malcolm X"}
	if !slices.Equal(got, want) {
		t.Errorf("cleanPeople = %q, want %q", got, want)
	}
}

func TestSmartDecide(t *testing.T) {
	t.Parallel()

	it := &abs.Item{MediaType: "book", RelPath: "K. Patrick/Mrs S", Media: abs.Media{Metadata: abs.Metadata{
		Title: "Mrs. S .mp3", AuthorName: "Nicole LePera", NarratorName: "Black Library, Heavy Entertainment",
		Series: abs.SeriesRefs{{Name: "Nemesis", Sequence: "2"}}, Genres: []string{"Audiobook"},
		Publisher: "Isis", PublishedYear: "2023-10-12T00:00:00", Language: "eng", Description: "Read by X",
	}}}
	hit := &abs.BookSearchResult{
		Title: "Mrs S", Author: "Dr. Nicole LePera, Someone - translator", Narrator: "Nicolette Chin",
		Series: []abs.SearchSeries{{Series: "Nemesis Series", Sequence: "2"}}, Genres: []string{"Literary Fiction"},
		Publisher: "Fourth Estate", PublishedYear: "2023", Language: "English", Description: "A long description of the book that runs well past the stub threshold and says something about the plot.",
	}
	// "Nemesis #2" against "Nemesis Series #2": the name differs, the
	// number agrees, and the name is the collector's to keep
	want := map[string]string{
		"title": actWritten, "authors": actKept, "narrators": actWritten, "series": actKept, "genres": actWritten,
		"publisher": actWritten, "year": actWritten, "language": actWritten, "description": actWritten,
	}
	for _, d := range smartDecide(it, hit) {
		if w, ok := want[d.Field]; !ok {
			t.Errorf("unexpected field %s: %+v", d.Field, d)
		} else if d.Action != w {
			t.Errorf("%s: %s (%s), want %s", d.Field, d.Action, d.Rule, w)
		}
		delete(want, d.Field)
	}
	for f := range want {
		t.Errorf("%s not decided", f)
	}

	// curated values are kept, a different reader is for review, an empty
	// field is filled by the match
	it.Media.Metadata = abs.Metadata{Title: "Feet of Clay", AuthorName: "Terry Pratchett", NarratorName: "Nigel Planer", Genres: []string{"Fantasy"}, PublishedYear: "1996", Description: string(make([]byte, 200))}
	hit = &abs.BookSearchResult{Title: "Feet Of Clay", Author: "Terry Pratchett", Narrator: "Jon Culshaw, Bill Nighy", Genres: []string{"Science Fiction & Fantasy"}, PublishedYear: "2023", Subtitle: "Discworld, Book 19", Description: "x"}
	it.Media.Metadata.Series = abs.SeriesRefs{{Name: "Foundation", Sequence: "7"}}
	hit.Series = []abs.SearchSeries{{Series: "The Foundation Series", Sequence: "2"}}
	got := map[string]string{}
	for _, d := range smartDecide(it, hit) {
		got[d.Field] = d.Action
	}
	for f, w := range map[string]string{"narrators": actReview, "genres": actKept, "year": actKept, "subtitle": actFilled, "series": actReview} {
		if got[f] != w {
			t.Errorf("%s: %s, want %s", f, got[f], w)
		}
	}
	if _, ok := got["title"]; ok {
		t.Error("a title differing only in case was reported as a difference")
	}
	if _, ok := got["description"]; ok {
		t.Error("a written description was decided against the provider's stub")
	}

	// a bare extra name from the provider is for review, never written:
	// Audible lists illustrators, translators and pen names this way as
	// often as co-authors
	it.Media.Metadata = abs.Metadata{Title: "The Rithmatist", AuthorName: "Brandon Sanderson"}
	hit = &abs.BookSearchResult{Title: "The Rithmatist", Author: "Brandon Sanderson, Ben McSweeney"}
	decided := false
	for _, d := range smartDecide(it, hit) {
		if d.Field != "authors" {
			continue
		}
		decided = true
		if d.Action != actReview {
			t.Errorf("authors: %s (%s), want review", d.Action, d.Rule)
		}
		if d.Provider != "Brandon Sanderson, Ben McSweeney" {
			t.Errorf("authors provider = %q", d.Provider)
		}
	}
	if !decided {
		t.Error("an added co-author was not reported")
	}
}

func TestProviderTag(t *testing.T) {
	t.Parallel()

	it := &abs.Item{Media: abs.Media{Tags: []string{"Fantasy", providerTagPrefix + "audible.ca"}}}
	if providerTag(it) != "audible.ca" {
		t.Errorf("providerTag = %q", providerTag(it))
	}
	if got := providerOrder(it, []string{"audible", "audible.ca"}); !slices.Equal(got, []string{"audible.ca", "audible"}) {
		t.Errorf("providerOrder = %v", got)
	}
	if got := withProviderTag(it.Media.Tags, "audible"); !slices.Equal(got, []string{"Fantasy", providerTagPrefix + "audible"}) {
		t.Errorf("withProviderTag = %v", got)
	}
	none := &abs.Item{MediaType: "book", Media: abs.Media{Tags: []string{providerTagPrefix + "none"}}}
	if !markedUnmatchable(none) {
		t.Error("provider:none not recognised")
	}
	if _, bad := auditChecksByName["unmatched"](none); bad {
		t.Error("audit_unmatched reported a book marked provider:none")
	}
	if got := providerOrder(none, []string{"audible"}); !slices.Equal(got, []string{"audible"}) {
		t.Errorf("providerOrder with none = %v", got)
	}
}

func TestProviderTagPrefix(t *testing.T) { //nolint:paralleltest // swaps the package-level prefix
	defer func(p string) { providerTagPrefix = p }(providerTagPrefix)
	providerTagPrefix = "provider:"
	it := &abs.Item{Media: abs.Media{Tags: []string{"zz-provider:audible", "provider:audible.ca"}}}
	if got := providerTag(it); got != "audible.ca" {
		t.Errorf("providerTag with a custom prefix = %q", got)
	}
	if got := withProviderTag([]string{"Fantasy"}, "audible"); !slices.Equal(got, []string{"Fantasy", "provider:audible"}) {
		t.Errorf("withProviderTag = %v", got)
	}
	providerTagPrefix = "off"
	if providerTag(it) != "" || !slices.Equal(withProviderTag([]string{"Fantasy"}, "audible"), []string{"Fantasy"}) {
		t.Error("off still tags")
	}
}
